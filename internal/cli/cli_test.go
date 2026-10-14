package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/XSpring/minipsi/internal/transport"
)

type env struct {
	t   *testing.T
	dir string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, dir: t.TempDir()}
	var out bytes.Buffer
	if code := Run(context.Background(), []string{"certs", "dev", "--out", e.path("certs")}, &out, &out); code != 0 {
		t.Fatalf("certs dev: %d %s", code, out.String())
	}
	return e
}

func (e *env) path(name string) string { return filepath.Join(e.dir, name) }

func (e *env) write(name, data string) string {
	e.t.Helper()
	p := e.path(name)
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		e.t.Fatal(err)
	}
	return p
}

func (e *env) tls(name, peer string) []string {
	return []string{"--cert", e.path("certs/" + name + ".crt"), "--key", e.path("certs/" + name + ".key"),
		"--ca", e.path("certs/ca.crt"), "--peer-name", peer}
}

func freeAddr(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

type runOut struct {
	code           int
	stdout, stderr string
}

func run(ctx context.Context, args ...string) runOut {
	var o, e bytes.Buffer
	code := Run(ctx, args, &o, &e)
	return runOut{code, o.String(), e.String()}
}

// pair runs the listener first and the dialer with retry, returning both outcomes.
func pair(t *testing.T, listener, dialer []string) (l, d runOut) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); l = run(ctx, listener...) }()
	go func() { defer wg.Done(); d = run(ctx, dialer...) }()
	wg.Wait()
	return l, d
}

func lines(n0, n1 int, prefix string) string {
	var b strings.Builder
	for i := n0; i < n1; i++ {
		fmt.Fprintf(&b, "%s%d\n", prefix, i)
	}
	return b.String()
}

func readLines(t *testing.T, path string) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(b) == 0 {
		out = nil
	}
	sort.Strings(out)
	return out
}

// Receiver listens (YAML config + flag overrides), sender dials.
func TestReceiverListensWithConfigFile(t *testing.T) {
	e := newEnv(t)
	addr := freeAddr(t)
	y := e.write("y.txt", lines(0, 300, "id-"))
	x := e.write("x.txt", lines(250, 400, "id-")+"id-7\nid-7\n")
	cfg := e.write("r.yaml", fmt.Sprintf(`
role: receiver
listen: %q
input: { path: %q }
output: %q
protocol: { hash: aes, padding: { extra: 1, min: 64, multiple: 16 } }
tls: { cert: %q, key: %q, ca: %q, peer_name: sender }
report: %q
log: { level: debug }
`, addr, y, e.path("out.txt"), e.path("certs/receiver.crt"), e.path("certs/receiver.key"), e.path("certs/ca.crt"), e.path("r.json")))
	recv := []string{"receiver", "--config", cfg, "--hash", "sha256"} // flag overrides the file
	send := append([]string{"sender", "--connect", addr, "--connect-retry", "20s", "--server-name", "receiver",
		"--input", x, "--report", e.path("s.json"), "--pad-multiple", "64"}, e.tls("sender", "receiver")...)
	r, s := pair(t, recv, send)
	if r.code != 0 || s.code != 0 {
		t.Fatalf("exit %d/%d\nreceiver: %s\nsender: %s", r.code, s.code, r.stderr, s.stderr)
	}
	want := strings.Split(strings.TrimSuffix(lines(250, 300, "id-")+"id-7\n", "\n"), "\n")
	sort.Strings(want)
	if got := readLines(t, e.path("out.txt")); !slices.Equal(got, want) {
		t.Fatalf("intersection %v", got)
	}
	var rep map[string]any
	b, _ := os.ReadFile(e.path("r.json"))
	if err := json.Unmarshal(b, &rep); err != nil {
		t.Fatal(err)
	}
	if rep["hash"] != "sha256" || rep["result_count"].(float64) != 51 || rep["exit_code"].(float64) != 0 ||
		rep["sid"] == "" || rep["role"] != "receiver" {
		t.Fatalf("report %s", b)
	}
	b, _ = os.ReadFile(e.path("s.json"))
	rep = map[string]any{}
	if err := json.Unmarshal(b, &rep); err != nil || rep["dedup_removed"].(float64) != 1 || rep["result_count"] != nil ||
		rep["padding"].(map[string]any)["multiple"].(float64) != 64 {
		t.Fatalf("sender report %s", b)
	}
	// Debug logs must not contain items.
	for _, it := range []string{"id-7", "id-260"} {
		if strings.Contains(r.stderr, it) || strings.Contains(s.stderr, it) {
			t.Fatalf("item %s logged", it)
		}
	}
}

// Batched CSV, sender listens, receiver dials; CSV output.
func TestBatchedCSV(t *testing.T) {
	e := newEnv(t)
	addr := freeAddr(t)
	var ys, xs strings.Builder
	ys.WriteString("inst,item\n")
	xs.WriteString("item;inst\n")
	for id := 1; id <= 20; id++ {
		for i := 0; i < 30; i++ {
			fmt.Fprintf(&ys, "%d,u%d-%d\n", id, id, i)
		}
		for i := 25; i < 50; i++ {
			fmt.Fprintf(&xs, "u%d-%d;%d\n", id, i, id)
		}
	}
	y := e.write("y.csv", ys.String())
	x := e.write("x.csv", strings.ReplaceAll(xs.String(), ";", ","))
	recv := append([]string{"receiver", "--connect", addr, "--connect-retry", "20s", "--server-name", "sender",
		"--input", y, "--format", "csv", "--header", "--batched", "--instance-column", "inst", "--column", "item",
		"--output", e.path("out.csv"), "--log-format", "json"}, e.tls("receiver", "sender")...)
	send := append([]string{"sender", "--listen", addr, "--input", x, "--format", "csv", "--header",
		"--batched", "--instance-column", "1", "--column", "0", "--pad-min", "40"}, e.tls("sender", "receiver")...)
	s, r := pair(t, send, recv)
	if r.code != 0 || s.code != 0 {
		t.Fatalf("exit %d/%d\nreceiver: %s\nsender: %s", r.code, s.code, r.stderr, s.stderr)
	}
	got := readLines(t, e.path("out.csv"))
	if len(got) != 20*5 || !slices.Contains(got, "7,u7-27") || !strings.HasPrefix(r.stderr, "{") {
		t.Fatalf("batched result (%d rows) %v", len(got), got[:3])
	}
}

func TestExitCodes(t *testing.T) {
	e := newEnv(t)
	y := e.write("y.txt", "a\nb\n")
	ctx := context.Background()
	base := func(role string, extra ...string) []string {
		return append(append([]string{role, "--input", y}, e.tls(role, "x")...), extra...)
	}
	cases := []struct {
		name string
		args []string
		code int
	}{
		{"unknown flag", []string{"receiver", "--bogus"}, ExitUsage},
		{"unknown command", []string{"frobnicate"}, ExitUsage},
		{"bad flag value", []string{"receiver", "--max-frame", "lots"}, ExitUsage},
		{"no endpoint", base("receiver"), ExitUsage},
		{"missing cert files", []string{"receiver", "--connect", "127.0.0.1:1", "--input", y, "--cert", "a", "--key", "b", "--ca", "c"}, ExitUsage},
		{"missing config", []string{"receiver", "--config", e.path("nope.yaml")}, ExitUsage},
		{"config role mismatch", []string{"sender", "--config", e.write("c.yaml", "role: receiver\n")}, ExitUsage},
		{"insecure without ack", []string{"receiver", "--connect", "127.0.0.1:1", "--input", y, "--insecure-dev"}, ExitUsage},
		{"missing input", base("receiver", "--connect", "127.0.0.1:1", "--input", e.path("none.txt")), ExitInput},
		{"own input over limit", base("sender", "--connect", "127.0.0.1:1", "--max-instance-size", "1"), ExitInput},
		{"connection refused", base("receiver", "--connect", freeAddr(t)), ExitNetwork},
		{"listen on bad address", base("receiver", "--listen", "256.0.0.1:1"), ExitNetwork},
	}
	for _, c := range cases {
		if got := run(ctx, c.args...); got.code != c.code {
			t.Errorf("%s: exit %d, want %d (%s)", c.name, got.code, c.code, got.stderr)
		}
	}
}

// Parameter mismatch: both parties exit 4 (protocol violation / peer abort).
func TestMismatchExit4(t *testing.T) {
	e := newEnv(t)
	addr := freeAddr(t)
	y := e.write("y.txt", "a\n")
	l, d := pair(t,
		append([]string{"sender", "--listen", addr, "--input", y, "--lambda", "64"}, e.tls("sender", "receiver")...),
		append([]string{"receiver", "--connect", addr, "--connect-retry", "20s", "--server-name", "sender", "--input", y, "--output", e.path("o")}, e.tls("receiver", "sender")...))
	if l.code != ExitProtocol || d.code != ExitProtocol {
		t.Fatalf("exit %d/%d: %s / %s", l.code, d.code, l.stderr, d.stderr)
	}
	if _, err := os.Stat(e.path("o")); !os.IsNotExist(err) {
		t.Fatal("output written on failure")
	}
}

// Interrupting a waiting listener exits 4 and still writes the report; an overall
// timeout exits 3.
func TestInterruptWritesReport(t *testing.T) {
	e := newEnv(t)
	y := e.write("y.txt", "a\n")
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(500*time.Millisecond, cancel) // what SIGINT/SIGTERM does via signal.NotifyContext
	got := run(ctx, append([]string{"receiver", "--listen", freeAddr(t), "--input", y, "--report", e.path("r.json")}, e.tls("receiver", "x")...)...)
	if got.code != ExitProtocol {
		t.Fatalf("exit %d: %s", got.code, got.stderr)
	}
	b, err := os.ReadFile(e.path("r.json"))
	if err != nil || !strings.Contains(string(b), `"exit_code": 4`) || !strings.Contains(string(b), "interrupted") {
		t.Fatalf("report: %v %s", err, b)
	}
	got = run(context.Background(), append([]string{"receiver", "--listen", freeAddr(t), "--input", y, "--timeout", "300ms"}, e.tls("receiver", "x")...)...)
	if got.code != ExitNetwork {
		t.Fatalf("timeout exit %d: %s", got.code, got.stderr)
	}
}

func TestInsecureDevMode(t *testing.T) {
	t.Setenv(transport.InsecureEnv, "1")
	e := &env{t: t, dir: t.TempDir()}
	addr := freeAddr(t)
	y := e.write("y.txt", "a\nb\nc\n")
	x := e.write("x.txt", "c\nd\n")
	l, d := pair(t,
		[]string{"sender", "--listen", addr, "--input", x, "--insecure-dev"},
		[]string{"receiver", "--connect", addr, "--connect-retry", "20s", "--input", y, "--insecure-dev", "--output", e.path("o")})
	if l.code != 0 || d.code != 0 || !strings.Contains(d.stderr, "PLAINTEXT") {
		t.Fatalf("exit %d/%d: %s", l.code, d.code, d.stderr)
	}
	if got := readLines(t, e.path("o")); !slices.Equal(got, []string{"c"}) {
		t.Fatalf("result %v", got)
	}
}

func TestToolCommands(t *testing.T) {
	v := run(context.Background(), "version")
	if v.code != 0 || !strings.Contains(v.stdout, "protocol: v1") {
		t.Fatalf("version: %+v", v)
	}
	dir := t.TempDir()
	c := run(context.Background(), "certs", "dev", "--out", dir, "--name", "a,b", "--host", "psi.example,10.0.0.1")
	if c.code != 0 {
		t.Fatalf("certs: %+v", c)
	}
	for _, f := range []string{"ca.crt", "a.crt", "a.key", "b.crt", "b.key"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatal(err)
		}
	}
	if again := run(context.Background(), "certs", "dev", "--out", dir); again.code != ExitUsage {
		t.Fatalf("overwrite: %+v", again)
	}
	if bad := run(context.Background(), "certs", "dev", "--out", t.TempDir(), "--validity", "-1h"); bad.code != ExitUsage {
		t.Fatalf("negative validity: %+v", bad)
	}
	b := run(context.Background(), "bench", "--n1", "300", "--n2", "200", "--instances", "3", "--overlap", "0.5")
	if b.code != 0 || !strings.Contains(b.stdout, "intersection") || !strings.Contains(b.stdout, "receiver: total") {
		t.Fatalf("bench: %+v", b)
	}
	for _, line := range strings.Split(b.stdout, "\n") {
		if strings.HasPrefix(line, "intersection") && !strings.HasSuffix(strings.TrimSpace(line), "99") {
			t.Fatalf("bench intersection line %q", line)
		}
	}
	if bad := run(context.Background(), "bench", "--overlap", "2"); bad.code != ExitUsage {
		t.Fatalf("bench validation: %+v", bad)
	}
	if bad := run(context.Background(), "bench", "--hash", "md5"); bad.code != ExitUsage {
		t.Fatalf("bench hash: %+v", bad)
	}
}
