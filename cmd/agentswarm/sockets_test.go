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
	"strings"
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

// resolvedDir returns the directory dir by its resolved path, its symbolic
// links followed: on macOS, the temporary directory lies under /var, a link
// to /private/var, and serve refuses a runner.docker.sockets that is not its
// resolved path.
func resolvedDir(t *testing.T, dir string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	return resolved
}

// sandboxDeployment returns a deployment of the Docker runner whose paths lie
// in directories of the test's own, each named by its resolved path
// (resolvedDir), and the group and the engine's socket that listenSockets is
// given with it. The deployment's secrets, output, store and repository are
// directories, and its addresses file is not written yet, as at serve's
// start. group is a group of the test's user other than its own gid
// (otherGroup), and engine, which stands in for the engine's socket, a file
// of the user's own gid: so the sandboxes' group is not the engine socket's.
// The sockets directory, whose path is short enough for a socket's
// (socketDir), is mode 0750 and group-owned by group, as the sandboxes
// traverse it.
func sandboxDeployment(t *testing.T) (d *deploy.Deployment, group int, engine string) {
	t.Helper()
	group = otherGroup(t)
	dir := resolvedDir(t, t.TempDir())
	path := func(name string) string { return filepath.Join(dir, name) }
	for _, name := range []string{"secrets", "turns", "images", "config"} {
		require.NoError(t, os.Mkdir(path(name), 0o700))
	}
	engine = path("docker.sock")
	require.NoError(t, os.WriteFile(engine, nil, 0o600))
	require.NoError(t, os.Chown(engine, -1, os.Getgid()))
	sockets := resolvedDir(t, socketDir(t))
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

// listenWithin calls listenSockets, and fails the test unless it returns
// within 5 s, so that a call that hangs, as an open of a FIFO waits for a
// writer, fails rather than hangs.
func listenWithin(t *testing.T, d *deploy.Deployment, group int, engine string) (mcp, llm net.Listener, unlock func(), err error) {
	t.Helper()
	type result struct {
		mcp, llm net.Listener
		unlock   func()
		err      error
	}
	done := make(chan result, 1)
	go func() {
		var r result
		r.mcp, r.llm, r.unlock, r.err = listenSockets(d, group, engine)
		done <- r
	}()
	select {
	case r := <-done:
		return r.mcp, r.llm, r.unlock, r.err
	case <-time.After(5 * time.Second):
		t.Fatal("listenSockets did not return within 5s")
		return nil, nil, nil, nil
	}
}

// A sockets directory that cannot serve is refused, naming
// runner.docker.sockets and why (Review Focus 5; Decision 8): one that is
// missing; a file; a FIFO, at once; one serve cannot write in; one its group
// or others can write in, as serve makes its sockets there by their paths;
// one the sandboxes' group cannot traverse; one that holds a file but serve's
// sockets; one another serve holds, as the test's flock stands in for; a
// symbolic link, even to a directory that could serve, as every sandbox
// mounts the path given, which the refusal names the resolved path in place
// of; one inside the secrets directory, which the deployment names through a
// link; one that holds the output directory; one that would hold the
// addresses file, which serve's first start has not written yet, and which
// the deployment names through a link; and one whose sockets' paths would be
// too long for a socket. Nothing is left listening, and a refusal that locked
// the directory frees its flock.
func TestASocketsDirectoryThatCannotServeIsRefusedNamingTheKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		// edit makes the deployment's sockets directory one that cannot
		// serve.
		edit func(t *testing.T, d *deploy.Deployment)
		// why is what the refusal says, besides the key.
		why string
		// locked says the refusal comes once listenSockets has locked the
		// directory runner.docker.sockets resolves to, whose flock it must
		// free.
		locked bool
		// resolved says the refusal names the directory runner.docker.sockets
		// resolves to, the path to give in its place.
		resolved bool
	}{
		{name: "missing", why: "no such file or directory", edit: func(_ *testing.T, d *deploy.Deployment) {
			d.Runner.Docker.Sockets = filepath.Join(d.Runner.Docker.Sockets, "gone")
		}},
		{name: "a file", why: "is not a directory", edit: func(t *testing.T, d *deploy.Deployment) {
			file := filepath.Join(d.Runner.Docker.Sockets, "file")
			require.NoError(t, os.WriteFile(file, nil, 0o600))
			d.Runner.Docker.Sockets = file
		}},
		{name: "a FIFO", why: "is not a directory", edit: func(t *testing.T, d *deploy.Deployment) {
			fifo := filepath.Join(d.Runner.Docker.Sockets, "fifo")
			require.NoError(t, syscall.Mkfifo(fifo, 0o600))
			// A writer frees an open that waits on the FIFO, once the test has
			// failed for it.
			t.Cleanup(func() {
				if w, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
					_ = w.Close()
				}
			})
			d.Runner.Docker.Sockets = fifo
		}},
		{name: "not writable", why: "cannot be written", locked: true, edit: func(t *testing.T, d *deploy.Deployment) {
			if os.Geteuid() == 0 {
				t.Skip("root writes in any directory")
			}
			chmod(t, d.Runner.Docker.Sockets, 0o550)
		}},
		{name: "mode 0770, which its group can write in", why: "can be written by its group or by others", locked: true, edit: func(t *testing.T, d *deploy.Deployment) {
			chmod(t, d.Runner.Docker.Sockets, 0o770)
		}},
		{name: "mode 0757, which others can write in", why: "can be written by its group or by others", locked: true, edit: func(t *testing.T, d *deploy.Deployment) {
			chmod(t, d.Runner.Docker.Sockets, 0o757)
		}},
		{name: "mode 0700 for the group", why: "cannot traverse", locked: true, edit: func(t *testing.T, d *deploy.Deployment) {
			chmod(t, d.Runner.Docker.Sockets, 0o700)
		}},
		{name: "holding notes.txt", why: "notes.txt", locked: true, edit: func(t *testing.T, d *deploy.Deployment) {
			require.NoError(t, os.WriteFile(filepath.Join(d.Runner.Docker.Sockets, "notes.txt"), nil, 0o600))
		}},
		{name: "flocked by the test", why: "another serve", edit: func(t *testing.T, d *deploy.Deployment) {
			holdFlock(t, d.Runner.Docker.Sockets)
		}},
		{name: "a symbolic link to a directory that could serve", why: "resolves to", resolved: true, edit: func(t *testing.T, d *deploy.Deployment) {
			link := filepath.Join(t.TempDir(), "sockets")
			require.NoError(t, os.Symlink(d.Runner.Docker.Sockets, link))
			d.Runner.Docker.Sockets = link
		}},
		{name: "a directory inside the secrets directory, which the deployment names through a link", why: "outside secrets", edit: func(t *testing.T, d *deploy.Deployment) {
			inside := filepath.Join(d.Secrets, "sockets")
			require.NoError(t, os.Mkdir(inside, 0o750))
			link := filepath.Join(t.TempDir(), "secrets")
			require.NoError(t, os.Symlink(d.Secrets, link))
			d.Secrets = link
			d.Runner.Docker.Sockets = inside
		}},
		{name: "a directory holding the output directory", why: "hold runner.output", locked: true, edit: func(t *testing.T, d *deploy.Deployment) {
			d.Runner.Output = filepath.Join(d.Runner.Docker.Sockets, "turns")
			require.NoError(t, os.Mkdir(d.Runner.Output, 0o700))
		}},
		{name: "a directory that would hold the addresses file, which the deployment names through a link", why: "hold listeners.addresses", locked: true, edit: func(t *testing.T, d *deploy.Deployment) {
			link := filepath.Join(t.TempDir(), "sockets")
			require.NoError(t, os.Symlink(d.Runner.Docker.Sockets, link))
			d.Listeners.Addresses = filepath.Join(link, "addresses.json")
		}},
		{name: "a directory whose sockets' paths are too long", why: "a Unix socket's path may hold", locked: true, edit: func(t *testing.T, d *deploy.Deployment) {
			long := filepath.Join(resolvedDir(t, t.TempDir()), strings.Repeat("d", 100))
			require.NoError(t, os.Mkdir(long, 0o750))
			require.NoError(t, os.Chown(long, -1, d.Runner.Docker.Group))
			d.Runner.Docker.Sockets = long
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, group, engine := sandboxDeployment(t)
			tc.edit(t, d)

			mcp, llm, unlock, err := listenWithin(t, d, group, engine)

			require.ErrorContains(t, err, "runner.docker.sockets")
			require.ErrorContains(t, err, tc.why)
			assert.Nil(t, mcp)
			assert.Nil(t, llm)
			assert.Nil(t, unlock)
			if tc.resolved {
				dir, evalErr := filepath.EvalSymlinks(d.Runner.Docker.Sockets)
				require.NoError(t, evalErr)
				require.ErrorContains(t, err, "resolves to "+dir+",", "the refusal names the path to give")
			}
			if tc.locked {
				dir, err := filepath.EvalSymlinks(d.Runner.Docker.Sockets)
				require.NoError(t, err)
				assert.True(t, flockFree(t, dir), "the refusal freed the flock of %s", dir)
			}
		})
	}
}

// names returns the names of the entries of the directory dir, sorted.
func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// Serve refuses, through listenSockets, a sockets directory that it does not
// own: the owner's check runs with serve's effective uid. /usr stands in for
// one: root owns it, neither its group nor others can write in it, and it is
// on the Mac and in the Linux test container alike. The refusal names
// runner.docker.sockets and the owner, writes nothing in /usr, and frees its
// flock. Run as root, serve owns /usr, so the test skips; build/test/run.sh,
// which runs the gates on Linux, refuses root, and the Mac's gates run as the
// developer.
func TestASocketsDirectoryRootOwnsIsRefusedNamingItsOwner(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root owns /usr, so a serve that runs as root passes the owner's check")
	}
	const usr = "/usr"
	info, err := os.Stat(usr)
	require.NoError(t, err)
	st, ok := info.Sys().(*syscall.Stat_t)
	require.True(t, ok)
	require.Zero(t, st.Uid, "root owns %s", usr)
	require.Zero(t, info.Mode().Perm()&0o022, "neither the group nor others can write in %s", usr)
	before := names(t, usr)
	d, group, engine := sandboxDeployment(t)
	d.Runner.Docker.Sockets = usr

	mcp, llm, unlock, err := listenWithin(t, d, group, engine)

	require.ErrorContains(t, err, "runner.docker.sockets")
	require.ErrorContains(t, err, usr+" is owned by uid 0, not serve's")
	assert.Nil(t, mcp)
	assert.Nil(t, llm)
	assert.Nil(t, unlock)
	assert.Equal(t, before, names(t, usr), "nothing was written in %s", usr)
	assert.True(t, flockFree(t, usr), "the refusal freed the flock of %s", usr)
}

// Serve refuses a sockets directory that it does not own (the controller's
// ruling on Decision 8). The test cannot make a directory of another user's,
// as a user but root cannot give a directory away, and the gates run as the
// developer's user: so the owner's check is given the stat of the test's own
// directory, which passes it, and another uid as serve's.
func TestASocketsDirectoryServeDoesNotOwnIsRefused(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o750))
	info, err := os.Stat(dir)
	require.NoError(t, err)
	require.NoError(t, closedToOthers(dir, info, os.Geteuid()), "the test's own directory, mode 0750")

	err = closedToOthers(dir, info, os.Geteuid()+1)

	require.ErrorContains(t, err, "is owned by uid")
	require.ErrorContains(t, err, "not serve's")
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
