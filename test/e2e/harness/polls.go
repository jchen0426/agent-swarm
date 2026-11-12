//go:build e2e

package harness

import (
	"encoding/json"
	"slices"
	"time"
)

// pollRow is one of a tab's polls, as /polls answers it (R186): when it was
// sent and the gap since the last poll's answer; its answer; and what the
// recorder saw that answer change, until the next answer came.
type pollRow struct {
	Seq       int        `json:"seq"` // its request's event
	RequestID string     `json:"requestId"`
	At        time.Time  `json:"at"`
	URL       string     `json:"url"`
	First     bool       `json:"first,omitempty"`    // the first poll of its document
	Gap       *float64   `json:"gap,omitempty"`      // seconds since the last poll's answer
	Answered  *time.Time `json:"answered,omitempty"` // when its answer, or its failure, came
	Status    int        `json:"status,omitempty"`
	Failed    string     `json:"failed,omitempty"` // Chrome's error, for a poll that got no answer
	Replayed  bool       `json:"replayed,omitempty"`
	Order     string     `json:"order,omitempty"` // a 200's data-order
	Round     string     `json:"round,omitempty"` // a 200's #summary data-round
	Changed   []string   `json:"changed"`         // the regions the recorder saw change after its answer
	Announced int        `json:"announced"`       // the writes to #announce it counted then
}

// pollReader reads a tab's polls from its events, in order. What the
// recorder sees change is the latest answer's, until another answer comes
// or a new document starts.
type pollReader struct {
	rows   []*pollRow
	byID   map[string]*pollRow
	latest *pollRow
	answer time.Time
	fresh  bool
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
	p := &pollRow{Seq: e.Seq, RequestID: d.RequestID, At: e.At, URL: d.URL, First: pr.fresh, Changed: []string{}}
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
	p.Answered, p.Status, p.Failed, p.Replayed = &at, d.Status, d.Failed, d.Replayed
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
		p.Order, p.Round, p.Replayed = d.Order, d.Round, p.Replayed || d.Replayed
	}
}

func (pr *pollReader) record(e *Event) {
	var r pageRecord
	if json.Unmarshal(e.Detail, &r) != nil {
		return
	}
	switch r.Kind {
	case "start":
		pr.latest, pr.fresh = nil, true
	case "region":
		var d struct {
			Region string `json:"region"`
		}
		if pr.latest != nil && json.Unmarshal(r.Detail, &d) == nil && !slices.Contains(pr.latest.Changed, d.Region) {
			pr.latest.Changed = append(pr.latest.Changed, d.Region)
		}
	case "announce":
		if pr.latest != nil {
			pr.latest.Announced++
		}
	}
}
