package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validServe() Serve {
	s := DefaultServe()
	s.Listen = "0.0.0.0:7443"
	s.TLS = TLS{Cert: "c", Key: "k", CA: "a"}
	s.Datasets = map[string]Dataset{"customers": {Input: Input{Path: "/data/c.txt"}}}
	return s
}

func TestServeLoadAndSessionConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "serve.yaml")
	data := `
listen: 0.0.0.0:7443
mode: direct
tls: { cert: b.crt, key: b.key, ca: ca.crt }
state_dir: /var/lib/minipsi
datasets:
  customers:
    input: { path: /data/customers.txt }
    protocol: { sender_padding: { min: 1024 } }
  daily-orders:
    input: { path: /data/orders.csv, format: csv, header: true, batched: true, instance_column: day, column: order_id }
    protocol: { hash: aes, lambda: 64 }
allowed_peers: [psi-a.example.org]
timeouts: { session: 1h, phase: 2m }
memory_limit: 1GiB
log: { format: json }
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	s := DefaultServe()
	if err := s.Load(path); err != nil {
		t.Fatal(err)
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if !s.MemoryLimitExplicit() || strings.Join(s.DatasetNames(), ",") != "customers,daily-orders" {
		t.Fatalf("loaded %+v", s)
	}
	c := s.SessionConfig("customers")
	if c.Role != "sender" || c.Input.Format != "lines" || c.Input.Column != "0" || c.Protocol.Hash != "sha256" ||
		c.Protocol.Lambda != 40 || c.Protocol.SenderPadding != (Padding{0, 1024, 1}) || c.Timeouts.Total.D() != time.Hour ||
		c.Timeouts.Phase.D() != 2*time.Minute || c.Timeouts.Handshake.D() != 30*time.Second || c.TLS.Cert != "b.crt" ||
		!c.MemoryLimitExplicit() || c.Log.Format != "json" {
		t.Fatalf("session config %+v", c)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	o := s.SessionConfig("daily-orders")
	if o.Protocol.Hash != "aes" || o.Protocol.Lambda != 64 || !o.Input.Batched || o.Input.InstanceColumn != "day" {
		t.Fatalf("orders %+v", o)
	}
	if !s.Allowed([]string{"x", "psi-a.example.org"}) || s.Allowed([]string{"psi-b.example.org"}) || s.Allowed(nil) {
		t.Fatal("allow-list")
	}
	s.AllowedPeers = nil
	if !s.Allowed(nil) {
		t.Fatal("empty allow-list must accept all")
	}
	// Unknown keys rejected.
	if err := os.WriteFile(path, []byte("listen: x\ndatasets: { a: { input: { path: p }, color: red } }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s = DefaultServe()
	if err := s.Load(path); err == nil {
		t.Fatal("unknown key accepted")
	}
	if err := s.Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("missing file accepted")
	}
}

func TestServeValidate(t *testing.T) {
	if err := func() error { s := validServe(); return s.Validate() }(); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(s *Serve){
		"no listen":     func(s *Serve) { s.Listen = "" },
		"relay":         func(s *Serve) { s.Mode = "relay" },
		"mode":          func(s *Serve) { s.Mode = "pigeon" },
		"no tls":        func(s *Serve) { s.TLS = TLS{} },
		"dev non-loop":  func(s *Serve) { s.TLS = TLS{Dev: true} },
		"no datasets":   func(s *Serve) { s.Datasets = nil },
		"bad name":      func(s *Serve) { s.Datasets["a/b"] = Dataset{Input: Input{Path: "x"}} },
		"stdin dataset": func(s *Serve) { s.Datasets["x"] = Dataset{Input: Input{Path: "-"}} },
		"bad hash": func(s *Serve) {
			s.Datasets["x"] = Dataset{Input: Input{Path: "p"}, Protocol: DatasetProtocol{Hash: "md5"}}
		},
		"bad padding": func(s *Serve) {
			s.Datasets["x"] = Dataset{Input: Input{Path: "p"}, Protocol: DatasetProtocol{SenderPadding: Padding{Min: -1}}}
		},
		"batched lines":   func(s *Serve) { s.Datasets["x"] = Dataset{Input: Input{Path: "p", Batched: true}} },
		"session timeout": func(s *Serve) { s.Timeouts.Session = -1 },
		"empty peer":      func(s *Serve) { s.AllowedPeers = []string{" "} },
		"backend":         func(s *Serve) { s.NTTBackend = "x" },
	}
	for name, mut := range cases {
		s := validServe()
		mut(&s)
		if s.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Shared errors are reported once, not once per dataset.
	s := validServe()
	s.Datasets["second"] = Dataset{Input: Input{Path: "p"}}
	s.Workers = -1
	if err := s.Validate(); err == nil || strings.Count(err.Error(), "workers") != 1 {
		t.Fatalf("dedup: %v", err)
	}
	// Dev on loopback is fine.
	s = validServe()
	s.Listen, s.TLS = "127.0.0.1:7443", TLS{Dev: true, Name: "b"}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if !ValidDatasetName("a.b_c-1") || ValidDatasetName("") || ValidDatasetName("-x") {
		t.Fatal("dataset names")
	}
}

func TestPartyDevModeRequest(t *testing.T) {
	cases := map[string]func(c *Config){
		"relay":            func(c *Config) { c.Mode = "relay" },
		"dev+files":        func(c *Config) { c.TLS.Dev = true },
		"dev non-loopback": func(c *Config) { c.TLS = TLS{Dev: true} },
		"name without dev": func(c *Config) { c.TLS.Name = "a" },
		"dataset+listen": func(c *Config) {
			c.Connect, c.Listen, c.Request.Dataset = "", "127.0.0.1:1", "d"
		},
		"identity alone": func(c *Config) { c.Request.Identity = "me" },
	}
	for name, mut := range cases {
		c := valid()
		mut(&c)
		if c.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	c := valid()
	c.Connect, c.TLS, c.Request = "localhost:7443", TLS{Dev: true, Name: "a"}, Request{Dataset: "d", Identity: "a"}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}
