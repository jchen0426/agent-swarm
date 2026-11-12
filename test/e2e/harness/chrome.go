//go:build e2e

package harness

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/net/html" //nolint:depguard // R184 reads each poll's body with it; not platform code, and already a direct dependency.
)

const (
	// ChromeEnv names the binary of Chrome for Testing that the browser
	// tests and the browser gate launch (R179); the browser tests skip
	// without it.
	ChromeEnv = "AGENTSWARM_BROWSER_GATE_CHROME"
	// chromeForTestingID is the one bundle identifier StartChrome launches
	// (R179): the person's own Google Chrome, com.google.Chrome, is refused.
	chromeForTestingID = "com.google.chrome.for.testing"
	// gateWorld is the isolated world the recorder runs in and Eval
	// evaluates in, and gateBinding the binding exposed to it alone (R183).
	gateWorld   = "gate"
	gateBinding = "gateRecord"
)

// The bounds of the browser's own steps.
const (
	// chromeStartTimeout bounds StartChrome's wait for the browser's first
	// answer and for its crash handlers.
	chromeStartTimeout = 30 * time.Second
	// closeWait is how long Close waits for Chrome and its crash handlers
	// after Browser.close (R182), and again after it kills what remains.
	closeWait = 5 * time.Second
	// waitBound bounds any wait of a tab's whose context has no earlier
	// deadline: a load, a restore, the gate world, a poll to replay.
	waitBound = time.Minute
	// visibilityWait bounds Hide's and Show's wait for the page to read
	// hidden or visible.
	visibilityWait = 10 * time.Second
	// pinDialTimeout bounds PinsOf's dial of each server.
	pinDialTimeout = 5 * time.Second
	// processPoll is how often a wait for processes looks again.
	processPoll = 50 * time.Millisecond
)

// defaultFontSize is the browser's own standard font size, px, which the
// zero Emulation restores, as Page.setFontSizes has no clear.
const defaultFontSize = 16

// ErrCertificate is a navigation refused for its certificate (R180).
var ErrCertificate = errors.New("harness: a navigation was refused for its certificate")

// regionIDs are the board's regions, as live.js's names and
// internal/web/pages' regionNames list them; TestReplayOlderIsDroppedByLiveJS
// holds them to live.js's own.
var regionIDs = []string{"switch", "standby", "board-header", "summary", "strip", "hypotheses", "feed"}

// tabName is what a tab's name may be: it names its bodies' files.
var tabName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

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

// ClearCookies deletes every cookie of the browser's.
func (c *Chrome) ClearCookies(ctx context.Context) error {
	return c.conn.call(ctx, "", "Storage.clearCookies", map[string]any{}, nil)
}

// Close ends the browser (R182): Browser.close, then up to 5 s for Chrome
// and its recorded crash handlers to exit; then it kills Chrome's process
// group and those handlers that still run, by their recorded pids alone,
// each checked again to be the handler it was. It is idempotent.
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
		c.kill()
		if !c.waitGone(closeWait) {
			errs = append(errs, fmt.Errorf("harness: Chrome's process group %d or a crash handler of its still runs after SIGKILL", c.pgid))
		}
	}
	_ = c.conn.close()
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
	out, err := exec.Command("/bin/ps", "-axww", "-o", "pid=,args=").Output()
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

// kill sends SIGKILL to Chrome's process group, and to each recorded crash
// handler still running as one: nothing else.
func (c *Chrome) kill() {
	if alive(-c.pgid) {
		_ = syscall.Kill(-c.pgid, syscall.SIGKILL)
	}
	for _, h := range c.runningHandlers() {
		_ = syscall.Kill(h.pid, syscall.SIGKILL)
	}
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

// writeEvent appends a tab's event to events.jsonl.
func (c *Chrome) writeEvent(tab string, e *Event) {
	line, err := json.Marshal(struct {
		Tab string `json:"tab"`
		*Event
	}{tab, e})
	c.evMu.Lock()
	defer c.evMu.Unlock()
	if err == nil {
		_, err = c.events.Write(append(line, '\n'))
	}
	if err != nil && c.evErr == nil {
		c.evErr = fmt.Errorf("harness: events.jsonl: %w", err)
	}
}

// Tab opens a page target, in a window of its own when newWindow, attached
// in a flat session, with R183's recorder and R184's evidence on.
func (c *Chrome) Tab(ctx context.Context, name string, newWindow bool) (*Tab, error) {
	if !tabName.MatchString(name) {
		return nil, fmt.Errorf("harness: %q is not a tab's name: lowercase letters, digits and dashes", name)
	}
	c.mu.Lock()
	_, taken := c.tabs[name]
	closed := c.closed
	if !taken && !closed {
		c.tabs[name] = nil
	}
	c.mu.Unlock()
	switch {
	case closed:
		return nil, errors.New("harness: the browser is closed")
	case taken:
		return nil, fmt.Errorf("harness: there is a tab %q already", name)
	}
	b, err := c.openTab(ctx, name, newWindow)
	c.mu.Lock()
	if err != nil {
		delete(c.tabs, name)
	} else {
		c.tabs[name] = b
	}
	c.mu.Unlock()
	return b, err
}

func (c *Chrome) openTab(ctx context.Context, name string, newWindow bool) (*Tab, error) {
	var created struct {
		TargetID string `json:"targetId"`
	}
	if err := c.conn.call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank", "newWindow": newWindow}, &created); err != nil {
		return nil, err
	}
	var attached struct {
		SessionID string `json:"sessionId"`
	}
	if err := c.conn.call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": created.TargetID, "flatten": true}, &attached); err != nil {
		return nil, errors.Join(err, c.conn.call(ctx, "", "Target.closeTarget", map[string]string{"targetId": created.TargetID}, nil))
	}
	b := newTab(c, name, created.TargetID, attached.SessionID)
	c.conn.subscribe(b.session, b.handle)
	if err := b.setup(ctx); err != nil {
		return nil, errors.Join(err, c.conn.call(ctx, "", "Target.closeTarget", map[string]string{"targetId": b.target}, nil))
	}
	return b, nil
}

// Tab is a page target, attached in a flat session of its own (R181), with
// the recorder (R183) and the evidence (R184) on.
type Tab struct {
	c       *Chrome
	name    string
	target  string
	session string
	window  int

	mu sync.Mutex
	// changed is closed, and replaced, at each change of the state below.
	changed chan struct{}
	closed  bool
	// frame is the main frame; gate and main are the unique ids of its
	// document's gate world and main world, "" while it has none.
	frame      string
	gate, main string
	contexts   map[int]tabContext
	// loaded are the loaders whose load fired; navs counts the main frame's
	// commits and restores its restores from the back-forward cache.
	loaded         map[string]bool
	navs, restores int
	lastLoader     string

	seq    int
	events []Event
	// requests are those in flight, by id; replayed the ids Fetch answered
	// with a replay.
	requests map[string]*tabRequest
	replayed map[string]bool
	// failed is each document URL's failed load, for its error page; certs
	// the loads already recorded as certificate events.
	failed map[string]string
	certs  map[string]bool
	// answers are the 200 polls kept, in order; order is the page's last
	// applied order, as live.js keeps it.
	answers []pollAnswer
	order   string
	replay  *armedReplay
}

// tabContext is a live execution context: its unique id, and its world.
type tabContext struct {
	unique, world string
}

// tabRequest is a request in flight.
type tabRequest struct {
	url      string
	poll     bool
	replayed bool
	status   int
	headers  map[string]any
}

// pollAnswer is a 200 poll kept for ReplayOlder: its body event's seq, its
// body's file, what R184 reads of it, and its response's headers.
type pollAnswer struct {
	seq     int
	url     string
	file    string
	facts   boardFacts
	headers map[string]any
}

// armedReplay is ReplayOlder's answer waiting for the next poll; from is
// the seq of the body event it replays.
type armedReplay struct {
	headers []map[string]string
	body    string
	result  Replay
	from    int
	done    chan replayDone
}

type replayDone struct {
	replay Replay
	err    error
}

func newTab(c *Chrome, name, target, session string) *Tab {
	return &Tab{
		c: c, name: name, target: target, session: session,
		changed: make(chan struct{}), frame: target, contexts: map[int]tabContext{},
		loaded: map[string]bool{}, requests: map[string]*tabRequest{}, replayed: map[string]bool{},
		failed: map[string]string{}, certs: map[string]bool{},
	}
}

// setup turns the evidence on, then the recorder, before the tab's first
// document: the main frame is known before any context is announced.
func (b *Tab) setup(ctx context.Context) error {
	var tree struct {
		FrameTree struct {
			Frame struct {
				ID string `json:"id"`
			} `json:"frame"`
		} `json:"frameTree"`
	}
	if err := b.call(ctx, "Page.enable", nil, nil); err != nil {
		return err
	}
	if err := b.call(ctx, "Page.getFrameTree", nil, &tree); err != nil {
		return err
	}
	b.mu.Lock()
	b.frame = tree.FrameTree.Frame.ID
	b.mu.Unlock()
	steps := []struct {
		method string
		params any
	}{
		{"Page.setLifecycleEventsEnabled", map[string]bool{"enabled": true}},
		{"Runtime.addBinding", map[string]string{"name": gateBinding, "executionContextName": gateWorld}},
		{"Page.addScriptToEvaluateOnNewDocument", map[string]string{"source": recorderJS, "worldName": gateWorld}},
		{"Network.enable", map[string]int{"maxTotalBufferSize": 200 << 20, "maxResourceBufferSize": 20 << 20}},
		{"Log.enable", nil},
		// The violations that say something of the code, not of the
		// machine's speed: none of the timed ones.
		{"Log.startViolationsReport", map[string]any{"config": []map[string]any{
			{"name": "blockedEvent", "threshold": 100},
			{"name": "blockedParser", "threshold": -1},
			{"name": "discouragedAPIUse", "threshold": -1},
		}}},
		{"Audits.enable", nil},
		{"Runtime.enable", nil},
		{"Page.createIsolatedWorld", map[string]string{"frameId": tree.FrameTree.Frame.ID, "worldName": gateWorld}},
	}
	for _, s := range steps {
		if err := b.call(ctx, s.method, s.params, nil); err != nil {
			return err
		}
	}
	var w struct {
		WindowID int `json:"windowId"`
	}
	if err := b.c.conn.call(ctx, "", "Browser.getWindowForTarget", map[string]string{"targetId": b.target}, &w); err != nil {
		return err
	}
	b.window = w.WindowID
	return nil
}

// call makes a call on the tab's session; a closed tab's fails at once.
func (b *Tab) call(ctx context.Context, method string, params, result any) error {
	b.mu.Lock()
	closed := b.closed
	b.mu.Unlock()
	if closed {
		return fmt.Errorf("harness: CDP %s: tab %s is closed", method, b.name)
	}
	return b.c.conn.call(ctx, b.session, method, params, result)
}

// bump marks a change of the tab's state; b.mu is held.
func (b *Tab) bump() {
	close(b.changed)
	b.changed = make(chan struct{})
}

// await waits until cond, read with b.mu held, holds.
func (b *Tab) await(ctx context.Context, what string, cond func() bool) error {
	for {
		b.mu.Lock()
		ok, closed, changed := cond(), b.closed, b.changed
		b.mu.Unlock()
		switch {
		case closed:
			return fmt.Errorf("harness: tab %s is closed: it cannot wait for %s", b.name, what)
		case ok:
			return nil
		}
		select {
		case <-changed:
		case <-b.c.conn.done:
			return fmt.Errorf("harness: tab %s: %s: %w", b.name, what, b.c.conn.closedErr())
		case <-ctx.Done():
			return fmt.Errorf("harness: tab %s: %s: %w", b.name, what, context.Cause(ctx))
		}
	}
}

func (b *Tab) markClosed() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed {
		b.closed = true
		b.bump()
	}
}

// Navigate loads rawURL and waits for its load. A certificate error is
// ErrCertificate, and is recorded (R180).
func (b *Tab) Navigate(ctx context.Context, rawURL string) error {
	ctx, cancel := context.WithTimeout(ctx, waitBound)
	defer cancel()
	var r struct {
		LoaderID  string `json:"loaderId"`
		ErrorText string `json:"errorText"`
	}
	if err := b.call(ctx, "Page.navigate", map[string]string{"url": rawURL}, &r); err != nil {
		return err
	}
	if r.ErrorText != "" {
		if certError(r.ErrorText) {
			b.certificate(r.LoaderID, rawURL, r.ErrorText, "navigate")
			return fmt.Errorf("%w: %s: %s", ErrCertificate, rawURL, r.ErrorText)
		}
		return fmt.Errorf("harness: tab %s: navigate to %s: %s", b.name, rawURL, r.ErrorText)
	}
	if r.LoaderID == "" {
		return nil
	}
	return b.await(ctx, "the load of "+rawURL, func() bool { return b.loaded[r.LoaderID] })
}

// Back goes back one entry, and waits for a load or a restore.
func (b *Tab) Back(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, waitBound)
	defer cancel()
	var h struct {
		CurrentIndex int `json:"currentIndex"`
		Entries      []struct {
			ID  int    `json:"id"`
			URL string `json:"url"`
		} `json:"entries"`
	}
	if err := b.call(ctx, "Page.getNavigationHistory", nil, &h); err != nil {
		return err
	}
	if h.CurrentIndex < 1 || h.CurrentIndex >= len(h.Entries) {
		return fmt.Errorf("harness: tab %s has no earlier entry to go back to", b.name)
	}
	to := h.Entries[h.CurrentIndex-1]
	b.mu.Lock()
	navs, restores := b.navs, b.restores
	b.mu.Unlock()
	if err := b.call(ctx, "Page.navigateToHistoryEntry", map[string]int{"entryId": to.ID}, nil); err != nil {
		return err
	}
	return b.await(ctx, "going back to "+to.URL, func() bool {
		return b.restores > restores || (b.navs > navs && b.loaded[b.lastLoader])
	})
}

// Reload reloads the page, and waits for the new document's load.
func (b *Tab) Reload(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, waitBound)
	defer cancel()
	b.mu.Lock()
	navs := b.navs
	b.mu.Unlock()
	if err := b.call(ctx, "Page.reload", nil, nil); err != nil {
		return err
	}
	return b.await(ctx, "the reload", func() bool { return b.navs > navs && b.loaded[b.lastLoader] })
}

// evalResult is Runtime.evaluate's and Runtime.callFunctionOn's answer.
type evalResult struct {
	Result struct {
		Type                string          `json:"type"`
		Subtype             string          `json:"subtype"`
		Value               json.RawMessage `json:"value"`
		UnserializableValue string          `json:"unserializableValue"`
		ObjectID            string          `json:"objectId"`
	} `json:"result"`
	ExceptionDetails *struct {
		Text      string `json:"text"`
		Exception *struct {
			Description string `json:"description"`
		} `json:"exception"`
	} `json:"exceptionDetails"`
}

// failure is the exception an evaluation threw, or nil.
func (r *evalResult) failure(expr string) error {
	if r.ExceptionDetails == nil {
		return nil
	}
	what := r.ExceptionDetails.Text
	if e := r.ExceptionDetails.Exception; e != nil && e.Description != "" {
		what = e.Description
	}
	return fmt.Errorf("harness: %s threw: %s", expr, what)
}

// Eval evaluates expr in the gate world of the page's document, waiting for
// its context, and decodes its value into out unless out is nil.
func (b *Tab) Eval(ctx context.Context, expr string, out any) error {
	return b.eval(ctx, gateWorld, expr, out)
}

// evalMain is Eval in the page's main world, which the tests alone read.
func (b *Tab) evalMain(ctx context.Context, expr string, out any) error {
	return b.eval(ctx, "main", expr, out)
}

func (b *Tab) eval(ctx context.Context, world, expr string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, waitBound)
	defer cancel()
	var r evalResult
	if err := b.evaluate(ctx, world, map[string]any{"expression": expr, "returnByValue": true, "awaitPromise": true}, &r); err != nil {
		return err
	}
	if err := r.failure(expr); err != nil {
		return err
	}
	if r.Result.UnserializableValue != "" {
		return fmt.Errorf("harness: %s gave %s, which JSON cannot hold", expr, r.Result.UnserializableValue)
	}
	if out == nil || len(r.Result.Value) == 0 {
		return nil
	}
	if err := json.Unmarshal(r.Result.Value, out); err != nil {
		return fmt.Errorf("harness: %s's value: %w", expr, err)
	}
	return nil
}

// evaluate is Runtime.evaluate with params in world's context of the
// page's document, waiting for it; a context that went with its document
// meanwhile is tried again in the next.
func (b *Tab) evaluate(ctx context.Context, world string, params map[string]any, r *evalResult) error {
	stale := ""
	for attempt := 0; ; attempt++ {
		var id string
		err := b.await(ctx, "the "+world+" world of the page's document", func() bool {
			id = b.main
			if world == gateWorld {
				id = b.gate
			}
			return id != "" && id != stale
		})
		if err != nil {
			return err
		}
		params["uniqueContextId"] = id
		err = b.call(ctx, "Runtime.evaluate", params, r)
		if err == nil || attempt == 3 || !contextGone(err) {
			return err
		}
		stale = id
	}
}

// contextGone says whether a call failed for a context that went with its
// document.
func contextGone(err error) bool {
	var ce *cdpError
	if !errors.As(err, &ce) {
		return false
	}
	m := strings.ToLower(ce.Message)
	return strings.Contains(m, "context") && (strings.Contains(m, "cannot find") || strings.Contains(m, "destroyed") || strings.Contains(m, "not found"))
}

// keyDef is a key Key presses: its DOM key and code, its Windows key code,
// and the text its keyDown types, for the keys that press a button only so.
type keyDef struct {
	key, code, text string
	vk              int
}

// keyDefs are the keys Key knows.
var keyDefs = map[string]keyDef{
	"Tab":        {key: "Tab", code: "Tab", vk: 9},
	"Enter":      {key: "Enter", code: "Enter", vk: 13, text: "\r"},
	"Space":      {key: " ", code: "Space", vk: 32, text: " "},
	"Escape":     {key: "Escape", code: "Escape", vk: 27},
	"ArrowUp":    {key: "ArrowUp", code: "ArrowUp", vk: 38},
	"ArrowDown":  {key: "ArrowDown", code: "ArrowDown", vk: 40},
	"ArrowLeft":  {key: "ArrowLeft", code: "ArrowLeft", vk: 37},
	"ArrowRight": {key: "ArrowRight", code: "ArrowRight", vk: 39},
}

// shiftModifier is Input.dispatchKeyEvent's Shift.
const shiftModifier = 8

// Key presses a key: Tab, Shift+Tab, Enter (a keyDown with text), Space,
// Escape or an arrow, by its DOM name.
func (b *Tab) Key(ctx context.Context, key string) error {
	name, shift := strings.CutPrefix(key, "Shift+")
	k, ok := keyDefs[name]
	if !ok {
		return fmt.Errorf("harness: Key presses Tab, Shift+Tab, Enter, Space, Escape and the arrows, not %q", key)
	}
	mods := 0
	if shift {
		mods = shiftModifier
	}
	press := func(typ string, k keyDef, mods int) error {
		ev := map[string]any{"type": typ, "key": k.key, "code": k.code, "windowsVirtualKeyCode": k.vk, "nativeVirtualKeyCode": k.vk, "modifiers": mods}
		if typ == "keyDown" {
			ev["text"], ev["unmodifiedText"] = k.text, k.text
		}
		return b.call(ctx, "Input.dispatchKeyEvent", ev, nil)
	}
	shiftKey := keyDef{key: "Shift", code: "ShiftLeft", vk: 16}
	if shift {
		if err := press("rawKeyDown", shiftKey, mods); err != nil {
			return err
		}
	}
	down := "rawKeyDown"
	if k.text != "" {
		down = "keyDown"
	}
	if err := press(down, k, mods); err != nil {
		return err
	}
	if err := press("keyUp", k, mods); err != nil {
		return err
	}
	if shift {
		return press("keyUp", shiftKey, 0)
	}
	return nil
}

// Type types text, as Input.insertText does.
func (b *Tab) Type(ctx context.Context, text string) error {
	return b.call(ctx, "Input.insertText", map[string]string{"text": text}, nil)
}

// Click clicks the centre of the box of the first element selector
// matches, scrolled into view.
func (b *Tab) Click(ctx context.Context, selector string) error {
	ctx, cancel := context.WithTimeout(ctx, waitBound)
	defer cancel()
	sel, err := json.Marshal(selector)
	if err != nil {
		return err
	}
	expr := "document.querySelector(" + string(sel) + ")"
	var r evalResult
	if err := b.evaluate(ctx, gateWorld, map[string]any{"expression": expr}, &r); err != nil {
		return err
	}
	if err := r.failure(expr); err != nil {
		return err
	}
	if r.Result.ObjectID == "" {
		return fmt.Errorf("harness: tab %s: no element matches %s", b.name, selector)
	}
	defer func() {
		_ = b.call(ctx, "Runtime.releaseObject", map[string]string{"objectId": r.Result.ObjectID}, nil)
	}()
	if err := b.call(ctx, "DOM.scrollIntoViewIfNeeded", map[string]string{"objectId": r.Result.ObjectID}, nil); err != nil {
		return err
	}
	var box evalResult
	if err := b.call(ctx, "Runtime.callFunctionOn", map[string]any{
		"objectId":            r.Result.ObjectID,
		"functionDeclaration": "function () { const r = this.getBoundingClientRect(); return {x: r.left + r.width / 2, y: r.top + r.height / 2, w: r.width, h: r.height}; }",
		"returnByValue":       true,
	}, &box); err != nil {
		return err
	}
	var at struct{ X, Y, W, H float64 }
	if err := json.Unmarshal(box.Result.Value, &at); err != nil {
		return fmt.Errorf("harness: the box of %s: %w", selector, err)
	}
	if at.W == 0 || at.H == 0 {
		return fmt.Errorf("harness: tab %s: %s has no box to click", b.name, selector)
	}
	for _, ev := range []map[string]any{
		{"type": "mouseMoved", "x": at.X, "y": at.Y},
		{"type": "mousePressed", "x": at.X, "y": at.Y, "button": "left", "buttons": 1, "clickCount": 1},
		{"type": "mouseReleased", "x": at.X, "y": at.Y, "button": "left", "buttons": 0, "clickCount": 1},
	} {
		if err := b.call(ctx, "Input.dispatchMouseEvent", ev, nil); err != nil {
			return err
		}
	}
	return nil
}

// Emulation is a tab's emulated device; the zero value clears it.
type Emulation struct {
	Width, Height int
	DPR           float64
	Mobile, Dark  bool
	FontSize      int // the browser's standard font size, px
}

// Emulate emulates e: the viewport, dark media and the browser's standard
// font size, which the zero value returns to 16 px, the browser's own.
func (b *Tab) Emulate(ctx context.Context, e Emulation) error {
	if e.Width < 0 || e.Height < 0 || e.DPR < 0 || e.FontSize < 0 {
		return fmt.Errorf("harness: an emulation of negative size: %+v", e)
	}
	var err error
	if e == (Emulation{Dark: e.Dark, FontSize: e.FontSize}) {
		err = b.call(ctx, "Emulation.clearDeviceMetricsOverride", nil, nil)
	} else {
		err = b.call(ctx, "Emulation.setDeviceMetricsOverride", map[string]any{
			"width": e.Width, "height": e.Height, "deviceScaleFactor": e.DPR, "mobile": e.Mobile,
		}, nil)
	}
	if err != nil {
		return err
	}
	features := []map[string]string{}
	if e.Dark {
		features = append(features, map[string]string{"name": "prefers-color-scheme", "value": "dark"})
	}
	if err := b.call(ctx, "Emulation.setEmulatedMedia", map[string]any{"media": "", "features": features}, nil); err != nil {
		return err
	}
	size := e.FontSize
	if size == 0 {
		size = defaultFontSize
	}
	return b.call(ctx, "Page.setFontSizes", map[string]any{"fontSizes": map[string]int{"standard": size}}, nil)
}

// Hide minimises the tab's window, which hides its page for real, and waits
// for the page to read hidden.
func (b *Tab) Hide(ctx context.Context) error { return b.windowState(ctx, "minimized", "hidden") }

// Show restores the tab's window, and waits for the page to read visible.
func (b *Tab) Show(ctx context.Context) error { return b.windowState(ctx, "normal", "visible") }

func (b *Tab) windowState(ctx context.Context, state, visibility string) error {
	if err := b.c.conn.call(ctx, "", "Browser.setWindowBounds", map[string]any{"windowId": b.window, "bounds": map[string]string{"windowState": state}}, nil); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, visibilityWait)
	defer cancel()
	for {
		var got string
		if err := b.Eval(ctx, "document.visibilityState", &got); err != nil {
			return err
		}
		if got == visibility {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("harness: tab %s still reads %s, not %s: is it its window's active tab? %w", b.name, got, visibility, context.Cause(ctx))
		case <-time.After(processPoll):
		}
	}
}

// Screenshot writes a PNG of the page's viewport to path.
func (b *Tab) Screenshot(ctx context.Context, path string) error {
	var shot struct {
		Data string `json:"data"`
	}
	if err := b.call(ctx, "Page.captureScreenshot", map[string]string{"format": "png"}, &shot); err != nil {
		return err
	}
	png, err := base64.StdEncoding.DecodeString(shot.Data)
	if err != nil {
		return fmt.Errorf("harness: the screenshot: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("harness: the screenshot: %w", err)
	}
	if err := os.WriteFile(path, png, 0o600); err != nil {
		return fmt.Errorf("harness: the screenshot: %w", err)
	}
	return nil
}

// Close closes the tab's page.
func (b *Tab) Close(ctx context.Context) error {
	b.mu.Lock()
	closed := b.closed
	b.mu.Unlock()
	if closed {
		return nil
	}
	err := b.c.conn.call(ctx, "", "Target.closeTarget", map[string]string{"targetId": b.target}, nil)
	b.markClosed()
	b.c.mu.Lock()
	if b.c.tabs[b.name] == b {
		delete(b.c.tabs, b.name)
	}
	b.c.mu.Unlock()
	return err
}

// Event is one piece of evidence (R184), numbered per tab.
type Event struct {
	Seq    int             `json:"seq"`
	At     time.Time       `json:"at"`
	Kind   string          `json:"kind"` // record, request, response, body, log, exception, console, issue, bfcache, navigated, certificate, replay, control
	Detail json.RawMessage `json:"detail"`
}

// Events are the tab's events after the seq, in order, of the kinds when any
// are given.
func (b *Tab) Events(after int, kinds ...string) []Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	i := sort.Search(len(b.events), func(i int) bool { return b.events[i].Seq > after })
	var out []Event
	for _, e := range b.events[i:] {
		if len(kinds) == 0 || slices.Contains(kinds, e.Kind) {
			out = append(out, e)
		}
	}
	return out
}

// record appends an event of the kind to the tab's evidence and to
// events.jsonl, and returns its seq.
func (b *Tab) record(kind string, detail any) int {
	raw, err := json.Marshal(detail)
	if err != nil {
		raw, _ = json.Marshal(map[string]string{"unmarshalable": err.Error()})
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.recordLocked(kind, raw)
}

// recordLocked is record, b.mu held.
func (b *Tab) recordLocked(kind string, raw json.RawMessage) int {
	b.seq++
	e := Event{Seq: b.seq, At: time.Now(), Kind: kind, Detail: raw}
	b.events = append(b.events, e)
	b.c.writeEvent(b.name, &e)
	b.bump()
	return e.Seq
}

// certificate records a certificate event (R180), once a load: Chrome
// gives a failed document's load and its error page the same loader.
func (b *Tab) certificate(load, rawURL, errorText, via string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if load != "" {
		if b.certs[load] {
			return
		}
		b.certs[load] = true
	}
	raw, _ := json.Marshal(map[string]string{"url": rawURL, "error": errorText, "via": via, "load": load})
	b.recordLocked("certificate", raw)
}

// certError says whether a network error is a certificate's.
func certError(errorText string) bool { return strings.HasPrefix(errorText, "net::ERR_CERT_") }

// handle applies an event of the tab's session.
func (b *Tab) handle(e cdpEvent) {
	if h := tabHandlers[e.Method]; h != nil {
		h(b, e.Params)
	}
}

// tabHandlers are what a tab does with each event of its session it reads.
var tabHandlers = map[string]func(*Tab, json.RawMessage){
	"Runtime.executionContextCreated":   (*Tab).onContextCreated,
	"Runtime.executionContextDestroyed": (*Tab).onContextDestroyed,
	"Runtime.executionContextsCleared":  (*Tab).onContextsCleared,
	"Runtime.bindingCalled":             (*Tab).onBinding,
	"Runtime.consoleAPICalled":          (*Tab).onConsole,
	"Runtime.exceptionThrown":           (*Tab).onException,
	"Log.entryAdded":                    (*Tab).onLog,
	"Audits.issueAdded":                 (*Tab).onIssue,
	"Page.lifecycleEvent":               (*Tab).onLifecycle,
	"Page.frameNavigated":               (*Tab).onFrameNavigated,
	"Page.backForwardCacheNotUsed":      (*Tab).onBFCacheNotUsed,
	"Network.requestWillBeSent":         (*Tab).onRequest,
	"Network.responseReceived":          (*Tab).onResponse,
	"Network.loadingFailed":             (*Tab).onLoadingFailed,
	"Network.loadingFinished":           (*Tab).onLoadingFinished,
	"Fetch.requestPaused":               (*Tab).onRequestPaused,
	"Inspector.targetCrashed":           (*Tab).onCrashed,
}

// onContextCreated follows the main frame's document's worlds: its gate
// world and its main world. A restore from the back-forward cache announces
// its document's old contexts again, which are then its contexts again.
func (b *Tab) onContextCreated(raw json.RawMessage) {
	var p struct {
		Context struct {
			ID       int    `json:"id"`
			UniqueID string `json:"uniqueId"`
			Name     string `json:"name"`
			AuxData  struct {
				FrameID   string `json:"frameId"`
				IsDefault bool   `json:"isDefault"`
			} `json:"auxData"`
		} `json:"context"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return
	}
	ctx := p.Context
	b.mu.Lock()
	defer b.mu.Unlock()
	if ctx.AuxData.FrameID != b.frame {
		return
	}
	switch {
	case ctx.AuxData.IsDefault:
		b.main = ctx.UniqueID
		b.contexts[ctx.ID] = tabContext{unique: ctx.UniqueID, world: "main"}
	case ctx.Name == gateWorld:
		b.gate = ctx.UniqueID
		b.contexts[ctx.ID] = tabContext{unique: ctx.UniqueID, world: gateWorld}
	default:
		return
	}
	b.bump()
}

func (b *Tab) onContextDestroyed(raw json.RawMessage) {
	var p struct {
		ID int `json:"executionContextId"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	c, ok := b.contexts[p.ID]
	if !ok {
		return
	}
	delete(b.contexts, p.ID)
	if b.gate == c.unique {
		b.gate = ""
	}
	if b.main == c.unique {
		b.main = ""
	}
	b.bump()
}

func (b *Tab) onContextsCleared(json.RawMessage) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.gate, b.main = "", ""
	clear(b.contexts)
	b.bump()
}

// pageRecord is one of the recorder's records (R183), stamped with the
// page's Date.now().
type pageRecord struct {
	Kind   string          `json:"kind"`
	T      int64           `json:"t"`
	Path   string          `json:"path"`
	Detail json.RawMessage `json:"detail"`
}

// onBinding records the recorder's records; a document's start sets the
// page's order to the document's own.
func (b *Tab) onBinding(raw json.RawMessage) {
	var p struct {
		Name    string `json:"name"`
		Payload string `json:"payload"`
	}
	if json.Unmarshal(raw, &p) != nil || p.Name != gateBinding {
		return
	}
	var r pageRecord
	if err := json.Unmarshal([]byte(p.Payload), &r); err != nil || r.Kind == "" {
		b.record("record", map[string]string{"invalid": p.Payload})
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.recordLocked("record", json.RawMessage(p.Payload))
	if r.Kind == "start" {
		var d struct {
			Order string `json:"order"`
		}
		_ = json.Unmarshal(r.Detail, &d)
		b.order = d.Order
	}
}

func (b *Tab) onConsole(raw json.RawMessage) {
	var p struct {
		Type string `json:"type"`
		Args []struct {
			Type        string          `json:"type"`
			Value       json.RawMessage `json:"value"`
			Description string          `json:"description"`
		} `json:"args"`
		ExecutionContextID int `json:"executionContextId"`
		StackTrace         *struct {
			CallFrames []struct {
				URL        string `json:"url"`
				LineNumber int    `json:"lineNumber"`
			} `json:"callFrames"`
		} `json:"stackTrace"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return
	}
	args := make([]string, 0, len(p.Args))
	for _, a := range p.Args {
		switch {
		case len(a.Value) > 0:
			args = append(args, string(a.Value))
		case a.Description != "":
			args = append(args, a.Description)
		default:
			args = append(args, a.Type)
		}
	}
	d := map[string]any{"type": p.Type, "args": args, "context": p.ExecutionContextID}
	if p.StackTrace != nil && len(p.StackTrace.CallFrames) > 0 {
		d["url"], d["line"] = p.StackTrace.CallFrames[0].URL, p.StackTrace.CallFrames[0].LineNumber
	}
	b.record("console", d)
}

func (b *Tab) onException(raw json.RawMessage) {
	var p struct {
		ExceptionDetails struct {
			Text               string `json:"text"`
			URL                string `json:"url"`
			LineNumber         int    `json:"lineNumber"`
			ColumnNumber       int    `json:"columnNumber"`
			ExecutionContextID int    `json:"executionContextId"`
			Exception          *struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return
	}
	x := p.ExceptionDetails
	d := map[string]any{"text": x.Text, "url": x.URL, "line": x.LineNumber, "column": x.ColumnNumber, "context": x.ExecutionContextID}
	if x.Exception != nil {
		d["description"] = x.Exception.Description
	}
	b.record("exception", d)
}

func (b *Tab) onLog(raw json.RawMessage) {
	var p struct {
		Entry json.RawMessage `json:"entry"`
	}
	if json.Unmarshal(raw, &p) == nil && len(p.Entry) > 0 {
		b.record("log", p.Entry)
	}
}

func (b *Tab) onIssue(raw json.RawMessage) {
	var p struct {
		Issue json.RawMessage `json:"issue"`
	}
	if json.Unmarshal(raw, &p) == nil && len(p.Issue) > 0 {
		b.record("issue", p.Issue)
	}
}

func (b *Tab) onCrashed(json.RawMessage) {
	b.record("exception", map[string]string{"text": "the page's renderer crashed (Inspector.targetCrashed)"})
}

func (b *Tab) onLifecycle(raw json.RawMessage) {
	var p struct {
		FrameID  string `json:"frameId"`
		LoaderID string `json:"loaderId"`
		Name     string `json:"name"`
	}
	if json.Unmarshal(raw, &p) != nil || p.Name != "load" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if p.FrameID == b.frame {
		b.loaded[p.LoaderID] = true
		b.bump()
	}
}

// onFrameNavigated records each commit and its type (R184); for the main
// frame, it counts a new document or a restore. An error page whose failed
// load was a certificate's, or whose cause no failed load recorded, is a
// certificate event (R180).
func (b *Tab) onFrameNavigated(raw json.RawMessage) {
	var p struct {
		Frame struct {
			ID             string `json:"id"`
			ParentID       string `json:"parentId"`
			LoaderID       string `json:"loaderId"`
			URL            string `json:"url"`
			UnreachableURL string `json:"unreachableUrl"`
			MimeType       string `json:"mimeType"`
		} `json:"frame"`
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return
	}
	f := p.Frame
	b.mu.Lock()
	main := f.ID == b.frame
	if main {
		if p.Type == "BackForwardCacheRestore" {
			b.restores++
		} else {
			b.navs++
			b.lastLoader = f.LoaderID
		}
	}
	cause := b.failed[f.UnreachableURL]
	detail, _ := json.Marshal(map[string]any{
		"frameId": f.ID, "main": main, "url": f.URL, "unreachableUrl": f.UnreachableURL,
		"type": p.Type, "loaderId": f.LoaderID, "mimeType": f.MimeType,
	})
	b.recordLocked("navigated", detail)
	b.mu.Unlock()
	if strings.HasPrefix(f.URL, "chrome-error://") {
		switch {
		case cause == "":
			b.certificate(f.LoaderID, f.UnreachableURL, "an error page committed, and no failed load says why", "error page")
		case certError(cause):
			b.certificate(f.LoaderID, f.UnreachableURL, cause, "error page")
		}
	}
}

func (b *Tab) onBFCacheNotUsed(raw json.RawMessage) {
	var p struct {
		LoaderID     string `json:"loaderId"`
		FrameID      string `json:"frameId"`
		Explanations []struct {
			Type   string `json:"type"`
			Reason string `json:"reason"`
		} `json:"notRestoredExplanations"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return
	}
	reasons := make([]string, 0, len(p.Explanations))
	for _, x := range p.Explanations {
		reasons = append(reasons, x.Reason)
	}
	b.record("bfcache", map[string]any{"loaderId": p.LoaderID, "frameId": p.FrameID, "reasons": reasons, "explanations": p.Explanations})
}

// isPoll says whether a request is the board's poll: a script's GET of
// <the page's path>/board (live.js).
func isPoll(resourceType, method, rawURL string) bool {
	if (resourceType != "XHR" && resourceType != "Fetch") || method != http.MethodGet {
		return false
	}
	u, err := url.Parse(rawURL)
	return err == nil && strings.HasSuffix(u.Path, "/board")
}

// cdpResponse is a response as the Network domain gives it.
type cdpResponse struct {
	URL          string         `json:"url"`
	Status       int            `json:"status"`
	StatusText   string         `json:"statusText"`
	Headers      map[string]any `json:"headers"`
	MimeType     string         `json:"mimeType"`
	FromCache    bool           `json:"fromDiskCache"`
	FromPrefetch bool           `json:"fromPrefetchCache"`
	RemoteIP     string         `json:"remoteIPAddress"`
	RemotePort   int            `json:"remotePort"`
}

func (b *Tab) onRequest(raw json.RawMessage) {
	var p struct {
		RequestID string `json:"requestId"`
		LoaderID  string `json:"loaderId"`
		Type      string `json:"type"`
		Request   struct {
			URL     string         `json:"url"`
			Method  string         `json:"method"`
			Headers map[string]any `json:"headers"`
		} `json:"request"`
		RedirectResponse *cdpResponse `json:"redirectResponse"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return
	}
	if r := p.RedirectResponse; r != nil {
		b.record("response", map[string]any{
			"requestId": p.RequestID, "url": r.URL, "type": p.Type, "status": r.Status, "statusText": r.StatusText,
			"headers": r.Headers, "redirect": true,
		})
	}
	poll := isPoll(p.Type, p.Request.Method, p.Request.URL)
	b.mu.Lock()
	b.requests[p.RequestID] = &tabRequest{url: p.Request.URL, poll: poll, replayed: b.replayed[p.RequestID]}
	b.mu.Unlock()
	b.record("request", map[string]any{
		"requestId": p.RequestID, "loaderId": p.LoaderID, "url": p.Request.URL, "method": p.Request.Method,
		"type": p.Type, "headers": p.Request.Headers, "poll": poll,
	})
}

func (b *Tab) onResponse(raw json.RawMessage) {
	var p struct {
		RequestID string      `json:"requestId"`
		Type      string      `json:"type"`
		Response  cdpResponse `json:"response"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return
	}
	r := p.Response
	b.mu.Lock()
	q := b.requests[p.RequestID]
	poll, replayed := false, b.replayed[p.RequestID]
	if q != nil {
		q.status, q.headers = r.Status, r.Headers
		poll = q.poll
	}
	b.mu.Unlock()
	d := map[string]any{
		"requestId": p.RequestID, "url": r.URL, "type": p.Type, "status": r.Status, "statusText": r.StatusText,
		"headers": r.Headers, "mimeType": r.MimeType, "fromCache": r.FromCache || r.FromPrefetch,
		"poll": poll, "replayed": replayed,
	}
	if r.RemoteIP != "" {
		d["remote"] = net.JoinHostPort(r.RemoteIP, strconv.Itoa(r.RemotePort))
	}
	b.record("response", d)
}

func (b *Tab) onLoadingFailed(raw json.RawMessage) {
	var p struct {
		RequestID     string `json:"requestId"`
		Type          string `json:"type"`
		ErrorText     string `json:"errorText"`
		Canceled      bool   `json:"canceled"`
		BlockedReason string `json:"blockedReason"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return
	}
	b.mu.Lock()
	var u string
	poll := false
	if q := b.requests[p.RequestID]; q != nil {
		u, poll = q.url, q.poll
	}
	delete(b.requests, p.RequestID)
	if p.Type == "Document" && u != "" {
		b.failed[u] = p.ErrorText
	}
	b.mu.Unlock()
	b.record("response", map[string]any{
		"requestId": p.RequestID, "url": u, "type": p.Type, "failed": p.ErrorText,
		"canceled": p.Canceled, "blockedReason": p.BlockedReason, "poll": poll,
	})
	if certError(p.ErrorText) {
		b.certificate(p.RequestID, u, p.ErrorText, "loadingFailed: "+p.Type)
	}
}

// onLoadingFinished keeps a 200 poll's body (R184): in bodies/<tab>-<seq>.html,
// and, in its event, its size and what live.js reads of it.
func (b *Tab) onLoadingFinished(raw json.RawMessage) {
	var p struct {
		RequestID string `json:"requestId"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return
	}
	b.mu.Lock()
	q := b.requests[p.RequestID]
	delete(b.requests, p.RequestID)
	b.mu.Unlock()
	if q == nil || !q.poll || q.status != http.StatusOK {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), cdpCallTimeout)
	defer cancel()
	var body struct {
		Body          string `json:"body"`
		Base64Encoded bool   `json:"base64Encoded"`
	}
	err := b.call(ctx, "Network.getResponseBody", map[string]string{"requestId": p.RequestID}, &body)
	content := []byte(body.Body)
	if err == nil && body.Base64Encoded {
		content, err = base64.StdEncoding.DecodeString(body.Body)
	}
	b.keepBody(p.RequestID, q, content, err)
}

// boardFacts are what R184 keeps of a poll's body, read with
// golang.org/x/net/html: #board's order and ETag, #summary's round, and
// each region's data-v.
type boardFacts struct {
	Order   string            `json:"order"`
	ETag    string            `json:"etag"`
	Round   string            `json:"round"`
	Regions map[string]string `json:"regions"`
	board   bool
}

// readBoard reads a regions document; the first element of an id counts,
// as getElementById's.
func readBoard(body []byte) (boardFacts, error) {
	f := boardFacts{Regions: map[string]string{}}
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return f, err
	}
	seen := map[string]bool{}
	for n := range doc.Descendants() {
		if n.Type != html.ElementNode {
			continue
		}
		id, _ := htmlAttr(n, "id")
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		switch id {
		case "board":
			f.board = true
			f.Order, _ = htmlAttr(n, "data-order")
			f.ETag, _ = htmlAttr(n, "data-etag")
		case "summary":
			f.Round, _ = htmlAttr(n, "data-round")
		}
		if slices.Contains(regionIDs, id) {
			f.Regions[id], _ = htmlAttr(n, "data-v")
		}
	}
	return f, nil
}

func htmlAttr(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Namespace == "" && a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

// keepBody writes a 200 poll's body and records it; a body that live.js
// would apply sets the page's order, by live.js's own rule.
func (b *Tab) keepBody(requestID string, q *tabRequest, content []byte, readErr error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	seq := b.seq + 1
	replayed := q.replayed || b.replayed[requestID]
	d := map[string]any{"requestId": requestID, "url": q.url, "replayed": replayed}
	if readErr != nil {
		d["error"] = readErr.Error()
		raw, _ := json.Marshal(d)
		b.recordLocked("body", raw)
		return
	}
	file := filepath.Join(b.c.runDir, "bodies", b.name+"-"+strconv.Itoa(seq)+".html")
	if err := os.WriteFile(file, content, 0o600); err != nil {
		d["error"] = err.Error()
		file = ""
	} else {
		d["file"] = filepath.Join("bodies", filepath.Base(file))
	}
	facts, err := readBoard(content)
	if err != nil {
		d["error"] = "harness: read the body: " + err.Error()
	}
	d["size"], d["order"], d["etag"], d["round"], d["regions"] = len(content), facts.Order, facts.ETag, facts.Round, facts.Regions
	raw, _ := json.Marshal(d)
	b.recordLocked("body", raw)
	if !facts.board || len(facts.Regions) != len(regionIDs) {
		return // live.js reloads the page rather than apply it
	}
	if !replayed && file != "" && facts.Order != "" {
		b.answers = append(b.answers, pollAnswer{seq: seq, url: q.url, file: file, facts: facts, headers: q.headers})
	}
	if facts.Order == "" || b.order == "" || !liveBelow(facts.Order, b.order) {
		b.order = facts.Order
	}
}

// orderOf reads an order, "<record seq>.<board seq>".
func orderOf(order string) (record, board float64, ok bool) {
	a, c, found := strings.Cut(order, ".")
	if !found {
		return 0, 0, false
	}
	r, err1 := strconv.ParseFloat(a, 64)
	s, err2 := strconv.ParseFloat(c, 64)
	return r, s, err1 == nil && err2 == nil
}

// liveBelow is live.js's below: a lower in either part.
func liveBelow(a, b string) bool {
	ar, ab, ok1 := orderOf(a)
	br, bb, ok2 := orderOf(b)
	return ok1 && ok2 && (ar < br || ab < bb)
}

// orderBelow says whether a is strictly below b: no part higher and one
// lower, as an instance's lag is; live.js drops such an answer.
func orderBelow(a, b string) bool {
	ar, ab, ok1 := orderOf(a)
	br, bb, ok2 := orderOf(b)
	return ok1 && ok2 && ar <= br && ab <= bb && (ar < br || ab < bb)
}

// Replay is what ReplayOlder replayed (R190).
type Replay struct {
	Replayed, PageOrder string
	Differing           []string // region ids whose data-v differ
}

// regionsExpr reads, in the gate world, the page's URL and its regions'
// data-v.
var regionsExpr = `(() => { const v = {}; for (const id of ` + jsonList(regionIDs) +
	`) { const e = document.getElementById(id); if (e) v[id] = e.dataset.v || ""; } return {href: location.href, v: v}; })()`

func jsonList(s []string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}

// ReplayOlder arms Fetch for the tab's next poll, and answers it with an
// earlier recorded 200 of the page's own poll URL (R190): the answer's
// headers, with Content-Type: text/html; charset=utf-8, and its body, whose
// order is strictly below the page's last applied order and whose regions
// differ in some data-v from the page's. The last applied order is the
// latest 200's live.js applied, or the document's own (R183). It then
// disables Fetch, and returns and records, as kind replay, the replayed
// order, the page's order, and the regions that differ.
func (b *Tab) ReplayOlder(ctx context.Context) (Replay, error) {
	ctx, cancel := context.WithTimeout(ctx, waitBound)
	defer cancel()
	var page struct {
		Href string            `json:"href"`
		V    map[string]string `json:"v"`
	}
	if err := b.Eval(ctx, regionsExpr, &page); err != nil {
		return Replay{}, err
	}
	poll, err := url.Parse(page.Href)
	if err != nil {
		return Replay{}, fmt.Errorf("harness: tab %s: the page's URL: %w", b.name, err)
	}
	poll.Fragment, poll.RawFragment, poll.RawPath = "", "", ""
	poll.Path += "/board"
	armed, err := b.arm(poll.String(), page.V)
	if err != nil {
		return Replay{}, err
	}
	if err := b.call(ctx, "Fetch.enable", map[string]any{"patterns": []map[string]string{
		{"urlPattern": "*/board*", "resourceType": "XHR", "requestStage": "Request"},
	}}, nil); err != nil {
		b.disarm(armed)
		return Replay{}, err
	}
	select {
	case d := <-armed.done:
		return d.replay, d.err
	case <-ctx.Done():
	}
	if b.disarm(armed) {
		dctx, dcancel := context.WithTimeout(context.Background(), cdpCallTimeout)
		defer dcancel()
		return Replay{}, errors.Join(fmt.Errorf("harness: tab %s: no poll came to replay: %w", b.name, context.Cause(ctx)),
			b.call(dctx, "Fetch.disable", nil, nil))
	}
	d := <-armed.done
	return d.replay, d.err
}

// arm picks the answer to replay, the latest that qualifies, and arms it.
func (b *Tab) arm(pollURL string, dom map[string]string) (*armedReplay, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.replay != nil {
		return nil, fmt.Errorf("harness: tab %s has a replay armed already", b.name)
	}
	current := b.order
	for i := len(b.answers) - 1; i >= 0; i-- {
		a := &b.answers[i]
		if !samePollURL(a.url, pollURL) || !orderBelow(a.facts.Order, current) {
			continue
		}
		var differing []string
		for _, id := range regionIDs {
			if v, ok := dom[id]; ok && a.facts.Regions[id] != v {
				differing = append(differing, id)
			}
		}
		if len(differing) == 0 {
			continue
		}
		body, err := os.ReadFile(a.file) //nolint:gosec // G703: a body the harness wrote, bodies/<tab>-<seq>.html of its own run directory.
		if err != nil {
			return nil, fmt.Errorf("harness: tab %s: the body to replay: %w", b.name, err)
		}
		b.replay = &armedReplay{
			headers: replayHeaders(a.headers),
			body:    base64.StdEncoding.EncodeToString(body),
			result:  Replay{Replayed: a.facts.Order, PageOrder: current, Differing: differing},
			from:    a.seq,
			done:    make(chan replayDone, 1),
		}
		return b.replay, nil
	}
	return nil, fmt.Errorf("harness: tab %s: no earlier 200 of %s is below the page's order %q and differs from its regions", b.name, pollURL, current)
}

// disarm takes armed back unless a poll took it; it says whether it did.
func (b *Tab) disarm(armed *armedReplay) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.replay != armed {
		return false
	}
	b.replay = nil
	return true
}

// replayHeaders are an answer's headers as Fetch.fulfillRequest takes
// them: its own, but those of its framing and encoding, which a replayed
// body does not keep, and Content-Type text/html, so that live.js's XHR
// parses it as HTML.
func replayHeaders(h map[string]any) []map[string]string {
	names := make([]string, 0, len(h))
	for name := range h {
		switch strings.ToLower(name) {
		case "content-type", "content-length", "content-encoding", "transfer-encoding":
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]map[string]string, 0, len(names)+1)
	for _, name := range names {
		for v := range strings.SplitSeq(fmt.Sprint(h[name]), "\n") {
			out = append(out, map[string]string{"name": name, "value": v})
		}
	}
	return append(out, map[string]string{"name": "Content-Type", "value": "text/html; charset=utf-8"})
}

// samePollURL says whether two URLs name the same poll.
func samePollURL(a, b string) bool {
	x, err1 := url.Parse(a)
	y, err2 := url.Parse(b)
	return err1 == nil && err2 == nil && x.Scheme == y.Scheme && x.Host == y.Host && x.EscapedPath() == y.EscapedPath() && x.RawQuery == y.RawQuery
}

// onRequestPaused answers the poll a replay is armed for, then disables
// Fetch; any other request goes on as it was.
func (b *Tab) onRequestPaused(raw json.RawMessage) {
	var p struct {
		RequestID    string `json:"requestId"`
		NetworkID    string `json:"networkId"`
		ResourceType string `json:"resourceType"`
		Request      struct {
			URL    string `json:"url"`
			Method string `json:"method"`
		} `json:"request"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), cdpCallTimeout)
	defer cancel()
	b.mu.Lock()
	armed := b.replay
	take := armed != nil && isPoll(p.ResourceType, p.Request.Method, p.Request.URL)
	if take {
		b.replay = nil
		if p.NetworkID != "" {
			b.replayed[p.NetworkID] = true
			if q := b.requests[p.NetworkID]; q != nil {
				q.replayed = true
			}
		}
	}
	b.mu.Unlock()
	if !take {
		_ = b.call(ctx, "Fetch.continueRequest", map[string]string{"requestId": p.RequestID}, nil)
		return
	}
	err := b.call(ctx, "Fetch.fulfillRequest", map[string]any{
		"requestId": p.RequestID, "responseCode": http.StatusOK, "responseHeaders": armed.headers, "body": armed.body,
	}, nil)
	err = errors.Join(err, b.call(ctx, "Fetch.disable", nil, nil))
	if err == nil {
		b.record("replay", map[string]any{
			"replayed": armed.result.Replayed, "page": armed.result.PageOrder, "differing": armed.result.Differing,
			"from": armed.from, "requestId": p.NetworkID, "url": p.Request.URL,
		})
	}
	armed.done <- replayDone{replay: armed.result, err: err}
}

// recorderJS is the recorder (R183), which runs in every new document in
// the gate world, and reports through the binding exposed to that world
// alone, each record stamped with the page's Date.now(): the document's
// start, once its parsing ends and before its deferred scripts run, with
// #board's data-order and data-etag; visibilitychange; pageshow with
// persisted; focusin's target; each write to #announce, counted by a
// MutationObserver; the bar's text; and each region that live.js replaces
// or updates in place. It reads the page, and writes to no DOM sink.
var recorderJS = `(() => {
  const send = (kind, detail) => {
    try { ` + gateBinding + `(JSON.stringify({kind: kind, t: Date.now(), path: location.pathname, detail: detail})); } catch (e) {}
  };
  const regions = ` + jsonList(regionIDs) + `;
  const regionOf = (n) => {
    for (let e = n; e && e.nodeType === 1; e = e.parentElement) if (regions.indexOf(e.id) >= 0) return e.id;
    return "";
  };
  const describe = (n) => {
    if (!n || n.nodeType !== 1) return null;
    let visible = false;
    try { visible = n.matches(":focus-visible"); } catch (e) {}
    return {tag: n.tagName.toLowerCase(), id: n.id || "", cls: typeof n.className === "string" ? n.className : "", region: regionOf(n), focusVisible: visible};
  };
  const watch = (target, options, f) => { try { new MutationObserver(f).observe(target, options); } catch (e) {} };
  let started = false;
  const start = () => {
    if (started) return;
    started = true;
    const board = document.getElementById("board");
    const bar = board && board.firstElementChild;
    const state = document.getElementById("live-state");
    send("start", {
      order: board ? board.getAttribute("data-order") || "" : null,
      etag: board ? board.getAttribute("data-etag") || "" : null,
      bar: state ? state.textContent : null,
      visibility: document.visibilityState
    });
    if (bar && state) watch(bar, {childList: true, characterData: true, subtree: true}, () => send("bar", {text: state.textContent}));
    const announce = document.getElementById("announce");
    let writes = 0;
    if (announce) watch(announce, {childList: true, characterData: true, subtree: true}, (ms) => {
      writes += ms.length;
      send("announce", {writes: writes, text: announce.textContent});
    });
    watch(document.documentElement, {childList: true, subtree: true, attributes: true, attributeFilter: ["data-v"]}, (ms) => {
      for (const m of ms) {
        if (m.type === "attributes") {
          if (regions.indexOf(m.target.id) >= 0) send("region", {region: m.target.id, v: m.target.getAttribute("data-v") || "", how: "in place"});
          continue;
        }
        for (const n of m.addedNodes) {
          if (n.nodeType === 1 && regions.indexOf(n.id) >= 0) send("region", {region: n.id, v: n.getAttribute("data-v") || "", how: "replaced"});
        }
      }
    });
  };
  document.addEventListener("readystatechange", () => { if (document.readyState !== "loading") start(); });
  if (document.readyState !== "loading") start();
  document.addEventListener("visibilitychange", () => send("visibility", {state: document.visibilityState}));
  window.addEventListener("pageshow", (e) => send("pageshow", {persisted: e.persisted}));
  document.addEventListener("focusin", (e) => send("focusin", describe(e.target)));
})();`
