package main

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"

	"github.com/jchen0426/agent-swarm/internal/deploy"
	"github.com/jchen0426/agent-swarm/internal/runner/docker"
)

// The keys that listenSockets' refusals name (Plan 7d-2, Decision 8).
const (
	socketsKey = "runner.docker.sockets"
	groupKey   = "runner.docker.group"
)

// socketMode is the sockets' mode (Decision 8): serve, which owns them, and
// the sandboxes' group connect; no one else does.
const socketMode = 0o660

// listenSockets checks runner.docker.sockets (Decision 8) on the system,
// takes its flock, replaces mcp.sock and llm.sock, and listens on both, mode
// 0660 and the group; engineSocket is the endpoint's path.
//
// It refuses, naming runner.docker.group, a group that is the engine
// socket's, or one serve is not a member of (checkGroup), before it touches
// the directory. It refuses, naming runner.docker.sockets, a directory that
// is missing or is not a directory, or that another serve holds (lockDir);
// that lies inside, or holds, a path of the deployment's, by the resolved
// paths of those that exist (standsApart); or that the group cannot
// traverse, that holds anything but mcp.sock and llm.sock, or that serve
// cannot write in (checkDir). It listens in the directory as it resolved it,
// the one it locked and checked. Each socket is removed when its listener
// closes; unlock releases the flock, once the caller has closed both. A
// refusal leaves nothing open and the flock free.
func listenSockets(d *deploy.Deployment, group int, engineSocket string) (mcp, llm net.Listener, unlock func(), err error) {
	if err := checkGroup(group, engineSocket); err != nil {
		return nil, nil, nil, err
	}
	dir, err := filepath.EvalSymlinks(d.Runner.Docker.Sockets)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%s: %w", socketsKey, err)
	}
	f, err := lockDir(dir)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%s: %w", socketsKey, err)
	}
	// Closing the directory releases its flock.
	release := sync.OnceFunc(func() { _ = f.Close() })
	if err := standsApart(d, dir, engineSocket); err != nil {
		release()
		return nil, nil, nil, err
	}
	if err := checkDir(f, dir, group); err != nil {
		release()
		return nil, nil, nil, fmt.Errorf("%s: %w", socketsKey, err)
	}
	if mcp, err = listenSocket(filepath.Join(dir, docker.MCPSocket), group); err != nil {
		release()
		return nil, nil, nil, fmt.Errorf("%s: %w", socketsKey, err)
	}
	if llm, err = listenSocket(filepath.Join(dir, docker.LLMSocket), group); err != nil {
		_ = mcp.Close()
		release()
		return nil, nil, nil, fmt.Errorf("%s: %w", socketsKey, err)
	}
	return mcp, llm, release, nil
}

// checkGroup refuses, naming runner.docker.group, the sandboxes' group when
// it is the group of the engine's socket, which would put every sandbox in
// the group that reaches the engine, root on the host in effect (Decision
// 10), and when serve is not a member of it, since serve could not then give
// it the sockets (Decision 8).
func checkGroup(group int, engineSocket string) error {
	info, err := os.Stat(engineSocket)
	if err != nil {
		return fmt.Errorf("%s: the engine's socket, whose group the sandboxes' must not be, cannot be read: %w", groupKey, err)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && int64(st.Gid) == int64(group) {
		return fmt.Errorf("%s: %d is the group of the engine's socket, %s: every sandbox would be in the group that reaches the engine, "+
			"root on the host in effect", groupKey, group, engineSocket)
	}
	groups, err := os.Getgroups()
	if err != nil {
		return fmt.Errorf("%s: serve's groups cannot be read: %w", groupKey, err)
	}
	if group != os.Getgid() && !slices.Contains(groups, group) {
		return fmt.Errorf("%s: serve is not a member of the group %d, so it cannot give the sockets to it", groupKey, group)
	}
	return nil
}

// lockDir opens the directory dir and takes its flock, which serve holds
// while it runs (Decision 8): it refuses a path that is not a directory, and
// a directory that another serve holds. Closing the file it returns releases
// the flock.
func lockDir(dir string) (*os.File, error) {
	f, err := os.Open(dir) //nolint:gosec // G304: the deployment's own sockets directory, resolved.
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	switch {
	case err != nil:
	case !info.IsDir():
		err = fmt.Errorf("%s is not a directory", dir)
	default:
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) //nolint:gosec // G115: a file descriptor fits an int.
		if errors.Is(err, syscall.EWOULDBLOCK) {
			err = fmt.Errorf("another serve holds the directory %s", dir)
		}
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// standsApart refuses a sockets directory, dir as it resolved, that lies
// inside, or holds, the secrets, the output, the store, the addresses file,
// the configuration repository or the engine's socket, by the resolved paths
// of those that exist (deploy.SocketsApart, which names
// runner.docker.sockets): every sandbox mounts the directory.
func standsApart(d *deploy.Deployment, dir, engineSocket string) error {
	return deploy.SocketsApart(dir, &deploy.Paths{
		Secrets: resolved(d.Secrets), Output: resolved(d.Runner.Output), Store: resolved(d.Runner.Docker.Store),
		Addresses: resolved(d.Listeners.Addresses), Repository: resolved(d.Configuration.Repository), Engine: resolved(engineSocket),
	})
}

// resolved is path made absolute, then its symbolic links followed, as
// deploy.SocketsApart compares it; "" for an empty path, and for one that
// does not exist, which SocketsApart skips.
func resolved(path string) string {
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	target, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return ""
	}
	return target
}

// checkDir refuses a sockets directory, dir, open as f, that the sandboxes'
// group cannot traverse, that holds anything but mcp.sock and llm.sock,
// which serve replaces, or that serve cannot write in.
func checkDir(f *os.File, dir string, group int) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !traversable(info, group) {
		return fmt.Errorf("the sandboxes' group, %d, cannot traverse %s, of mode %s: it must be group-owned by the group with g+x, or have o+x", group, dir, info.Mode().Perm())
	}
	names, err := f.Readdirnames(-1)
	if err != nil {
		return err
	}
	strays := slices.DeleteFunc(names, func(name string) bool { return name == docker.MCPSocket || name == docker.LLMSocket })
	if len(strays) > 0 {
		slices.Sort(strays)
		return fmt.Errorf("%s holds %s: every sandbox mounts it, so it holds %s and %s alone", dir, strings.Join(strays, ", "), docker.MCPSocket, docker.LLMSocket)
	}
	return writableDir(dir)
}

// traversable reports whether the sandboxes' group can traverse a directory
// of info: it is group-owned by the group with g+x, or anyone can, with o+x.
func traversable(info os.FileInfo, group int) bool {
	perm := info.Mode().Perm()
	if perm&0o001 != 0 {
		return true
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int64(st.Gid) == int64(group) && perm&0o010 != 0
}

// listenSocket replaces the socket at path, a stale one that a serve which
// exited at once left included, and listens on it, mode 0660 and of the
// sandboxes' group (Decision 8). The listener removes the socket when it
// closes, as net.Listen made it.
func listenSocket(path string, group int) (net.Listener, error) {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, socketMode); err != nil {
		_ = ln.Close()
		return nil, err
	}
	if err := os.Chown(path, -1, group); err != nil {
		_ = ln.Close()
		return nil, err
	}
	return ln, nil
}

// sandboxListener serves TLS on ln, 1.3 at least, with the gateways'
// certificate (Decision 2). Over ALPN it offers http/1.1 alone, the one
// protocol the agents' servers speak: a client that offers h2 too gets
// HTTP/1.1, where a server that took h2 would close the connection.
func sandboxListener(ln net.Listener, cert *tls.Certificate) net.Listener {
	return tls.NewListener(ln, &tls.Config{
		Certificates: []tls.Certificate{*cert},
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{"http/1.1"},
	})
}
