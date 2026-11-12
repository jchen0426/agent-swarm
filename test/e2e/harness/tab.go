//go:build e2e

package harness

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// cleanupWait bounds a best-effort cleanup's call, which a context of its
// own carries (cleanup).
const cleanupWait = 5 * time.Second

// cleanup is the context of a best-effort cleanup: ctx's values, none of its
// cancellation, and a bound of its own, so that a request cancelled, by a
// client that left or by the socket's stop, still sends it.
func cleanup(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), cleanupWait)
}

// The bounds of a tab's own waits.
const (
	// waitBound bounds any wait of a tab's whose context has no earlier
	// deadline: a load, a restore, the gate world, a poll to replay.
	waitBound = time.Minute
	// visibilityWait bounds Hide's and Show's wait for the page to read
	// hidden or visible.
	visibilityWait = 10 * time.Second
	// clearWait bounds Emulate's wait, once it cleared an emulated size, for
	// the page to read another size.
	clearWait = time.Second
)

// pageSize reads, in the gate world, the page's viewport: its width and
// height.
const pageSize = "[innerWidth, innerHeight]"

// defaultFontSize is the browser's own standard font size, px, which the
// zero Emulation restores, as Page.setFontSizes has no clear.
const defaultFontSize = 16

// ErrCertificate is a navigation refused for its certificate (R180).
var ErrCertificate = errors.New("harness: a navigation was refused for its certificate")

// tabName is what a tab's name may be: it names its bodies' files.
var tabName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

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
		return nil, errors.Join(err, c.closeTarget(ctx, created.TargetID))
	}
	b := newTab(c, name, created.TargetID, attached.SessionID)
	c.conn.subscribe(b.session, b.handle)
	if err := b.setup(ctx); err != nil {
		return nil, errors.Join(err, c.closeTarget(ctx, b.target))
	}
	return b, nil
}

// closeTarget closes a page target, under a context of its own (cleanup), so
// that a request cancelled meanwhile leaves no window open behind it.
func (c *Chrome) closeTarget(ctx context.Context, target string) error {
	ctx, cancel := cleanup(ctx)
	defer cancel()
	return c.conn.call(ctx, "", "Target.closeTarget", map[string]string{"targetId": target}, nil)
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
	// changed is closed, and replaced, at each change of the state below;
	// done is closed once the tab has closed.
	changed chan struct{}
	done    chan struct{}
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
	// sized is whether Emulate set a size that no clear has cleared since.
	sized bool
}

// tabContext is a live execution context: its unique id, and its world.
type tabContext struct {
	unique, world string
}

func newTab(c *Chrome, name, target, session string) *Tab {
	return &Tab{
		c: c, name: name, target: target, session: session,
		changed: make(chan struct{}), done: make(chan struct{}), frame: target, contexts: map[int]tabContext{},
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
		close(b.done)
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
// the text its keyDown types, for the keys that press a button only so, and
// its side of the keyboard, for Shift.
type keyDef struct {
	key, code, text string
	vk, location    int
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

// leftShift is the left Shift key, which Shift+Tab holds over its Tab.
var leftShift = keyDef{key: "Shift", code: "ShiftLeft", vk: 16, location: 1}

// shiftModifier is Input.dispatchKeyEvent's Shift.
const shiftModifier = 8

// Key presses a key: Tab, Shift+Tab, Enter (a keyDown with text), Space,
// Escape or an arrow, by its DOM name. Shift goes with Tab alone. Shift+Tab
// is a Shift key held over a Tab, as a keyboard sends it: the Shift's
// keyDown and the Tab's events carry the Shift modifier, and the Shift's
// keyUp does not.
//
// Its events carry no native key code (nativeVirtualKeyCode), as
// Puppeteer's and Playwright's do not. In headless Chrome 157 a key event
// the page does not consume hides the page, its window not minimised, when
// the event carries a native key code, whatever its value: Escape, an arrow
// or a Shift key of its own did, as the final review found, while a Tab,
// which focus navigation consumes, did not. Without one, none of these keys
// hides the page.
func (b *Tab) Key(ctx context.Context, key string) error {
	name, shift := strings.CutPrefix(key, "Shift+")
	k, ok := keyDefs[name]
	if !ok || (shift && name != "Tab") {
		return fmt.Errorf("harness: Key presses Tab, Shift+Tab, Enter, Space, Escape and the arrows, not %q", key)
	}
	press := func(ctx context.Context, typ string, k keyDef, mods int) error {
		ev := map[string]any{"type": typ, "key": k.key, "code": k.code, "windowsVirtualKeyCode": k.vk, "modifiers": mods}
		if typ == "keyDown" {
			ev["text"], ev["unmodifiedText"] = k.text, k.text
		}
		if k.location != 0 {
			ev["location"] = k.location
		}
		return b.call(ctx, "Input.dispatchKeyEvent", ev, nil)
	}
	mods := 0
	if shift {
		mods = shiftModifier
		if err := press(ctx, "rawKeyDown", leftShift, mods); err != nil {
			return err
		}
	}
	down := "rawKeyDown"
	if k.text != "" {
		down = "keyDown"
	}
	err := press(ctx, down, k, mods)
	if err == nil {
		err = press(ctx, "keyUp", k, mods)
	}
	if shift {
		// The Shift is let go even when the Tab failed or the request was
		// cancelled, so that the page never finds it held.
		up, cancel := cleanup(ctx)
		defer cancel()
		err = errors.Join(err, press(up, "keyUp", leftShift, 0))
	}
	return err
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
		ctx, cancel := cleanup(ctx)
		defer cancel()
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
// font size, which the zero value returns to 16 px, the browser's own. A
// size e sets, the page reads as Emulate returns, as Chrome applies it
// before it answers; nothing waits for it. Only a clear of an emulated size,
// e with none, waits: up to clearWait, for the page to read another size
// than it read before the clear (clearMetrics). A clear that runs out its
// second returns with no error, and with whatever size the page reads then.
func (b *Tab) Emulate(ctx context.Context, e Emulation) error {
	if e.Width < 0 || e.Height < 0 || e.DPR < 0 || e.FontSize < 0 {
		return fmt.Errorf("harness: an emulation of negative size: %+v", e)
	}
	var err error
	if e == (Emulation{Dark: e.Dark, FontSize: e.FontSize}) {
		err = b.clearMetrics(ctx)
	} else {
		err = b.call(ctx, "Emulation.setDeviceMetricsOverride", map[string]any{
			"width": e.Width, "height": e.Height, "deviceScaleFactor": e.DPR, "mobile": e.Mobile,
		}, nil)
		if err == nil && (e.Width > 0 || e.Height > 0) {
			b.mu.Lock()
			b.sized = true
			b.mu.Unlock()
		}
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

// clearMetrics clears the device's emulated metrics. Chrome can answer the
// clear before the page has its window's own size back, as on a board, some
// 20 ms before, and a read straight after it then finds the size emulated:
// so a clear of an emulated size waits, up to clearWait, for the page to
// read another size than it read before the clear, which it never does when
// the window's own size is the one emulated. A page whose size cannot be
// read is not waited for.
func (b *Tab) clearMetrics(ctx context.Context) error {
	b.mu.Lock()
	sized := b.sized
	b.mu.Unlock()
	wait, cancel := context.WithTimeout(ctx, clearWait)
	defer cancel()
	var before [2]float64
	if sized && b.Eval(wait, pageSize, &before) != nil {
		sized = false
	}
	if err := b.call(ctx, "Emulation.clearDeviceMetricsOverride", nil, nil); err != nil {
		return err
	}
	b.mu.Lock()
	b.sized = false
	b.mu.Unlock()
	for sized {
		var now [2]float64
		if b.Eval(wait, pageSize, &now) != nil || now != before {
			break
		}
		select {
		case <-wait.Done():
			sized = false
		case <-time.After(processPoll):
		}
	}
	return context.Cause(ctx)
}

// Hide minimises the tab's window, which hides its page for real, and waits
// for the page to read hidden.
func (b *Tab) Hide(ctx context.Context) error {
	if err := b.windowState(ctx, "minimized"); err != nil {
		return err
	}
	return b.awaitVisibility(ctx, "hidden", "its window was minimised")
}

// Show restores the tab's window and brings its page to the front, as a
// person clicking its window would, then waits for the page to read
// visible. A page can be hidden with its window not minimised: in headless
// Chrome 157 a key event the page does not consume hides it when the event
// carries a native key code, as the walks' Shift key did (Key's events carry
// none), and restoring its window does not bring it back; bringing it to
// the front does, in some 20 ms, as the second walk measured. A tab that
// shares its window with another becomes the one that window shows.
func (b *Tab) Show(ctx context.Context) error {
	if err := b.windowState(ctx, "normal"); err != nil {
		return err
	}
	if err := b.call(ctx, "Page.bringToFront", nil, nil); err != nil {
		return err
	}
	return b.awaitVisibility(ctx, "visible", "its window was restored and its page brought to the front")
}

// windowState sets the tab's window's state: minimized or normal.
func (b *Tab) windowState(ctx context.Context, state string) error {
	return b.c.conn.call(ctx, "", "Browser.setWindowBounds", map[string]any{"windowId": b.window, "bounds": map[string]string{"windowState": state}}, nil)
}

// awaitVisibility waits, up to visibilityWait, for the page to read want.
// It checks its context before each read, and so reads nothing once its
// context is done; a read its deadline cut, in the instant before the
// context reads done, writes nothing (cdp's write) and fails with the
// deadline's error, which ends the wait too. Either way it answers with its
// own timeout, what the page last read, never the cut read's error.
func (b *Tab) awaitVisibility(ctx context.Context, want, after string) error {
	ctx, cancel := context.WithTimeout(ctx, visibilityWait)
	defer cancel()
	got := "nothing"
	timedOut := func() error {
		cause := context.Cause(ctx)
		if cause == nil {
			cause = context.DeadlineExceeded
		}
		return fmt.Errorf("harness: tab %s still reads %s, not %s, though %s: %w", b.name, got, want, after, cause)
	}
	for {
		if ctx.Err() != nil {
			return timedOut()
		}
		if err := b.Eval(ctx, "document.visibilityState", &got); err != nil {
			if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
				return timedOut()
			}
			return err
		}
		if got == want {
			return nil
		}
		select {
		case <-ctx.Done():
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

// Close closes the tab's page, even for a request cancelled meanwhile, as
// its window would stay open, and poll, otherwise.
func (b *Tab) Close(ctx context.Context) error {
	b.mu.Lock()
	closed := b.closed
	b.mu.Unlock()
	if closed {
		return nil
	}
	err := b.c.closeTarget(ctx, b.target)
	b.markClosed()
	b.c.mu.Lock()
	if b.c.tabs[b.name] == b {
		delete(b.c.tabs, b.name)
	}
	b.c.mu.Unlock()
	return err
}
