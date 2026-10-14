// Package version exposes build and protocol version information.
package version

// Set at build time via -ldflags "-X github.com/XSpring/minipsi/internal/version.Version=...".
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

const (
	// ProtocolVersion is the wire protocol version.
	ProtocolVersion = 1
	// Suite identifies the cryptographic suite.
	Suite = "minipsi-v1-ed25519-ell2"
)
