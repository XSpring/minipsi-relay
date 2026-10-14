// Package config holds the CLI configuration: defaults, strict YAML
// loading, flag overrides (an explicitly set flag beats the file) and validation.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/XSpring/minipsi/internal/devcerts"
)

// Config is the full configuration of one minipsi party.
type Config struct {
	Role        string   `yaml:"role"` // "receiver" or "sender" (set by the subcommand)
	Mode        string   `yaml:"mode"` // communication mode: direct (default) | relay (not implemented)
	Listen      string   `yaml:"listen"`
	Connect     string   `yaml:"connect"`
	Request     Request  `yaml:"request"` // daemon request (minipsi request)
	Input       Input    `yaml:"input"`
	Output      string   `yaml:"output"`
	Protocol    Protocol `yaml:"protocol"`
	TLS         TLS      `yaml:"tls"`
	Limits      Limits   `yaml:"limits"`
	Timeouts    Timeouts `yaml:"timeouts"`
	Workers     int      `yaml:"workers"`
	NTTBackend  string   `yaml:"ntt_backend"`
	MemoryLimit Size     `yaml:"memory_limit"` // soft GC limit; 0 = off
	PolyCache   Size     `yaml:"poly_cache"`   // per-tree transform cache; 0 = off
	Report      string   `yaml:"report"`
	Log         Log      `yaml:"log"`
	memLimitSet bool     // memory_limit given in the file or by flag
}

// Request names what a receiver asks a peer daemon for.
type Request struct {
	Dataset  string `yaml:"dataset"`  // dataset name on the peer daemon
	Identity string `yaml:"identity"` // self-declared identity; default: first SAN of the certificate
}

// Input describes where and how items are read.
type Input struct {
	Path           string `yaml:"path"`   // "-" = stdin
	Format         string `yaml:"format"` // lines | csv
	Column         string `yaml:"column"` // CSV item column: index or header name
	InstanceColumn string `yaml:"instance_column"`
	Header         bool   `yaml:"header"` // CSV has a header row
	Batched        bool   `yaml:"batched"`
}

// Protocol holds parameters that must match the peer (hash, lambda) and the party-local
// padding policies: Padding for the receiver, SenderPadding for the sender.
// Each role rejects a non-default policy of the other role, so a privacy
// setting is never silently ignored.
type Protocol struct {
	Hash          string  `yaml:"hash"`
	Lambda        int     `yaml:"lambda"`
	Padding       Padding `yaml:"padding"`
	SenderPadding Padding `yaml:"sender_padding"`
}

// Padding is a padding policy.
type Padding struct {
	Extra    int `yaml:"extra"`
	Min      int `yaml:"min"`
	Multiple int `yaml:"multiple"`
}

// TLS configures mutual TLS.
type TLS struct {
	Cert        string `yaml:"cert"`
	Key         string `yaml:"key"`
	CA          string `yaml:"ca"`
	PeerName    string `yaml:"peer_name"`
	ServerName  string `yaml:"server_name"`
	InsecureDev bool   `yaml:"insecure_dev"`
	// Dev uses the shared development PKI: certificates for Name are created on
	// first use; loopback addresses only.
	Dev  bool   `yaml:"dev"`
	Name string `yaml:"name"` // party name for Dev
}

// Limits bound what the peer may send.
type Limits struct {
	MaxFrame        Size `yaml:"max_frame"`
	MaxInstanceSize int  `yaml:"max_instance_size"`
	MaxTotalItems   int  `yaml:"max_total_items"`
	MaxInstances    int  `yaml:"max_instances"`
}

// Timeouts bound each phase of a run.
type Timeouts struct {
	Handshake    Duration `yaml:"handshake"`
	Phase        Duration `yaml:"phase"`
	Total        Duration `yaml:"total"` // 0 = none
	ConnectRetry Duration `yaml:"connect_retry"`
}

// Log configures logging.
type Log struct {
	Format string `yaml:"format"` // text | json
	Level  string `yaml:"level"`  // debug | info | warn | error
}

// Default returns the default configuration.
func Default() Config {
	return Config{
		Mode:        "direct",
		Input:       Input{Path: "-", Format: "lines", Column: "0"},
		Output:      "-",
		Protocol:    Protocol{Hash: "sha256", Lambda: 40, Padding: Padding{Extra: 1, Min: 0, Multiple: 1}, SenderPadding: Padding{Extra: 0, Min: 0, Multiple: 1}},
		Limits:      Limits{MaxFrame: 4 << 20, MaxInstanceSize: 1 << 22, MaxTotalItems: 1 << 24, MaxInstances: 1 << 24},
		Timeouts:    Timeouts{Handshake: Duration(30 * time.Second), Phase: Duration(10 * time.Minute)},
		NTTBackend:  "native",
		MemoryLimit: 2 << 30,
		PolyCache:   256 << 20,
		Log:         Log{Format: "text", Level: "info"},
	}
}

// Load overlays the YAML file at path onto c. Unknown keys are errors.
func (c *Config) Load(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var probe struct {
		MemoryLimit *Size `yaml:"memory_limit"`
	}
	if err := yaml.Unmarshal(data, &probe); err == nil && probe.MemoryLimit != nil {
		c.memLimitSet = true
	}
	if err := dec.Decode(c); err != nil && !errors.Is(err, io.EOF) { // empty file: io.EOF
		return fmt.Errorf("config %s: %w", path, err)
	}
	return nil
}

// MemoryLimitExplicit reports whether memory_limit was set by file or flag (otherwise a
// GOMEMLIMIT environment variable takes precedence over the default).
func (c *Config) MemoryLimitExplicit() bool { return c.memLimitSet }

// SetMemoryLimitExplicit marks the memory limit as explicitly configured.
func (c *Config) SetMemoryLimitExplicit() { c.memLimitSet = true }

// Validate checks consistency; it does not touch the filesystem.
func (c *Config) Validate() error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	switch c.Role {
	case "receiver", "sender":
	default:
		add("role must be receiver or sender, got %q", c.Role)
	}
	if (c.Listen == "") == (c.Connect == "") {
		add("exactly one of listen or connect is required")
	}
	if err := validMode(c.Mode); err != nil {
		errs = append(errs, err)
	}
	if c.Request.Dataset != "" && (c.Role != "receiver" || c.Connect == "") {
		add("request.dataset needs the receiver role and a peer to connect to")
	}
	if c.Request.Identity != "" && c.Request.Dataset == "" {
		add("request.identity needs request.dataset")
	}
	if c.Input.Path == "" {
		add("input path is required")
	}
	switch c.Input.Format {
	case "lines":
		if c.Input.Batched {
			add("batched mode requires format csv")
		}
	case "csv":
		if c.Input.Batched && c.Input.InstanceColumn == "" {
			add("batched mode requires instance_column")
		}
	default:
		add("input format must be lines or csv, got %q", c.Input.Format)
	}
	if c.Input.Column == "" {
		add("input column must not be empty")
	}
	switch c.Protocol.Hash {
	case "sha256", "aes":
	default:
		add("hash must be sha256 or aes, got %q", c.Protocol.Hash)
	}
	if c.Protocol.Lambda < 40 || c.Protocol.Lambda > 256 {
		add("lambda must be in [40, 256], got %d", c.Protocol.Lambda)
	}
	if p := c.Protocol.Padding; p.Extra < 0 || p.Min < 0 || p.Multiple < 1 {
		add("padding needs extra ≥ 0, min ≥ 0, multiple ≥ 1")
	}
	if p := c.Protocol.SenderPadding; p.Extra < 0 || p.Min < 0 || p.Multiple < 1 {
		add("sender_padding needs extra ≥ 0, min ≥ 0, multiple ≥ 1")
	}
	def := Default().Protocol
	if c.Role == "sender" && c.Protocol.Padding != def.Padding {
		add("padding is the receiver's policy; the sender uses sender_padding")
	}
	if c.Role == "receiver" && c.Protocol.SenderPadding != def.SenderPadding {
		add("sender_padding is the sender's policy; the receiver uses padding")
	}
	errs = append(errs, validTLS(c.TLS, c.Listen+c.Connect)...)
	if c.Limits.MaxFrame < 64<<10 {
		add("max_frame must be ≥ 64KiB")
	}
	if c.Limits.MaxInstanceSize < 1 || c.Limits.MaxTotalItems < 1 || c.Limits.MaxInstances < 1 {
		add("limits must be positive")
	}
	if c.Timeouts.Handshake <= 0 || c.Timeouts.Phase <= 0 || c.Timeouts.Total < 0 || c.Timeouts.ConnectRetry < 0 {
		add("timeouts: handshake and phase must be > 0, total and connect_retry ≥ 0")
	}
	if c.Workers < 0 {
		add("workers must be ≥ 0")
	}
	switch c.NTTBackend {
	case "native", "lattigo":
	default:
		add("ntt_backend must be native or lattigo, got %q", c.NTTBackend)
	}
	if c.MemoryLimit < 0 || c.PolyCache < 0 {
		add("memory_limit and poly_cache must be ≥ 0")
	}
	switch c.Log.Format {
	case "text", "json":
	default:
		add("log format must be text or json")
	}
	switch strings.ToLower(c.Log.Level) {
	case "debug", "info", "warn", "error":
	default:
		add("log level must be debug, info, warn or error")
	}
	if c.Role == "sender" && c.Output != "-" && c.Output != "" {
		add("output is only valid for the receiver")
	}
	return errors.Join(errs...)
}

func validMode(m string) error {
	switch m {
	case "direct":
		return nil
	case "relay":
		return errors.New("mode relay (via the Orchestrator) is not implemented yet; use direct")
	}
	return fmt.Errorf("mode must be direct or relay, got %q", m)
}

// validTLS checks the TLS settings; addr is the listen or connect address.
func validTLS(t TLS, addr string) []error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	files := t.Cert != "" || t.Key != "" || t.CA != ""
	switch {
	case t.Dev:
		if files || t.InsecureDev {
			add("dev cannot be combined with TLS files or insecure_dev")
		}
		if addr != "" && !devcerts.IsLoopback(addr) {
			add("dev mode only allows loopback addresses (127.0.0.1, ::1, localhost), got %q", addr)
		}
	case t.InsecureDev:
		if files {
			add("insecure_dev cannot be combined with TLS files")
		}
	case t.Cert == "" || t.Key == "" || t.CA == "":
		add("tls cert, key and ca are required (or dev / insecure_dev for development)")
	}
	if t.Name != "" && !t.Dev {
		add("tls name is only used with dev")
	}
	return errs
}

// Size is a byte count that parses "4MiB", "2GiB", "512KiB", "64KB" or plain integers.
type Size int64

var sizeUnits = []struct {
	suffix string
	mult   int64
}{{"KiB", 1 << 10}, {"MiB", 1 << 20}, {"GiB", 1 << 30}, {"TiB", 1 << 40},
	{"KB", 1e3}, {"MB", 1e6}, {"GB", 1e9}, {"TB", 1e12}, {"B", 1}}

// ParseSize parses a size string.
func ParseSize(s string) (Size, error) {
	t := strings.TrimSpace(s)
	mult := int64(1)
	for _, u := range sizeUnits {
		if strings.HasSuffix(t, u.suffix) {
			t, mult = strings.TrimSpace(strings.TrimSuffix(t, u.suffix)), u.mult
			break
		}
	}
	n, err := strconv.ParseInt(t, 10, 64)
	if err != nil || n < 0 || (mult > 1 && n > (1<<62)/mult) {
		return 0, fmt.Errorf("invalid size %q (use e.g. 4MiB, 2GiB or a byte count)", s)
	}
	return Size(n * mult), nil
}

func (s Size) String() string {
	switch {
	case s == 0:
		return "0"
	case s%(1<<30) == 0:
		return fmt.Sprintf("%dGiB", s>>30)
	case s%(1<<20) == 0:
		return fmt.Sprintf("%dMiB", s>>20)
	case s%(1<<10) == 0:
		return fmt.Sprintf("%dKiB", s>>10)
	}
	return strconv.FormatInt(int64(s), 10)
}

// UnmarshalYAML accepts integers and size strings.
func (s *Size) UnmarshalYAML(n *yaml.Node) error {
	v, err := ParseSize(n.Value)
	if err != nil {
		return err
	}
	*s = v
	return nil
}

// Set and Type make Size a pflag.Value.
func (s *Size) Set(v string) error {
	x, err := ParseSize(v)
	if err != nil {
		return err
	}
	*s = x
	return nil
}

// Type implements pflag.Value.
func (s *Size) Type() string { return "size" }

// Duration is a time.Duration that parses Go duration strings in YAML.
type Duration time.Duration

// UnmarshalYAML parses "30s", "10m", "1h30m".
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := time.ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("invalid duration %q", n.Value)
	}
	*d = Duration(v)
	return nil
}

// Set implements pflag.Value.
func (d *Duration) Set(v string) error {
	x, err := time.ParseDuration(v)
	if err != nil {
		return err
	}
	*d = Duration(x)
	return nil
}

// Type implements pflag.Value.
func (d *Duration) Type() string { return "duration" }

func (d Duration) String() string { return time.Duration(d).String() }

// D returns the time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }
