//go:build integration && linux

package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	git "github.com/go-git/go-git/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Serve's tests of the Docker runner run in the Linux test container that make
// test-docker and make test-linux start, on the engine whose socket it mounts
// (Plan 7d-2, Contract C15).
const (
	// e2eDir holds the directories a sandbox mounts: the targets make it on
	// the engine's host, and mount it at the same path in the test container.
	e2eDir = "/srv/agentswarm-e2e"
	// itRunLabel carries the id of a build/test/run.sh invocation,
	// AGENTSWARM_IT_RUN, by which run.sh removes what a test's cleanup never
	// reached.
	itRunLabel = "agentswarm-it.run"
)

// dockerServe is a deployment of the Docker runner that a test serves.
type dockerServe struct {
	deployment, addresses string
	sockets, store        string
	tenant, holder        string
}

// dockerDeployment writes a deployment of the Docker runner whose database is
// url, as servedDeployment writes the process runner's: a tenant of the
// test's own, it-<8 hex digits>, so that its start sweep meets no other
// test's containers; the runtime image AGENTSWARM_E2E_IMAGE names, which the
// targets build, failing the test, saying so, when it is unset; a sockets
// directory of the test's own under e2eDir, mode 0750, where a sandbox's
// bind mount needs it, removed when the test ends; a store of its own; the
// labels every container a test makes carries; and a configuration
// repository.
func dockerDeployment(t *testing.T, url string) *dockerServe {
	t.Helper()
	image := os.Getenv("AGENTSWARM_E2E_IMAGE")
	require.NotEmpty(t, image, "AGENTSWARM_E2E_IMAGE is unset: it names the runtime image's id, which make test-docker and make test-linux build and pass in")
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	// The holder is servedDeployment's.
	ds := &dockerServe{store: t.TempDir(), tenant: "it-" + hex.EncodeToString(b), holder: "instance-a"}
	sockets, err := os.MkdirTemp(e2eDir, "it-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(sockets) })
	require.NoError(t, os.Chmod(sockets, 0o750))
	ds.sockets = sockets
	labels := `"agentswarm.project": agent-swarm`
	if run := os.Getenv("AGENTSWARM_IT_RUN"); run != "" {
		labels += fmt.Sprintf(", %q: %q", itRunLabel, run)
	}
	ds.deployment, ds.addresses = servedDeployment(t, url, func(text string) string {
		text = asDocker(t, text, fmt.Sprintf("{sockets: %q, store: %q, labels: {%s}}", sockets, ds.store, labels))
		for _, edit := range []struct{ old, replacement string }{
			{old: "tenant: example\n", replacement: "tenant: " + ds.tenant + "\n"},
			{old: "sha256:" + strings.Repeat("0", 64), replacement: image},
		} {
			require.Contains(t, text, edit.old)
			text = strings.Replace(text, edit.old, edit.replacement, 1)
		}
		return text
	})
	_, err = git.PlainInit(filepath.Join(filepath.Dir(ds.deployment), "config"), false)
	require.NoError(t, err)
	return ds
}

// takeHoldersLock takes the holder's lock as Contract C12 gives it, the file
// <store>/holders/<hex of the tenant>.<hex of the holder>.lock, with
// flock(LOCK_EX|LOCK_NB), and returns its release. It takes it itself, not
// through the docker package's lockHolder, which is that package's own: a
// test that re-derives the contract from outside catches the runner's path
// drifting from it. The file must exist, as the runner makes it when it takes
// the lock.
func takeHoldersLock(store, tenant, holder string) (release func(), err error) {
	path := filepath.Join(store, "holders", hex.EncodeToString([]byte(tenant))+"."+hex.EncodeToString([]byte(holder))+".lock")
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() { _ = f.Close() }, nil
}

// overSocket sends a GET of / to the gateway whose socket is path, at TLS
// 1.3, trusting alone the certificate that a first handshake presents, which
// serve made at its start and keeps in its memory alone, for the name host;
// it sends it through a transport cloned from http.DefaultTransport, which
// offers h2 and http/1.1. It returns the answer, whose body it has read.
func overSocket(t *testing.T, path, host string) *http.Response {
	t.Helper()
	conn, err := tls.Dial("unix", path, &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13})
	require.NoError(t, err)
	presented := conn.ConnectionState().PeerCertificates
	require.NoError(t, conn.Close())
	require.NotEmpty(t, presented)
	roots := x509.NewCertPool()
	roots.AddCert(presented[0])
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", path)
	}
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13, NextProtos: []string{"h2", "http/1.1"}}
	t.Cleanup(transport.CloseIdleConnections)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+host+"/", http.NoBody)
	require.NoError(t, err)
	resp, err := (&http.Client{Transport: transport}).Do(req)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	require.NoError(t, resp.Body.Close())
	return resp
}

// Serve starts with the Docker runner on Linux (Plan 7d-2, Decision 8), on a
// fresh database and the runtime image the targets build. Its addresses file
// names the agents' sockets, unix:<path> (C2); a request over the MCP
// socket, at TLS 1.3 and with the certificate its handshake presents, is
// answered over HTTP/1.1 (Decision 2); and /v1/status shows the runner,
// sandboxed, its engine ok and no image missing (C4). Then, as a drain ends
// it, close removes the sockets but leaves the runner to exit, which releases
// the holder's lock after it (R19): the lock, taken as Contract C12 gives it,
// is held between the two, and free after both.
func TestServeStartsWithTheDockerRunner(t *testing.T) {
	u := freshDatabase(t, "serve_docker")
	migrated(t, u)
	ds := dockerDeployment(t, u.String())
	s, err := start(t.Context(), slog.New(slog.DiscardHandler), ds.deployment)
	require.NoError(t, err)
	closed, exited := false, false
	t.Cleanup(func() {
		if !closed {
			s.close(context.Background())
		}
		if !exited {
			_ = s.exit(context.Background(), exitOK)
		}
	})

	raw, err := os.ReadFile(ds.addresses)
	require.NoError(t, err)
	var addrs addresses
	require.NoError(t, json.Unmarshal(raw, &addrs))
	dir, err := filepath.EvalSymlinks(ds.sockets)
	require.NoError(t, err)
	assert.Equal(t, "unix:"+filepath.Join(dir, "mcp.sock"), addrs.MCP)
	assert.Equal(t, "unix:"+filepath.Join(dir, "llm.sock"), addrs.LLM)
	resp := overSocket(t, strings.TrimPrefix(addrs.MCP, "unix:"), "mcp.agentswarm.internal")
	assert.Equal(t, "HTTP/1.1", resp.Proto)
	assert.Equal(t, uint16(tls.VersionTLS13), resp.TLS.Version)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "the MCP gateway answers a request without a session's token")
	status, body := adminGet(t, "http://"+addrs.Admin+"/v1/status", "admin-token")
	require.Equal(t, http.StatusOK, status, body)
	var shown map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(body), &shown))
	assert.JSONEq(t, `{"kind": "docker", "sandboxed": true, "engine": "ok", "missingImages": []}`, string(shown["runner"]))

	s.close(context.Background())
	closed = true

	for _, name := range []string{"mcp.sock", "llm.sock"} {
		assert.NoFileExists(t, filepath.Join(dir, name), "close removed the socket")
	}
	_, err = takeHoldersLock(ds.store, ds.tenant, ds.holder)
	require.ErrorIs(t, err, syscall.EWOULDBLOCK, "close left the runner, and its holder's lock, to exit")

	err = s.exit(context.Background(), exitOK)
	exited = true

	require.NoError(t, err)
	release, err := takeHoldersLock(ds.store, ds.tenant, ds.holder)
	require.NoError(t, err, "exit released the holder's lock")
	release()
}

// A start that fails once the Docker runner is open closes the runner, as it
// closes what else it opened; the runner has no turn yet (R19). A sockets
// directory that holds notes.txt is refused by the listeners, which bind once
// the runner is open (R20), and the holder's lock, which the runner took,
// taken as Contract C12 gives it, is free once start has returned.
func TestAStartThatFailsAfterOpeningTheDockerRunnerClosesIt(t *testing.T) {
	u := freshDatabase(t, "serve_docker_refused")
	migrated(t, u)
	ds := dockerDeployment(t, u.String())
	require.NoError(t, os.WriteFile(filepath.Join(ds.sockets, "notes.txt"), nil, 0o600))

	s, err := start(t.Context(), slog.New(slog.DiscardHandler), ds.deployment)

	assert.Nil(t, s)
	require.ErrorContains(t, err, "runner.docker.sockets")
	require.ErrorContains(t, err, "notes.txt")
	release, err := takeHoldersLock(ds.store, ds.tenant, ds.holder)
	require.NoError(t, err, "the runner that the start opened, and failed after, holds the holder's lock still")
	release()
	assert.NoFileExists(t, ds.addresses, "serve wrote no addresses")
}
