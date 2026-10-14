// Package devcerts generates throw-away X.509 material for tests and `minipsi certs dev`:
// a self-signed CA and leaf certificates usable for both TLS server and client auth (either
// PSI role may be the gRPC server or client). Not for production use.
package devcerts

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// Authority is a CA that can issue leaves.
type Authority struct {
	Cert    *x509.Certificate
	Key     *ecdsa.PrivateKey
	CertPEM []byte
}

// Leaf is an issued certificate with its key, PEM-encoded.
type Leaf struct {
	CertPEM, KeyPEM []byte
}

// LeafOptions customise a leaf (tests use them for negative cases).
type LeafOptions struct {
	DNSNames  []string
	IPs       []net.IP
	URIs      []string
	NotBefore time.Time // zero: now − 1h
	NotAfter  time.Time // zero: now + validity
	Validity  time.Duration
}

// NewAuthority creates a self-signed ECDSA P-256 CA valid for validity.
func NewAuthority(commonName string, validity time.Duration) (*Authority, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := serialNumber()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(validity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &Authority{Cert: cert, Key: key, CertPEM: pemBlock("CERTIFICATE", der)}, nil
}

// Issue creates a leaf for commonName with server+client auth usage.
func (a *Authority) Issue(commonName string, o LeafOptions) (*Leaf, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := serialNumber()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	if o.Validity == 0 {
		o.Validity = 30 * 24 * time.Hour
	}
	if o.NotBefore.IsZero() {
		o.NotBefore = now.Add(-time.Hour)
	}
	if o.NotAfter.IsZero() {
		o.NotAfter = now.Add(o.Validity)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    o.NotBefore,
		NotAfter:     o.NotAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:     o.DNSNames,
		IPAddresses:  o.IPs,
	}
	for _, u := range o.URIs {
		pu, err := url.Parse(u)
		if err != nil {
			return nil, fmt.Errorf("devcerts: URI %q: %w", u, err)
		}
		tmpl.URIs = append(tmpl.URIs, pu)
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, a.Cert, &key.PublicKey, a.Key)
	if err != nil {
		return nil, err
	}
	kder, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	return &Leaf{CertPEM: pemBlock("CERTIFICATE", der), KeyPEM: pemBlock("PRIVATE KEY", kder)}, nil
}

// WriteFiles writes ca.crt and, per leaf name, <name>.crt / <name>.key (0600) into dir.
// Existing files are not overwritten.
func WriteFiles(dir string, a *Authority, leaves map[string]*Leaf) error {
	files := map[string][]byte{"ca.crt": a.CertPEM}
	for name, l := range leaves {
		files[name+".crt"] = l.CertPEM
		files[name+".key"] = l.KeyPEM
	}
	for name := range files {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return fmt.Errorf("devcerts: %s already exists", filepath.Join(dir, name))
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for name, data := range files {
		mode := os.FileMode(0o644)
		if filepath.Ext(name) == ".key" {
			mode = 0o600
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, mode); err != nil {
			return err
		}
	}
	return nil
}

func serialNumber() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
}

func pemBlock(typ string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
}
