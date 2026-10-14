package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func valid() Config {
	c := Default()
	c.Role = "receiver"
	c.Connect = "host:1"
	c.TLS = TLS{Cert: "c", Key: "k", CA: "a"}
	return c
}

func TestDefaultsValidate(t *testing.T) {
	c := valid()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.MemoryLimit != 2<<30 || c.PolyCache != 256<<20 || c.Limits.MaxFrame != 4<<20 {
		t.Fatal("unexpected defaults")
	}
	if c.MemoryLimitExplicit() {
		t.Fatal("default memory limit marked explicit")
	}
}

func TestValidateErrors(t *testing.T) {
	cases := map[string]func(c *Config){
		"role":          func(c *Config) { c.Role = "both" },
		"no endpoint":   func(c *Config) { c.Connect = "" },
		"two endpoints": func(c *Config) { c.Listen = ":1" },
		"no input":      func(c *Config) { c.Input.Path = "" },
		"lines batched": func(c *Config) { c.Input.Batched = true },
		"csv batched":   func(c *Config) { c.Input.Format, c.Input.Batched = "csv", true },
		"format":        func(c *Config) { c.Input.Format = "xml" },
		"column":        func(c *Config) { c.Input.Column = "" },
		"hash":          func(c *Config) { c.Protocol.Hash = "md5" },
		"lambda":        func(c *Config) { c.Protocol.Lambda = 39 },
		"padding":       func(c *Config) { c.Protocol.Padding.Multiple = 0 },
		"sender padding": func(c *Config) {
			c.Role, c.Output, c.Protocol.SenderPadding.Min = "sender", "", -1
		},
		"receiver sets sender_padding": func(c *Config) { c.Protocol.SenderPadding.Min = 16 },
		"sender sets padding":          func(c *Config) { c.Role, c.Output, c.Protocol.Padding.Min = "sender", "", 16 },
		"tls missing":                  func(c *Config) { c.TLS.CA = "" },
		"insecure+tls":                 func(c *Config) { c.TLS.InsecureDev = true },
		"frame":                        func(c *Config) { c.Limits.MaxFrame = 1000 },
		"limits":                       func(c *Config) { c.Limits.MaxInstances = 0 },
		"timeouts":                     func(c *Config) { c.Timeouts.Phase = 0 },
		"workers":                      func(c *Config) { c.Workers = -1 },
		"backend":                      func(c *Config) { c.NTTBackend = "fftw" },
		"memory":                       func(c *Config) { c.MemoryLimit = -1 },
		"log format":                   func(c *Config) { c.Log.Format = "xml" },
		"log level":                    func(c *Config) { c.Log.Level = "loud" },
		"sender output":                func(c *Config) { c.Role, c.Output = "sender", "out.txt" },
	}
	for name, mut := range cases {
		c := valid()
		mut(&c)
		if c.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	c := valid()
	c.TLS = TLS{InsecureDev: true}
	if err := c.Validate(); err != nil {
		t.Fatalf("insecure-dev alone: %v", err)
	}
	c = valid()
	c.Role, c.Output, c.Protocol.SenderPadding = "sender", "", Padding{Extra: 2, Min: 64, Multiple: 16}
	if err := c.Validate(); err != nil {
		t.Fatalf("sender with sender_padding: %v", err)
	}
}

func TestLoadYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	data := `
role: receiver
connect: "psi-b.example.org:7443"
input: { path: y.txt, format: csv, column: email, header: true }
output: out.txt
protocol: { hash: aes, lambda: 64, padding: { extra: 0, min: 16, multiple: 8 } }
tls: { cert: r.crt, key: r.key, ca: ca.crt, peer_name: sender.example.org }
limits: { max_frame: 8MiB, max_instance_size: 1000 }
timeouts: { handshake: 5s, phase: 2m, total: 1h, connect_retry: 30s }
workers: 3
ntt_backend: lattigo
memory_limit: 1GiB
poly_cache: 0
report: run.json
log: { format: json, level: debug }
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	c := Default()
	if err := c.Load(path); err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.Input.Column != "email" || !c.Input.Header || c.Protocol.Hash != "aes" || c.Protocol.Lambda != 64 ||
		c.Protocol.Padding != (Padding{0, 16, 8}) || c.Limits.MaxFrame != 8<<20 || c.Limits.MaxInstanceSize != 1000 ||
		c.Limits.MaxTotalItems != 1<<24 || c.Timeouts.Total.D() != time.Hour || c.Timeouts.ConnectRetry.D() != 30*time.Second ||
		c.Workers != 3 || c.NTTBackend != "lattigo" || c.MemoryLimit != 1<<30 || c.PolyCache != 0 || c.Log.Level != "debug" {
		t.Fatalf("loaded config wrong: %+v", c)
	}
	if !c.MemoryLimitExplicit() {
		t.Fatal("memory_limit from file not marked explicit")
	}

	// Unknown keys, bad sizes/durations and missing files are errors; empty files are fine.
	for name, body := range map[string]string{
		"unknown key": "rolee: receiver\n",
		"bad size":    "limits: { max_frame: 4 parsecs }\n",
		"bad dur":     "timeouts: { phase: soon }\n",
		"bad yaml":    "input: [\n",
	} {
		p := filepath.Join(dir, strings.ReplaceAll(name, " ", "_")+".yaml")
		os.WriteFile(p, []byte(body), 0o600)
		c := Default()
		if err := c.Load(p); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	empty := filepath.Join(dir, "empty.yaml")
	os.WriteFile(empty, nil, 0o600)
	c = Default()
	if err := c.Load(empty); err != nil {
		t.Fatalf("empty file: %v", err)
	}
	if err := c.Load(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Fatal("missing file accepted")
	}
}

func TestSizeParsing(t *testing.T) {
	good := map[string]Size{"0": 0, "1024": 1024, "4MiB": 4 << 20, "2GiB": 2 << 30, "64KiB": 64 << 10,
		"1 TiB": 1 << 40, "5KB": 5000, "3MB": 3e6, "1GB": 1e9, "2TB": 2e12, "7B": 7}
	for s, want := range good {
		got, err := ParseSize(s)
		if err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", s, got, err, want)
		}
	}
	for _, s := range []string{"", "-1", "1.5GiB", "4 parsecs", "999999999999TiB"} {
		if _, err := ParseSize(s); err == nil {
			t.Errorf("ParseSize(%q) accepted", s)
		}
	}
	for s, want := range map[Size]string{0: "0", 4 << 20: "4MiB", 2 << 30: "2GiB", 64 << 10: "64KiB", 1000: "1000"} {
		if s.String() != want {
			t.Errorf("String(%d) = %s", int64(s), s.String())
		}
	}
	var s Size
	if s.Set("8MiB") != nil || s != 8<<20 || s.Type() != "size" || s.Set("x") == nil {
		t.Fatal("pflag interface")
	}
	var d Duration
	if d.Set("90s") != nil || d.D() != 90*time.Second || d.String() != "1m30s" || d.Type() != "duration" || d.Set("x") == nil {
		t.Fatal("duration flag interface")
	}
}
