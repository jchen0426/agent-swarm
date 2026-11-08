package deploy_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/deploy"
)

// secretValue is a value a person might mistake for a reference: no refusal
// may repeat it.
const secretValue = "hunter2-sk-web"

// withWeb is the minimal deployment with a web block of its required keys,
// and its listener.
func withWeb() map[string]any {
	doc := minimal()
	set(doc, "listeners.web", "127.0.0.1:7404")
	doc["web"] = map[string]any{
		"publicURL": "https://agents.example.com",
		"oidc": map[string]any{
			"issuer": "https://idp.example.com/realms/ops", "clientID": "agentswarm", "clientSecretRef": "secret://web/oidc",
		},
		"groups":        map[string]any{"viewers": "ops-view", "responders": "ops-respond", "admins": "ops-admin"},
		"sessionKeyRef": "secret://web/session",
	}
	return doc
}

// caPEM writes a PEM file of one self-signed CA certificate, and returns its
// path.
func caPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	return path
}

// A web block of its required keys loads with its defaults, and a readable
// CA bundle is taken; a deployment without one has no web block.
func TestAWebBlockLoadsWithItsDefaults(t *testing.T) {
	d, err := deploy.Load(write(t, minimal()))
	require.NoError(t, err)
	assert.Nil(t, d.Web, "no web block, no web listener")
	assert.Empty(t, d.Listeners.Web)

	doc := withWeb()
	ca := caPEM(t)
	set(doc, "web.oidc.caBundle", ca)
	d, err = deploy.Load(write(t, doc))

	require.NoError(t, err)
	require.NotNil(t, d.Web)
	assert.Equal(t, "127.0.0.1:7404", d.Listeners.Web)
	assert.Equal(t, deploy.Web{
		PublicURL: "https://agents.example.com",
		OIDC: deploy.OIDC{
			Issuer: "https://idp.example.com/realms/ops", ClientID: "agentswarm", ClientSecretRef: "secret://web/oidc", CABundle: ca,
			SubjectClaim: "sub", GroupsClaim: "groups",
		},
		Groups:        deploy.Groups{Viewers: "ops-view", Responders: "ops-respond", Admins: "ops-admin"},
		SessionKeyRef: "secret://web/session", SessionLength: 8 * time.Hour, FreshWindow: 5 * time.Minute, TimeZone: "UTC",
	}, *d.Web)
}

// Each web key the deployment refuses is refused, named by its path, and no
// refusal repeats the value it refused, which may be a secret.
func TestDeployRefusesEachBadWebField(t *testing.T) {
	unreadable := filepath.Join(t.TempDir(), "missing.pem")
	notPEM := filepath.Join(t.TempDir(), "not.pem")
	require.NoError(t, os.WriteFile(notPEM, []byte(secretValue), 0o600))
	for _, tc := range []struct {
		name, key string
		value     any
		// remove leaves the key out, rather than setting it.
		remove bool
	}{
		{name: "no web listener", key: "listeners.web", remove: true},
		{name: "a web listener off loopback", key: "listeners.web", value: "0.0.0.0:7404"},
		{name: "a web listener by name", key: "listeners.web", value: "localhost:7404"},
		{name: "no public URL", key: "web.publicURL", remove: true},
		{name: "a plain-HTTP public URL", key: "web.publicURL", value: "http://agents.example.com"},
		{name: "a public URL with a path", key: "web.publicURL", value: "https://agents.example.com/agents"},
		{name: "a public URL with a query", key: "web.publicURL", value: "https://agents.example.com/?" + secretValue},
		{name: "a public URL with credentials", key: "web.publicURL", value: "https://u:" + secretValue + "@agents.example.com/"},
		{name: "a public URL without a host", key: "web.publicURL", value: "https:///"},
		{name: "no issuer", key: "web.oidc.issuer", remove: true},
		{name: "a plain-HTTP issuer", key: "web.oidc.issuer", value: "http://idp.example.com/" + secretValue},
		{name: "no client id", key: "web.oidc.clientID", remove: true},
		{name: "a client secret in place of its reference", key: "web.oidc.clientSecretRef", value: secretValue},
		{name: "an unreadable CA bundle", key: "web.oidc.caBundle", value: unreadable},
		{name: "a CA bundle that is not PEM", key: "web.oidc.caBundle", value: notPEM},
		{name: "an empty subject claim", key: "web.oidc.subjectClaim", value: ""},
		{name: "an empty groups claim", key: "web.oidc.groupsClaim", value: ""},
		{name: "no viewers' group", key: "web.groups.viewers", remove: true},
		{name: "no responders' group", key: "web.groups.responders", value: ""},
		{name: "no admins' group", key: "web.groups.admins", remove: true},
		{name: "responders the viewers' group", key: "web.groups.responders", value: "ops-view"},
		{name: "admins the responders' group", key: "web.groups.admins", value: "ops-respond"},
		{name: "a session key in place of its reference", key: "web.sessionKeyRef", value: secretValue},
		{name: "no session key", key: "web.sessionKeyRef", remove: true},
		{name: "a session below an hour", key: "web.sessionLength", value: "59m"},
		{name: "a session above a day", key: "web.sessionLength", value: "25h"},
		{name: "a fresh window below a minute", key: "web.freshWindow", value: "30s"},
		{name: "a fresh window above 15 minutes", key: "web.freshWindow", value: "16m"},
		{name: "a time zone that is not IANA's", key: "web.timeZone", value: "Mars/Olympus"},
		{name: "the host's own time zone", key: "web.timeZone", value: "Local"},
		{name: "an empty time zone", key: "web.timeZone", value: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := withWeb()
			if tc.remove {
				remove(doc, tc.key)
			} else {
				set(doc, tc.key, tc.value)
			}

			ps := refused(t, doc)

			assert.Equal(t, []string{tc.key}, keys(ps))
			assert.NotContains(t, ps.Error(), secretValue, "no refusal repeats the value")
		})
	}
}

// The web block's bounds hold: an hour and a day of session, a minute and 15
// minutes of fresh window, and an IANA zone are taken.
func TestTheWebBlocksBoundsAreTaken(t *testing.T) {
	for key, value := range map[string]string{
		"web.sessionLength": "1h", "web.freshWindow": "15m", "web.timeZone": "Europe/Berlin",
	} {
		doc := withWeb()
		set(doc, key, value)
		_, err := deploy.Load(write(t, doc))
		require.NoError(t, err, key)
	}
	doc := withWeb()
	set(doc, "web.sessionLength", "24h")
	set(doc, "web.freshWindow", "1m")
	set(doc, "web.publicURL", "https://agents.example.com:8443/")
	_, err := deploy.Load(write(t, doc))
	require.NoError(t, err)
}

// A web listener without a web block is refused: it would serve nothing.
func TestAWebListenerWithoutAWebBlockIsRefused(t *testing.T) {
	doc := minimal()
	set(doc, "listeners.web", "127.0.0.1:7404")

	ps := refused(t, doc)

	assert.Equal(t, []string{"listeners.web"}, keys(ps))
}
