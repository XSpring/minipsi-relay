package devcerts

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// DevPKIEnv overrides the directory of the shared development PKI.
const DevPKIEnv = "MINIPSI_DEV_PKI"

// devValidity is the lifetime of the dev CA and of each party certificate.
const devValidity = 365 * 24 * time.Hour

// DevFiles are the paths a party uses in --dev mode.
type DevFiles struct {
	Cert, Key, CA string
}

var devName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// DefaultDevDir returns $MINIPSI_DEV_PKI, else $XDG_STATE_HOME/minipsi/dev-pki, else
// ~/.local/state/minipsi/dev-pki.
func DefaultDevDir() (string, error) {
	if d := os.Getenv(DevPKIEnv); d != "" {
		return d, nil
	}
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "minipsi", "dev-pki"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("devcerts: no home directory for the dev PKI (set %s): %w", DevPKIEnv, err)
	}
	return filepath.Join(home, ".local", "state", "minipsi", "dev-pki"), nil
}

// EnsureDev returns the certificate, key and CA paths for party name in the development
// PKI under dir, creating the CA and the party's certificate on first use. Every process
// using the same dir trusts every other one. Creation is atomic (a temporary directory
// renamed into place), so concurrent first runs agree on one CA. Party certificates carry
// the SANs name, localhost, 127.0.0.1 and ::1. Not for production use.
func EnsureDev(dir, name string) (DevFiles, error) {
	if !devName.MatchString(name) {
		return DevFiles{}, fmt.Errorf("devcerts: party name %q must be a lower-case DNS label", name)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return DevFiles{}, err
	}
	caDir := filepath.Join(dir, "ca")
	err := createOnce(dir, caDir, func(tmp string) error {
		a, err := NewAuthority("minipsi dev CA (testing only)", devValidity)
		if err != nil {
			return err
		}
		kder, err := x509.MarshalPKCS8PrivateKey(a.Key)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(tmp, "ca.crt"), a.CertPEM, 0o644); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(tmp, "ca.key"), pemBlock("PRIVATE KEY", kder), 0o600)
	})
	if err != nil {
		return DevFiles{}, err
	}
	ca, err := loadAuthority(caDir)
	if err != nil {
		return DevFiles{}, err
	}
	partyDir := filepath.Join(dir, "parties", name)
	if err := os.MkdirAll(filepath.Dir(partyDir), 0o700); err != nil {
		return DevFiles{}, err
	}
	err = createOnce(filepath.Dir(partyDir), partyDir, func(tmp string) error {
		leaf, err := ca.Issue(name, LeafOptions{
			DNSNames: []string{name, "localhost"},
			IPs:      []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
			Validity: devValidity,
		})
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(tmp, "cert.pem"), leaf.CertPEM, 0o644); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(tmp, "key.pem"), leaf.KeyPEM, 0o600)
	})
	if err != nil {
		return DevFiles{}, err
	}
	f := DevFiles{
		Cert: filepath.Join(partyDir, "cert.pem"),
		Key:  filepath.Join(partyDir, "key.pem"),
		CA:   filepath.Join(caDir, "ca.crt"),
	}
	cert, err := readCert(f.Cert)
	if err != nil {
		return DevFiles{}, err
	}
	if err := cert.CheckSignatureFrom(ca.Cert); err != nil {
		return DevFiles{}, fmt.Errorf("devcerts: %s was not issued by the dev CA in %s (delete the directory to start over)", f.Cert, dir)
	}
	now := time.Now()
	if now.After(cert.NotAfter) || now.After(ca.Cert.NotAfter) {
		return DevFiles{}, fmt.Errorf("devcerts: dev PKI in %s has expired; delete the directory to recreate it", dir)
	}
	return f, nil
}

// createOnce fills a temporary directory in parent and renames it to final, unless final
// already exists (another process won the race: its content is used).
func createOnce(parent, final string, fill func(tmp string) error) error {
	if _, err := os.Stat(final); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmp, err := os.MkdirTemp(parent, ".tmp-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := fill(tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		if _, serr := os.Stat(final); serr == nil {
			return nil // lost the race
		}
		return err
	}
	return nil
}

func loadAuthority(dir string) (*Authority, error) {
	cert, err := readCert(filepath.Join(dir, "ca.crt"))
	if err != nil {
		return nil, err
	}
	kpem, err := os.ReadFile(filepath.Join(dir, "ca.key"))
	if err != nil {
		return nil, err
	}
	b, _ := pem.Decode(kpem)
	if b == nil {
		return nil, fmt.Errorf("devcerts: %s: no PEM key", dir)
	}
	k, err := x509.ParsePKCS8PrivateKey(b.Bytes)
	if err != nil {
		return nil, fmt.Errorf("devcerts: %s: %w", dir, err)
	}
	key, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("devcerts: %s: unexpected CA key type", dir)
	}
	return &Authority{Cert: cert, Key: key, CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})}, nil
}

func readCert(path string) (*x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	b, _ := pem.Decode(data)
	if b == nil || b.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("devcerts: %s: no PEM certificate", path)
	}
	return x509.ParseCertificate(b.Bytes)
}

// IsLoopback reports whether the host part of addr ("host:port") is a loopback address or
// "localhost". An empty host (all interfaces) is not loopback.
func IsLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
