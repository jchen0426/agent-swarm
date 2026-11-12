//go:build e2e

package harness

import (
	"encoding/json"
	"slices"
	"time"
)

// pollRow is one of a tab's polls, as /polls answers it (R186): when it was
// sent and the gap since its document's last poll's answer; its answer; and
// what the recorder saw that answer change, until the next answer came.
type pollRow struct {
	Seq       int        `json:"seq"` // its request's event
	RequestID string     `json:"requestId"`
	At        time.Time  `json:"at"`
	URL       string     `json:"url"`
	First     bool       `json:"first,omitempty"`    // the first poll of its document, which has no gap
	Gap       *float64   `json:"gap,omitempty"`      // seconds since its document's last poll's answer
	Answered  *time.Time `json:"answered,omitempty"` // when its answer, or its failure, came
	Status    int        `json:"status,omitempty"`
	Failed    string     `json:"failed,omitempty"` // Chrome's error, for a poll that got no answer
	Replayed  bool       `json:"replayed,omitempty"`
	Order     string     `json:"order,omitempty"` // a 200's data-order
	Round     string     `json:"round,omitempty"` // a 200's #summary data-round
	Changed   []string   `json:"changed"`         // the regions the recorder saw change after its answer
	Announced int        `json:"announced"`       // the writes to #announce the recorder counted then
	// touched is the seq of the latest event that changed the poll: its
	// request, its answer, its body, or a record that followed its answer.
	touched int
}

// pollReader reads a tab's polls from its events, in order. What the
// recorder sees change is the latest answer's, until another answer comes
// or a new document starts. A new document's first poll has no gap, as the
// time since the last document's answer spans the navigation between.
type pollReader struct {
	rows   []*pollRow
	byID   map[string]*pollRow
	latest *pollRow
	answer time.Time
	fresh  bool
	// writes is the document's writes to #announce as its recorder last
	// counted them, which it counts from 0 in each new document.
	writes int
}

func pollRows(events []Event) []*pollRow {
	pr := &pollReader{byID: map[string]*pollRow{}}
	for i := range events {
		e := &events[i]
		switch e.Kind {
		case "request":
			pr.request(e)
		case "response":
			pr.response(e)
		case "body":
			pr.body(e)
		case "record":
			pr.record(e)
		}
	}
	return pr.rows
}

func (pr *pollReader) request(e *Event) {
	var d struct {
		RequestID string `json:"requestId"`
		URL       string `json:"url"`
		Poll      bool   `json:"poll"`
	}
	if json.Unmarshal(e.Detail, &d) != nil || !d.Poll {
		return
	}
	p := &pollRow{Seq: e.Seq, RequestID: d.RequestID, At: e.At, URL: d.URL, First: pr.fresh, Changed: []string{}, touched: e.Seq}
	pr.fresh = false
	if !pr.answer.IsZero() {
		gap := e.At.Sub(pr.answer).Seconds()
		p.Gap = &gap
	}
	pr.rows = append(pr.rows, p)
	pr.byID[d.RequestID] = p
}

// response is a poll's answer, or its failure; a redirect is neither.
func (pr *pollReader) response(e *Event) {
	var d struct {
		RequestID string `json:"requestId"`
		Status    int    `json:"status"`
		Failed    string `json:"failed"`
		Redirect  bool   `json:"redirect"`
		Replayed  bool   `json:"replayed"`
	}
	if json.Unmarshal(e.Detail, &d) != nil || d.Redirect {
		return
	}
	p := pr.byID[d.RequestID]
	if p == nil || p.Answered != nil {
		return
	}
	at := e.At
	p.Answered, p.Status, p.Failed, p.Replayed, p.touched = &at, d.Status, d.Failed, d.Replayed, e.Seq
	pr.answer, pr.latest = at, p
}

func (pr *pollReader) body(e *Event) {
	var d struct {
		RequestID string `json:"requestId"`
		Order     string `json:"order"`
		Round     string `json:"round"`
		Replayed  bool   `json:"replayed"`
	}
	if json.Unmarshal(e.Detail, &d) != nil {
		return
	}
	if p := pr.byID[d.RequestID]; p != nil {
		p.Order, p.Round, p.Replayed, p.touched = d.Order, d.Round, p.Replayed || d.Replayed, e.Seq
	}
}

func (pr *pollReader) record(e *Event) {
	var r pageRecord
	if json.Unmarshal(e.Detail, &r) != nil {
		return
	}
	switch r.Kind {
	case "start":
		pr.latest, pr.fresh, pr.answer, pr.writes = nil, true, time.Time{}, 0
	case "region":
		var d struct {
			Region string `json:"region"`
		}
		if pr.latest != nil && json.Unmarshal(r.Detail, &d) == nil {
			if !slices.Contains(pr.latest.Changed, d.Region) {
				pr.latest.Changed = append(pr.latest.Changed, d.Region)
			}
			pr.latest.touched = e.Seq
		}
	case "announce":
		if pr.announce(r.Detail) && pr.latest != nil {
			pr.latest.touched = e.Seq
		}
	}
}

// announce counts the writes to #announce a record adds, to the latest
// answer, and says whether it read one: the recorder sends one record a
// MutationObserver callback, whose writes is the document's count so far,
// so that two writes in one task are one record that adds 2.
func (pr *pollReader) announce(raw json.RawMessage) bool {
	var d struct {
		Writes int `json:"writes"`
	}
	if json.Unmarshal(raw, &d) != nil {
		return false
	}
	added := d.Writes - pr.writes
	if added < 0 {
		added = d.Writes // a count that went back is a document whose start was not seen
	}
	pr.writes = d.Writes
	if pr.latest != nil {
		pr.latest.Announced += added
	}
	return true
}
