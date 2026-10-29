package docker

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/runner/spec"
	"github.com/jchen0426/agent-swarm/internal/work/app"
)

// testCert returns a self-signed PEM certificate for the gateways' names,
// for options whose Cert must hold one.
func testCert(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "mcp.agentswarm.internal"},
		DNSNames:     []string{"mcp.agentswarm.internal", "llm.agentswarm.internal"},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// testOptions returns options the runner can run turns with: the
// deployment's defaults, with directories of the test's own.
func testOptions(t *testing.T) Options {
	t.Helper()
	return Options{
		Tenant: "tenant", Holder: "holder",
		Output: t.TempDir(), OutputCap: 1 << 20, Slack: 2 * time.Second, MaxTokens: 4096,
		Sockets: "/run/agentswarm-sockets", Store: t.TempDir(), Group: 1000,
		Memory: 256 << 20, CPUs: 1, Pids: 64, Files: 256, Tmp: 16 << 20,
		Cert: testCert(t),
	}
}

// testTurn returns a turn of the image id.
func testTurn(id string) *app.Turn {
	return &app.Turn{
		ID: "turn-1", Run: "run-1", Agent: "alpha", Round: 3,
		Deadline: time.Now().Add(time.Minute), Image: id, Model: "model", Prompt: "prompt",
	}
}

// TestOptionsThatCannotRunTurnsAreRefused: check refuses each value the
// runner cannot run turns with, naming its field, and passes options of
// every value right.
func TestOptionsThatCannotRunTurnsAreRefused(t *testing.T) {
	key := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("a key")}))
	garbled := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("no DER")}))
	for _, row := range []struct {
		name string
		set  func(o *Options)
		// field is the field the refusal names; "" for options that pass.
		field string
	}{
		{"every value right", func(*Options) {}, ""},
		{"no slack", func(o *Options) { o.Slack = 0 }, ""},
		{"an empty tenant", func(o *Options) { o.Tenant = "" }, "Tenant"},
		{"an empty holder", func(o *Options) { o.Holder = "" }, "Holder"},
		{"an empty output", func(o *Options) { o.Output = "" }, "Output"},
		{"empty sockets", func(o *Options) { o.Sockets = "" }, "Sockets"},
		{"an empty store", func(o *Options) { o.Store = "" }, "Store"},
		{"a cap of 0", func(o *Options) { o.OutputCap = 0 }, "OutputCap"},
		{"a maxTokens of 0", func(o *Options) { o.MaxTokens = 0 }, "MaxTokens"},
		{"a memory of 0", func(o *Options) { o.Memory = 0 }, "Memory"},
		{"a negative memory", func(o *Options) { o.Memory = -1 }, "Memory"},
		{"no CPU", func(o *Options) { o.CPUs = 0 }, "CPUs"},
		{"no process", func(o *Options) { o.Pids = 0 }, "Pids"},
		{"no open file", func(o *Options) { o.Files = 0 }, "Files"},
		{"a /tmp of 0", func(o *Options) { o.Tmp = 0 }, "Tmp"},
		{"a negative slack", func(o *Options) { o.Slack = -time.Nanosecond }, "Slack"},
		{"the root group", func(o *Options) { o.Group = 0 }, "Group"},
		{"a group left unresolved", func(o *Options) { o.Group = -1 }, "Group"},
		{"no cert", func(o *Options) { o.Cert = "" }, "Cert"},
		{"a cert that is no PEM", func(o *Options) { o.Cert = "a certificate" }, "Cert"},
		{"a cert of a key alone", func(o *Options) { o.Cert = key }, "Cert"},
		{"a cert whose certificate does not parse", func(o *Options) { o.Cert = garbled }, "Cert"},
	} {
		t.Run(row.name, func(t *testing.T) {
			o := testOptions(t)
			row.set(&o)
			err := o.check()
			if row.field == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Regexp(t, `\bOptions\.`+row.field+`\b`, err.Error())
		})
	}
}

// TestTheProbeSeamSkipsTheContractAndReplacesTheEntrypoint (R15): with the
// tests' probe set, an image that breaks the contract still gives the spec's
// version 2, and the sandbox runs the probe's command in place of the
// image's entrypoint; without it, neither.
func TestTheProbeSeamSkipsTheContractAndReplacesTheEntrypoint(t *testing.T) {
	o := testOptions(t)
	curl := &Image{
		ID: "sha256:" + strings.Repeat("c", 64), OS: "linux", Arch: "arm64",
		Env: []string{"PATH=/usr/bin:/bin", "CURL_CA_BUNDLE=/cacert.pem"},
	}
	turn := testTurn(curl.ID)

	_, err := o.specVersion(curl, "arm64")
	require.Error(t, err, "without the probe, the contract applies")
	_, cfg, _ := o.sandbox(turn)
	require.NotNil(t, cfg)
	assert.Nil(t, cfg.Entrypoint)

	o.probe = []string{"sh", "-c", "head -c 1 >/dev/null; exit 10"}
	version, err := o.specVersion(curl, "arm64")
	require.NoError(t, err)
	assert.Equal(t, spec.V2, version)
	_, cfg, _ = o.sandbox(turn)
	require.NotNil(t, cfg)
	assert.Equal(t, o.probe, cfg.Entrypoint)
	assert.Nil(t, cfg.Cmd, "the image's command is not the probe's argument")
}
