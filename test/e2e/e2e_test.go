//go:build e2e

// Package e2e runs the minipsi binary as separate processes over real mTLS on localhost.
// Run with: go test -tags e2e ./test/e2e   (builds the binary itself).
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var bin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "minipsi-e2e-")
	if err != nil {
		panic(err)
	}
	bin = filepath.Join(dir, "minipsi")
	build := exec.Command("go", "build", "-o", bin, "../../cmd/minipsi")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// syncBuffer is a bytes.Buffer safe for the process's writer and the test's readers.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type proc struct {
	cmd    *exec.Cmd
	stderr syncBuffer
	done   chan struct{}
	code   int
}

func start(t *testing.T, dir string, args ...string) *proc {
	t.Helper()
	p := &proc{cmd: exec.Command(bin, args...), done: make(chan struct{})}
	p.cmd.Dir = dir
	p.cmd.Stderr = &p.stderr
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = p.cmd.Wait()
		p.code = p.cmd.ProcessState.ExitCode()
		close(p.done)
	}()
	return p
}

func (p *proc) wait(t *testing.T, timeout time.Duration) int {
	t.Helper()
	select {
	case <-p.done:
		return p.code
	case <-time.After(timeout):
		p.cmd.Process.Kill()
		t.Fatalf("process did not exit within %v; stderr:\n%s", timeout, p.stderr.String())
	}
	return -1
}

func setup(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	out, err := exec.Command(bin, "certs", "dev", "--out", filepath.Join(dir, "certs")).CombinedOutput()
	if err != nil {
		t.Fatalf("certs dev: %v %s", err, out)
	}
	return dir
}

func freeAddr(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func tlsArgs(role, peer string) []string {
	return []string{"--cert", "certs/" + role + ".crt", "--key", "certs/" + role + ".key", "--ca", "certs/ca.crt", "--peer-name", peer}
}

func writeLines(t *testing.T, path string, lo, hi int) {
	var b strings.Builder
	for i := lo; i < hi; i++ {
		fmt.Fprintf(&b, "item-%d\n", i)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

// partyArgs returns the args for role in a run where listenRole listens.
func partyArgs(role, listenRole, addr string, extra ...string) []string {
	peer := map[string]string{"receiver": "sender", "sender": "receiver"}[role]
	args := []string{role}
	if role == listenRole {
		args = append(args, "--listen", addr)
	} else {
		args = append(args, "--connect", addr, "--connect-retry", "30s", "--server-name", peer)
	}
	return append(append(args, tlsArgs(role, peer)...), extra...)
}

func sortedLines(t *testing.T, path string) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	l := strings.Fields(string(b))
	sort.Strings(l)
	return l
}

func TestMatrix(t *testing.T) {
	for _, listen := range []string{"sender", "receiver"} {
		for _, hash := range []string{"sha256", "aes"} {
			t.Run(listen+"-listens/"+hash, func(t *testing.T) {
				dir := setup(t)
				addr := freeAddr(t)
				writeLines(t, filepath.Join(dir, "y.txt"), 0, 1500)
				writeLines(t, filepath.Join(dir, "x.txt"), 1000, 4000)
				extra := []string{"--hash", hash}
				r := start(t, dir, partyArgs("receiver", listen, addr, append(extra, "--input", "y.txt", "--output", "out.txt", "--report", "r.json")...)...)
				s := start(t, dir, partyArgs("sender", listen, addr, append(extra, "--input", "x.txt", "--ntt-backend", "lattigo")...)...)
				if rc, sc := r.wait(t, 3*time.Minute), s.wait(t, time.Minute); rc != 0 || sc != 0 {
					t.Fatalf("exit %d/%d\nreceiver:\n%s\nsender:\n%s", rc, sc, r.stderr.String(), s.stderr.String())
				}
				got := sortedLines(t, filepath.Join(dir, "out.txt"))
				var want []string
				for i := 1000; i < 1500; i++ {
					want = append(want, fmt.Sprintf("item-%d", i))
				}
				sort.Strings(want)
				if !slices.Equal(got, want) {
					t.Fatalf("intersection: %d items", len(got))
				}
				var rep struct {
					ResultCount int    `json:"result_count"`
					Hash        string `json:"hash"`
					ExitCode    int    `json:"exit_code"`
				}
				b, _ := os.ReadFile(filepath.Join(dir, "r.json"))
				if err := json.Unmarshal(b, &rep); err != nil || rep.ResultCount != 500 || rep.Hash != hash {
					t.Fatalf("report %s", b)
				}
			})
		}
	}
}

// SIGTERM mid-run: the interrupted party exits 4 and the peer receives ERR_CANCELLED (exit 4).
func TestSigtermMidRun(t *testing.T) {
	dir := setup(t)
	addr := freeAddr(t)
	writeLines(t, filepath.Join(dir, "y.txt"), 0, 1<<14)
	writeLines(t, filepath.Join(dir, "x.txt"), 0, 1<<14)
	s := start(t, dir, partyArgs("sender", "sender", addr, "--input", "x.txt")...)
	r := start(t, dir, partyArgs("receiver", "sender", addr, "--input", "y.txt", "--output", "out.txt")...)
	waitFor(t, &s.stderr, "peer connected", 30*time.Second)
	time.Sleep(300 * time.Millisecond)
	if err := s.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if sc := s.wait(t, time.Minute); sc != 4 {
		t.Fatalf("sender exit %d: %s", sc, s.stderr.String())
	}
	if rc := r.wait(t, time.Minute); rc != 4 || !strings.Contains(r.stderr.String(), "ERR_CANCELLED") {
		t.Fatalf("receiver exit %d: %s", rc, r.stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "out.txt")); !os.IsNotExist(err) {
		t.Fatal("output written after cancellation")
	}
}

// A peer that dies without an Abort leaves the survivor with a network error (exit 3).
func TestPeerKilled(t *testing.T) {
	dir := setup(t)
	addr := freeAddr(t)
	writeLines(t, filepath.Join(dir, "y.txt"), 0, 1<<14)
	writeLines(t, filepath.Join(dir, "x.txt"), 0, 1<<14)
	s := start(t, dir, partyArgs("sender", "sender", addr, "--input", "x.txt")...)
	r := start(t, dir, partyArgs("receiver", "sender", addr, "--input", "y.txt", "--output", "out.txt")...)
	waitFor(t, &s.stderr, "peer connected", 30*time.Second)
	time.Sleep(300 * time.Millisecond)
	s.cmd.Process.Kill()
	s.wait(t, time.Minute)
	if rc := r.wait(t, time.Minute); rc != 3 {
		t.Fatalf("receiver exit %d: %s", rc, r.stderr.String())
	}
}

func waitFor(t *testing.T, buf *syncBuffer, substr string, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for !strings.Contains(buf.String(), substr) {
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %q; got:\n%s", substr, buf.String())
		case <-time.After(50 * time.Millisecond):
		}
	}
}
