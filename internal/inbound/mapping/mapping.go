// Package mapping evaluates a trigger mapping (spec Section 3.6) over one
// inbound request body: it splits the body into elements and normalizes each
// into an Event, with every stored text redacted. It is pure: it reads no
// clock, store or network, and it holds no vendor's payload shape.
package mapping

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jchen0426/agent-swarm/internal/actions/redact"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/registry/selector"
	"github.com/jchen0426/agent-swarm/internal/registry/tmpl"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// The limits of one request and one stored event.
const (
	// MaxEvents is how many elements a body may split into.
	MaxEvents = 500
	// MaxPayloadBytes is the most a stored payload holds.
	MaxPayloadBytes = 16 << 10
	// Skew is how far ahead of the receipt an event's time may count.
	Skew = 2 * time.Minute

	maxKeyRunes = 256
	// maxTitleRunes caps a stored title, as the coordinator caps an item's.
	maxTitleRunes = 200
	withheld      = "[withheld]"
	cutMark       = "…"
)

var (
	// ErrBody marks a body that is not one JSON document.
	ErrBody = errors.New("mapping: the body is not one JSON document")
	// ErrTooManyEvents marks a body that splits into more than MaxEvents.
	ErrTooManyEvents = errors.New("mapping: the body splits into more events than the limit")
)

// Event is one normalized inbound event (spec 3.6), as stored and ingested.
type Event struct {
	Source, Mapping, Owner, Type string        // the mapping's source, metadata.name, metadata.owner, workItemType
	Within                       time.Duration // the correlation window; 0 without correlate
	EventID                      string
	InstanceID, DedupKey         string
	CorrelationKey               string
	Status                       item.TriggerStatus
	Occurred, Clamped            time.Time
	Title                        string
	Routing                      map[string]string
	Payload                      string
	Unroutable                   string // a reason, or ""
	Refused                      string // checks_unsupported, or ""
}

// Redactor is the rules that apply always, then the mapping's redact rules.
func Redactor(rev *config.Revision, m *config.TriggerMapping) *redact.Redactor {
	rules := []redact.Rule{redact.SessionTokens()}
	for _, r := range rev.AlwaysRedactionRules() {
		rules = append(rules, redact.FromConfig(r))
	}
	for _, name := range m.Spec.Redact {
		if r := rev.RedactionRule(name); r != nil {
			rules = append(rules, redact.FromConfig(r))
		}
	}
	return redact.New(rules...)
}

// Evaluate splits body by the mapping and normalizes each element. The
// mapping's templates are checked at load, so a parse failure here is an
// element's missing value, never an error.
func Evaluate(m *config.TriggerMapping, body []byte, received time.Time, r *redact.Redactor) ([]Event, error) {
	elements, err := split(m, body)
	if err != nil {
		return nil, err
	}
	ev := evaluator{m: m, received: received, r: r}
	out := make([]Event, 0, len(elements))
	for _, el := range elements {
		e := ev.event(el)
		if len(m.Spec.Checks) > 0 {
			e.Refused = "checks_unsupported"
		}
		out = append(out, e)
	}
	return out, nil
}

func split(m *config.TriggerMapping, body []byte) ([]any, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, ErrBody
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, ErrBody
	}
	if m.Spec.Split == "" {
		return []any{doc}, nil
	}
	sel, err := selector.Parse(m.Spec.Split)
	if err != nil {
		return nil, ErrBody
	}
	elements := sel.Select(doc)
	if len(elements) > MaxEvents {
		return nil, ErrTooManyEvents
	}
	return elements, nil
}

type evaluator struct {
	m        *config.TriggerMapping
	received time.Time
	r        *redact.Redactor
}

// base is an event with what the mapping alone decides.
func (ev *evaluator) base() Event {
	return Event{Source: ev.m.Spec.Source, Mapping: ev.m.Metadata.Name, Owner: ev.m.Metadata.Owner, Type: ev.m.Spec.WorkItemType}
}

// event normalizes one element, in the order of the spec: the first problem
// found names the reason.
func (ev *evaluator) event(el any) Event {
	sp := &ev.m.Spec
	trusted := map[string]string{}
	for _, name := range sp.TrustedFields {
		if v, ok := scalarAt(el, name); ok {
			trusted[name] = v
		}
	}
	fromTrusted := func(name string) (string, bool) { v, ok := trusted[name]; return v, ok }

	status, statusReason := ev.statusOf(fromTrusted)
	var occurred time.Time
	occurredReason := ""
	if statusReason == "" {
		occurred, occurredReason = ev.occurred(status, fromTrusted)
	}

	e := ev.base()
	if reason, raw := ev.eventID(&e, trusted, status, occurred, fromTrusted); reason != "" {
		e.EventID = hashID(raw)
		e.Unroutable = reason
		return e
	}

	// The rest of the event is fixed whatever its routing, so that an
	// unroutable event shows what it was.
	e.Status = status
	if !occurred.IsZero() {
		e.Occurred, e.Clamped = occurred, minTime(occurred, ev.received.Add(Skew))
	}
	e.Title = ev.title(el)
	e.Payload = ev.payload(el)
	k, reason := routedKeys{}, cmp.Or(statusReason, occurredReason)
	if reason == "" {
		k, reason = ev.keys(trusted, fromTrusted)
	}
	if reason != "" {
		e.Unroutable = reason
		return e
	}

	// From here an event that is refused keeps its id and reason only.
	bare := ev.base()
	bare.EventID, bare.Status = e.EventID, e.Status
	if reason := ev.refusal(k); reason != "" {
		bare.Unroutable = reason
		return bare
	}

	e.InstanceID, e.DedupKey, e.CorrelationKey = k.instance, k.dedup, k.correlation
	if len(k.routing) > 0 {
		e.Routing = k.routing
	}
	if sp.Correlate != nil {
		e.Within = sp.Correlate.WithinOrDefault()
	}
	return e
}

// routedKeys are the values that route an event, once rendered.
type routedKeys struct {
	instance, dedup, correlation string
	routing                      map[string]string
}

// keys renders the instance, the dedup key, a declared correlation key and the
// routing entries; the first that lacks a value names the reason.
func (ev *evaluator) keys(trusted map[string]string, lookup func(string) (string, bool)) (k routedKeys, reason string) {
	sp := &ev.m.Spec
	var ok bool
	if k.instance, ok = render(sp.InstanceID, lookup); !ok {
		return k, "missing:instanceId"
	}
	if k.dedup, ok = render(sp.DedupKey, lookup); !ok {
		return k, "missing:dedupKey"
	}
	if sp.Correlate != nil {
		if k.correlation, ok = render(sp.Correlate.Key, lookup); !ok {
			return k, "missing:correlate.key"
		}
	}
	k.routing = map[string]string{}
	for _, key := range slices.Sorted(maps.Keys(sp.Routing)) {
		v, ok := trusted[sp.Routing[key]]
		if !ok {
			return k, "missing:routing." + key
		}
		k.routing[key] = v
	}
	return k, ""
}

// refusal returns why the keys may not be stored: one out of form, or one the
// rules would change.
func (ev *evaluator) refusal(k routedKeys) string {
	if !formOK(k.instance) {
		return "field_invalid:instanceId"
	}
	if !formOK(k.dedup) {
		return "field_invalid:dedupKey"
	}
	if ev.m.Spec.Correlate != nil && !formOK(k.correlation) {
		return "field_invalid:correlate.key"
	}
	values := []string{k.instance, k.dedup, k.correlation}
	for _, key := range slices.Sorted(maps.Keys(k.routing)) {
		values = append(values, k.routing[key])
	}
	if slices.ContainsFunc(values, ev.changes) {
		return "routing_refused"
	}
	return ""
}

// changes reports whether redaction would change s, or an escape could hide a
// match in it.
func (ev *evaluator) changes(s string) bool { return ev.r.Text(s) != s || ev.r.Escaped(s) }

// eventID sets e.EventID and returns "" when the id is fit to store, else the
// reason and the raw id.
func (ev *evaluator) eventID(e *Event, trusted map[string]string, status item.TriggerStatus, occurred time.Time, lookup func(string) (string, bool)) (reason, raw string) {
	sp := &ev.m.Spec
	id, ok := render(sp.EventID, lookup)
	if sp.EventID == "" || !ok {
		h := sha256.New()
		write := func(s string) { h.Write([]byte(s)); h.Write([]byte{0}) }
		write(sp.Source)
		for _, name := range slices.Sorted(maps.Keys(trusted)) {
			write(name)
			write(trusted[name])
		}
		write(string(status))
		write(occurred.UTC().Format(time.RFC3339Nano))
		id = hex.EncodeToString(h.Sum(nil))
	}
	if !formOK(id) {
		return "field_invalid:eventId", id
	}
	if ev.changes(id) {
		return "id_redacted", id
	}
	e.EventID = id
	return "", id
}

func hashID(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// formOK reports whether s is 1 to 256 runes with none a reader could miss.
func formOK(s string) bool {
	n := utf8.RuneCountInString(s)
	return n >= 1 && n <= maxKeyRunes && !strings.ContainsFunc(s, config.Unseen)
}

func (ev *evaluator) statusOf(lookup func(string) (string, bool)) (status item.TriggerStatus, reason string) {
	sp := &ev.m.Spec
	v, ok := render(sp.Status.Value, lookup)
	switch {
	case !ok:
		return "", "missing:status"
	case slices.Contains(sp.Status.FiringValues(), v):
		return item.Firing, ""
	case slices.Contains(sp.Status.ResolvedValues(), v):
		return item.Resolved, ""
	}
	return "", "status_unknown"
}

func (ev *evaluator) occurred(status item.TriggerStatus, lookup func(string) (string, bool)) (at time.Time, reason string) {
	text := ev.m.Spec.OccurredAt.Firing
	if status == item.Resolved {
		text = ev.m.Spec.OccurredAt.Resolved
	}
	v, ok := render(text, lookup)
	if !ok {
		return time.Time{}, "missing:occurredAt"
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, "time_unparsed"
	}
	return t, ""
}

// title renders the title over every field of the element, on one line.
func (ev *evaluator) title(el any) string {
	text, ok := render(ev.m.Spec.Title, func(name string) (string, bool) { return scalarAt(el, name) })
	if !ok {
		return ""
	}
	// Redact the text as written, then as folded: the rules must see both
	// forms, so that the fold cannot hide a match from them.
	if ev.r.Escaped(text) {
		return withheld
	}
	text = ev.r.Text(text)
	text = strings.Map(func(r rune) rune {
		if config.Unseen(r) {
			return ' '
		}
		return r
	}, text)
	if ev.r.Escaped(text) {
		return withheld
	}
	text = ev.r.Text(text)
	if runes := []rune(text); len(runes) > maxTitleRunes {
		return string(runes[:maxTitleRunes-1]) + cutMark
	}
	return text
}

// payload is the element, redacted first and cut after.
func (ev *evaluator) payload(el any) string {
	v := ev.r.Value(el)
	if ev.r.EscapedValue(v) {
		return withheld
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return withheld
	}
	s := strings.TrimSuffix(b.String(), "\n")
	if len(s) <= MaxPayloadBytes {
		return s
	}
	n := MaxPayloadBytes - len(cutMark)
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + cutMark
}

// render renders a template text; an empty or unparsable text, or a hole
// without a value, gives false.
func render(text string, lookup func(string) (string, bool)) (string, bool) {
	if text == "" {
		return "", false
	}
	t, err := tmpl.Parse(text)
	if err != nil {
		return "", false
	}
	s, err := t.Render(lookup)
	return s, err == nil
}

// scalarAt returns the value at a dotted path as text: a string as is, a
// number or a boolean as its JSON text; anything else is absent.
func scalarAt(el any, path string) (string, bool) {
	cur := el
	for _, seg := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return "", false
		}
		if cur, ok = m[seg]; !ok {
			return "", false
		}
	}
	switch v := cur.(type) {
	case string:
		return v, true
	case json.Number:
		return v.String(), true
	case bool:
		if v {
			return "true", true
		}
		return "false", true
	}
	return "", false
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}
