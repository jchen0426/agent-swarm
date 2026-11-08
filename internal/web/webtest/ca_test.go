package webtest_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/web/webtest"
)

// CAOf issues under a CA made elsewhere, such as the harness's: a client
// that trusts that CA's certificate alone reaches a server CAOf's CA
// starts, and its PEM is that certificate.
func TestCAOfIssuesUnderAnExistingCA(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "elsewhere"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)

	ca := webtest.CAOf(cert, key)
	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM(ca.PEM), "the PEM holds the certificate")
	assert.True(t, pool.Equal(ca.Pool()))
	s := ca.UnstartedServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	s.StartTLS()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, s.URL, http.NoBody)
	require.NoError(t, err)
	resp, err := ca.Client().Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
}
