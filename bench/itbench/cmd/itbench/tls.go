package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"time"
)

// certLife is how long serve's certificates and their CA are valid.
const certLife = 30 * 24 * time.Hour

// selfSigned makes a CA and, signed by it, a certificate for the host of
// addr, writes the CA's certificate to caPath for clients to trust, and
// returns the server's TLS configuration. A server registration takes an
// https endpoint only, so serve offers HTTPS to a platform that reaches its
// tools.
func selfSigned(addr, caPath string) (*tls.Config, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("tls: %w", err)
	}
	now := time.Now()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("tls: %w", err)
	}
	ca := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "itbench serve CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(certLife),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("tls: %w", err)
	}
	if ca, err = x509.ParseCertificate(caDER); err != nil {
		return nil, fmt.Errorf("tls: %w", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("tls: %w", err)
	}
	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "itbench serve"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(certLife),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	switch ip := net.ParseIP(host); {
	case ip != nil:
		leaf.IPAddresses = []net.IP{ip}
	case host != "":
		leaf.DNSNames = []string{host}
	default:
		leaf.IPAddresses = []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}
		leaf.DNSNames = []string{"localhost"}
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("tls: %w", err)
	}
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o600); err != nil {
		return nil, fmt.Errorf("tls: %w", err)
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{{Certificate: [][]byte{leafDER}, PrivateKey: key}},
	}, nil
}
