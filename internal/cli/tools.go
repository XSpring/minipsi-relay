package cli

import (
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/XSpring/minipsi/internal/devcerts"
	"github.com/XSpring/minipsi/internal/hashing"
	"github.com/XSpring/minipsi/internal/protocol"
)

func newCertsCmd(stdout io.Writer) *cobra.Command {
	certs := &cobra.Command{Use: "certs", Short: "Certificate utilities"}
	var (
		out      string
		names    []string
		hosts    []string
		validity time.Duration
	)
	dev := &cobra.Command{
		Use:   "dev",
		Short: "Generate a throw-away CA and leaf certificates (testing only)",
		Long: "Writes ca.crt and <name>.crt/<name>.key for each --name into --out. Each leaf has the\n" +
			"DNS SAN <name> plus every --host (DNS name or IP), and is valid for TLS server and\n" +
			"client authentication. Existing files are never overwritten. NOT for production.",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if validity <= 0 {
				return &exitError{code: ExitUsage, err: fmt.Errorf("validity must be positive")}
			}
			ca, err := devcerts.NewAuthority("minipsi dev CA", validity)
			if err != nil {
				return &exitError{code: ExitInternal, err: err}
			}
			var o devcerts.LeafOptions
			for _, h := range hosts {
				if ip := net.ParseIP(h); ip != nil {
					o.IPs = append(o.IPs, ip)
				} else {
					o.DNSNames = append(o.DNSNames, h)
				}
			}
			o.Validity = validity
			leaves := map[string]*devcerts.Leaf{}
			for _, n := range names {
				lo := o
				lo.DNSNames = append([]string{n}, o.DNSNames...)
				leaf, err := ca.Issue(n, lo)
				if err != nil {
					return &exitError{code: ExitInternal, err: err}
				}
				leaves[n] = leaf
			}
			if err := devcerts.WriteFiles(out, ca, leaves); err != nil {
				return &exitError{code: ExitUsage, err: err}
			}
			fmt.Fprintf(stdout, "wrote ca.crt and %s certificates to %s (testing only)\n", strings.Join(names, ", "), out)
			return nil
		},
	}
	dev.Flags().StringVar(&out, "out", "certs", "output directory")
	dev.Flags().StringSliceVar(&names, "name", []string{"receiver", "sender"}, "leaf names (also used as DNS SANs)")
	dev.Flags().StringSliceVar(&hosts, "host", []string{"localhost", "127.0.0.1"}, "extra DNS names / IPs for every leaf")
	dev.Flags().DurationVar(&validity, "validity", 30*24*time.Hour, "certificate validity")
	certs.AddCommand(dev)
	return certs
}

// newBenchCmd runs both parties in-process over an in-memory pipe (no network) on random
// data, for capacity planning.
func newBenchCmd(stdout io.Writer) *cobra.Command {
	var (
		n1, n2, instances, workers int
		overlap                    float64
		hash, backend              string
	)
	cmd := &cobra.Command{
		Use:   "bench",
		Short: "Local in-process benchmark (no network) for capacity planning",
		Long: "Runs receiver and sender in one process over an in-memory connection on random items.\n" +
			"Both parties share this host's CPUs, so wall time approximates the sum of both\n" +
			"parties' work; per-phase timings are reported for each side.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if n1 < 0 || n2 < 0 || instances < 1 || overlap < 0 || overlap > 1 {
				return &exitError{code: ExitUsage, err: fmt.Errorf("need n1, n2 ≥ 0, instances ≥ 1, 0 ≤ overlap ≤ 1")}
			}
			alg, err := hashing.ParseAlg(hash)
			if err != nil {
				return &exitError{code: ExitUsage, err: err}
			}
			rinst, sinst := benchSets(n2, n1, instances, overlap)
			cfg := protocol.Config{Hash: alg, Workers: workers, NTTBackend: backend}
			a, b := protocol.Pipe(8)
			var (
				wg     sync.WaitGroup
				res    *protocol.Result
				sres   *protocol.SenderResult
				rerr   error
				serr   error
				starts = time.Now()
			)
			wg.Add(2)
			go func() {
				defer wg.Done()
				defer a.Close()
				res, rerr = protocol.RunReceiver(cmd.Context(), cfg, a, rinst)
			}()
			go func() {
				defer wg.Done()
				defer b.Close()
				sres, serr = protocol.RunSender(cmd.Context(), cfg, b, sinst)
			}()
			wg.Wait()
			wall := time.Since(starts)
			if rerr != nil || serr != nil {
				return &exitError{code: ExitInternal, err: fmt.Errorf("bench failed: receiver %v, sender %v", rerr, serr)}
			}
			found := 0
			for _, ir := range res.Instances {
				found += len(ir.Matches)
			}
			tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintf(tw, "instances\t%d\nreceiver items (n2)\t%d\nsender items (n1)\t%d\nintersection\t%d\nhash / backend\t%s / %s\nwall time\t%v\n",
				instances, n2, n1, found, alg, backend, wall.Round(time.Millisecond))
			printPhases(tw, "receiver", res.Stats)
			printPhases(tw, "sender", sres.Stats)
			fmt.Fprintf(tw, "bytes R→S / S→R\t%d / %d\n", res.Stats.BytesSent, sres.Stats.BytesSent)
			return tw.Flush()
		},
	}
	f := cmd.Flags()
	f.IntVar(&n1, "n1", 1<<12, "sender items (total over instances)")
	f.IntVar(&n2, "n2", 1<<12, "receiver items (total over instances)")
	f.IntVar(&instances, "instances", 1, "number of instances (batched mode if > 1)")
	f.Float64Var(&overlap, "overlap", 0.1, "fraction of receiver items also held by the sender")
	f.StringVar(&hash, "hash", "sha256", "comparison hash: sha256 or aes")
	f.StringVar(&backend, "ntt-backend", "native", "NTT backend: native or lattigo")
	f.IntVar(&workers, "workers", 0, "CPU parallelism per party (0 = GOMAXPROCS)")
	return cmd
}

func printPhases(w io.Writer, who string, st protocol.Stats) {
	ph := append([]protocol.Phase(nil), st.Phases...)
	sort.SliceStable(ph, func(i, j int) bool { return ph[i].Name == "total" && ph[j].Name != "total" })
	for _, p := range ph {
		fmt.Fprintf(w, "%s: %s\t%v\n", who, p.Name, p.Duration.Round(time.Millisecond))
	}
}

// benchSets builds random sets with the requested overlap, split evenly over instances.
func benchSets(n1recv, n1send, instances int, overlap float64) (recv, send []protocol.Instance) {
	item := func() []byte {
		var b [16]byte
		_, _ = rand.Read(b[:])
		return b[:]
	}
	for b := 0; b < instances; b++ {
		nr := n1recv/instances + boolInt(b < n1recv%instances)
		ns := n1send/instances + boolInt(b < n1send%instances)
		common := min(int(overlap*float64(nr)), ns)
		r := make([][]byte, nr)
		for i := range r {
			r[i] = item()
		}
		s := make([][]byte, ns)
		for i := range s {
			if i < common {
				s[i] = r[i]
			} else {
				s[i] = item()
			}
		}
		id := uint32(b)
		recv = append(recv, protocol.Instance{ID: id, Items: r})
		send = append(send, protocol.Instance{ID: id, Items: s})
	}
	return recv, send
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
