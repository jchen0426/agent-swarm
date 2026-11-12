//go:build e2e

package harness

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

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

// tabRequest is a request in flight.
type tabRequest struct {
	url      string
	poll     bool
	replayed bool
	status   int
	headers  map[string]any
}

// Event is one piece of evidence (R184), numbered per tab.
type Event struct {
	Seq    int             `json:"seq"`
	At     time.Time       `json:"at"`
	Kind   string          `json:"kind"` // one of eventKinds
	Detail json.RawMessage `json:"detail"`
}

// eventKinds are the kinds of Event the harness records.
var eventKinds = []string{
	"record", "request", "response", "body", "log", "exception", "console", "issue",
	"bfcache", "navigated", "certificate", "replay", "control",
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
