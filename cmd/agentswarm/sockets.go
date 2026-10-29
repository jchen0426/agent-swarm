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

// maxSocketPath is the most bytes a Unix socket's path may hold on Linux,
// whose sun_path holds 108 with the terminating NUL: the deployment's check
// measures the configured sockets path by it, and listenSockets the
// resolved one, which it binds.
const maxSocketPath = 107

// listenSockets checks runner.docker.sockets (Decision 8) on the system,
// takes its flock, replaces mcp.sock and llm.sock, and listens on both, mode
// 0660 and the group; engineSocket is the endpoint's path.
//
// It refuses, naming runner.docker.group, a group that is the engine
// socket's, or one serve is not a member of (checkGroup), before it touches
// the directory. It refuses, naming runner.docker.sockets, a directory that
// is missing; that is not a directory, a FIFO among them, or that another
// serve holds (lockDir); that lies inside, or holds, a path of the
// deployment's, by the resolved paths of those that exist and the addresses
// file's by its directory's (standsApart); that serve does not own, or its
// group or others can write in, that the group cannot traverse, that holds
// anything but mcp.sock and llm.sock, or that serve cannot write in
// (checkDir); and, before it binds, one whose resolved path gives mcp.sock a
// path longer than a socket's may be. It listens in the directory as it
// resolved it, the one it locked and checked. Each socket is removed when
// its listener closes; unlock releases the flock, once the caller has closed
// both. A refusal leaves nothing open and the flock free.
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
	if n := len(filepath.Join(dir, docker.MCPSocket)); n > maxSocketPath {
		release()
		return nil, nil, nil, fmt.Errorf("%s: resolves to %s, which gives %s a path of %d bytes, above the %d a Unix socket's path may hold",
			socketsKey, dir, docker.MCPSocket, n, maxSocketPath)
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
// while it runs (Decision 8): it refuses a path that is not a directory, at
// once, a FIFO among them, which a plain open would wait on until a writer
// came; and a directory that another serve holds. Closing the file it
// returns releases the flock.
func lockDir(dir string) (*os.File, error) {
	f, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_DIRECTORY, 0) //nolint:gosec // G304: the deployment's own sockets directory, resolved.
	if errors.Is(err, syscall.ENOTDIR) {
		return nil, fmt.Errorf("%s is not a directory", dir)
	}
	if err != nil {
		return nil, err
	}
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) //nolint:gosec // G115: a file descriptor fits an int.
	if errors.Is(err, syscall.EWOULDBLOCK) {
		err = fmt.Errorf("another serve holds the directory %s", dir)
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
// of those that exist, and the addresses file's by its directory's
// (deploy.SocketsApart, which names runner.docker.sockets): every sandbox
// mounts the directory.
func standsApart(d *deploy.Deployment, dir, engineSocket string) error {
	return deploy.SocketsApart(dir, &deploy.Paths{
		Secrets: resolved(d.Secrets), Output: resolved(d.Runner.Output), Store: resolved(d.Runner.Docker.Store),
		Addresses: resolvedFile(d.Listeners.Addresses), Repository: resolved(d.Configuration.Repository), Engine: resolved(engineSocket),
	})
}

// resolvedFile is a file's path placed by its directory: the directory
// resolved, as resolved resolves it, with the file's name joined. So the
// addresses file, which serve writes only once it listens, is placed at
// serve's first start too. It is "" for an empty path, and for one whose
// directory does not exist.
func resolvedFile(path string) string {
	if path == "" {
		return ""
	}
	dir := resolved(filepath.Dir(path))
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, filepath.Base(path))
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

// checkDir refuses a sockets directory, dir, open as f, that serve does not
// own, or that its group or others can write in (closedToOthers); that the
// sandboxes' group cannot traverse; that holds anything but mcp.sock and
// llm.sock, which serve replaces; or that serve cannot write in.
func checkDir(f *os.File, dir string, group int) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if err := closedToOthers(dir, info, os.Geteuid()); err != nil {
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

// closedToOthers refuses a sockets directory, dir, of info, that serve's
// effective user, euid, does not own, or that its group or others can write
// in (the controller's ruling on Decision 8). Serve gives each socket its
// mode and group by its path, after the bind, so a writer of the directory
// could swap a socket for a link to a file of serve's in between, which
// serve would then open to the sandboxes' group; and could leave a socket of
// its own in serve's way.
func closedToOthers(dir string, info os.FileInfo, euid int) error {
	why := "serve makes its sockets there, by their paths, so the directory must be serve's own, and closed to other writers"
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("the owner of %s cannot be read: %s", dir, why)
	}
	if int64(st.Uid) != int64(euid) {
		return fmt.Errorf("%s is owned by uid %d, not serve's, %d: %s", dir, st.Uid, euid, why)
	}
	if perm := info.Mode().Perm(); perm&0o022 != 0 {
		return fmt.Errorf("%s, of mode %s, can be written by its group or by others: %s", dir, perm, why)
	}
	return nil
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
