package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/deploy"
	"github.com/jchen0426/agent-swarm/internal/runner/docker"
)

// otherGroup returns a group of the test's user other than its own gid, as
// the sandboxes' group is, and skips the test when the user has none.
func otherGroup(t *testing.T) int {
	t.Helper()
	groups, err := os.Getgroups()
	require.NoError(t, err)
	for _, g := range groups {
		if g != os.Getgid() {
			return g
		}
	}
	t.Skip("the test's user is a member of no group but its own")
	return 0
}

// notMember returns a gid that the test's user is not a member of.
func notMember(t *testing.T) int {
	t.Helper()
	groups, err := os.Getgroups()
	require.NoError(t, err)
	g := 1
	for g == os.Getgid() || slices.Contains(groups, g) {
		g++
	}
	return g
}

// sandboxDeployment returns a deployment of the Docker runner whose paths lie
// in directories of the test's own, and the group and the engine's socket
// that listenSockets is given with it. The deployment's secrets, output,
// store and repository are directories, and its addresses file is not
// written yet, as at serve's start. group is a group of the test's user
// other than its own gid (otherGroup), and engine, which stands in for the
// engine's socket, a file of the user's own gid: so the sandboxes' group is
// not the engine socket's. The sockets directory, whose path is short enough
// for a socket's (socketDir), is mode 0750 and group-owned by group, as the
// sandboxes traverse it.
func sandboxDeployment(t *testing.T) (d *deploy.Deployment, group int, engine string) {
	t.Helper()
	group = otherGroup(t)
	dir := t.TempDir()
	path := func(name string) string { return filepath.Join(dir, name) }
	for _, name := range []string{"secrets", "turns", "images", "config"} {
		require.NoError(t, os.Mkdir(path(name), 0o700))
	}
	engine = path("docker.sock")
	require.NoError(t, os.WriteFile(engine, nil, 0o600))
	require.NoError(t, os.Chown(engine, -1, os.Getgid()))
	sockets := socketDir(t)
	require.NoError(t, os.Chmod(sockets, 0o750))
	require.NoError(t, os.Chown(sockets, -1, group))
	d = &deploy.Deployment{
		Secrets:       path("secrets"),
		Configuration: deploy.Configuration{Repository: path("config")},
		Listeners:     deploy.Listeners{Addresses: path("addresses.json")},
		Runner: deploy.Runner{
			Kind: deploy.KindDocker, Output: path("turns"),
			Docker: &deploy.Docker{Sockets: sockets, Store: path("images"), Group: group},
		},
	}
	return d, group, engine
}

// chmod sets a directory's mode, and gives it back its owner's rights when
// the test ends, so that the test's cleanup can remove it.
func chmod(t *testing.T, dir string, mode os.FileMode) {
	t.Helper()
	require.NoError(t, os.Chmod(dir, mode))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
}

// holdFlock takes the flock of the directory dir, as another serve holds it,
// until the test ends.
func holdFlock(t *testing.T, dir string) {
	t.Helper()
	f, err := os.Open(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })
	require.NoError(t, syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB))
}

// flockFree reports whether the flock of the directory dir is free: the test
// takes it, and releases it at once.
func flockFree(t *testing.T, dir string) bool {
	t.Helper()
	f, err := os.Open(dir)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return false
	}
	require.NoError(t, err)
	return true
}

// A sockets directory that cannot serve is refused, naming
// runner.docker.sockets and why (Review Focus 5; Decision 8): one that is
// missing; a file; one serve cannot write in; one the sandboxes' group cannot
// traverse; one that holds a file but serve's sockets; one another serve
// holds, as the test's flock stands in for; a symbolic link that resolves
// into the secrets directory; and one that holds the output directory.
// Nothing is left listening.
func TestASocketsDirectoryThatCannotServeIsRefusedNamingTheKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		// edit makes the deployment's sockets directory one that cannot
		// serve.
		edit func(t *testing.T, d *deploy.Deployment)
		// why is what the refusal says, besides the key.
		why string
	}{
		{name: "missing", why: "no such file or directory", edit: func(_ *testing.T, d *deploy.Deployment) {
			d.Runner.Docker.Sockets = filepath.Join(d.Runner.Docker.Sockets, "gone")
		}},
		{name: "a file", why: "is not a directory", edit: func(t *testing.T, d *deploy.Deployment) {
			file := filepath.Join(d.Runner.Docker.Sockets, "file")
			require.NoError(t, os.WriteFile(file, nil, 0o600))
			d.Runner.Docker.Sockets = file
		}},
		{name: "not writable", why: "cannot be written", edit: func(t *testing.T, d *deploy.Deployment) {
			if os.Geteuid() == 0 {
				t.Skip("root writes in any directory")
			}
			chmod(t, d.Runner.Docker.Sockets, 0o550)
		}},
		{name: "mode 0700 for the group", why: "cannot traverse", edit: func(t *testing.T, d *deploy.Deployment) {
			chmod(t, d.Runner.Docker.Sockets, 0o700)
		}},
		{name: "holding notes.txt", why: "notes.txt", edit: func(t *testing.T, d *deploy.Deployment) {
			require.NoError(t, os.WriteFile(filepath.Join(d.Runner.Docker.Sockets, "notes.txt"), nil, 0o600))
		}},
		{name: "flocked by the test", why: "another serve", edit: func(t *testing.T, d *deploy.Deployment) {
			holdFlock(t, d.Runner.Docker.Sockets)
		}},
		{name: "a symbolic link into the secrets directory", why: "outside secrets", edit: func(t *testing.T, d *deploy.Deployment) {
			inside := filepath.Join(d.Secrets, "sockets")
			require.NoError(t, os.Mkdir(inside, 0o750))
			link := filepath.Join(d.Runner.Docker.Sockets, "link")
			require.NoError(t, os.Symlink(inside, link))
			d.Runner.Docker.Sockets = link
		}},
		{name: "a directory holding the output directory", why: "hold runner.output", edit: func(t *testing.T, d *deploy.Deployment) {
			d.Runner.Output = filepath.Join(d.Runner.Docker.Sockets, "turns")
			require.NoError(t, os.Mkdir(d.Runner.Output, 0o700))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, group, engine := sandboxDeployment(t)
			tc.edit(t, d)

			mcp, llm, unlock, err := listenSockets(d, group, engine)

			require.ErrorContains(t, err, "runner.docker.sockets")
			require.ErrorContains(t, err, tc.why)
			assert.Nil(t, mcp)
			assert.Nil(t, llm)
			assert.Nil(t, unlock)
		})
	}
}

// listenSockets replaces a stale mcp.sock, as a serve that exited at once
// leaves one, and listens on mcp.sock and llm.sock, each mode 0660 and of the
// sandboxes' group (Decision 8), while it holds the directory's flock.
// Closing the listeners removes the sockets, and unlock releases the flock.
// The directory is of the user's own group, and anyone may traverse it
// (o+x), so that the sockets take the sandboxes' group from listenSockets
// alone: macOS gives a new file its directory's group.
func TestTheSocketsAreMadeForTheSandboxesGroup(t *testing.T) {
	d, group, engine := sandboxDeployment(t)
	dir := d.Runner.Docker.Sockets
	require.NoError(t, os.Chown(dir, -1, os.Getgid()))
	require.NoError(t, os.Chmod(dir, 0o751))
	stale, err := net.Listen("unix", filepath.Join(dir, "mcp.sock"))
	require.NoError(t, err)
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	require.NoError(t, stale.Close())
	_, err = net.Dial("unix", filepath.Join(dir, "mcp.sock"))
	require.Error(t, err, "a stale socket takes no connection")

	mcp, llm, unlock, err := listenSockets(d, group, engine)

	require.NoError(t, err)
	for _, name := range []string{"mcp.sock", "llm.sock"} {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		require.NoError(t, err, name)
		assert.Equal(t, os.ModeSocket|0o660, info.Mode(), name)
		st, ok := info.Sys().(*syscall.Stat_t)
		require.True(t, ok, name)
		assert.EqualValues(t, group, st.Gid, "%s: the sandboxes' group", name)
		conn, err := net.Dial("unix", path)
		require.NoError(t, err, "%s takes connections", name)
		require.NoError(t, conn.Close())
	}
	assert.False(t, flockFree(t, dir), "serve holds the directory's flock")

	require.NoError(t, mcp.Close())
	require.NoError(t, llm.Close())
	unlock()

	for _, name := range []string{"mcp.sock", "llm.sock"} {
		assert.NoFileExists(t, filepath.Join(dir, name), "closing its listener removed the socket")
	}
	assert.True(t, flockFree(t, dir), "unlock released the flock")
}

// listenSockets refuses, naming runner.docker.group, the group of the
// engine's socket, which would give the sandboxes' group the engine
// (Decision 10), and a group serve is not a member of, to which it cannot
// give the sockets (Decision 8). It refuses them before it touches the
// sockets directory.
func TestAGroupThatIsTheEnginesOrNotServesIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name  string
		group func(t *testing.T) int
		why   string
	}{
		// sandboxDeployment's engine socket is of the user's own gid.
		{name: "the engine socket's", group: func(*testing.T) int { return os.Getgid() }, why: "the engine's socket"},
		{name: "one serve is not a member of", group: notMember, why: "not a member"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, _, engine := sandboxDeployment(t)

			mcp, llm, unlock, err := listenSockets(d, tc.group(t), engine)

			require.ErrorContains(t, err, "runner.docker.group")
			require.ErrorContains(t, err, tc.why)
			assert.Nil(t, mcp)
			assert.Nil(t, llm)
			assert.Nil(t, unlock)
			assert.True(t, flockFree(t, d.Runner.Docker.Sockets), "the sockets directory was not locked")
		})
	}
}

// The agents' listeners under the Docker runner serve TLS 1.3 at least, with
// the gateways' certificate, and HTTP/1.1 alone (Decision 2), over a
// socket: a client capped at TLS 1.2 fails its handshake, and one that offers
// h2 and http/1.1, as a transport cloned from http.DefaultTransport does, is
// served HTTP/1.1. The servers' own settings are asserted by
// TestServesHTTPServersBoundTheirReads.
func TestTheAgentsListenersServeTLS13AndHTTP11Alone(t *testing.T) {
	cert, err := docker.NewCertificate(time.Now())
	require.NoError(t, err)
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM([]byte(cert.PEM)))
	s := newServers(nil, &bodies{}, http.NotFoundHandler(), slog.New(slog.DiscardHandler))
	dir := socketDir(t)
	for _, gw := range []struct {
		name string
		srv  *http.Server
	}{{"mcp", s.mcp}, {"llm", s.llm}} {
		path := filepath.Join(dir, gw.name+".sock")
		ln, err := net.Listen("unix", path)
		require.NoError(t, err)
		go func() { _ = gw.srv.Serve(sandboxListener(ln, &cert.TLS)) }()
		t.Cleanup(func() { _ = gw.srv.Close() })
		host := gw.name + ".agentswarm.internal"

		_, err = tls.Dial("unix", path, &tls.Config{RootCAs: roots, ServerName: host, MaxVersion: tls.VersionTLS12})

		require.ErrorContains(t, err, "protocol version", "%s: a client capped at TLS 1.2", gw.name)

		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: roots, NextProtos: []string{"h2", "http/1.1"}}
		t.Cleanup(transport.CloseIdleConnections)
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+host+"/", http.NoBody)
		require.NoError(t, err)

		resp, err := (&http.Client{Transport: transport}).Do(req)

		require.NoError(t, err, gw.name)
		require.NoError(t, resp.Body.Close())
		assert.Equal(t, "HTTP/1.1", resp.Proto, gw.name)
		assert.Equal(t, uint16(tls.VersionTLS13), resp.TLS.Version, gw.name)
	}
}
