//go:build e2e

package harness

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const (
	// ChromeEnv names the binary of Chrome for Testing that the browser
	// tests and the browser gate launch (R179); the browser tests skip
	// without it.
	ChromeEnv = "AGENTSWARM_BROWSER_GATE_CHROME"
	// chromeForTestingID is the one bundle identifier StartChrome launches
	// (R179): the person's own Google Chrome, com.google.Chrome, is refused.
	chromeForTestingID = "com.google.chrome.for.testing"
)

// The bounds of the browser's own steps.
const (
	// chromeStartTimeout bounds StartChrome's wait for the browser's first
	// answer and for its crash handlers.
	chromeStartTimeout = 30 * time.Second
	// closeWait is how long Close waits for Chrome and its crash handlers
	// after Browser.close (R182), and again after it kills what remains.
	closeWait = 5 * time.Second
	// pinDialTimeout bounds PinsOf's dial of each server.
	pinDialTimeout = 5 * time.Second
	// processPoll is how often a wait for processes looks again.
	processPoll = 50 * time.Millisecond
)

// ChromeOptions say how StartChrome launches the browser.
type ChromeOptions struct {
	Binary  string   // AGENTSWARM_BROWSER_GATE_CHROME (R179)
	Pins    []string // R180
	RunDir  string   // R187: netlog.json, chrome.log, events.jsonl, crash/ and bodies/ go here
	Allowed []string // host:port of every connect R185 allows
}

// Chrome is a throwaway headless Chrome for Testing (R182), driven over its
// pipe (R181).
type Chrome struct {
	cmd  *exec.Cmd
	pgid int
	conn *cdp
	// exited is closed once Chrome has exited and been reaped.
	exited chan struct{}
	// runDir and crash are resolved, as Chrome's crash handlers name them.
	runDir, crash string
	// handler is the bundle's own chrome_crashpad_handler.
	handler string
	allowed []string
	version string
	log     *os.File

	evMu sync.Mutex
	// events is the run's events.jsonl (R184); evErr the first write to it
	// that failed.
	events *os.File
	evErr  error

	mu       sync.Mutex
	tabs     map[string]*Tab
	handlers []crashHandler
	// closed is set once Close has run to its end.
	closed    bool
	closeOnce sync.Once
	closeErr  error
}

// crashHandler is one of Chrome's crash handlers: its pid, and its
// arguments as ps shows them.
type crashHandler struct {
	pid  int
	args []string
}

// PinsOf dials each loopback address, verifies the presented chain against
// pool, and returns each presented leaf's SPKI pin: SHA-256, base64 (R180).
func PinsOf(pool *x509.CertPool, addrs ...string) ([]string, error) {
	if pool == nil || len(addrs) == 0 {
		return nil, errors.New("harness: PinsOf needs a pool and an address")
	}
	pins := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("harness: pin %s: %w", addr, err)
		}
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			return nil, fmt.Errorf("harness: pin %s: not a loopback address, which alone the harness pins", addr)
		}
		d := &tls.Dialer{
			NetDialer: &net.Dialer{Timeout: pinDialTimeout},
			Config:    &tls.Config{RootCAs: pool, ServerName: host, MinVersion: tls.VersionTLS12},
		}
		ctx, cancel := context.WithTimeout(context.Background(), pinDialTimeout)
		conn, err := d.DialContext(ctx, "tcp", addr)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("harness: pin %s: its chain does not verify against the pool: %w", addr, err)
		}
		state := conn.(*tls.Conn).ConnectionState()
		_ = conn.Close()
		if len(state.PeerCertificates) == 0 {
			return nil, fmt.Errorf("harness: pin %s: it presented no certificate", addr)
		}
		pins = append(pins, spkiPin(state.PeerCertificates[0]))
	}
	return pins, nil
}

// spkiPin is a certificate's SPKI pin, as --ignore-certificate-errors-spki-list
// takes it.
func spkiPin(c *x509.Certificate) string {
	sum := sha256.Sum256(c.RawSubjectPublicKeyInfo)
	return base64.StdEncoding.EncodeToString(sum[:])
}

// StartChrome launches R182's throwaway browser and registers Close with t.
// It refuses a bundle that is not Chrome for Testing (R179).
//
// It launches in its own process group, in a profile that is a t.TempDir()
// and with the run directory, both resolved, R182's flags and an
// environment of the person's that holds no more than a process needs, with
// BREAKPAD_DUMP_LOCATION the run's crash directory. It then waits for the
// browser's first answer, and for its crash handlers, which it records; it
// fails, closing the browser, when none names the run's own crash database,
// or one keeps metrics outside the run.
//
//nolint:gocritic // hugeParam: the options are a value the caller builds once, as the browser gate's interface takes them.
func StartChrome(t testing.TB, o ChromeOptions) (*Chrome, error) {
	t.Helper()
	bin, handler, err := chromeForTesting(o.Binary)
	if err != nil {
		return nil, err
	}
	if err := checkPins(o.Pins); err != nil {
		return nil, err
	}
	runDir, err := resolvedDir(o.RunDir)
	if err != nil {
		return nil, err
	}
	for _, d := range []string{"crash", "bodies"} {
		if err := os.MkdirAll(filepath.Join(runDir, d), 0o700); err != nil {
			return nil, fmt.Errorf("harness: the run directory: %w", err)
		}
	}
	profile, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		return nil, fmt.Errorf("harness: the profile: %w", err)
	}
	c := &Chrome{
		runDir: runDir, crash: filepath.Join(runDir, "crash"), handler: handler,
		allowed: slices.Clone(o.Allowed), exited: make(chan struct{}), tabs: map[string]*Tab{},
	}
	if err := c.launch(bin, profile, o.Pins); err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.started(); err != nil {
		return nil, errors.Join(err, c.Close())
	}
	return c, nil
}

// launch starts Chrome, its pipe on its descriptors 3 and 4, its output in
// chrome.log.
func (c *Chrome) launch(bin, profile string, pins []string) error {
	logf, err := os.OpenFile(filepath.Join(c.runDir, "chrome.log"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600) //nolint:gosec // G304: the run directory's own file.
	if err != nil {
		return fmt.Errorf("harness: chrome.log: %w", err)
	}
	events, err := os.OpenFile(filepath.Join(c.runDir, "events.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600) //nolint:gosec // G304: as above.
	if err != nil {
		_ = logf.Close()
		return fmt.Errorf("harness: events.jsonl: %w", err)
	}
	// Chrome reads its calls from its descriptor 3 and writes to its 4.
	chromeIn, toChrome, err := os.Pipe()
	if err != nil {
		return errors.Join(fmt.Errorf("harness: a pipe: %w", err), logf.Close(), events.Close())
	}
	fromChrome, chromeOut, err := os.Pipe()
	if err != nil {
		return errors.Join(fmt.Errorf("harness: a pipe: %w", err), logf.Close(), events.Close(), chromeIn.Close(), toChrome.Close())
	}
	cmd := exec.Command(bin, chromeFlags(profile, c.runDir, pins)...) //nolint:gosec // G204: Chrome for Testing, which chromeForTesting checked, with R182's flags.
	cmd.Env = chromeEnv(c.crash)
	cmd.ExtraFiles = []*os.File{chromeIn, chromeOut}
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	err = cmd.Start()
	_ = chromeIn.Close()
	_ = chromeOut.Close()
	if err != nil {
		return errors.Join(fmt.Errorf("harness: start Chrome for Testing: %w", err), logf.Close(), events.Close(), toChrome.Close(), fromChrome.Close())
	}
	c.cmd, c.pgid, c.log, c.events = cmd, cmd.Process.Pid, logf, events
	go func() {
		_ = cmd.Wait()
		close(c.exited)
	}()
	c.conn = newCDP(fromChrome, toChrome)
	return nil
}

// started waits for the browser's first answer, then for its crash
// handlers, which it records and checks (R182).
func (c *Chrome) started() error {
	ctx, cancel := context.WithTimeout(context.Background(), chromeStartTimeout)
	defer cancel()
	var v struct {
		Product string `json:"product"`
	}
	if err := c.conn.call(ctx, "", "Browser.getVersion", nil, &v); err != nil {
		return fmt.Errorf("harness: Chrome for Testing did not answer; see %s: %w", filepath.Join(c.runDir, "chrome.log"), err)
	}
	c.version = v.Product
	c.conn.subscribe("", c.onBrowserEvent)
	for {
		found, err := c.crashHandlers()
		if err != nil {
			return err
		}
		c.remember(found)
		if len(c.recorded()) > 0 {
			break
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("harness: no crash handler of %s names the run's crash database, --database=%s: R182 cannot hold", c.handler, c.crash)
		case <-c.exited:
			return fmt.Errorf("harness: Chrome for Testing exited at its start; see %s", filepath.Join(c.runDir, "chrome.log"))
		case <-time.After(processPoll):
		}
	}
	for _, h := range c.recorded() {
		for _, a := range h.args {
			if dir, ok := strings.CutPrefix(a, "--metrics-dir="); ok && !within(c.runDir, dir) {
				return fmt.Errorf("harness: crash handler %d keeps its metrics in %s, outside the run", h.pid, dir)
			}
		}
	}
	return nil
}

// chromeForTesting resolves binary, and refuses it unless it is the
// executable of a Chrome for Testing bundle (R179): <X>.app/Contents/MacOS/
// <its CFBundleExecutable>, of a bundle whose CFBundleIdentifier is
// com.google.chrome.for.testing. It also returns the bundle's crash handler,
// Helpers/chrome_crashpad_handler of its framework's version. Nothing runs
// but plutil, which reads the bundle's Info.plist.
func chromeForTesting(binary string) (bin, handler string, err error) {
	if binary == "" {
		return "", "", errors.New("harness: ChromeOptions.Binary is empty: " + ChromeEnv + " names Chrome for Testing")
	}
	bin, err = filepath.EvalSymlinks(binary)
	if err == nil {
		bin, err = filepath.Abs(bin)
	}
	if err != nil {
		return "", "", fmt.Errorf("harness: %s: %w", binary, err)
	}
	macos := filepath.Dir(bin)
	contents := filepath.Dir(macos)
	if filepath.Base(macos) != "MacOS" || filepath.Base(contents) != "Contents" || filepath.Ext(filepath.Dir(contents)) != ".app" {
		return "", "", fmt.Errorf("harness: %s is not in an application bundle: StartChrome launches Chrome for Testing alone (R179)", bin)
	}
	plist := filepath.Join(contents, "Info.plist")
	id, err := plistString(plist, "CFBundleIdentifier")
	if err != nil {
		return "", "", err
	}
	if id != chromeForTestingID {
		return "", "", fmt.Errorf("harness: %s is %s's, not Chrome for Testing's, %s: StartChrome launches no other browser (R179)", bin, id, chromeForTestingID)
	}
	exe, err := plistString(plist, "CFBundleExecutable")
	if err != nil {
		return "", "", err
	}
	if exe != filepath.Base(bin) {
		return "", "", fmt.Errorf("harness: %s is not its bundle's executable, %s", bin, exe)
	}
	version, err := plistString(plist, "CFBundleShortVersionString")
	if err != nil {
		return "", "", err
	}
	if strings.Trim(version, "0123456789.") != "" {
		return "", "", fmt.Errorf("harness: %s: %q is not a version", plist, version)
	}
	matches, _ := filepath.Glob(filepath.Join(contents, "Frameworks", "*.framework", "Versions", version, "Helpers", "chrome_crashpad_handler"))
	if len(matches) != 1 {
		return "", "", fmt.Errorf("harness: %s has no one crash handler of version %s, which Close must find (R182)", filepath.Dir(contents), version)
	}
	return bin, matches[0], nil
}

// plistString is the string at key of an Info.plist, as plutil reads it.
func plistString(plist, key string) (string, error) {
	out, err := exec.Command("/usr/bin/plutil", "-extract", key, "raw", "-expect", "string", "-o", "-", plist).Output() //nolint:gosec // G204: macOS's plutil, reading a bundle's Info.plist.
	if err != nil {
		return "", fmt.Errorf("harness: read %s from %s: %w", key, plist, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// checkPins refuses a pin that is not a SHA-256 in base64, so that nothing
// else reaches the flag's list.
func checkPins(pins []string) error {
	for _, p := range pins {
		if raw, err := base64.StdEncoding.DecodeString(p); err != nil || len(raw) != sha256.Size {
			return fmt.Errorf("harness: %q is not an SPKI pin, a SHA-256 in base64", p)
		}
	}
	return nil
}

// resolvedDir makes dir and returns it resolved, as Chrome's crash handlers
// name it: /tmp is /private/tmp on macOS.
func resolvedDir(dir string) (string, error) {
	if dir == "" {
		return "", errors.New("harness: ChromeOptions.RunDir is empty")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("harness: the run directory: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err == nil {
		resolved, err = filepath.Abs(resolved)
	}
	if err != nil {
		return "", fmt.Errorf("harness: the run directory: %w", err)
	}
	return resolved, nil
}

// chromeFlags are R182's flags, then the first page, about:blank. With no
// pin the SPKI list is left out, and Chrome trusts no certificate of the
// harness's.
func chromeFlags(profile, runDir string, pins []string) []string {
	flags := []string{"--headless", "--user-data-dir=" + profile, "--remote-debugging-pipe"}
	if len(pins) > 0 {
		flags = append(flags, "--ignore-certificate-errors-spki-list="+strings.Join(pins, ","))
	}
	return append(flags,
		"--no-proxy-server",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-background-networking",
		"--disable-component-update",
		"--disable-sync",
		"--disable-default-apps",
		"--disable-extensions",
		"--disable-client-side-phishing-detection",
		"--disable-backgrounding-occluded-windows",
		"--metrics-recording-only",
		"--mute-audio",
		"--use-mock-keychain",
		"--window-size=1280,900",
		"--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE 127.0.0.1",
		"--log-net-log="+filepath.Join(runDir, "netlog.json"),
		"about:blank",
	)
}

// chromeEnv is Chrome's environment (R182): BREAKPAD_DUMP_LOCATION, the
// run's crash directory, and of the person's own no more than a process
// needs, so that no key or proxy of their shell's reaches the browser.
func chromeEnv(crash string) []string {
	env := []string{"BREAKPAD_DUMP_LOCATION=" + crash}
	for _, k := range []string{"HOME", "USER", "LOGNAME", "PATH", "TMPDIR", "LANG", "LC_ALL", "LC_CTYPE"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// within says whether path is inside dir.
func within(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// Version is the browser's product, as Browser.getVersion gives it.
func (c *Chrome) Version() string { return c.version }

// browserState is how the browser's pipe stands: "open", or "pipe closed:
// <why>" once it has closed, as when a write broke the stream or Chrome
// exited, so that a controller sees a dead browser at once rather than from
// its next command's error. A stand-in with no pipe has "no pipe".
func (c *Chrome) browserState() string {
	if c.conn == nil {
		return "no pipe"
	}
	select {
	case <-c.conn.done:
		return "pipe closed: " + c.conn.closedErr().Error()
	default:
		return "open"
	}
}

// ClearCookies deletes every cookie of the browser's.
func (c *Chrome) ClearCookies(ctx context.Context) error {
	return c.conn.call(ctx, "", "Storage.clearCookies", map[string]any{}, nil)
}

// Close ends the browser (R182): Browser.close, then up to 5 s for Chrome
// and its recorded crash handlers to exit; then it kills Chrome's process
// group and those handlers that still run, by their recorded pids alone,
// each checked again to be the handler it was, and none when the processes
// cannot be listed. It then delivers every event queued, up to 5 s, before
// events.jsonl closes, and returns any write to it that failed. It is
// idempotent.
func (c *Chrome) Close() error {
	c.closeOnce.Do(func() { c.closeErr = c.shutdown() })
	return c.closeErr
}

func (c *Chrome) shutdown() error {
	if found, err := c.crashHandlers(); err == nil {
		c.remember(found)
	}
	ctx, cancel := context.WithTimeout(context.Background(), closeWait)
	_ = c.conn.call(ctx, "", "Browser.close", nil, nil)
	cancel()
	var errs []error
	if !c.waitGone(closeWait) {
		if err := c.kill(); err != nil {
			errs = append(errs, err)
		}
		if !c.waitGone(closeWait) {
			errs = append(errs, fmt.Errorf("harness: Chrome's process group %d or a crash handler of its still runs after SIGKILL", c.pgid))
		}
	}
	_ = c.conn.close()
	// Every event queued reaches events.jsonl before the file closes, and a
	// write that failed is Close's error.
	if !c.conn.drained(closeWait) {
		errs = append(errs, errors.New("harness: the tabs' events were still being delivered when Close gave up: events.jsonl lacks its tail"))
	}
	c.mu.Lock()
	tabs := make([]*Tab, 0, len(c.tabs))
	for _, b := range c.tabs {
		if b != nil {
			tabs = append(tabs, b)
		}
	}
	c.mu.Unlock()
	for _, b := range tabs {
		b.markClosed()
	}
	c.evMu.Lock()
	errs = append(errs, c.evErr, c.events.Close())
	c.evMu.Unlock()
	errs = append(errs, c.log.Close())
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return errors.Join(errs...)
}

// OffLoopback reads the run's NetLog after Close: R185's findings, each one
// line.
func (c *Chrome) OffLoopback() ([]string, error) {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	select {
	case <-c.exited:
	default:
		closed = false
	}
	if !closed {
		return nil, errors.New("harness: OffLoopback reads the NetLog after Close, once Chrome has exited and the log is complete (R185)")
	}
	r, err := readNetLog(filepath.Join(c.runDir, "netlog.json"), c.allowed)
	if err != nil {
		return nil, err
	}
	findings, _ := r.verdict()
	if !r.complete {
		findings = append(findings, "the NetLog does not end as a complete one does: Chrome did not close it, so R185 read only part of the run")
	}
	return findings, nil
}

// crashHandlers are the running processes whose executable is the bundle's
// own chrome_crashpad_handler and one of whose arguments is exactly
// --database=<the run's resolved crash directory> (R182), as ps lists them.
func (c *Chrome) crashHandlers() ([]crashHandler, error) {
	out, err := processList()
	if err != nil {
		return nil, fmt.Errorf("harness: list the processes: %w", err)
	}
	database := "--database=" + c.crash
	var found []crashHandler
	for line := range strings.SplitSeq(string(out), "\n") {
		pidField, args, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		pid, err := strconv.Atoi(pidField)
		if err != nil {
			continue
		}
		rest, ok := strings.CutPrefix(strings.TrimLeft(args, " "), c.handler)
		if !ok || (rest != "" && rest[0] != ' ') {
			continue
		}
		if argv := handlerArgs(rest); slices.Contains(argv, database) {
			found = append(found, crashHandler{pid: pid, args: argv})
		}
	}
	return found, nil
}

// processList is every process, a pid and its arguments a line, as ps
// lists them; a test makes it fail.
var processList = func() ([]byte, error) {
	return exec.Command("/bin/ps", "-axww", "-o", "pid=,args=").Output()
}

// handlerArgs splits what follows a crash handler's executable on ps's line
// into its arguments: each of them begins with --, and a value may hold
// spaces, as --annotation=plat=OS X does.
func handlerArgs(rest string) []string {
	parts := strings.Split(rest, " --")
	argv := make([]string, 0, len(parts))
	for _, a := range parts[1:] {
		argv = append(argv, "--"+a)
	}
	return argv
}

// remember adds handlers not yet recorded.
func (c *Chrome) remember(found []crashHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, h := range found {
		if !slices.ContainsFunc(c.handlers, func(r crashHandler) bool { return r.pid == h.pid }) {
			c.handlers = append(c.handlers, h)
		}
	}
}

// recorded are the crash handlers recorded so far.
func (c *Chrome) recorded() []crashHandler {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.handlers)
}

// waitGone waits up to d for Chrome to exit, its process group to empty,
// and its recorded crash handlers to end.
func (c *Chrome) waitGone(d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if c.gone() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(processPoll)
	}
}

func (c *Chrome) gone() bool {
	select {
	case <-c.exited:
	default:
		return false
	}
	if alive(-c.pgid) {
		return false
	}
	return len(c.runningHandlers()) == 0
}

// runningHandlers are the recorded handlers still running as the handlers
// they were: a pid that runs something else now is not one.
func (c *Chrome) runningHandlers() []crashHandler {
	var maybe []crashHandler
	for _, h := range c.recorded() {
		if alive(h.pid) {
			maybe = append(maybe, h)
		}
	}
	if len(maybe) == 0 {
		return nil
	}
	found, err := c.crashHandlers()
	if err != nil {
		return maybe
	}
	var running []crashHandler
	for _, h := range maybe {
		if slices.ContainsFunc(found, func(f crashHandler) bool { return f.pid == h.pid }) {
			running = append(running, h)
		}
	}
	return running
}

// kill sends SIGKILL to Chrome's process group, which the harness made, and
// to each recorded crash handler that a fresh scan shows is still that
// handler: nothing else. When the processes cannot be listed it kills no
// handler, as a recorded pid may run something else by now, and says so.
func (c *Chrome) kill() error {
	if c.pgid > 0 && alive(-c.pgid) {
		_ = syscall.Kill(-c.pgid, syscall.SIGKILL)
	}
	found, err := c.crashHandlers()
	if err != nil {
		return fmt.Errorf("harness: no crash handler was killed, as none could be verified: %w", err)
	}
	for _, h := range c.recorded() {
		if slices.ContainsFunc(found, func(f crashHandler) bool { return f.pid == h.pid }) {
			_ = syscall.Kill(h.pid, syscall.SIGKILL)
		}
	}
	return nil
}

// alive says whether a process, or with a negative pid a process group,
// exists.
func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// onBrowserEvent follows the browser's own events: a tab whose session
// detaches is closed.
func (c *Chrome) onBrowserEvent(e cdpEvent) {
	if e.Method != "Target.detachedFromTarget" {
		return
	}
	var p struct {
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(e.Params, &p) != nil {
		return
	}
	c.mu.Lock()
	var gone *Tab
	for name, b := range c.tabs {
		if b != nil && b.session == p.SessionID {
			gone = b
			delete(c.tabs, name)
		}
	}
	c.mu.Unlock()
	if gone != nil {
		gone.markClosed()
	}
	c.conn.forget(p.SessionID)
}
