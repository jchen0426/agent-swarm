package docker

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
	"time"
)

// certificateYears is how long the gateways' certificate lasts, fixed in
// code (Decision 2): a restart of serve makes a new one, so nothing ever
// needs it renewed.
const certificateYears = 10

// Certificate is the gateways' certificate for the sandboxes (Decision 2):
// the TLS certificate the agents' two listeners serve, its key in serve's
// memory alone, and its PEM, which every turn's spec carries, and which the
// runtime trusts alone.
type Certificate struct {
	TLS tls.Certificate
	PEM string
}

// NewCertificate makes a self-signed ECDSA P-256 certificate for
// mcp.agentswarm.internal and llm.agentswarm.internal, the hosts of MCPURL
// and LLMURL, valid ten years from now (Decision 2). It is a leaf, which a
// client that trusts it alone verifies for those two names. x509 keeps its
// times to the second.
func NewCertificate(now time.Time) (Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Certificate{}, fmt.Errorf("docker: make the gateways' certificate's key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return Certificate{}, fmt.Errorf("docker: make the gateways' certificate's serial number: %w", err)
	}
	names := []string{mcpHost, llmHost}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: names[0]},
		DNSNames:     names,
		NotBefore:    now,
		NotAfter:     now.AddDate(certificateYears, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		// A leaf: it signs no other certificate.
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return Certificate{}, fmt.Errorf("docker: make the gateways' certificate: %w", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return Certificate{}, fmt.Errorf("docker: read the gateways' certificate: %w", err)
	}
	return Certificate{
		TLS: tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf},
		PEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
	}, nil
}
