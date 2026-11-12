//go:build e2e

package harness

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// regionIDs are the board's regions, as live.js's names and
// internal/web/pages' regionNames list them; TestReplayOlderIsDroppedByLiveJS
// holds them to live.js's own.
var regionIDs = []string{"switch", "standby", "board-header", "summary", "strip", "hypotheses", "feed"}

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

// isPoll says whether a request is the board's poll: an XHR GET of <the
// page's path>/board, as live.js polls by XMLHttpRequest. It is XHR alone,
// as the Fetch interception that ReplayOlder arms matches, so that a
// request kept as a poll is one a replay can answer.
func isPoll(resourceType, method, rawURL string) bool {
	if resourceType != "XHR" || method != http.MethodGet {
		return false
	}
	u, err := url.Parse(rawURL)
	return err == nil && strings.HasSuffix(u.Path, "/board")
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
// order, the page's order, and the regions that differ. It ends at once,
// with an error, when the tab or the pipe closes before a poll comes.
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
	// XHR alone, as isPoll keeps polls: live.js polls by XMLHttpRequest.
	if err := b.call(ctx, "Fetch.enable", map[string]any{"patterns": []map[string]string{
		{"urlPattern": "*/board*", "resourceType": "XHR", "requestStage": "Request"},
	}}, nil); err != nil {
		b.disarm(armed)
		return Replay{}, err
	}
	var why error
	select {
	case d := <-armed.done:
		return d.replay, d.err
	case <-ctx.Done():
		why = fmt.Errorf("no poll came to replay: %w", context.Cause(ctx))
	case <-b.done:
		why = errors.New("the tab closed")
	case <-b.c.conn.done:
		why = fmt.Errorf("the browser's pipe closed: %w", b.c.conn.closedErr())
	}
	if !b.disarm(armed) {
		// A poll took the replay just now; its calls end at once if the tab
		// or the pipe has gone.
		d := <-armed.done
		return d.replay, d.err
	}
	err = fmt.Errorf("harness: tab %s: ReplayOlder ended before a poll: %w", b.name, why)
	if ctx.Err() == nil {
		return Replay{}, err
	}
	dctx, dcancel := context.WithTimeout(context.Background(), cdpCallTimeout)
	defer dcancel()
	return Replay{}, errors.Join(err, b.call(dctx, "Fetch.disable", nil, nil))
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
