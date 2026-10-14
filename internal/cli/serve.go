package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/XSpring/minipsi/internal/config"
	"github.com/XSpring/minipsi/internal/itemio"
	"github.com/XSpring/minipsi/internal/protocol"
	"github.com/XSpring/minipsi/internal/transport"
)

// newServeCmd builds `minipsi serve`: a long-running listener that
// answers session requests as sender for its named datasets, one session at a time.
func newServeCmd(stdout, stderr io.Writer) *cobra.Command {
	var (
		configPath string
		datasets   []string
	)
	fs := config.DefaultServe()
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve named datasets as PSI sender to requesting peers (daemon)",
		Long: "Listens for session requests (minipsi request) and runs each as PSI sender on the\n" +
			"requested dataset, one session at a time; concurrent requests are told to retry.\n" +
			"Datasets come from the config file and/or --dataset NAME=PATH (lines format).",
		Args: cobra.NoArgs,
	}
	f := cmd.Flags()
	f.StringVar(&configPath, "config", "", "YAML daemon configuration (flags override it)")
	f.StringArrayVar(&datasets, "dataset", nil, "serve PATH (one item per line) as dataset NAME: NAME=PATH (repeatable)")
	f.StringVar(&fs.Listen, "listen", "", "listen on ADDR (host:port)")
	f.StringVar(&fs.Mode, "mode", fs.Mode, "communication mode: direct (relay via the Orchestrator: not implemented yet)")
	f.StringVar(&fs.StateDir, "state-dir", "", "directory for per-session reports (reports/); empty = none")
	f.StringVar(&fs.TLS.Cert, "cert", "", "PEM certificate (chain) for mutual TLS")
	f.StringVar(&fs.TLS.Key, "key", "", "PEM private key")
	f.StringVar(&fs.TLS.CA, "ca", "", "PEM CA bundle trusted for requesting peers")
	f.BoolVar(&fs.TLS.Dev, "dev", false, "development mode: shared local dev PKI, loopback only")
	f.StringVar(&fs.TLS.Name, "name", "", "party name for --dev (default: sender)")
	f.BoolVar(&fs.TLS.InsecureDev, "insecure-dev", false, "plaintext transport for development (requires "+transport.InsecureEnv+"=1)")
	f.IntVar(&fs.Workers, "workers", 0, "CPU parallelism (0 = GOMAXPROCS)")
	f.Var(&fs.MemoryLimit, "memory-limit", "soft memory limit for the Go runtime, e.g. 2GiB (0 = off)")
	f.StringVar(&fs.Log.Format, "log-format", fs.Log.Format, "log format: text or json")
	f.StringVar(&fs.Log.Level, "log-level", fs.Log.Level, "log level: debug, info, warn, error")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		sc := config.DefaultServe()
		if configPath != "" {
			if err := sc.Load(configPath); err != nil {
				return &exitError{code: ExitUsage, err: err}
			}
		}
		set := func(name string, apply func()) {
			if f.Changed(name) {
				apply()
			}
		}
		set("listen", func() { sc.Listen = fs.Listen })
		set("mode", func() { sc.Mode = fs.Mode })
		set("state-dir", func() { sc.StateDir = fs.StateDir })
		set("cert", func() { sc.TLS.Cert = fs.TLS.Cert })
		set("key", func() { sc.TLS.Key = fs.TLS.Key })
		set("ca", func() { sc.TLS.CA = fs.TLS.CA })
		set("dev", func() { sc.TLS.Dev = fs.TLS.Dev })
		set("name", func() { sc.TLS.Name = fs.TLS.Name })
		set("insecure-dev", func() { sc.TLS.InsecureDev = fs.TLS.InsecureDev })
		set("workers", func() { sc.Workers = fs.Workers })
		set("memory-limit", func() { sc.MemoryLimit = fs.MemoryLimit; sc.SetMemoryLimitExplicit() })
		set("log-format", func() { sc.Log.Format = fs.Log.Format })
		set("log-level", func() { sc.Log.Level = fs.Log.Level })
		for _, d := range datasets {
			name, path, ok := strings.Cut(d, "=")
			if !ok || name == "" || path == "" {
				return &exitError{code: ExitUsage, err: fmt.Errorf("--dataset %q: want NAME=PATH", d)}
			}
			if sc.Datasets == nil {
				sc.Datasets = map[string]config.Dataset{}
			}
			sc.Datasets[name] = config.Dataset{Input: config.Input{Path: path}}
		}
		if err := sc.Validate(); err != nil {
			return &exitError{code: ExitUsage, err: err}
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return runServe(ctx, &sc, stderr)
	}
	return cmd
}

// runServe runs the daemon until ctx is cancelled. Failed sessions are logged and reported
// but never stop the daemon; a clean shutdown returns nil.
func runServe(ctx context.Context, sc *config.Serve, stderr io.Writer) error {
	log := newLogger(sc.Log, stderr)
	applyMemoryLimit(sc.MemoryLimit, sc.MemoryLimitExplicit(), log)
	if err := resolveDev(&sc.TLS, "sender", log); err != nil {
		return &exitError{code: ExitUsage, err: err}
	}
	// Fail fast: every dataset must load and pass the local checks now.
	for _, name := range sc.DatasetNames() {
		cfg := sc.SessionConfig(name)
		if _, _, err := loadInput(&cfg, newReport(&cfg), slog.New(slog.DiscardHandler)); err != nil {
			return &exitError{code: exitCode(err), err: fmt.Errorf("dataset %s: %w", name, err)}
		}
	}
	if sc.StateDir != "" {
		if err := os.MkdirAll(filepath.Join(sc.StateDir, "reports"), 0o700); err != nil {
			return &exitError{code: ExitUsage, err: fmt.Errorf("state dir: %w", err)}
		}
	}
	base := sc.SessionConfig(sc.DatasetNames()[0]) // shared TLS/limit settings
	topts, err := transportOptions(&base, log)
	if err != nil {
		return &exitError{code: ExitUsage, err: err}
	}
	if topts.TLS != nil {
		topts.TLS = topts.TLS.Clone()
	}
	topts.Repeat = true
	topts.Admit = func(r transport.Request) error {
		name := r.Metadata[config.MetaDataset]
		ds, ok := sc.Datasets[name]
		if !ok || !sc.Allowed(r.PeerSANs) {
			// Same answer for unknown and not-allowed: existence is not revealed.
			return &transport.Rejection{Msg: "dataset not available"}
		}
		if _, err := os.Stat(ds.Input.Path); err != nil {
			return fmt.Errorf("dataset %s: %w", name, err) // logged locally, peer sees "unavailable"
		}
		return nil
	}
	srv, err := transport.Listen(sc.Listen, topts)
	if err != nil {
		return classifyTransport(err)
	}
	defer srv.Close()
	allowed := "all authenticated peers"
	if len(sc.AllowedPeers) > 0 {
		allowed = strings.Join(sc.AllowedPeers, ",")
	}
	log.Info("serving", "addr", srv.Addr().String(), "mode", sc.Mode, "datasets", strings.Join(sc.DatasetNames(), ","),
		"allowed_peers", allowed)
	var seq atomic.Uint64
	for {
		sess, err := srv.Accept(ctx)
		if err != nil {
			if ctx.Err() != nil {
				log.Info("shutting down")
				return nil
			}
			return classifyTransport(err)
		}
		serveSession(ctx, sc, sess, seq.Add(1), log)
	}
}

// serveSession runs one sender session on the requested dataset and writes its report.
func serveSession(ctx context.Context, sc *config.Serve, sess *transport.Session, n uint64, log *slog.Logger) {
	defer sess.Close()
	md := sess.Metadata()
	name := md[config.MetaDataset]
	cfg := sc.SessionConfig(name)
	log = log.With("session", n, "dataset", name, "requester", md[config.MetaIdentity])
	log.Info("session started", "peer", sess.PeerIdentity())
	start := time.Now()
	rep := newReport(&cfg)
	rep.Dataset, rep.Requester, rep.Peer = name, md[config.MetaIdentity], sess.PeerIdentity()
	err := func() error {
		in, pcfg, err := loadInput(&cfg, rep, log)
		if err != nil {
			// Never tell the peer why (paths, local details): a generic input abort.
			_ = protocol.AbortConn(ctx, sess.Conn(), protocol.CodeInput, "sender dataset unavailable")
			return err
		}
		sctx := ctx
		if cfg.Timeouts.Total > 0 {
			var cancel context.CancelFunc
			sctx, cancel = context.WithTimeout(ctx, cfg.Timeouts.Total.D())
			defer cancel()
		}
		res, err := protocol.RunSender(sctx, pcfg, sess.Conn(), in.Instances)
		if err != nil {
			return err
		}
		rep.SetSession(res.SID, res.OutputLen, res.Stats)
		return nil
	}()
	sess.Close()
	rep.ExitCode = exitCode(err)
	rep.TimingsMS["wall"] = time.Since(start).Milliseconds()
	if err != nil {
		rep.Error = err.Error()
		var pe *protocol.Error
		if errors.As(err, &pe) && pe.Code == protocol.CodeCancelled && !pe.Remote {
			log.Warn("session interrupted by shutdown")
		} else {
			log.Error("session failed", "error", err)
		}
	} else {
		log.Info("session complete", "sid", rep.SID, "wall", time.Since(start).Round(time.Millisecond))
	}
	if sc.StateDir != "" {
		id := rep.SID
		if id == "" {
			id = "failed"
		}
		path := filepath.Join(sc.StateDir, "reports", fmt.Sprintf("%s-%04d-%s.json", start.UTC().Format("20060102T150405Z"), n%10000, id))
		if werr := itemio.WriteFileAtomic(path, rep.JSON()); werr != nil {
			log.Error("writing session report", "error", werr)
		}
	}
}
