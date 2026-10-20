//go:build e2e

package harness

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	// parentDirEnv names the directory in which a parent keeps its child's
	// output.
	parentDirEnv = "AGENTSWARM_E2E_PARENT_DIR"
	// parentGrace is how long a parent lets its child run on after it closed
	// the child's standard input, before it kills the child's process group.
	parentGrace = 5 * time.Second
	// parentWait is how long a test waits for a parent to exit.
	parentWait = 10 * time.Second
)

// Parent is a process running under a parent process (R20). The parent holds
// the process's standard input open, a pipe nothing writes to, and keeps its
// output in files. It exits, closing the pipe, when its own standard input
// closes, which is a pipe the test process holds: so a test binary that dies,
// as on go test's timeout, takes its parents and what they run with it.
// Killing the parent with SIGKILL closes the pipe too.
type Parent struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	dir   string
	// exited is closed once the parent has exited and been reaped.
	exited chan struct{}
}

// StartParent runs argv under a parent process: the test binary again, in the
// mode ParentMain implements, which starts the command with a pipe on its
// standard input and its output in files, and exits when its own standard
// input closes. env holds KEY=VALUE pairs that the command gets besides the
// test process's environment. The command's exit code, if it ends first, is
// the parent's. t.Cleanup ends the parent.
func (e *Env) StartParent(t *testing.T, argv, env []string) *Parent {
	t.Helper()
	if len(argv) == 0 {
		t.Fatal("harness: StartParent needs a command")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("harness: find the test binary: %v", err)
	}
	dir, err := os.MkdirTemp(e.dir, "parent-")
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	for _, name := range []string{"stdout", "stderr", "parent.log"} {
		writeFile(t, filepath.Join(dir, name), "")
	}
	log, err := os.OpenFile(filepath.Join(dir, "parent.log"), os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // G304: a file the harness just made.
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	defer func() { _ = log.Close() }()

	cmd := exec.Command(self, argv...) //nolint:gosec // G204: the test binary, with the arguments of a test.
	cmd.Env = append(os.Environ(), env...)
	cmd.Env = append(cmd.Env, ParentEnv+"=1", parentDirEnv+"="+dir)
	cmd.Stdout, cmd.Stderr = log, log
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("harness: start the parent: %v", err)
	}
	p := &Parent{cmd: cmd, stdin: stdin, dir: dir, exited: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(p.exited)
	}()
	t.Cleanup(func() { p.shutdown(t) })
	return p
}

// shutdown ends the parent at the end of the test: closing its input is
// enough, and a parent that stays is killed.
func (p *Parent) shutdown(t *testing.T) {
	_ = p.stdin.Close()
	select {
	case <-p.exited:
	case <-time.After(parentGrace + parentWait):
		_ = p.cmd.Process.Kill()
		<-p.exited
		t.Logf("harness: a parent did not exit after its input closed, and was killed")
	}
	if log, err := os.ReadFile(filepath.Join(p.dir, "parent.log")); err == nil && len(log) > 0 { //nolint:gosec // G304: a file the harness made.
		t.Logf("harness: the parent said:\n%s", log)
	}
}

// Kill sends SIGKILL to the parent, and waits until it is gone. Its child's
// standard input closes with it.
func (p *Parent) Kill(t *testing.T) {
	t.Helper()
	if err := p.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("harness: kill the parent: %v", err)
	}
	select {
	case <-p.exited:
	case <-time.After(parentWait):
		t.Fatal("harness: the parent is still there after SIGKILL")
	}
}

// Close closes the parent's own standard input, the pipe the test process
// holds: the parent closes its child's input, waits for the child, and exits.
func (p *Parent) Close(t *testing.T) {
	t.Helper()
	if err := p.stdin.Close(); err != nil {
		t.Fatalf("harness: close the parent's input: %v", err)
	}
}

// Wait waits until the parent has exited, and returns its exit code: that of
// its child, 128 and the signal number if a signal ended the child, or -1 if a
// signal ended the parent.
func (p *Parent) Wait(t *testing.T, timeout time.Duration) int {
	t.Helper()
	select {
	case <-p.exited:
		return p.cmd.ProcessState.ExitCode()
	case <-time.After(timeout):
		t.Fatalf("harness: the parent did not exit within %v", timeout)
		return -1
	}
}

// Stdout returns the path of the file that holds the child's standard output.
func (p *Parent) Stdout() string { return filepath.Join(p.dir, "stdout") }

// Stderr returns the path of the file that holds the child's standard error.
func (p *Parent) Stderr() string { return filepath.Join(p.dir, "stderr") }

// ParentMain is the parent mode of the test binary, which TestMain runs when
// ParentEnv is set, and which does not return. The command is the program's
// arguments, and its output files are in the directory parentDirEnv names. The
// parent starts the command in a process group of its own, with a pipe on its
// standard input that it holds open. It exits, with the command's exit code,
// when the command ends, or when its own standard input closes: then it closes
// the command's input, and kills the command's process group if the command
// has not ended after parentGrace.
func ParentMain() { os.Exit(runParent()) }

func runParent() int {
	dir, args := os.Getenv(parentDirEnv), os.Args[1:]
	if dir == "" || len(args) == 0 {
		fmt.Fprintf(os.Stderr, "harness parent: it needs %s and a command\n", parentDirEnv)
		return 2
	}
	stdout, err := os.OpenFile(filepath.Join(dir, "stdout"), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600) //nolint:gosec // G304: the directory the test made.
	if err != nil {
		fmt.Fprintf(os.Stderr, "harness parent: %v\n", err)
		return 2
	}
	defer func() { _ = stdout.Close() }()
	stderr, err := os.OpenFile(filepath.Join(dir, "stderr"), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600) //nolint:gosec // G304: the directory the test made.
	if err != nil {
		fmt.Fprintf(os.Stderr, "harness parent: %v\n", err)
		return 2
	}
	defer func() { _ = stderr.Close() }()

	cmd := exec.Command(args[0], args[1:]...) //nolint:gosec // G204: the command the test asked for.
	cmd.Env = childEnv(os.Environ())
	cmd.Stdout, cmd.Stderr = stdout, stderr
	// A process group of its own, so that the parent's last resort kills the
	// command's processes and never its own or the test's.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	input, err := cmd.StdinPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "harness parent: %v\n", err)
		return 2
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "harness parent: start %s: %v\n", args[0], err) //nolint:gosec // G705: a diagnostic on standard error, not a page.
		return 127
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	ended := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		close(ended)
	}()
	select {
	case <-exited:
	case <-ended:
		_ = input.Close()
		select {
		case <-exited:
		case <-time.After(parentGrace):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-exited
		}
	}
	return exitCode(cmd.ProcessState)
}

// childEnv returns the environment a parent's command gets: its own, but for
// the variables that make it a parent.
func childEnv(environ []string) []string {
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		if strings.HasPrefix(kv, ParentEnv+"=") || strings.HasPrefix(kv, parentDirEnv+"=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// exitCode returns the code a parent exits with for its child: the child's,
// or 128 and the signal's number if a signal ended it.
func exitCode(state *os.ProcessState) int {
	if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal())
	}
	return state.ExitCode()
}
