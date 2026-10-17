package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The anchor store is named by a URL: s3:// with its endpoint and two secret
// references, or file:// with a directory. Anything else is refused before
// any connection is made.
func TestTheAnchorStoreIsNamedByAURL(t *testing.T) {
	secrets := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(secrets, "audit"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(secrets, "audit", "key"), []byte("id\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(secrets, "audit", "secret"), []byte("s3cret\n"), 0o600))
	ctx := t.Context()

	for _, good := range []string{
		"s3://anchors/agentswarm?endpoint=anchors.example&key=secret://audit/key&secret=secret://audit/secret",
		"s3://anchors?endpoint=127.0.0.1:9000&insecure=true&region=eu-1&key=secret://audit/key&secret=secret://audit/secret",
		"file://" + t.TempDir(),
	} {
		s, done, err := openAnchors(ctx, good, secrets)
		require.NoError(t, err, good)
		assert.NotNil(t, s)
		done()
	}
	for bad, why := range map[string]string{
		"s3://anchors?endpoint=anchors.example:9000&insecure=true&key=secret://audit/key&secret=secret://audit/secret": "loopback",
		"s3://anchors?endpoint=anchors.example&key=secret://audit/key&secret=secret://audit/missing":                   "no such secret",
		"s3://anchors?endpoint=anchors.example&key=audit/key&secret=secret://audit/secret":                             "not a secret reference",
		"s3://anchors?endpoint=anchors.example&key=secret://audit/key&secret=secret://audit/secret&acl=public":         `no "acl"`,
		"file://host/dir": "file:///DIR",
		"http://anchors":  "neither s3:// nor file://",
	} {
		_, _, err := openAnchors(ctx, bad, secrets)
		require.ErrorContains(t, err, why, bad)
	}
	_, _, err := openAnchors(ctx, "s3://anchors?endpoint=anchors.example&key=secret://audit/key&secret=secret://audit/secret", "")
	require.ErrorContains(t, err, "--secrets")
}

// Both audit commands need a tenant and an anchor store.
func TestTheAuditCommandsNeedATenantAndAStore(t *testing.T) {
	for _, cmd := range []string{"anchor", "verify"} {
		_, errOut, code := runCommand("audit", cmd, "--anchors", "file:///tmp")
		assert.Equal(t, exitUsage, code, cmd)
		assert.Contains(t, errOut, "tenant", cmd)
	}
	_, errOut, code := runCommand("audit", "anchor", "--tenant", "Bad/Tenant", "--anchors", "file://"+t.TempDir(), "--database-url", "postgres://nobody@127.0.0.1:1/none?connect_timeout=1")
	assert.Equal(t, exitError, code)
	assert.Contains(t, errOut, `--tenant: "Bad/Tenant" is not a tenant id`, "refused before any connection")
}
