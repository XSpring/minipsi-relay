package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Serve is the configuration of `minipsi serve`: a long-running
// listener that answers session requests as sender for its named datasets, one at a time.
type Serve struct {
	Listen       string             `yaml:"listen"`
	Mode         string             `yaml:"mode"` // direct (default) | relay (not implemented)
	TLS          TLS                `yaml:"tls"`
	StateDir     string             `yaml:"state_dir"` // reports/ per session; empty = no report files
	Datasets     map[string]Dataset `yaml:"datasets"`
	AllowedPeers []string           `yaml:"allowed_peers"` // empty = accept every authenticated peer
	Limits       Limits             `yaml:"limits"`
	Timeouts     ServeTimeouts      `yaml:"timeouts"`
	Workers      int                `yaml:"workers"`
	NTTBackend   string             `yaml:"ntt_backend"`
	MemoryLimit  Size               `yaml:"memory_limit"`
	PolyCache    Size               `yaml:"poly_cache"`
	Log          Log                `yaml:"log"`
	memLimitSet  bool
}

// Dataset is one input the daemon serves, with its protocol settings.
type Dataset struct {
	Input    Input           `yaml:"input"`
	Protocol DatasetProtocol `yaml:"protocol"`
}

// DatasetProtocol holds the per-dataset protocol settings (the requester must use the
// same hash and lambda; sender padding is the daemon's choice).
type DatasetProtocol struct {
	Hash          string  `yaml:"hash"`
	Lambda        int     `yaml:"lambda"`
	SenderPadding Padding `yaml:"sender_padding"`
}

// ServeTimeouts bound each session.
type ServeTimeouts struct {
	Handshake Duration `yaml:"handshake"`
	Phase     Duration `yaml:"phase"`
	Session   Duration `yaml:"session"` // whole session; 0 = none
}

// MetaDataset and MetaIdentity are the request metadata keys (gRPC headers).
const (
	MetaDataset  = "minipsi-dataset"
	MetaIdentity = "minipsi-identity"
)

var datasetName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ValidDatasetName reports whether s is an acceptable dataset name.
func ValidDatasetName(s string) bool { return datasetName.MatchString(s) }

// DefaultServe returns the default daemon configuration.
func DefaultServe() Serve {
	d := Default()
	return Serve{
		Mode: "direct", Datasets: map[string]Dataset{}, Limits: d.Limits,
		Timeouts:   ServeTimeouts{Handshake: d.Timeouts.Handshake, Phase: d.Timeouts.Phase},
		NTTBackend: d.NTTBackend, MemoryLimit: d.MemoryLimit, PolyCache: d.PolyCache, Log: d.Log,
	}
}

// Load overlays the YAML file at path onto s. Unknown keys are errors.
func (s *Serve) Load(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	var probe struct {
		MemoryLimit *Size `yaml:"memory_limit"`
	}
	if err := yaml.Unmarshal(data, &probe); err == nil && probe.MemoryLimit != nil {
		s.memLimitSet = true
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(s); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("config %s: %w", path, err)
	}
	return nil
}

// MemoryLimitExplicit reports whether memory_limit was set by file or flag.
func (s *Serve) MemoryLimitExplicit() bool { return s.memLimitSet }

// SetMemoryLimitExplicit marks the memory limit as explicitly configured.
func (s *Serve) SetMemoryLimitExplicit() { s.memLimitSet = true }

// DatasetNames returns the dataset names, sorted.
func (s *Serve) DatasetNames() []string {
	names := make([]string, 0, len(s.Datasets))
	for n := range s.Datasets {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// Allowed reports whether a peer with these certificate SANs may request sessions.
func (s *Serve) Allowed(sans []string) bool {
	if len(s.AllowedPeers) == 0 {
		return true
	}
	for _, san := range sans {
		if slices.Contains(s.AllowedPeers, san) {
			return true
		}
	}
	return false
}

// SessionConfig returns the sender configuration for one session on dataset name
// (which must exist). Unset dataset fields take the party defaults.
func (s *Serve) SessionConfig(name string) Config {
	ds := s.Datasets[name]
	c := Default()
	c.Role, c.Mode, c.Listen, c.Output = "sender", s.Mode, s.Listen, ""
	in := ds.Input
	if in.Format == "" {
		in.Format = c.Input.Format
	}
	if in.Column == "" {
		in.Column = c.Input.Column
	}
	c.Input = in
	if ds.Protocol.Hash != "" {
		c.Protocol.Hash = ds.Protocol.Hash
	}
	if ds.Protocol.Lambda != 0 {
		c.Protocol.Lambda = ds.Protocol.Lambda
	}
	sp := ds.Protocol.SenderPadding
	if sp.Multiple == 0 {
		sp.Multiple = 1
	}
	c.Protocol.SenderPadding = sp
	c.TLS, c.Limits, c.Workers, c.NTTBackend = s.TLS, s.Limits, s.Workers, s.NTTBackend
	c.Timeouts = Timeouts{Handshake: s.Timeouts.Handshake, Phase: s.Timeouts.Phase, Total: s.Timeouts.Session}
	c.MemoryLimit, c.PolyCache, c.Log = s.MemoryLimit, s.PolyCache, s.Log
	c.memLimitSet = s.memLimitSet
	return c
}

// Validate checks the daemon configuration, including every dataset's settings.
func (s *Serve) Validate() error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	if s.Listen == "" {
		add("listen is required")
	}
	if err := validMode(s.Mode); err != nil {
		errs = append(errs, err)
	}
	errs = append(errs, validTLS(s.TLS, s.Listen)...)
	if len(s.Datasets) == 0 {
		add("at least one dataset is required")
	}
	if s.Timeouts.Session < 0 {
		add("timeouts.session must be ≥ 0")
	}
	for _, p := range s.AllowedPeers {
		if strings.TrimSpace(p) == "" {
			add("allowed_peers must not contain empty names")
		}
	}
	for _, name := range s.DatasetNames() {
		if !ValidDatasetName(name) {
			add("dataset name %q: use letters, digits, '.', '_' or '-' (max 64)", name)
			continue
		}
		ds := s.Datasets[name]
		if ds.Input.Path == "" || ds.Input.Path == "-" {
			add("dataset %s: input.path must name a file", name)
			continue
		}
		c := s.SessionConfig(name)
		if err := c.Validate(); err != nil {
			for _, e := range unjoin(err) {
				add("dataset %s: %v", name, e)
			}
		}
	}
	return errors.Join(dedupErrs(errs)...)
}

func unjoin(err error) []error {
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		return j.Unwrap()
	}
	return []error{err}
}

// dedupErrs drops repeated messages (shared settings fail once per dataset otherwise).
func dedupErrs(errs []error) []error {
	seen := map[string]bool{}
	var out []error
	for _, e := range errs {
		m := e.Error()
		if i := strings.Index(m, ": "); strings.HasPrefix(m, "dataset ") && i > 0 {
			m = m[i+2:]
		}
		if seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, e)
	}
	return out
}
