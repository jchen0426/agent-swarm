//go:build e2e

package harness

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	// serveWait bounds the wait for serve to write its addresses, or to exit
	// before it does: its start reads the database and the configuration.
	serveWait = 2 * time.Minute
	// logTail is how much of serve's log a failed test shows.
	logTail = 64 << 10
)

// Addresses are what serve writes once its listeners are bound (Contract
// C2): its process id, and the address of each listener.
type Addresses struct {
	PID   int    `json:"pid"`
	MCP   string `json:"mcp"`
	LLM   string `json:"llm"`
	Admin string `json:"admin"`
	// Web is the web listener's, with a web block (Plan 9a-1); "" without.
	Web string `json:"web,omitempty"`
	// Inbound is the inbound listener's, with listeners.inbound (Plan
	// 7c-1b); "" without.
	Inbound string `json:"inbound,omitempty"`
}

// Instance is a serve the harness runs, under a parent (R20): agentswarm
// serve --exit-with-stdin, whose standard input the parent holds. Closing it,
// as the test's end does, makes serve stop every turn and exit at once (R6).
type Instance struct {
	deployment string
	// turns is the deployment's runner.output, the turns' output files.
	turns string
	// sockets is runner.docker.sockets under Docker; "" otherwise.
	sockets string
	parent  *Parent
	addrs   Addresses
	log     *Log
	admin   *Admin
}

// Serve runs agentswarm serve on a deployment of the instance, with
// --exit-with-stdin, under StartParent, and returns once serve has written its
// addresses (Contract C2), or has exited before it did, as a serve that
// refuses to start does: Wait then gives its exit code, and Log says why. An
// addresses file an earlier serve of the instance left is set aside first. A
// serve that does neither within serveWait fails the test. When the test
// fails, its log shows the end of serve's. Serve's temporary directory, under
// which the runner makes each turn's, is the instance's own, which the test's
// end removes: a serve that exits at once leaves its turns' directories.
//
// Under Docker, Serve skips the test on a system other than Linux, where a
// sandbox's socket cannot reach serve: "needs Linux: make test-linux". It
// makes the instance's sockets directory first (makeSockets).
func (e *Env) Serve(t *testing.T, instance, deployment string) *Instance {
	t.Helper()
	if e.runner == dockerRunner && runtime.GOOS != "linux" {
		t.Skip("needs Linux: make test-linux")
	}
	in := e.instance(t, instance)
	if in.sockets != "" {
		makeSockets(t, in.sockets)
	}
	setAside(t, in.addresses)
	tmp := filepath.Join(in.dir, "tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatalf("harness: %v", err)
	}
	p := e.StartParent(t, []string{binary(t), "serve", "--deployment", deployment, "--exit-with-stdin"}, []string{"TMPDIR=" + tmp})
	i := &Instance{deployment: deployment, turns: in.output, sockets: in.sockets, parent: p, log: &Log{path: p.Stderr(), exited: p.exited}}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("harness: the end of the log of serve %s:\n%s", instance, i.log.tail(logTail))
		}
	})
	deadline := time.Now().Add(serveWait)
	for {
		if a, ok := readAddresses(in.addresses); ok {
			i.addrs = a
			i.admin = newAdmin(a.Admin, in.adminToken)
			if a.Web != "" {
				e.Web().register(instance, a.Web)
			}
			return i
		}
		select {
		case <-p.exited:
			return i
		case <-time.After(20 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatalf("harness: serve %s wrote no addresses within %v; its log:\n%s", instance, serveWait, i.log.tail(logTail))
		}
	}
}

// makeSockets makes an instance's sockets directory, runner.docker.sockets,
// and its tenant's directory, under e2eDir, which build/test/run.sh removes at
// its end (Contract C9; R17). The instance's is mode 0750, whatever the
// umask: serve takes a directory of its own that its group cannot write, and
// every sandbox, in that group, traverses it. The test's end removes it, and
// what a killed serve left in it, its sockets, once serve has exited: the
// removal is registered before serve's parent, whose end runs first.
func makeSockets(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("harness: make the sockets directory under %s, which make test-linux and make test-docker make: %v", e2eDir, err)
	}
	if err := os.Chmod(dir, 0o750); err != nil { //nolint:gosec // G302: a directory the sandboxes' group must traverse (C9), which only its owner writes.
		t.Fatalf("harness: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Logf("harness: remove the sockets directory %s: %v", dir, err)
		}
	})
}

// setAside renames a file an earlier serve left, so that the file the next
// one writes is told from it; nothing is removed.
func setAside(t testing.TB, path string) {
	t.Helper()
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return
	}
	if err := os.Rename(path, path+"."+strconv.FormatInt(time.Now().UnixNano(), 10)); err != nil {
		t.Fatalf("harness: set aside %s: %v", path, err)
	}
}

// readAddresses reads an addresses file, which serve writes whole, by a
// rename: it reports false while there is none.
func readAddresses(path string) (Addresses, bool) {
	raw, err := os.ReadFile(path) //nolint:gosec // G304: the instance's own addresses file.
	if err != nil {
		return Addresses{}, false
	}
	var a Addresses
	if json.Unmarshal(raw, &a) != nil || a.PID <= 0 {
		return Addresses{}, false
	}
	return a, true
}

// Addresses returns what serve wrote once its listeners were bound (C2): the
// zero Addresses when serve exited before it bound them.
func (i *Instance) Addresses() Addresses { return i.addrs }

// Log returns serve's log, the JSON lines on its standard error (C3).
func (i *Instance) Log() *Log { return i.log }

// Deployment returns the path of the deployment file serve runs on (C1).
func (i *Instance) Deployment() string { return i.deployment }

// Admin returns serve's admin API (C4), called with the instance's admin
// token; nil when serve exited before it bound its listeners.
func (i *Instance) Admin() *Admin { return i.admin }

// Signal sends sig to serve itself, by the process id it wrote (C2); its
// parent and its turns get nothing.
func (i *Instance) Signal(t *testing.T, sig syscall.Signal) {
	t.Helper()
	if i.addrs.PID <= 0 {
		t.Fatal("harness: serve wrote no process id to signal")
	}
	if err := syscall.Kill(i.addrs.PID, sig); err != nil {
		t.Fatalf("harness: send %v to serve: %v", sig, err)
	}
}

// KillParent sends SIGKILL to serve's parent, and waits until it is gone:
// serve's standard input closes with it.
func (i *Instance) KillParent(t *testing.T) {
	t.Helper()
	i.parent.Kill(t)
}

// Exited says, without waiting, whether serve has exited: its parent exits
// with it, and is then reaped. Once it has, its pid is no longer serve's.
func (i *Instance) Exited() bool {
	select {
	case <-i.parent.exited:
		return true
	default:
		return false
	}
}

// Wait waits until serve has exited, and returns its exit code, which its
// parent exits with: 128 and the signal's number when a signal ended it, or
// -1 when the parent itself was killed.
func (i *Instance) Wait(t *testing.T, timeout time.Duration) int {
	t.Helper()
	return i.parent.Wait(t, timeout)
}

// Output returns all serve has written so far: its standard output, then its
// standard error.
func (i *Instance) Output(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, path := range []string{i.parent.Stdout(), i.parent.Stderr()} {
		raw, err := os.ReadFile(path) //nolint:gosec // G304: a file of the parent's, which the harness made.
		if err != nil {
			t.Fatalf("harness: read what serve wrote: %v", err)
		}
		b.Write(raw)
	}
	return b.String()
}

// TurnOutputs returns the turns' output files, <runner.output>/<turn id>.out,
// what each holds by its turn's id.
func (i *Instance) TurnOutputs(t *testing.T) map[string]string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(i.turns, "*.out"))
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	out := make(map[string]string, len(paths))
	for _, path := range paths {
		raw, err := os.ReadFile(path) //nolint:gosec // G304: a turn's output file, in the instance's directory.
		if err != nil {
			t.Fatalf("harness: read a turn's output: %v", err)
		}
		out[strings.TrimSuffix(filepath.Base(path), ".out")] = string(raw)
	}
	return out
}

// TurnStarted is a turn serve logged as started (Contract C3).
type TurnStarted struct {
	Turn      string
	PID       int    // the process runner's
	Container string // the Docker runner's
}

// TurnsStarted returns the turns serve logged as started so far, in order: by
// its turn started lines, which name a turn's process under the process
// runner and its container under Docker.
func (i *Instance) TurnsStarted() []TurnStarted {
	var out []TurnStarted
	for _, line := range i.log.Lines() {
		if line["msg"] != "turn started" {
			continue
		}
		var s TurnStarted
		s.Turn, _ = line["turn"].(string)
		if pid, ok := line["pid"].(float64); ok {
			s.PID = int(pid)
		}
		s.Container, _ = line["container"].(string)
		out = append(out, s)
	}
	return out
}

// SocketsDir is the instance's runner.docker.sockets under Docker; ""
// otherwise.
func (i *Instance) SocketsDir() string { return i.sockets }
