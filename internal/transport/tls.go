package transport

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"slices"
)

// TLSMaterial is a party's certificate chain and the CA pool trusted for the peer.
type TLSMaterial struct {
	Certificate tls.Certificate
	Roots       *x509.CertPool
}

// LoadTLSFiles reads PEM files: certificate (chain), private key and CA bundle.
func LoadTLSFiles(certFile, keyFile, caFile string) (*TLSMaterial, error) {
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		return nil, fmt.Errorf("transport: reading certificate: %w", err)
	}
	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, fmt.Errorf("transport: reading key: %w", err)
	}
	caPEM, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("transport: reading CA: %w", err)
	}
	return TLSFromPEM(certPEM, keyPEM, caPEM)
}

// TLSFromPEM builds TLSMaterial from PEM bytes.
func TLSFromPEM(certPEM, keyPEM, caPEM []byte) (*TLSMaterial, error) {
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("transport: certificate/key: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("transport: no CA certificates found")
	}
	return &TLSMaterial{Certificate: cert, Roots: roots}, nil
}

// ServerConfig returns a TLS 1.3 server config requiring a client certificate signed by
// the trusted CA; if peerName is set, the client certificate must carry it as a DNS or
// URI SAN.
func (m *TLSMaterial) ServerConfig(peerName string) *tls.Config {
	return &tls.Config{
		MinVersion:       tls.VersionTLS13,
		Certificates:     []tls.Certificate{m.Certificate},
		ClientAuth:       tls.RequireAndVerifyClientCert,
		ClientCAs:        m.Roots,
		VerifyConnection: pinPeer(peerName),
		NextProtos:       []string{"h2"},
	}
}

// ClientConfig returns a TLS 1.3 client config presenting the certificate, verifying the
// server against the trusted CA and serverName, and (if set) pinning peerName.
func (m *TLSMaterial) ClientConfig(serverName, peerName string) *tls.Config {
	return &tls.Config{
		MinVersion:       tls.VersionTLS13,
		Certificates:     []tls.Certificate{m.Certificate},
		RootCAs:          m.Roots,
		ServerName:       serverName,
		VerifyConnection: pinPeer(peerName),
		NextProtos:       []string{"h2"},
	}
}

// pinPeer checks the verified peer leaf for a DNS or URI SAN equal to name.
func pinPeer(name string) func(tls.ConnectionState) error {
	if name == "" {
		return nil
	}
	return func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return errors.New("transport: peer presented no certificate")
		}
		leaf := cs.PeerCertificates[0]
		if slices.Contains(leaf.DNSNames, name) {
			return nil
		}
		for _, u := range leaf.URIs {
			if u.String() == name {
				return nil
			}
		}
		return fmt.Errorf("transport: peer certificate does not name %q (SANs %v %v)", name, leaf.DNSNames, leaf.URIs)
	}
}
