//go:build e2e

package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
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
)

// ControlPathMax is the longest path macOS binds a Unix socket at, and so
// ServeControl's socket's: its sun_path holds 104 bytes, the last a NUL.
const ControlPathMax = 103

// The control socket's other limits (R186).
const (
	// controlBodyMax bounds a command's JSON.
	controlBodyMax = 1 << 20
	// controlHeaderTimeout bounds the read of a command's header.
	controlHeaderTimeout = 10 * time.Second
	// controlStopWait bounds the stop's wait for the commands in flight,
	// which end once their context is cancelled, but for a CDP call's own
	// bound, cdpCallTimeout.
	controlStopWait = cdpCallTimeout + 10*time.Second
)

// screenshotName is what a screenshot's name may be (R186): it names its
// file in the run directory's screenshots/.
var screenshotName = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)

// controlListen binds the control socket; a test makes its listener fail.
var controlListen = net.Listen

// ControlOptions say what ServeControl serves.
type ControlOptions struct {
	Path    string     // the socket, at most 103 bytes
	Origins []string   // what /tab and /navigate may open, beside about:blank
	World   func() any // GET /world
	End     func()     // POST /end
}

// ServeControl serves R186's commands for c until t ends. It refuses a path
// over 103 bytes, saying why.
//
// The socket is HTTP over a Unix socket at o.Path, whose directory must be
// the user's own and 0700, and each command takes and answers JSON:
//
//	POST /tab {"name","url","window"}  a tab, in a window of its own unless window is false
//	POST /close {"tab"}
//	POST /navigate {"tab","url"}, /back {"tab"}, /reload {"tab"}
//	POST /eval {"tab","expr"}  in the gate world
//	POST /key {"tab","key"}  answering the page's visibility after the key
//	POST /type {"tab","text"}, /click {"tab","selector"}
//	POST /emulate {"tab","width","height","dpr","mobile","dark","fontSize"}  all zero clears it
//	POST /hide {"tab"}, /show {"tab"}
//	POST /screenshot {"tab","name"}  screenshots/<name>.png, answering its path
//	POST /replay {"tab"}  R190's
//	POST /cookies/clear
//	POST /end  End, once
//	GET /events?tab=&after=&kind=  a tab's events after the seq, of the kinds, comma-separated, and last
//	GET /polls?tab=&after=  a tab's polls that changed after the seq, whole, and last
//	GET /netlog  R185's check on the NetLog so far, provisional while Chrome runs
//	GET /world  World's answer
//	GET /status  the browser's version and state, open or its pipe closed and why, the run directory, the socket and the tabs
//
// /tab and /navigate open about:blank and the origins alone. A tab's name is
// used once a run, as events.jsonl keys each event by it, and a closed tab's
// events are still read. Each command that acts or evaluates is recorded on
// its tab as kind control (R184); one refused before it acts is not. A
// refusal answers 4xx, and what the browser could not do 502, each with
// the reason; after /end no command acts, and reads still answer. World and
// End are called from the socket's goroutines; End must return at once.
//
// At t's end the socket closes and the commands in flight are cancelled; the
// cleanup returns once each has answered, and recorded itself, so that no
// control record comes after the browser's Close, which the caller makes
// after it: in a cleanup registered before ServeControl, or once t has
// ended (R184).
func ServeControl(t testing.TB, c *Chrome, o ControlOptions) error {
	t.Helper()
	if n := len(o.Path); n > ControlPathMax {
		return fmt.Errorf("harness: the control socket's path %s is %d bytes, over the %d macOS binds a Unix socket at: put it in a shorter directory", o.Path, n, ControlPathMax)
	}
	if c == nil || o.World == nil || o.End == nil {
		return errors.New("harness: ServeControl needs a browser, a World and an End")
	}
	origins, err := controlOrigins(o.Origins)
	if err != nil {
		return err
	}
	if err := privateDir(filepath.Dir(o.Path)); err != nil {
		return err
	}
	ln, err := controlListen("unix", o.Path)
	if err != nil {
		return fmt.Errorf("harness: the control socket: %w", err)
	}
	s := &control{c: c, path: o.Path, origins: origins, world: o.World, end: o.End, tabs: map[string]*Tab{}}
	s.routes = s.commands()
	s.adopt()
	ctx, cancel := context.WithCancel(t.Context())
	srv := &http.Server{
		Handler:           s,
		ReadHeaderTimeout: controlHeaderTimeout,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	served := make(chan struct{})
	go func() {
		defer close(served)
		// Its error, but for the stop's own, is reported: the test still
		// runs, as the stop waits for this goroutine.
		if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("harness: the control socket stopped serving, and its commands go unanswered: %v", err)
		}
	}()
	t.Cleanup(func() {
		// The commands in flight are cancelled, and Shutdown waits for each
		// to answer, as its connection is active while it runs: a World,
		// which takes no context, runs to its end.
		cancel()
		stop, stopped := context.WithTimeout(context.Background(), controlStopWait)
		defer stopped()
		if err := srv.Shutdown(stop); err != nil {
			_ = srv.Close()
			t.Errorf("harness: the control socket's commands still ran %v after its stop, and what they record after the browser's Close is lost: %v", controlStopWait, err)
		}
		<-served
	})
	return nil
}

// control is a control socket's server (R186).
type control struct {
	c       *Chrome
	path    string
	origins []string
	world   func() any
	end     func()
	routes  map[string]controlRoute

	mu sync.Mutex
	// tabs are the run's tabs the socket knows, open or closed, nil while one
	// opens, by name, in the order it came to know them; ending is set by
	// POST /end.
	tabs   map[string]*Tab
	order  []string
	ending bool
}

// controlRoute is a command: its method, and what it does, which answers
// the JSON to write, or an error.
type controlRoute struct {
	method string
	run    func(*http.Request) (any, error)
}

func (s *control) commands() map[string]controlRoute {
	post, get := http.MethodPost, http.MethodGet
	return map[string]controlRoute{
		"/tab":           {post, s.openTab},
		"/close":         {post, onTab(s, "close", s.closeTab)},
		"/navigate":      {post, onTab(s, "navigate", s.navigate)},
		"/back":          {post, onTab(s, "back", s.back)},
		"/reload":        {post, onTab(s, "reload", s.reload)},
		"/eval":          {post, onTab(s, "eval", s.eval)},
		"/key":           {post, onTab(s, "key", s.key)},
		"/type":          {post, onTab(s, "type", s.typeText)},
		"/click":         {post, onTab(s, "click", s.click)},
		"/emulate":       {post, onTab(s, "emulate", s.emulate)},
		"/hide":          {post, onTab(s, "hide", s.hide)},
		"/show":          {post, onTab(s, "show", s.show)},
		"/screenshot":    {post, onTab(s, "screenshot", s.screenshot)},
		"/replay":        {post, onTab(s, "replay", s.replay)},
		"/cookies/clear": {post, s.clearCookies},
		"/end":           {post, s.endRun},
		"/events":        {get, s.events},
		"/polls":         {get, s.polls},
		"/netlog":        {get, s.netlog},
		"/world":         {get, s.worldNow},
		"/status":        {get, s.status},
	}
}

func (s *control) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	route, known := s.routes[r.URL.Path]
	switch {
	case !known:
		writeControl(w, http.StatusNotFound, failed("harness: no command %s; the commands are %s", r.URL.Path, s.commandList()))
		return
	case r.Method != route.method:
		w.Header().Set("Allow", route.method)
		writeControl(w, http.StatusMethodNotAllowed, failed("harness: %s is a %s command, not %s", r.URL.Path, route.method, r.Method))
		return
	case route.method == http.MethodPost && r.URL.Path != "/end" && s.isEnding():
		writeControl(w, http.StatusServiceUnavailable, failed("harness: the run is ending, as POST /end asked: no command acts now"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, controlBodyMax)
	out, err := route.run(r)
	if err != nil {
		status, answer := failureAnswer(err)
		writeControl(w, status, answer)
		return
	}
	writeControl(w, http.StatusOK, out)
}

func (s *control) commandList() string {
	names := make([]string, 0, len(s.routes))
	for name := range s.routes {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func (s *control) isEnding() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ending
}

// writeControl writes a command's answer, JSON with HTML left unescaped;
// one that is no JSON is a 500 that says why.
func writeControl(w http.ResponseWriter, status int, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		status = http.StatusInternalServerError
		buf.Reset()
		_ = enc.Encode(map[string]string{"error": "harness: the answer is no JSON: " + err.Error()})
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// controlError is a command the socket refuses: its status, 4xx, and why.
type controlError struct {
	status int
	why    string
}

func (e *controlError) Error() string { return e.why }

// refuse is a refusal of a command, with its status and why.
func refuse(status int, format string, args ...any) error {
	return &controlError{status: status, why: fmt.Sprintf(format, args...)}
}

// failed is an answer that says why.
func failed(format string, args ...any) map[string]string {
	return map[string]string{"error": fmt.Sprintf(format, args...)}
}

// failureAnswer is an error's status and answer: a refusal's own, or 502 for
// what the browser could not do, certificate set for a navigation refused
// for its certificate (R180).
func failureAnswer(err error) (status int, answer map[string]any) {
	var refused *controlError
	if errors.As(err, &refused) {
		return refused.status, map[string]any{"error": refused.why}
	}
	answer = map[string]any{"error": err.Error()}
	if errors.Is(err, ErrCertificate) {
		answer["certificate"] = true
	}
	return http.StatusBadGateway, answer
}

// controlOrigins reads the origins the socket opens, each a scheme and a
// host alone, as https://127.0.0.1:8443.
func controlOrigins(raw []string) ([]string, error) {
	origins := make([]string, 0, len(raw))
	for _, o := range raw {
		u, err := url.Parse(o)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil ||
			strings.Trim(u.Path, "/") != "" || u.RawQuery != "" || u.Fragment != "" {
			return nil, fmt.Errorf("harness: %q is not an origin, a scheme and a host alone, as https://127.0.0.1:8443", o)
		}
		origins = append(origins, u.Scheme+"://"+u.Host)
	}
	return origins, nil
}

// allowed is rawURL as the socket opens it (R186): about:blank, or a URL on
// one of its origins, written again as url.Parse read it, so that the
// browser opens the URL that was checked. Anything else is refused.
func (s *control) allowed(rawURL string) (string, error) {
	switch rawURL {
	case "":
		return "", refuse(http.StatusBadRequest, `harness: name the URL: "url"`)
	case "about:blank":
		return rawURL, nil
	}
	u, err := url.Parse(rawURL)
	if err == nil && u.User == nil && u.Opaque == "" && slices.Contains(s.origins, u.Scheme+"://"+u.Host) {
		return u.String(), nil
	}
	return "", refuse(http.StatusForbidden, "harness: the socket opens about:blank and %s alone (R186), not %q", listOr(s.origins), rawURL)
}

// privateDir refuses a socket's directory that is not the user's own and
// 0700 (R186): whoever enters it reaches the socket.
func privateDir(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("harness: the control socket's directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("harness: the control socket's directory %s is not a directory", dir)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		return fmt.Errorf("harness: the control socket's directory %s is %#o, not 0700: R186 keeps the socket where its owner alone reaches it", dir, perm)
	}
	if st, isUnix := info.Sys().(*syscall.Stat_t); !isUnix || int64(st.Uid) != int64(os.Getuid()) {
		return fmt.Errorf("harness: the control socket's directory %s is not the user's own (R186)", dir)
	}
	return nil
}

// listOr is names as a list in words, "none" for no name.
func listOr(names []string) string {
	switch len(names) {
	case 0:
		return "none"
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// adopt takes in the tabs c has open, such as one the environment opened
// before the socket served.
func (s *control) adopt() {
	s.c.mu.Lock()
	names := make([]string, 0, len(s.c.tabs))
	tabs := map[string]*Tab{}
	for name, b := range s.c.tabs {
		if b != nil {
			names = append(names, name)
			tabs[name] = b
		}
	}
	s.c.mu.Unlock()
	sort.Strings(names)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, name := range names {
		s.tabs[name] = tabs[name]
		s.order = append(s.order, name)
	}
}

// tabNamed is the run's tab of the name, open or closed, for a command that
// reads it. A tab c opened beside the socket is taken in.
func (s *control) tabNamed(name string) (*Tab, error) {
	if name == "" {
		return nil, refuse(http.StatusBadRequest, `harness: name the tab: "tab"`)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, known := s.tabs[name]
	if !known {
		s.c.mu.Lock()
		b = s.c.tabs[name]
		s.c.mu.Unlock()
		if b == nil {
			return nil, refuse(http.StatusNotFound, "harness: there is no tab %s; the run's tabs are %s", name, listOr(s.order))
		}
		s.tabs[name] = b
		s.order = append(s.order, name)
	}
	if b == nil {
		return nil, refuse(http.StatusConflict, "harness: tab %s is still opening", name)
	}
	return b, nil
}

// openTabNamed is tabNamed's tab, for a command that acts on it, which a
// closed tab refuses.
func (s *control) openTabNamed(name string) (*Tab, error) {
	b, err := s.tabNamed(name)
	if err == nil && b.isClosed() {
		return nil, refuse(http.StatusConflict, "harness: tab %s is closed", name)
	}
	return b, err
}

// openTabs are the run's open tabs, in order.
func (s *control) openTabs() []*Tab {
	s.mu.Lock()
	defer s.mu.Unlock()
	var open []*Tab
	for _, name := range s.order {
		if b := s.tabs[name]; b != nil && !b.isClosed() {
			open = append(open, b)
		}
	}
	return open
}

// reserve holds a name for /tab, and refuses one the run has used, open or
// closed, as events.jsonl keys each event by its tab's name.
func (s *control) reserve(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, used := s.tabs[name]
	if !used {
		s.c.mu.Lock()
		_, used = s.c.tabs[name]
		s.c.mu.Unlock()
	}
	if used {
		return refuse(http.StatusConflict, "harness: the run has had a tab %s: name this one otherwise, as events.jsonl keys each event by its tab's name", name)
	}
	s.tabs[name] = nil
	s.order = append(s.order, name)
	return nil
}

// opened registers the tab /tab opened, or frees its name when none did.
func (s *control) opened(name string, b *Tab) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b != nil {
		s.tabs[name] = b
		return
	}
	delete(s.tabs, name)
	s.order = slices.DeleteFunc(s.order, func(n string) bool { return n == name })
}

// isClosed says whether the tab has closed.
func (b *Tab) isClosed() bool {
	select {
	case <-b.done:
		return true
	default:
		return false
	}
}

// tabSeq is the seq of the tab's latest event, 0 before any.
func tabSeq(b *Tab) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.seq
}

// locationOf is the URL of the tab's main frame's latest commit, about:blank
// before any.
func locationOf(b *Tab) string {
	navs := b.Events(0, "navigated")
	for i := len(navs) - 1; i >= 0; i-- {
		var d struct {
			Main bool   `json:"main"`
			URL  string `json:"url"`
		}
		if json.Unmarshal(navs[i].Detail, &d) == nil && d.Main {
			return d.URL
		}
	}
	return "about:blank"
}

// located is a navigation's answer: the tab, and where it is now.
func located(b *Tab) map[string]any {
	return map[string]any{"tab": b.name, "location": locationOf(b)}
}

// recordCommand records a command on each of tabs as kind control (R184):
// what it was, its arguments, when it began, how long it took, and its
// error.
func recordCommand(tabs []*Tab, cmd string, args any, began time.Time, err error) {
	d := map[string]any{"cmd": cmd, "began": began, "ms": time.Since(began).Milliseconds()}
	if args != nil {
		d["args"] = args
	}
	if err != nil {
		d["error"] = err.Error()
	}
	for _, b := range tabs {
		b.record("control", d)
	}
}

// decodeArgs reads a command's JSON object into v, and refuses a field v
// has not; no body at all is {}.
func decodeArgs(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		return refuse(http.StatusBadRequest, "harness: the JSON of %s: %v", r.URL.Path, err)
	}
	return nil
}

// queryOf is a read's query, refusing a parameter it does not take.
func queryOf(r *http.Request, takes ...string) (url.Values, error) {
	q := r.URL.Query()
	for k := range q {
		if !slices.Contains(takes, k) {
			return nil, refuse(http.StatusBadRequest, "harness: %s takes %s, not %q", r.URL.Path, listOr(takes), k)
		}
	}
	return q, nil
}

// afterOf is the seq a read starts after: its after, 0 when none.
func afterOf(q url.Values) (int, error) {
	raw := q.Get("after")
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, refuse(http.StatusBadRequest, "harness: after is a seq, a whole number of 0 or more, not %q", raw)
	}
	return n, nil
}

// needed refuses a field left empty.
func needed(field, value string) error {
	if value == "" {
		return refuse(http.StatusBadRequest, "harness: name the %s: %q", field, field)
	}
	return nil
}

// tabCommand is a command's arguments: the open tab it acts on, and a check
// of their own before it acts.
type tabCommand interface {
	tabName() string
	check(s *control) error
}

// onTab is a command on an open tab: it reads its arguments, checks them,
// finds the tab, acts, and records the command on the tab (R184), the act's
// error with it. A command refused before it acts is not recorded.
func onTab[A tabCommand](s *control, cmd string, act func(ctx context.Context, b *Tab, a A) (any, error)) func(*http.Request) (any, error) {
	return func(r *http.Request) (any, error) {
		var a A
		if err := decodeArgs(r, &a); err != nil {
			return nil, err
		}
		if err := a.check(s); err != nil {
			return nil, err
		}
		b, err := s.openTabNamed(a.tabName())
		if err != nil {
			return nil, err
		}
		began := time.Now()
		out, err := act(r.Context(), b, a)
		recordCommand([]*Tab{b}, cmd, a, began, err)
		return out, err
	}
}

// tabArgs name the tab a command acts on.
type tabArgs struct {
	Tab string `json:"tab"`
}

func (a tabArgs) tabName() string { return a.Tab }

func (tabArgs) check(*control) error { return nil }

// navigateArgs are /navigate's.
type navigateArgs struct {
	tabArgs
	URL string `json:"url"`
}

func (a navigateArgs) check(s *control) error {
	_, err := s.allowed(a.URL)
	return err
}

// evalArgs are /eval's.
type evalArgs struct {
	tabArgs
	Expr string `json:"expr"`
}

func (a evalArgs) check(*control) error { return needed("expr", a.Expr) }

// keyArgs are /key's.
type keyArgs struct {
	tabArgs
	Key string `json:"key"`
}

func (a keyArgs) check(*control) error { return needed("key", a.Key) }

// typeArgs are /type's.
type typeArgs struct {
	tabArgs
	Text string `json:"text"`
}

func (a typeArgs) check(*control) error { return needed("text", a.Text) }

// clickArgs are /click's.
type clickArgs struct {
	tabArgs
	Selector string `json:"selector"`
}

func (a clickArgs) check(*control) error { return needed("selector", a.Selector) }

// emulateArgs are /emulate's: an Emulation, which all zero clears.
type emulateArgs struct {
	tabArgs
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	DPR      float64 `json:"dpr"`
	Mobile   bool    `json:"mobile"`
	Dark     bool    `json:"dark"`
	FontSize int     `json:"fontSize"`
}

// screenshotArgs are /screenshot's.
type screenshotArgs struct {
	tabArgs
	Name string `json:"name"`
}

// check refuses a name off the pattern (R186), and one taken, so that no
// screenshot is lost.
func (a screenshotArgs) check(s *control) error {
	if !screenshotName.MatchString(a.Name) {
		return refuse(http.StatusBadRequest, "harness: a screenshot's name is 1 to 40 lowercase letters, digits and dashes (R186), not %q", a.Name)
	}
	if _, err := os.Lstat(s.shotPath(a.Name)); !errors.Is(err, os.ErrNotExist) {
		return refuse(http.StatusConflict, "harness: screenshots/%s.png is taken: name this one otherwise, so that neither is lost", a.Name)
	}
	return nil
}

func (s *control) shotPath(name string) string {
	return filepath.Join(s.c.runDir, "screenshots", name+".png")
}

// openArgs are /tab's: the tab's name, the URL it opens, about:blank when
// none, and whether it has a window of its own, as it does unless window is
// false: a tab with none lands in the last active window, and hides the tab
// there.
type openArgs struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	Window *bool  `json:"window"`
}

func (s *control) openTab(r *http.Request) (any, error) {
	var a openArgs
	if err := decodeArgs(r, &a); err != nil {
		return nil, err
	}
	if !tabName.MatchString(a.Name) {
		return nil, refuse(http.StatusBadRequest, "harness: %q is not a tab's name: a lowercase letter or digit, then up to 31 more or dashes", a.Name)
	}
	target := "about:blank"
	if a.URL != "" {
		u, err := s.allowed(a.URL)
		if err != nil {
			return nil, err
		}
		target = u
	}
	window := a.Window == nil || *a.Window
	if err := s.reserve(a.Name); err != nil {
		return nil, err
	}
	began := time.Now()
	b, err := s.c.Tab(r.Context(), a.Name, window)
	s.opened(a.Name, b)
	if err != nil {
		return nil, err
	}
	if target != "about:blank" {
		err = b.Navigate(r.Context(), target)
	}
	recordCommand([]*Tab{b}, "tab", map[string]any{"name": a.Name, "url": target, "window": window}, began, err)
	if err != nil {
		return nil, fmt.Errorf("harness: tab %s is open, but its navigation failed: %w", a.Name, err)
	}
	return map[string]any{"tab": b.name, "window": b.window, "location": locationOf(b)}, nil
}

func (*control) closeTab(ctx context.Context, b *Tab, _ tabArgs) (any, error) {
	if err := b.Close(ctx); err != nil {
		return nil, err
	}
	return map[string]any{"tab": b.name, "closed": true}, nil
}

func (s *control) navigate(ctx context.Context, b *Tab, a navigateArgs) (any, error) {
	u, err := s.allowed(a.URL)
	if err != nil {
		return nil, err
	}
	if err := b.Navigate(ctx, u); err != nil {
		return nil, err
	}
	return located(b), nil
}

func (*control) back(ctx context.Context, b *Tab, _ tabArgs) (any, error) {
	if err := b.Back(ctx); err != nil {
		return nil, err
	}
	return located(b), nil
}

func (*control) reload(ctx context.Context, b *Tab, _ tabArgs) (any, error) {
	if err := b.Reload(ctx); err != nil {
		return nil, err
	}
	return located(b), nil
}

func (*control) eval(ctx context.Context, b *Tab, a evalArgs) (any, error) {
	var v json.RawMessage
	if err := b.Eval(ctx, a.Expr, &v); err != nil {
		return nil, err
	}
	return map[string]any{"tab": b.name, "value": v}, nil
}

// key presses the key and answers with the page's visibility as it reads
// straight after: a key that hid the page, as one with a native key code
// does in headless Chrome 157, has hidden it by the time Key returns.
func (*control) key(ctx context.Context, b *Tab, a keyArgs) (any, error) {
	if err := b.Key(ctx, a.Key); err != nil {
		return nil, err
	}
	var v string
	if err := b.Eval(ctx, "document.visibilityState", &v); err != nil {
		return nil, fmt.Errorf("harness: the key %s was pressed, but its page's visibility could not be read: %w", a.Key, err)
	}
	return map[string]any{"tab": b.name, "key": a.Key, "visibility": v}, nil
}

func (*control) typeText(ctx context.Context, b *Tab, a typeArgs) (any, error) {
	if err := b.Type(ctx, a.Text); err != nil {
		return nil, err
	}
	return a, nil
}

func (*control) click(ctx context.Context, b *Tab, a clickArgs) (any, error) {
	if err := b.Click(ctx, a.Selector); err != nil {
		return nil, err
	}
	return a, nil
}

func (*control) emulate(ctx context.Context, b *Tab, a emulateArgs) (any, error) {
	e := Emulation{Width: a.Width, Height: a.Height, DPR: a.DPR, Mobile: a.Mobile, Dark: a.Dark, FontSize: a.FontSize}
	if err := b.Emulate(ctx, e); err != nil {
		return nil, err
	}
	return a, nil
}

func (*control) hide(ctx context.Context, b *Tab, _ tabArgs) (any, error) {
	if err := b.Hide(ctx); err != nil {
		return nil, err
	}
	return map[string]any{"tab": b.name, "visibility": "hidden"}, nil
}

func (*control) show(ctx context.Context, b *Tab, _ tabArgs) (any, error) {
	if err := b.Show(ctx); err != nil {
		return nil, err
	}
	return map[string]any{"tab": b.name, "visibility": "visible"}, nil
}

func (s *control) screenshot(ctx context.Context, b *Tab, a screenshotArgs) (any, error) {
	path := s.shotPath(a.Name)
	if err := b.Screenshot(ctx, path); err != nil {
		return nil, err
	}
	return map[string]any{"tab": b.name, "path": path}, nil
}

func (*control) replay(ctx context.Context, b *Tab, _ tabArgs) (any, error) {
	r, err := b.ReplayOlder(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"tab": b.name, "replayed": r.Replayed, "page": r.PageOrder, "differing": r.Differing}, nil
}

// clearCookies clears the browser's cookies, which every open tab records.
func (s *control) clearCookies(r *http.Request) (any, error) {
	if err := decodeArgs(r, &struct{}{}); err != nil {
		return nil, err
	}
	began := time.Now()
	err := s.c.ClearCookies(r.Context())
	recordCommand(s.openTabs(), "cookies/clear", nil, began, err)
	if err != nil {
		return nil, err
	}
	return map[string]bool{"cleared": true}, nil
}

// endRun calls End, once, which every open tab records first.
func (s *control) endRun(r *http.Request) (any, error) {
	if err := decodeArgs(r, &struct{}{}); err != nil {
		return nil, err
	}
	s.mu.Lock()
	first := !s.ending
	s.ending = true
	s.mu.Unlock()
	if first {
		recordCommand(s.openTabs(), "end", nil, time.Now(), nil)
		s.end()
	}
	return map[string]bool{"ending": true}, nil
}

// events answers a tab's events after the seq, of the kinds when any are
// given, up to last, the tab's latest seq as the read began: a read after
// last next misses none. It refuses a kind no event has, as a misspelt one
// would read as none of it.
func (s *control) events(r *http.Request) (any, error) {
	q, err := queryOf(r, "tab", "after", "kind")
	if err != nil {
		return nil, err
	}
	after, err := afterOf(q)
	if err != nil {
		return nil, err
	}
	var kinds []string
	if k := q.Get("kind"); k != "" {
		kinds = strings.Split(k, ",")
	}
	for _, k := range kinds {
		if !slices.Contains(eventKinds, k) {
			return nil, refuse(http.StatusBadRequest, "harness: no event is of the kind %q: the kinds are %s", k, listOr(eventKinds))
		}
	}
	b, err := s.tabNamed(q.Get("tab"))
	if err != nil {
		return nil, err
	}
	last := tabSeq(b)
	events := upTo(b.Events(after, kinds...), last)
	if events == nil {
		events = []Event{}
	}
	return map[string]any{"tab": b.name, "last": last, "events": events}, nil
}

// upTo is events, in order, but those after the seq.
func upTo(events []Event, last int) []Event {
	if i := slices.IndexFunc(events, func(e Event) bool { return e.Seq > last }); i >= 0 {
		return events[:i]
	}
	return events
}

// polls answers each of a tab's polls that changed after the seq, whole:
// its request, its answer, its body, or what the recorder saw follow its
// answer; every poll when no seq is given. It reads the tab's events up to
// last, its latest seq as the read began, which it answers too: a read
// after last next answers every poll that changed since, as one that was
// unanswered then and has its answer now.
func (s *control) polls(r *http.Request) (any, error) {
	q, err := queryOf(r, "tab", "after")
	if err != nil {
		return nil, err
	}
	after, err := afterOf(q)
	if err != nil {
		return nil, err
	}
	b, err := s.tabNamed(q.Get("tab"))
	if err != nil {
		return nil, err
	}
	last := tabSeq(b)
	rows := pollRows(upTo(b.Events(0, "request", "response", "body", "record"), last))
	out := make([]*pollRow, 0, len(rows))
	for _, p := range rows {
		if p.touched > after {
			out = append(out, p)
		}
	}
	return map[string]any{"tab": b.name, "last": last, "polls": out}, nil
}

// netlog answers R185's check on the NetLog as it stands: provisional while
// Chrome runs, as the log lags then.
func (s *control) netlog(r *http.Request) (any, error) {
	if _, err := queryOf(r); err != nil {
		return nil, err
	}
	nl, err := readNetLog(filepath.Join(s.c.runDir, "netlog.json"), s.c.allowed)
	if err != nil {
		return nil, err
	}
	findings, connects := nl.verdict()
	if findings == nil {
		findings = []string{}
	}
	answer := map[string]any{"provisional": !nl.complete, "findings": findings, "connects": connects}
	if !nl.complete {
		answer["note"] = "read while Chrome runs, and the NetLog lags: R185's check on what it holds so far; the run's own check reads it whole after Close"
	}
	return answer, nil
}

func (s *control) worldNow(r *http.Request) (any, error) {
	if _, err := queryOf(r); err != nil {
		return nil, err
	}
	return s.world(), nil
}

// tabState is a tab as /status shows it.
type tabState struct {
	Name     string `json:"name"`
	Open     bool   `json:"open"`
	Window   int    `json:"window"`
	Location string `json:"location"`
	Last     int    `json:"last"` // the seq of its latest event
}

func (s *control) status(r *http.Request) (any, error) {
	if _, err := queryOf(r); err != nil {
		return nil, err
	}
	s.mu.Lock()
	tabs := make([]*Tab, 0, len(s.order))
	for _, name := range s.order {
		if b := s.tabs[name]; b != nil {
			tabs = append(tabs, b)
		}
	}
	s.mu.Unlock()
	states := make([]tabState, 0, len(tabs))
	for _, b := range tabs {
		states = append(states, tabState{Name: b.name, Open: !b.isClosed(), Window: b.window, Location: locationOf(b), Last: tabSeq(b)})
	}
	return map[string]any{"version": s.c.Version(), "browser": s.c.browserState(), "runDir": s.c.runDir, "socket": s.path, "tabs": states}, nil
}
