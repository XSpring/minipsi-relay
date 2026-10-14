package devcerts

import (
	"crypto/x509"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
)

func TestEnsureDev(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pki")
	// Concurrent first use by several parties agrees on one CA.
	names := []string{"a", "b", "a", "sender-1", "b"}
	files := make([]DevFiles, len(names))
	errs := make([]error, len(names))
	var wg sync.WaitGroup
	for i, n := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			files[i], errs[i] = EnsureDev(dir, n)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("%s: %v", names[i], err)
		}
	}
	ca, err := readCert(files[0].CA)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	for i, f := range files {
		if f.CA != files[0].CA {
			t.Fatal("different CA paths")
		}
		c, err := readCert(f.Cert)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Verify(x509.VerifyOptions{Roots: pool, DNSName: "localhost",
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
			t.Fatalf("%s: %v", names[i], err)
		}
		if !slices.Contains(c.DNSNames, names[i]) || len(c.IPAddresses) != 2 {
			t.Fatalf("%s: SANs %v %v", names[i], c.DNSNames, c.IPAddresses)
		}
		st, err := os.Stat(f.Key)
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("key mode %v %v", st.Mode(), err)
		}
	}
	// Reuse: same files, same certificate.
	before, _ := os.ReadFile(files[1].Cert)
	again, err := EnsureDev(dir, "b")
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(again.Cert)
	if string(before) != string(after) {
		t.Fatal("certificate regenerated")
	}
	if st, err := os.Stat(dir); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v %v", st.Mode(), err)
	}
	// A party certificate from another CA is detected.
	other := filepath.Join(t.TempDir(), "other")
	o, err := EnsureDev(other, "c")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "parties", "c"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, p := range [][2]string{{o.Cert, "cert.pem"}, {o.Key, "key.pem"}} {
		data, _ := os.ReadFile(p[0])
		if err := os.WriteFile(filepath.Join(dir, "parties", "c", p[1]), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := EnsureDev(dir, "c"); err == nil {
		t.Fatal("foreign certificate accepted")
	}
	for _, bad := range []string{"", "A", "a_b", "-a", "a.b", "../x"} {
		if _, err := EnsureDev(dir, bad); err == nil {
			t.Errorf("name %q accepted", bad)
		}
	}
}

func TestDefaultDevDir(t *testing.T) {
	t.Setenv(DevPKIEnv, "/x/pki")
	if d, _ := DefaultDevDir(); d != "/x/pki" {
		t.Fatal(d)
	}
	t.Setenv(DevPKIEnv, "")
	t.Setenv("XDG_STATE_HOME", "/state")
	if d, _ := DefaultDevDir(); d != "/state/minipsi/dev-pki" {
		t.Fatal(d)
	}
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "/home/u")
	if d, _ := DefaultDevDir(); d != "/home/u/.local/state/minipsi/dev-pki" {
		t.Fatal(d)
	}
}

func TestIsLoopback(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:1": true, "localhost:7443": true, "[::1]:1": true, "127.5.5.5:9": true,
		":7443": false, "0.0.0.0:1": false, "10.0.0.1:1": false, "example.org:1": false, "nope": false,
	} {
		if IsLoopback(addr) != want {
			t.Errorf("%s: want %v", addr, want)
		}
	}
}
