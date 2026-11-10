package pages

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"html/template"
	"strings"
)

// regionNames, in the page's order, are what live.js replaces (Task 8): the
// layout's kill switch header and standby notice, then the board's header,
// round summary, incident strip, hypotheses and feed. Each is its own named
// template, "region-<name>", which the page, and Task 8's regions document,
// execute; each exists on every board page, in every state and in the hidden
// copy, empty when it has nothing to show (R170).
var regionNames = []string{"switch", "standby", "board-header", "summary", "strip", "hypotheses", "feed"}

// The region models (R156; plan review M3) are plain data, every word
// rendered already and every time formatted to the minute, so that no
// template reads a clock and a region's content changes only as its words
// do. V is the region's version, set once its content is rendered (see
// versioned); a region rendered with none carries no data-v.

// headerRegion is the board's header (design D3, D5; the words of the
// board): a later build's item's refusal alone; or a hidden item's state
// word and copy alone (D13); or the kill switch's banner (row 14), the item,
// its type and its title (R168), its state in words and the state's rows
// (rows 1 to 9), a queued item's promote form, the conclusion, the budget
// used, the suggestions counted, the alert and each instance, the item a
// flap's opening followed, the round's notices, and when it was last
// updated. The state is a line, a state this build has no words for shown as
// itself (see line.code). Stale is the indicator's words while the polls
// fail, its data-stale (R155).
type headerRegion struct {
	V, Stale    string
	Refusal     string
	Banner      string
	Item        line
	State       line
	Hidden      string
	Lines       []line
	Promote     string
	Conclusion  *conclusionView
	Budget      string
	Suggestions string
	Alert       string
	Instances   []line
	Flap        line
	Notices     string
	Updated     string
}

// conclusionView is the header's conclusion: its key and standing, and its
// statement, model-written.
type conclusionView struct {
	Line, Statement line
}

// summaryRegion is the round summary (R177): Round, its data-round, the
// latest ended round and the run's stops so far, "<n>.<k>" (see summaryOf),
// and its sentence; empty, with no round, before any round ended.
type summaryRegion struct {
	V, Round string
	Line     line
}

// stripRegion is the incident's other items (design D7): that they could
// not be listed; or each item's line, and how many more there are.
type stripRegion struct {
	V      string
	Unread bool
	Items  []line
	More   string
}

// hypothesesRegion is the board's hypotheses (design D3, D4, D6): the ranked
// ones' cards, then those ruled out.
type hypothesesRegion struct {
	V                string
	Ranked, RuledOut []cardView
}

// cardView is a hypothesis's card: its status word, then its key, score,
// counts and sources, and not converged when the run's stop marks it; its
// statement, model-written; its quotes on each side and how many more; and
// its suggestions, by target.
type cardView struct {
	Status                    string
	Line, Statement           line
	Supports, Refutes         []line
	MoreSupports, MoreRefutes string
	Groups                    []groupView
}

// groupView is a hypothesis's suggestions of one target (design D6).
type groupView struct {
	Target      line
	Suggestions []suggestionView
}

// suggestionView is a suggestion (design D6): its summary, its facts, its
// hypothesis's status, its proposer and the proposer's own evidence, and the
// superseded and service marks, each empty when it does not apply.
type suggestionView struct {
	Summary, Facts, Status, Proposed line
	Superseded, Service              string
}

// feedRegion is the board's feed (design D8; R167): its lines, oldest
// first, the newest of them unless every line was asked (see feedOf); and
// Earlier, the path of the whole feed, ?feed=all, when the region leaves
// lines out. Its lines' times are formatted already, to the second, so that
// its template reads no clock.
type feedRegion struct {
	V       string
	Lines   []feedLine
	Earlier string
}

// standbyRegion is the layout's standby notice: the notice, nil on a leader.
type standbyRegion struct {
	V    string
	View *standbyView
}

// regionVersion is R156's data-v: the first 16 hex characters of SHA-256
// over the region's name, a zero byte, and its content as rendered, its own
// named template executed over its model; the name keeps two regions'
// versions apart though their contents be the same.
func regionVersion(name string, content []byte) string {
	b := make([]byte, 0, len(name)+1+len(content))
	b = append(append(append(b, name...), 0), content...)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// boardETag is R156's ETag: `"`, the first 32 hex characters of SHA-256 over
// the regions' versions in regionNames' order, and `"`.
func boardETag(versions []string) string {
	sum := sha256.Sum256([]byte(strings.Join(versions, "")))
	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

// versioned gives each region of page, a board page framed by l, its
// version, the first render of its own template of t over its model with
// none (regionVersion), and page its ETag over them (boardETag), so that the
// page then renders each region with the version of its content (R156).
func versioned(t *template.Template, l *layout, page *boardPage) error {
	l.Switch.V, l.StandbyV = "", ""
	page.Header.V, page.Summary.V, page.Strip.V, page.Hypotheses.V, page.Feed.V = "", "", "", "", ""
	regions := []struct {
		model any
		v     *string
	}{
		{l.Switch, &l.Switch.V}, {l.StandbyRegion(), &l.StandbyV}, {page.Header, &page.Header.V}, {page.Summary, &page.Summary.V},
		{page.Strip, &page.Strip.V}, {page.Hypotheses, &page.Hypotheses.V}, {page.Feed, &page.Feed.V},
	}
	versions := make([]string, len(regionNames))
	for i, name := range regionNames {
		var b bytes.Buffer
		if err := t.ExecuteTemplate(&b, "region-"+name, regions[i].model); err != nil {
			return err
		}
		versions[i] = regionVersion(name, b.Bytes())
		*regions[i].v = versions[i]
	}
	page.ETag = boardETag(versions)
	return nil
}
