package cli

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/XSpring/minipsi/internal/config"
	"github.com/XSpring/minipsi/internal/devcerts"
	"github.com/XSpring/minipsi/internal/hashing"
	"github.com/XSpring/minipsi/internal/itemio"
	"github.com/XSpring/minipsi/internal/protocol"
	"github.com/XSpring/minipsi/internal/report"
	"github.com/XSpring/minipsi/internal/transport"
	"github.com/XSpring/minipsi/internal/version"
)

// flagBinding copies one explicitly set flag from the flag-backed config into the
// effective config (so that set flags override the YAML file, unset ones do not).
type flagBinding struct {
	name string
	copy func(dst, src *config.Config)
}

// partyRole maps a party command (receiver, sender, request) to its protocol role.
func partyRole(cmd string) string {
	if cmd == "request" {
		return "receiver"
	}
	return cmd
}

// defaultDevName is the --dev party name when --name is not given.
func defaultDevName(role string) string { return role }

// partyFlags registers the receiver/sender/request flags on fs,
// backed by fc.
func partyFlags(fs *pflag.FlagSet, fc *config.Config, cmdName string) []flagBinding {
	role := partyRole(cmdName)
	var b []flagBinding
	str := func(p *string, name, usage string, cp func(dst, src *config.Config)) {
		fs.StringVar(p, name, *p, usage)
		b = append(b, flagBinding{name, cp})
	}
	integer := func(p *int, name, usage string, cp func(dst, src *config.Config)) {
		fs.IntVar(p, name, *p, usage)
		b = append(b, flagBinding{name, cp})
	}
	boolean := func(p *bool, name, usage string, cp func(dst, src *config.Config)) {
		fs.BoolVar(p, name, *p, usage)
		b = append(b, flagBinding{name, cp})
	}
	val := func(v pflag.Value, name, usage string, cp func(dst, src *config.Config)) {
		fs.Var(v, name, usage)
		b = append(b, flagBinding{name, cp})
	}
	if cmdName == "request" {
		str(&fc.Connect, "peer", "peer daemon to request from, ADDR (host:port)", func(d, s *config.Config) { d.Connect = s.Connect })
		str(&fc.Request.Dataset, "dataset", "dataset name on the peer daemon (required)", func(d, s *config.Config) { d.Request.Dataset = s.Request.Dataset })
		str(&fc.Request.Identity, "identity", "identity sent with the request (default: first SAN of the certificate)", func(d, s *config.Config) { d.Request.Identity = s.Request.Identity })
		str(&fc.Mode, "mode", "communication mode: direct (relay via the Orchestrator: not implemented yet)", func(d, s *config.Config) { d.Mode = s.Mode })
	} else {
		str(&fc.Listen, "listen", "listen on ADDR (host:port) and wait for the peer", func(d, s *config.Config) { d.Listen = s.Listen })
		str(&fc.Connect, "connect", "connect to the peer at ADDR (host:port)", func(d, s *config.Config) { d.Connect = s.Connect })
	}
	str(&fc.Input.Path, "input", `input file ("-" = stdin)`, func(d, s *config.Config) { d.Input.Path = s.Input.Path })
	str(&fc.Input.Format, "format", "input format: lines or csv", func(d, s *config.Config) { d.Input.Format = s.Input.Format })
	str(&fc.Input.Column, "column", "CSV item column (index or header name)", func(d, s *config.Config) { d.Input.Column = s.Input.Column })
	str(&fc.Input.InstanceColumn, "instance-column", "CSV instance-ID column for batched mode", func(d, s *config.Config) { d.Input.InstanceColumn = s.Input.InstanceColumn })
	boolean(&fc.Input.Header, "header", "CSV input has a header row", func(d, s *config.Config) { d.Input.Header = s.Input.Header })
	boolean(&fc.Input.Batched, "batched", "batched mode: many instances, grouped by instance column", func(d, s *config.Config) { d.Input.Batched = s.Input.Batched })
	str(&fc.Protocol.Hash, "hash", "comparison hash: sha256 or aes (must match the peer)", func(d, s *config.Config) { d.Protocol.Hash = s.Protocol.Hash })
	integer(&fc.Protocol.Lambda, "lambda", "statistical security parameter (≥ 40, must match the peer)", func(d, s *config.Config) { d.Protocol.Lambda = s.Protocol.Lambda })
	if role == "receiver" {
		str(&fc.Output, "output", `result file ("-" = stdout)`, func(d, s *config.Config) { d.Output = s.Output })
		integer(&fc.Protocol.Padding.Extra, "pad-extra", "random dummy points added per instance", func(d, s *config.Config) { d.Protocol.Padding.Extra = s.Protocol.Padding.Extra })
		integer(&fc.Protocol.Padding.Min, "pad-min", "minimum point count per instance (hides small set sizes)", func(d, s *config.Config) { d.Protocol.Padding.Min = s.Protocol.Padding.Min })
		integer(&fc.Protocol.Padding.Multiple, "pad-multiple", "round the point count up to a multiple of N (block padding)", func(d, s *config.Config) { d.Protocol.Padding.Multiple = s.Protocol.Padding.Multiple })
	} else {
		integer(&fc.Protocol.SenderPadding.Extra, "pad-extra", "dummy outputs added per instance", func(d, s *config.Config) { d.Protocol.SenderPadding.Extra = s.Protocol.SenderPadding.Extra })
		integer(&fc.Protocol.SenderPadding.Min, "pad-min", "minimum output count per instance (hides small set sizes)", func(d, s *config.Config) { d.Protocol.SenderPadding.Min = s.Protocol.SenderPadding.Min })
		integer(&fc.Protocol.SenderPadding.Multiple, "pad-multiple", "round the output count up to a multiple of N (block padding)", func(d, s *config.Config) { d.Protocol.SenderPadding.Multiple = s.Protocol.SenderPadding.Multiple })
	}
	str(&fc.TLS.Cert, "cert", "PEM certificate (chain) for mutual TLS", func(d, s *config.Config) { d.TLS.Cert = s.TLS.Cert })
	str(&fc.TLS.Key, "key", "PEM private key", func(d, s *config.Config) { d.TLS.Key = s.TLS.Key })
	str(&fc.TLS.CA, "ca", "PEM CA bundle trusted for the peer", func(d, s *config.Config) { d.TLS.CA = s.TLS.CA })
	str(&fc.TLS.PeerName, "peer-name", "require the peer certificate to carry this DNS/URI SAN", func(d, s *config.Config) { d.TLS.PeerName = s.TLS.PeerName })
	str(&fc.TLS.ServerName, "server-name", "TLS server name to verify when connecting (default: host of --connect)", func(d, s *config.Config) { d.TLS.ServerName = s.TLS.ServerName })
	boolean(&fc.TLS.Dev, "dev", "development mode: shared local dev PKI, loopback only", func(d, s *config.Config) { d.TLS.Dev = s.TLS.Dev })
	str(&fc.TLS.Name, "name", "party name for --dev (default: "+defaultDevName(role)+")", func(d, s *config.Config) { d.TLS.Name = s.TLS.Name })
	boolean(&fc.TLS.InsecureDev, "insecure-dev", "plaintext transport for development (requires "+transport.InsecureEnv+"=1)", func(d, s *config.Config) { d.TLS.InsecureDev = s.TLS.InsecureDev })
	integer(&fc.Workers, "workers", "CPU parallelism (0 = GOMAXPROCS)", func(d, s *config.Config) { d.Workers = s.Workers })
	str(&fc.NTTBackend, "ntt-backend", "polynomial NTT backend: native or lattigo (local only)", func(d, s *config.Config) { d.NTTBackend = s.NTTBackend })
	val(&fc.MemoryLimit, "memory-limit", "soft memory limit for the Go runtime, e.g. 2GiB (0 = off)", func(d, s *config.Config) {
		d.MemoryLimit = s.MemoryLimit
		d.SetMemoryLimitExplicit()
	})
	val(&fc.PolyCache, "poly-cache", "per-tree NTT transform cache, e.g. 256MiB (0 = off)", func(d, s *config.Config) { d.PolyCache = s.PolyCache })
	val(&fc.Limits.MaxFrame, "max-frame", "maximum frame size", func(d, s *config.Config) { d.Limits.MaxFrame = s.Limits.MaxFrame })
	integer(&fc.Limits.MaxInstanceSize, "max-instance-size", "maximum items per instance (either side)", func(d, s *config.Config) { d.Limits.MaxInstanceSize = s.Limits.MaxInstanceSize })
	integer(&fc.Limits.MaxTotalItems, "max-total-items", "maximum items per side", func(d, s *config.Config) { d.Limits.MaxTotalItems = s.Limits.MaxTotalItems })
	integer(&fc.Limits.MaxInstances, "max-instances", "maximum number of instances", func(d, s *config.Config) { d.Limits.MaxInstances = s.Limits.MaxInstances })
	val(&fc.Timeouts.Handshake, "handshake-timeout", "wait for the peer's handshake", func(d, s *config.Config) { d.Timeouts.Handshake = s.Timeouts.Handshake })
	val(&fc.Timeouts.Phase, "phase-timeout", "maximum wait for any single message from the peer", func(d, s *config.Config) { d.Timeouts.Phase = s.Timeouts.Phase })
	val(&fc.Timeouts.Total, "timeout", "overall deadline for the run (0 = none)", func(d, s *config.Config) { d.Timeouts.Total = s.Timeouts.Total })
	val(&fc.Timeouts.ConnectRetry, "connect-retry", "keep retrying the connection for this long (peer may start later)", func(d, s *config.Config) { d.Timeouts.ConnectRetry = s.Timeouts.ConnectRetry })
	str(&fc.Report, "report", "write a JSON run report to FILE", func(d, s *config.Config) { d.Report = s.Report })
	str(&fc.Log.Format, "log-format", "log format: text or json", func(d, s *config.Config) { d.Log.Format = s.Log.Format })
	str(&fc.Log.Level, "log-level", "log level: debug, info, warn, error", func(d, s *config.Config) { d.Log.Level = s.Log.Level })
	return b
}

// requestConnectRetry is the default retry window of `minipsi request` (a busy daemon
// refuses new sessions until the running one ends).
const requestConnectRetry = 2 * time.Minute

func partyDefaults(cmdName string) config.Config {
	c := config.Default()
	if cmdName == "request" {
		c.Timeouts.ConnectRetry = config.Duration(requestConnectRetry)
	}
	return c
}

func newPartyCmd(cmdName string, stdout, stderr io.Writer) *cobra.Command {
	role := partyRole(cmdName)
	fc := partyDefaults(cmdName)
	var configPath string
	cmd := &cobra.Command{
		Use: cmdName,
		Short: map[string]string{
			"receiver": "Run as PSI receiver (learns the intersection)",
			"sender":   "Run as PSI sender",
			"request":  "Request a session from a peer daemon (minipsi serve) and receive the intersection",
		}[cmdName],
		Args: cobra.NoArgs,
	}
	cmd.Flags().StringVar(&configPath, "config", "", "YAML configuration file (flags override it)")
	bindings := partyFlags(cmd.Flags(), &fc, cmdName)
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		cfg := partyDefaults(cmdName)
		if configPath != "" {
			if err := cfg.Load(configPath); err != nil {
				return &exitError{code: ExitUsage, err: err}
			}
		}
		if cfg.Role != "" && cfg.Role != role {
			return &exitError{code: ExitUsage, err: fmt.Errorf("config file is for role %q, command is %q", cfg.Role, role)}
		}
		cfg.Role = role
		for _, fb := range bindings {
			if cmd.Flags().Changed(fb.name) {
				fb.copy(&cfg, &fc)
			}
		}
		if cmdName == "request" && cfg.Request.Dataset == "" {
			return &exitError{code: ExitUsage, err: errors.New("request needs --dataset")}
		}
		if cmdName != "request" && cfg.Request.Dataset != "" {
			return &exitError{code: ExitUsage, err: errors.New("request settings are only valid for `minipsi request`")}
		}
		if err := cfg.Validate(); err != nil {
			return &exitError{code: ExitUsage, err: err}
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return runParty(ctx, &cfg, stdout, stderr)
	}
	return cmd
}

// newReport starts the run report for cfg.
func newReport(cfg *config.Config) *report.Report {
	rep := &report.Report{
		Version: version.Version, Commit: version.Commit, Role: cfg.Role, Suite: version.Suite,
		Hash: cfg.Protocol.Hash, Lambda: cfg.Protocol.Lambda, NTTBackend: cfg.NTTBackend,
		TimingsMS: map[string]int64{},
	}
	p := cfg.Protocol.Padding
	if cfg.Role == "sender" {
		p = cfg.Protocol.SenderPadding
	}
	rep.Padding = &report.Padding{Extra: p.Extra, Min: p.Min, Multiple: p.Multiple}
	rep.Dataset = cfg.Request.Dataset
	return rep
}

// loadInput reads and checks the party's input (exit 2 on problems) and builds the
// protocol configuration, before any network activity.
func loadInput(cfg *config.Config, rep *report.Report, log *slog.Logger) (*itemio.Input, protocol.Config, error) {
	t0 := time.Now()
	in, err := itemio.ReadFile(cfg.Input.Path, itemio.Options{
		Format: cfg.Input.Format, Column: cfg.Input.Column, InstanceColumn: cfg.Input.InstanceColumn,
		Header: cfg.Input.Header, Batched: cfg.Input.Batched,
	})
	if err != nil {
		return nil, protocol.Config{}, &exitError{code: ExitInput, err: fmt.Errorf("reading input: %w", err)}
	}
	rep.SetInput(in)
	rep.TimingsMS["read_input"] = time.Since(t0).Milliseconds()
	if in.Stats.DupRemoved > 0 {
		log.Warn("duplicate items removed", "count", in.Stats.DupRemoved)
	}
	log.Info("input read", "instances", rep.Instances, "items", rep.NSelf, "empty_skipped", in.Stats.EmptySkipped)

	pcfg, err := protocolConfig(cfg, log)
	if err != nil {
		return nil, protocol.Config{}, &exitError{code: ExitUsage, err: err}
	}
	prole := protocol.RoleSender
	if cfg.Role == "receiver" {
		prole = protocol.RoleReceiver
	}
	if err := protocol.CheckInput(pcfg, prole, in.Instances); err != nil {
		return nil, protocol.Config{}, &exitError{code: ExitInput, err: err}
	}
	return in, pcfg, nil
}

// runParty executes one protocol run and writes output and report.
func runParty(ctx context.Context, cfg *config.Config, stdout, stderr io.Writer) (err error) {
	log := newLogger(cfg.Log, stderr)
	applyMemoryLimit(cfg.MemoryLimit, cfg.MemoryLimitExplicit(), log)
	start := time.Now()
	rep := newReport(cfg)
	defer func() {
		code := exitCode(err)
		rep.ExitCode = code
		if err != nil {
			rep.Error = err.Error()
			log.Error("run failed", "exit_code", code, "error", err)
		}
		rep.TimingsMS["wall"] = time.Since(start).Milliseconds()
		if cfg.Report != "" {
			if werr := itemio.WriteFileAtomic(cfg.Report, rep.JSON()); werr != nil {
				log.Error("writing report", "error", werr)
				if err == nil {
					err = &exitError{code: ExitInternal, err: werr}
				}
			}
		}
	}()

	// 1. Input.
	in, pcfg, err := loadInput(cfg, rep, log)
	if err != nil {
		return err
	}
	prole := protocol.RoleSender
	if cfg.Role == "receiver" {
		prole = protocol.RoleReceiver
	}

	// 2. Transport.
	if err := resolveDev(&cfg.TLS, cfg.Role, log); err != nil {
		return &exitError{code: ExitUsage, err: err}
	}
	topts, err := transportOptions(cfg, log)
	if err != nil {
		return &exitError{code: ExitUsage, err: err}
	}
	if cfg.Timeouts.Total > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Timeouts.Total.D())
		defer cancel()
	}
	sess, closeAll, err := openSession(ctx, cfg, topts, log)
	if err != nil {
		return err
	}
	defer closeAll()
	rep.Peer = sess.PeerIdentity()
	log.Info("peer connected", "peer", rep.Peer)

	// 3. Protocol.
	if prole == protocol.RoleSender {
		res, err := protocol.RunSender(ctx, pcfg, sess.Conn(), in.Instances)
		if err != nil {
			return err
		}
		rep.SetSession(res.SID, res.OutputLen, res.Stats)
		log.Info("run complete", "sid", rep.SID, "wall", time.Since(start).Round(time.Millisecond))
		return nil
	}
	res, err := protocol.RunReceiver(ctx, pcfg, sess.Conn(), in.Instances)
	// Close the session before writing the result: when the sender is the gRPC client its
	// shutdown waits for our close, which must not depend on the intersection size.
	closeAll()
	if err != nil {
		return err
	}
	rep.SetSession(res.SID, res.OutputLen, res.Stats)
	rep.SetResult(res)
	t0 := time.Now()
	if err := itemio.WriteResult(cfg.Output, in, res, cfg.Input.Batched); err != nil {
		return &exitError{code: ExitInternal, err: fmt.Errorf("writing output: %w", err)}
	}
	rep.TimingsMS["write_output"] = time.Since(t0).Milliseconds()
	log.Info("run complete", "sid", rep.SID, "intersection", *rep.ResultCount, "wall", time.Since(start).Round(time.Millisecond))
	return nil
}

func protocolConfig(cfg *config.Config, log *slog.Logger) (protocol.Config, error) {
	alg, err := hashing.ParseAlg(cfg.Protocol.Hash)
	if err != nil {
		return protocol.Config{}, err
	}
	cache := int64(cfg.PolyCache)
	if cache == 0 {
		cache = -1 // protocol: negative disables, 0 means default
	}
	p, sp := cfg.Protocol.Padding, cfg.Protocol.SenderPadding
	return protocol.Config{
		Hash: alg, Lambda: cfg.Protocol.Lambda, Workers: cfg.Workers, NTTBackend: cfg.NTTBackend,
		PolyCacheBytes: cache, MaxFrame: int(cfg.Limits.MaxFrame),
		Limits: protocol.Limits{
			MaxInstanceSize: cfg.Limits.MaxInstanceSize, MaxTotalItems: cfg.Limits.MaxTotalItems,
			MaxInstances: cfg.Limits.MaxInstances,
		},
		HandshakeTimeout: cfg.Timeouts.Handshake.D(), PhaseTimeout: cfg.Timeouts.Phase.D(),
		Padding:       &protocol.PadPolicy{Extra: p.Extra, Min: p.Min, Multiple: p.Multiple},
		SenderPadding: &protocol.PadPolicy{Extra: sp.Extra, Min: sp.Min, Multiple: sp.Multiple},
		Logger:        log,
	}, nil
}

func transportOptions(cfg *config.Config, log *slog.Logger) (transport.Options, error) {
	o := transport.Options{
		Insecure: cfg.TLS.InsecureDev, MaxFrame: int(cfg.Limits.MaxFrame),
		ConnectRetry: cfg.Timeouts.ConnectRetry.D(), Logger: log,
	}
	if cfg.TLS.InsecureDev {
		log.Warn("PLAINTEXT TRANSPORT: --insecure-dev is for development only")
		return o, nil
	}
	m, err := transport.LoadTLSFiles(cfg.TLS.Cert, cfg.TLS.Key, cfg.TLS.CA)
	if err != nil {
		return o, err
	}
	if cfg.Listen != "" {
		o.TLS = m.ServerConfig(cfg.TLS.PeerName)
	} else {
		name := cfg.TLS.ServerName
		if name == "" {
			host, _, err := net.SplitHostPort(cfg.Connect)
			if err != nil {
				return o, fmt.Errorf("connect address %q: %w", cfg.Connect, err)
			}
			name = host
		}
		o.TLS = m.ClientConfig(name, cfg.TLS.PeerName)
	}
	if cfg.Request.Dataset != "" {
		id := cfg.Request.Identity
		if id == "" {
			if id, err = firstSAN(cfg.TLS.Cert); err != nil {
				return o, err
			}
		}
		o.Metadata = map[string]string{config.MetaDataset: cfg.Request.Dataset, config.MetaIdentity: id}
	}
	return o, nil
}

// resolveDev points t at the shared development PKI when t.Dev is set,
// creating the CA and this party's certificate on first use.
func resolveDev(t *config.TLS, role string, log *slog.Logger) error {
	if !t.Dev {
		return nil
	}
	dir, err := devcerts.DefaultDevDir()
	if err != nil {
		return err
	}
	name := t.Name
	if name == "" {
		name = defaultDevName(role)
	}
	f, err := devcerts.EnsureDev(dir, name)
	if err != nil {
		return err
	}
	t.Cert, t.Key, t.CA = f.Cert, f.Key, f.CA
	log.Warn("DEV MODE: shared development certificates, loopback only — not for production", "name", name, "pki", dir)
	return nil
}

// firstSAN returns the first DNS or URI SAN of the PEM certificate at path.
func firstSAN(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	b, _ := pem.Decode(data)
	if b == nil {
		return "", fmt.Errorf("%s: no PEM certificate", path)
	}
	c, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	if len(c.DNSNames) > 0 {
		return c.DNSNames[0], nil
	}
	if len(c.URIs) > 0 {
		return c.URIs[0].String(), nil
	}
	return c.Subject.CommonName, nil
}

// openSession listens (accepting one session) or dials, per cfg.
func openSession(ctx context.Context, cfg *config.Config, o transport.Options, log *slog.Logger) (*transport.Session, func(), error) {
	if cfg.Listen != "" {
		srv, err := transport.Listen(cfg.Listen, o)
		if err != nil {
			return nil, nil, classifyTransport(err)
		}
		log.Info("waiting for peer", "addr", srv.Addr().String())
		sess, err := srv.Accept(ctx)
		if err != nil {
			srv.Close()
			return nil, nil, classifyTransport(err)
		}
		var once sync.Once
		return sess, func() { once.Do(func() { sess.Close(); srv.Close() }) }, nil
	}
	sess, err := transport.Dial(ctx, cfg.Connect, o)
	if err != nil {
		return nil, nil, classifyTransport(err)
	}
	return sess, func() { sess.Close() }, nil // Session.Close is idempotent
}

func classifyTransport(err error) error {
	switch {
	case errors.Is(err, transport.ErrInsecureNotAcknowledged):
		return &exitError{code: ExitUsage, err: err}
	case errors.Is(err, transport.ErrRejected):
		return &exitError{code: ExitProtocol, err: err}
	case errors.Is(err, context.Canceled):
		return &exitError{code: ExitProtocol, err: fmt.Errorf("interrupted: %w", err)}
	}
	return &exitError{code: ExitNetwork, err: err}
}

func newLogger(c config.Log, w io.Writer) *slog.Logger {
	var lvl slog.Level
	_ = lvl.UnmarshalText([]byte(strings.ToUpper(c.Level)))
	opts := &slog.HandlerOptions{Level: lvl}
	if c.Format == "json" {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	return slog.New(slog.NewTextHandler(w, opts))
}

// applyMemoryLimit sets the runtime soft memory limit: an explicit setting wins, then the
// GOMEMLIMIT environment variable, then the default (2 GiB). 0 disables the limit.
func applyMemoryLimit(memLimit config.Size, explicit bool, log *slog.Logger) {
	if !explicit && os.Getenv("GOMEMLIMIT") != "" {
		log.Debug("memory limit from GOMEMLIMIT", "value", os.Getenv("GOMEMLIMIT"))
		return
	}
	limit := int64(memLimit)
	if limit == 0 {
		limit = math.MaxInt64
	}
	debug.SetMemoryLimit(limit)
	log.Debug("memory limit", "bytes", limit)
}
