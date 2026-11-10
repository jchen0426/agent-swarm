package pages

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"strings"
)

// liveScript is live.js, the board page's one script (design D1), and
// liveName its name as served, live.<the first 12 hex of its SHA-256>.js,
// which changes with every change of it, so that a deploy never leaves a
// browser running a stale one (review M8); livePath is its path.
var (
	liveScript, liveName = script()
	livePath             = "/static/" + liveName
)

// script is the embedded live.js, and its name as served.
func script() (content []byte, served string) {
	content, err := fs.ReadFile(files, "static/live.js")
	if err != nil {
		panic("pages: the embedded live.js cannot be read: " + err.Error())
	}
	sum := sha256.Sum256(content)
	return content, "live." + hex.EncodeToString(sum[:6]) + ".js"
}

// liveJS serves live.js at its name alone, GET /static/live.<hash>.js, as
// the stylesheet is served: outside SignedIn, so that loading it needs no
// session and an expired one never starts a sign-in through it (plan review
// M10(i); review I1.4); kept a year, as immutable, since its name changes
// with it. Any other name under /static/ answers 404.
func liveJS(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("file") != liveName {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	_, _ = w.Write(liveScript)
}

// regionsDoc is a board's regions document (design D1), which its poll
// answers: the layout's two regions and the page's five, from the page's own
// templates over the same models, in a <div id="board"> of their own with
// the page's ETag and, for a visible item, its order.
type regionsDoc struct {
	Switch  switchHeader
	Standby standbyRegion
	Page    *boardPage
}

// boardRegions answers a board's poll, GET /items/{id}/board (design D1;
// R156, R173): its read, its failures and its seam as the board page's (see
// shown), each region rendered to hash it; then, to an If-None-Match that
// holds the regions' ETag, compared weakly, 304 with no body, which writes
// no region; otherwise 200 with the regions document. Either carries the
// ETag and, for a visible item, X-Board-Order, the order of what it shows
// (re-review N3), which live.js reads from the document itself, so that a
// proxy that weakens or drops a header changes nothing. A hidden item's
// carries no order, and an ETag of its hidden regions alone (design D13).
func (s *site) boardRegions(w http.ResponseWriter, r *http.Request) {
	l, page, ok := s.shown(w, r)
	if !ok {
		return
	}
	if matchesWeakly(r.Header.Values("If-None-Match"), page.ETag) {
		ordered(w.Header(), page)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	var b bytes.Buffer
	if err := templates["board"].ExecuteTemplate(&b, "regions", &regionsDoc{Switch: l.Switch, Standby: l.StandbyRegion(), Page: page}); err != nil {
		s.unrendered(w, r, "regions", err)
		return
	}
	ordered(w.Header(), page)
	written(w, http.StatusOK, &b)
}

// ordered sets h's ETag, page's, and, for a visible item, its X-Board-Order.
func ordered(h http.Header, page *boardPage) {
	h.Set("ETag", page.ETag)
	if page.Order != "" {
		h.Set("X-Board-Order", page.Order)
	}
}

// matchesWeakly says whether the If-None-Match field values hold etag under
// the weak comparison (R173; RFC 9110 §8.8.3.2, §13.1.2): a list of entity
// tags over every line, empty elements allowed, each compared by its opaque
// tag, W/ ignored. Any other value matches nothing: *, which no poll sends,
// and a list that does not parse, wholly, so that a malformed header is
// answered in full, never with a 304.
func matchesWeakly(values []string, etag string) bool {
	found := false
	for _, v := range values {
		for rest := v; ; {
			rest = strings.TrimLeft(rest, " \t")
			if rest == "" {
				break
			}
			if rest[0] == ',' {
				rest = rest[1:]
				continue
			}
			tag, after, ok := entityTag(strings.TrimPrefix(rest, "W/"))
			if !ok {
				return false
			}
			found = found || tag == etag
			if rest = strings.TrimLeft(after, " \t"); rest != "" && rest[0] != ',' {
				return false
			}
		}
	}
	return found
}

// entityTag reads the opaque tag that s begins with, a quoted string of
// etagc (RFC 9110 §8.8.3: %x21, %x23-7E and obs-text), and gives it with its
// quotes, and what follows it; false when s does not begin with one.
func entityTag(s string) (tag, rest string, ok bool) {
	if !strings.HasPrefix(s, `"`) {
		return "", "", false
	}
	for i := 1; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"':
			return s[:i+1], s[i+1:], true
		case c < 0x21 || c == 0x7f:
			return "", "", false
		}
	}
	return "", "", false
}
