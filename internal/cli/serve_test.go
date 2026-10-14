package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// daemon runs `minipsi serve` with args until stop is called; stop returns its outcome.
func daemon(t *testing.T, args ...string) (stop func() runOut) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan runOut, 1)
	go func() { done <- run(ctx, append([]string{"serve"}, args...)...) }()
	var once sync.Once
	var out runOut
	stop = func() runOut {
		once.Do(func() {
			cancel()
			select {
			case out = <-done:
			case <-time.After(30 * time.Second):
				t.Fatal("serve did not stop")
			}
		})
		return out
	}
	t.Cleanup(func() { stop() })
	return stop
}

func reportsIn(t *testing.T, dir string) []map[string]any {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	var out []map[string]any
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]any{}
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

func TestServeAndRequest(t *testing.T) {
	e := newEnv(t)
	addr := freeAddr(t)
	x := e.write("x.txt", lines(100, 400, "id-"))
	var xs, ys strings.Builder
	xs.WriteString("day,order\n")
	ys.WriteString("day,order\n")
	for d := 1; d <= 5; d++ {
		for i := 0; i < 20; i++ {
			fmt.Fprintf(&xs, "%d,o%d-%d\n", d, d, i)
			fmt.Fprintf(&ys, "%d,o%d-%d\n", d, d, i+15)
		}
	}
	xo := e.write("x.csv", xs.String())
	cfg := e.write("serve.yaml", fmt.Sprintf(`
listen: %q
tls: { cert: %q, key: %q, ca: %q }
state_dir: %q
datasets:
  customers:
    input: { path: %q }
  orders:
    input: { path: %q, format: csv, header: true, batched: true, instance_column: day, column: order }
    protocol: { hash: aes, sender_padding: { min: 64 } }
timeouts: { session: 5m }
log: { level: debug }
`, addr, e.path("certs/sender.crt"), e.path("certs/sender.key"), e.path("certs/ca.crt"), e.path("state"), x, xo))
	stop := daemon(t, "--config", cfg)

	tlsR := []string{"--cert", e.path("certs/receiver.crt"), "--key", e.path("certs/receiver.key"), "--ca", e.path("certs/ca.crt"),
		"--server-name", "sender", "--peer-name", "sender"}
	req := func(dataset, input, output string, extra ...string) runOut {
		args := append([]string{"request", "--peer", addr, "--dataset", dataset, "--input", input, "--output", output,
			"--connect-retry", "30s"}, tlsR...)
		return run(context.Background(), append(args, extra...)...)
	}
	y := e.write("y.txt", lines(0, 200, "id-")+"id-399\n")
	r := req("customers", y, e.path("out1.txt"), "--report", e.path("r1.json"), "--identity", "team-a")
	if r.code != 0 {
		t.Fatalf("request: %d %s", r.code, r.stderr)
	}
	want := strings.Split(strings.TrimSuffix(lines(100, 200, "id-")+"id-399\n", "\n"), "\n")
	slices.Sort(want)
	if got := readLines(t, e.path("out1.txt")); !slices.Equal(got, want) {
		t.Fatalf("intersection %d items", len(got))
	}
	var rep map[string]any
	b, _ := os.ReadFile(e.path("r1.json"))
	if err := json.Unmarshal(b, &rep); err != nil || rep["dataset"] != "customers" || rep["role"] != "receiver" {
		t.Fatalf("receiver report %s", b)
	}

	// Batched CSV dataset with AES and sender padding; the requester must use the same hash.
	yo := e.write("y.csv", ys.String())
	batched := []string{"--format", "csv", "--header", "--batched", "--instance-column", "day", "--column", "order"}
	if r := req("orders", yo, e.path("out2.csv"), append(batched, "--hash", "aes")...); r.code != 0 {
		t.Fatalf("batched: %d %s", r.code, r.stderr)
	}
	if got := readLines(t, e.path("out2.csv")); len(got) != 5*5 || !slices.Contains(got, "3,o3-17") {
		t.Fatalf("batched result %v", got)
	}

	// Failures the daemon survives: unknown dataset (fast, exit 4), parameter mismatch
	// (exit 4), then a normal request again.
	start := time.Now()
	if r := req("secret", y, e.path("o3")); r.code != ExitProtocol || !strings.Contains(r.stderr, "dataset not available") || time.Since(start) > 10*time.Second {
		t.Fatalf("unknown dataset: %d %s (%v)", r.code, r.stderr, time.Since(start))
	}
	if r := req("orders", yo, e.path("o4"), batched...); r.code != ExitProtocol || !strings.Contains(r.stderr, "hash") {
		t.Fatalf("mismatch: %d %s", r.code, r.stderr)
	}

	// Concurrent requests: one waits (busy) and both succeed.
	var wg sync.WaitGroup
	outs := make([]runOut, 3)
	for i := range outs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			outs[i] = req("customers", y, e.path(fmt.Sprintf("c%d.txt", i)))
		}()
	}
	wg.Wait()
	for i, o := range outs {
		if o.code != 0 || len(readLines(t, e.path(fmt.Sprintf("c%d.txt", i)))) != len(want) {
			t.Fatalf("concurrent %d: %d %s", i, o.code, o.stderr)
		}
	}

	// Dataset becomes unreadable/invalid while serving: missing file → retried as
	// unavailable until the window ends (exit 3); invalid content → abort (exit 4) without
	// revealing local details.
	if err := os.Rename(x, x+".bak"); err != nil {
		t.Fatal(err)
	}
	if r := req("customers", y, e.path("o5"), "--connect-retry", "1s"); r.code != ExitNetwork {
		t.Fatalf("missing dataset: %d %s", r.code, r.stderr)
	}
	if err := os.Rename(x+".bak", x); err != nil {
		t.Fatal(err)
	}
	good, _ := os.ReadFile(xo)
	if err := os.WriteFile(xo, []byte("day,order\nnot-a-number,o1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r = req("orders", yo, e.path("o6"), append(batched, "--hash", "aes")...)
	if r.code != ExitProtocol || !strings.Contains(r.stderr, "sender dataset unavailable") || strings.Contains(r.stderr, xo) {
		t.Fatalf("invalid dataset: %d %s", r.code, r.stderr)
	}
	if err := os.WriteFile(xo, good, 0o600); err != nil {
		t.Fatal(err)
	}
	if r := req("customers", y, e.path("o7")); r.code != 0 {
		t.Fatalf("after failures: %d %s", r.code, r.stderr)
	}

	out := stop()
	if out.code != 0 {
		t.Fatalf("serve exit %d: %s", out.code, out.stderr)
	}
	reps := reportsIn(t, e.path("state/reports"))
	ok, failed := 0, 0
	for _, rp := range reps {
		if rp["role"] != "sender" || rp["result_count"] != nil {
			t.Fatalf("sender report %v", rp)
		}
		if rp["exit_code"].(float64) == 0 {
			ok++
		} else {
			failed++
		}
	}
	if ok != 6 || failed != 2 {
		t.Fatalf("reports: %d ok, %d failed", ok, failed)
	}
	if !strings.Contains(out.stderr, "requester=team-a") || strings.Contains(out.stderr, "id-150") {
		t.Fatal("daemon log: requester missing or item logged")
	}
}

// A non-empty allow-list admits only listed peers; others get the same answer as for an
// unknown dataset.
func TestServeAllowList(t *testing.T) {
	e := newEnv(t)
	x := e.write("x.txt", lines(0, 50, "k"))
	for _, c := range []struct {
		allowed string
		code    int
	}{{"[somebody-else]", ExitProtocol}, {"[receiver]", 0}} {
		addr := freeAddr(t)
		cfg := e.write("s.yaml", fmt.Sprintf("listen: %q\ntls: { cert: %q, key: %q, ca: %q }\ndatasets: { d: { input: { path: %q } } }\nallowed_peers: %s\n",
			addr, e.path("certs/sender.crt"), e.path("certs/sender.key"), e.path("certs/ca.crt"), x, c.allowed))
		stop := daemon(t, "--config", cfg)
		r := run(context.Background(), append([]string{"request", "--peer", addr, "--dataset", "d", "--input", x,
			"--output", e.path("o"), "--connect-retry", "20s", "--server-name", "sender"}, e.tls("receiver", "sender")...)...)
		if r.code != c.code || (c.code != 0 && !strings.Contains(r.stderr, "dataset not available")) {
			t.Fatalf("allowed %s: exit %d %s", c.allowed, r.code, r.stderr)
		}
		stop()
	}
}

// --dev: shared dev PKI, no TLS flags, loopback only; also for the one-shot commands.
func TestDevMode(t *testing.T) {
	t.Setenv("MINIPSI_DEV_PKI", filepath.Join(t.TempDir(), "pki"))
	e := &env{t: t, dir: t.TempDir()}
	a := e.write("a.txt", lines(0, 100, "u"))
	b := e.write("b.txt", lines(50, 150, "u"))
	addr := freeAddr(t)
	stop := daemon(t, "--dev", "--name", "b", "--listen", addr, "--dataset", "users="+b)
	r := run(context.Background(), "request", "--dev", "--name", "a", "--peer", addr, "--dataset", "users",
		"--input", a, "--output", e.path("out.txt"))
	if r.code != 0 || len(readLines(t, e.path("out.txt"))) != 50 || !strings.Contains(r.stderr, "DEV MODE") {
		t.Fatalf("dev request: %d %s", r.code, r.stderr)
	}
	if out := stop(); out.code != 0 {
		t.Fatalf("serve: %d %s", out.code, out.stderr)
	}
	// One-shot commands with --dev.
	addr = freeAddr(t)
	l, d := pair(t, []string{"sender", "--dev", "--listen", addr, "--input", b},
		[]string{"receiver", "--dev", "--connect", addr, "--connect-retry", "20s", "--input", a, "--output", e.path("o2.txt")})
	if l.code != 0 || d.code != 0 || len(readLines(t, e.path("o2.txt"))) != 50 {
		t.Fatalf("one-shot dev: %d/%d %s %s", l.code, d.code, l.stderr, d.stderr)
	}

	ctx := context.Background()
	usage := [][]string{
		{"request", "--dev", "--peer", "10.0.0.1:7443", "--dataset", "d", "--input", a},
		{"request", "--dev", "--peer", "127.0.0.1:1", "--input", a},
		{"request", "--dev", "--mode", "relay", "--peer", "127.0.0.1:1", "--dataset", "d", "--input", a},
		{"request", "--dev", "--peer", "127.0.0.1:1", "--dataset", "d", "--input", a, "--cert", "c"},
		{"request", "--dev", "--name", "Bad_Name", "--peer", "127.0.0.1:1", "--dataset", "d", "--input", a},
		{"receiver", "--dev", "--connect", "127.0.0.1:1", "--input", a, "--config", e.write("r.yaml", "request: { dataset: d }\n")},
		{"serve", "--dev", "--listen", "0.0.0.0:7443", "--dataset", "d=" + a},
		{"serve", "--dev", "--listen", "127.0.0.1:1"},
		{"serve", "--dev", "--listen", "127.0.0.1:1", "--dataset", "nopath"},
		{"serve", "--dev", "--listen", "127.0.0.1:1", "--mode", "relay", "--dataset", "d=" + a},
		{"serve", "--config", e.path("missing.yaml")},
	}
	for _, args := range usage {
		if r := run(ctx, args...); r.code != ExitUsage {
			t.Errorf("%v: exit %d, want 1 (%s)", args, r.code, r.stderr)
		}
	}
	// A dataset that cannot be read fails at startup with an input error.
	if r := run(ctx, "serve", "--dev", "--listen", freeAddr(t), "--dataset", "d="+e.path("none.txt")); r.code != ExitInput {
		t.Fatalf("missing dataset at startup: %d %s", r.code, r.stderr)
	}
	// Listening on a busy port is a network error.
	busy := freeAddr(t)
	stop = daemon(t, "--dev", "--listen", busy, "--dataset", "d="+a)
	time.Sleep(300 * time.Millisecond)
	if r := run(ctx, "serve", "--dev", "--listen", busy, "--dataset", "d="+a); r.code != ExitNetwork {
		t.Fatalf("port in use: %d %s", r.code, r.stderr)
	}
	stop()
}
