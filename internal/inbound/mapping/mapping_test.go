package mapping_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/actions/redact"
	"github.com/jchen0426/agent-swarm/internal/inbound/mapping"
	"github.com/jchen0426/agent-swarm/internal/registry/celeval"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/registry/dirsource"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

var received = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

// setup returns the example deployment's mapping, without its checks, and the
// redactor of its revision.
func setup(t *testing.T) (*config.TriggerMapping, *redact.Redactor) {
	t.Helper()
	_, m, r := setupRevision(t)
	return m, r
}

func setupRevision(t *testing.T) (*config.Revision, *config.TriggerMapping, *redact.Redactor) {
	t.Helper()
	snap, err := dirsource.New("../../../examples/config").Read(t.Context(), "")
	require.NoError(t, err)
	conditions, err := celeval.New()
	require.NoError(t, err)
	rev, err := config.Build(snap.Files, snap.Commit, conditions)
	require.NoError(t, err)
	m := rev.TriggerMapping("alerting")
	require.NotNil(t, m)
	m.Spec.Checks = nil
	return rev, m, mapping.Redactor(rev, m)
}

// alert is one alert of the example source's body.
func alert(fingerprint, status, startsAt, service string) string {
	return fmt.Sprintf(`{"fingerprint":%q,"status":%q,"startsAt":%q,"endsAt":%q,"labels":{"service":%q,"rule_id":"r1"},"annotations":{"summary":"disk is full"}}`,
		fingerprint, status, startsAt, startsAt, service)
}

func body(alerts ...string) []byte { return []byte(`{"alerts":[` + strings.Join(alerts, ",") + `]}`) }

func one(t *testing.T, m *config.TriggerMapping, r *redact.Redactor, doc string) mapping.Event {
	t.Helper()
	events, err := mapping.Evaluate(m, []byte(doc), received, r)
	require.NoError(t, err)
	require.Len(t, events, 1)
	return events[0]
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

const start = "2026-11-09T11:00:00Z"

func TestAGroupSplitsIntoOneEventPerElement(t *testing.T) {
	m, r := setup(t)

	events, err := mapping.Evaluate(m, body(alert("a", "firing", start, "web"), alert("b", "firing", start, "web"), alert("c", "resolved", start, "db")), received, r)

	require.NoError(t, err)
	require.Len(t, events, 3)
	assert.Equal(t, []string{"a:firing:" + start, "b:firing:" + start, "c:resolved:" + start}, []string{events[0].EventID, events[1].EventID, events[2].EventID})
	e := events[0]
	assert.Equal(t, "alerting", e.Source)
	assert.Equal(t, "alerts", e.Mapping)
	assert.Equal(t, "sre-leads", e.Owner)
	assert.Equal(t, "incident", e.Type)
	assert.Equal(t, 30*time.Minute, e.Within)
	assert.Equal(t, "a", e.InstanceID)
	assert.Equal(t, "web:r1", e.DedupKey)
	assert.Equal(t, "web", e.CorrelationKey)
	assert.Equal(t, map[string]string{"service": "web"}, e.Routing)
	assert.Equal(t, "disk is full", e.Title)
	assert.Empty(t, e.Unroutable)
	assert.Empty(t, e.Refused)

	m.Spec.Split = ""
	m.Spec.Status.Value = "{status}"
	events, err = mapping.Evaluate(m, []byte(alert("a", "firing", start, "web")), received, r)
	require.NoError(t, err)
	assert.Len(t, events, 1)
}

func TestOnlyTrustedFieldsFillTheRoutedTemplates(t *testing.T) {
	m, r := setup(t)
	m.Spec.TrustedFields = append(m.Spec.TrustedFields, "count", "paging", "labels")

	t.Run("a number is its JSON text, a boolean true", func(t *testing.T) {
		m := *m
		m.Spec.DedupKey = "{labels.service}:{count}:{paging}"
		events, err := mapping.Evaluate(&m, []byte(`{"alerts":[{"fingerprint":"a","status":"firing","startsAt":"`+start+`","labels":{"service":"web"},"count":5,"paging":true}]}`), received, r)
		require.NoError(t, err)
		assert.Equal(t, "web:5:true", events[0].DedupKey)
	})
	t.Run("an object is absent", func(t *testing.T) {
		m := *m
		m.Spec.DedupKey = "{labels}"
		events, err := mapping.Evaluate(&m, body(alert("a", "firing", start, "web")), received, r)
		require.NoError(t, err)
		assert.Equal(t, "missing:dedupKey", events[0].Unroutable)
	})
	t.Run("an untrusted field never routes, but the title may use it", func(t *testing.T) {
		m := *m
		m.Spec.Routing = map[string]string{"service": "labels.service"}
		m.Spec.InstanceID = "{annotations.summary}"
		m.Spec.Title = "{annotations.summary} on {labels.service}"
		events, err := mapping.Evaluate(&m, body(alert("a", "firing", start, "web")), received, r)
		require.NoError(t, err)
		assert.Equal(t, "missing:instanceId", events[0].Unroutable)
		assert.Equal(t, "disk is full on web", events[0].Title)
	})
}

func TestStatusIsMappedThroughItsLists(t *testing.T) {
	m, r := setup(t)
	m.Spec.Status.Firing = []string{"firing", "active"}

	for status, want := range map[string]item.TriggerStatus{"firing": item.Firing, "resolved": item.Resolved, "active": item.Firing} {
		e := one(t, m, r, string(body(alert("a", status, start, "web"))))
		assert.Equal(t, want, e.Status, status)
		assert.Empty(t, e.Unroutable, status)
	}

	e := one(t, m, r, string(body(alert("a", "pending", start, "web"))))
	assert.Equal(t, "status_unknown", e.Unroutable)
}

func TestOccurredAtIsClampedButTheIDKeepsTheParsedTime(t *testing.T) {
	m, r := setup(t)
	m.Spec.EventID = ""
	ahead := received.Add(24 * time.Hour).Format(time.RFC3339)
	doc := string(body(alert("a", "firing", ahead, "web")))

	first := one(t, m, r, doc)
	events, err := mapping.Evaluate(m, []byte(doc), received.Add(time.Hour), r)
	require.NoError(t, err)

	assert.Equal(t, received.Add(24*time.Hour), first.Occurred)
	assert.Equal(t, received.Add(mapping.Skew), first.Clamped)
	assert.Equal(t, received.Add(time.Hour+mapping.Skew), events[0].Clamped)
	assert.Equal(t, first.EventID, events[0].EventID)

	past := one(t, m, r, string(body(alert("a", "firing", start, "web"))))
	assert.Equal(t, past.Occurred, past.Clamped)
}

func TestAnAbsentEventIDIsDerivedFromTheTrustedFields(t *testing.T) {
	m, r := setup(t)
	m.Spec.EventID = ""
	const doc = `{"alerts":[{"fingerprint":"a","status":"firing","startsAt":"2026-11-09T11:00:00Z","endsAt":"","labels":{"service":"web","rule_id":"r1"}}]}`
	const reordered = `{"alerts":[{"labels":{"rule_id":"r1","service":"web"},"endsAt":"","startsAt":"2026-11-09T11:00:00Z","status":"firing","fingerprint":"a"}]}`

	e := one(t, m, r, doc)

	assert.Equal(t, "abb283543d624072b18f823050ca2eae2ee394b1ecd6e7209f03995146812a28", e.EventID)
	assert.Equal(t, e.EventID, one(t, m, r, reordered).EventID)
	assert.NotEqual(t, e.EventID, one(t, m, r, strings.Replace(doc, `"firing"`, `"resolved"`, 1)).EventID)

	m.Spec.TrustedFields = append(m.Spec.TrustedFields, "nothing.here")
	m.Spec.EventID = "{nothing.here}"
	assert.Equal(t, e.EventID, one(t, m, r, doc).EventID, "an id template lacking a value is treated as absent (R122)")
}

func TestAnEventIDTheRulesWouldChangeMakesTheEventUnroutable(t *testing.T) {
	m, r := setup(t)
	m.Spec.EventID = "{fingerprint}"
	const secret = "password=hunter2hunter2"

	e := one(t, m, r, string(body(alert(secret, "firing", start, "web"))))

	assert.Equal(t, "id_redacted", e.Unroutable)
	assert.Equal(t, sha(secret), e.EventID)
	assert.Equal(t, mapping.Event{Source: "alerting", Mapping: "alerts", Owner: "sre-leads", Type: "incident", EventID: sha(secret), Unroutable: "id_redacted"}, e)
}

func TestAMissingRoutedFieldMakesTheEventUnroutable(t *testing.T) {
	m, r := setup(t)
	m.Spec.Routing = map[string]string{"service": "labels.service", "zone": "labels.zone"}
	m.Spec.TrustedFields = append(m.Spec.TrustedFields, "labels.zone")
	full := map[string]string{
		"fingerprint": `"a"`, "status": `"firing"`, "startsAt": `"` + start + `"`,
		"labels": `{"service":"web","rule_id":"r1","zone":"z1"}`,
	}
	cases := []struct {
		name, without, reason string
		with                  map[string]string
	}{
		{name: "status", without: "status", reason: "missing:status"},
		{name: "occurredAt", without: "startsAt", reason: "missing:occurredAt"},
		{name: "instanceId", without: "fingerprint", reason: "missing:instanceId"},
		{name: "dedupKey", with: map[string]string{"labels": `{"service":"web","zone":"z1"}`}, reason: "missing:dedupKey"},
		{name: "correlate.key", reason: "missing:correlate.key"},
		{name: "routing", with: map[string]string{"labels": `{"service":"web","rule_id":"r1"}`}, reason: "missing:routing.zone"},
		{name: "time", with: map[string]string{"startsAt": `"yesterday"`}, reason: "time_unparsed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := *m
			if c.name == "correlate.key" {
				m.Spec.Correlate = &config.Correlation{Key: "{labels.team}"}
				m.Spec.TrustedFields = append(slicesClone(m.Spec.TrustedFields), "labels.team")
			}
			var parts []string
			for k, v := range full {
				if k == c.without {
					continue
				}
				if w, ok := c.with[k]; ok {
					v = w
				}
				parts = append(parts, fmt.Sprintf("%q:%s", k, v))
			}
			doc := `{"alerts":[{` + strings.Join(parts, ",") + `}]}`

			events, err := mapping.Evaluate(&m, []byte(doc), received, r)

			require.NoError(t, err)
			require.Len(t, events, 1)
			assert.Equal(t, c.reason, events[0].Unroutable)
			assert.Empty(t, events[0].InstanceID)
			assert.Empty(t, events[0].DedupKey)
			assert.Empty(t, events[0].Routing)
		})
	}
}

func slicesClone(s []string) []string { return append([]string(nil), s...) }

func TestAnIDOrKeyOutOfFormIsUnroutable(t *testing.T) {
	m, r := setup(t)
	m.Spec.EventID = "{fingerprint}"
	m.Spec.InstanceID = "{labels.rule_id}"
	m.Spec.DedupKey = "{labels.service}"
	m.Spec.Correlate.Key = "{status}"
	m.Spec.TrustedFields = append(m.Spec.TrustedFields, "labels.rule_id")
	wide := strings.Repeat("é", 256)
	doc := func(fingerprint, rule, service, status string) string {
		return fmt.Sprintf(`{"alerts":[{"fingerprint":%q,"status":%q,"startsAt":%q,"labels":{"service":%q,"rule_id":%q}}]}`, fingerprint, status, start, service, rule)
	}
	m.Spec.Status.Firing = []string{"firing", "fire\tx"}

	t.Run("the event id", func(t *testing.T) {
		for _, id := range []string{strings.Repeat("x", 257), "a\nb", ""} {
			e := one(t, m, r, doc(id, "r", "s", "firing"))

			assert.Equal(t, "field_invalid:eventId", e.Unroutable)
			assert.Equal(t, sha(id), e.EventID)
			assert.Equal(t, mapping.Event{Source: "alerting", Mapping: "alerts", Owner: "sre-leads", Type: "incident", EventID: sha(id), Unroutable: "field_invalid:eventId"}, e)
		}
	})
	t.Run("the keys", func(t *testing.T) {
		cases := []struct{ name, rule, service, status, reason string }{
			{"instance", "a\u200bb", "s", "firing", "field_invalid:instanceId"},
			{"dedup key", "r", strings.Repeat("x", 257), "firing", "field_invalid:dedupKey"},
			{"correlation key", "r", "s", "fire\tx", "field_invalid:correlate.key"},
		}
		for _, c := range cases {
			e := one(t, m, r, doc("id", c.rule, c.service, c.status))

			assert.Equal(t, c.reason, e.Unroutable, c.name)
			assert.Equal(t, "id", e.EventID, c.name)
			assert.Empty(t, e.InstanceID+e.DedupKey+e.CorrelationKey, c.name)
			assert.Empty(t, e.Routing, c.name)
		}
	})
	t.Run("exactly 256 runes pass", func(t *testing.T) {
		e := one(t, m, r, doc(wide, wide, wide, "firing"))

		assert.Empty(t, e.Unroutable)
		assert.Equal(t, wide, e.EventID)
		assert.Equal(t, wide, e.InstanceID)
		assert.Equal(t, wide, e.DedupKey)
	})
}

func TestARoutedValueTheRulesWouldChangeIsRefusedWithoutItsValue(t *testing.T) {
	m, r := setup(t)

	e := one(t, m, r, string(body(alert("a", "firing", start, "password=hunter2hunter2"))))

	assert.Equal(t, "routing_refused", e.Unroutable)
	assert.Equal(t, "a:firing:"+start, e.EventID)
	assert.Empty(t, e.InstanceID+e.DedupKey+e.CorrelationKey)
	assert.Empty(t, e.Routing)
	assert.Empty(t, e.Title)
	assert.Empty(t, e.Payload)
	assert.NotContains(t, fmt.Sprintf("%+v", e), "hunter2")
}

func TestStoredTextIsRedactedFirstAndCutAfter(t *testing.T) {
	m, r := setup(t)
	extra := func(field string) string {
		return `{"alerts":[{"fingerprint":"a","status":"firing","startsAt":"` + start + `","labels":{"service":"web","rule_id":"r1"},` + field + `}]}`
	}

	t.Run("a member is redacted", func(t *testing.T) {
		e := one(t, m, r, extra(`"password":"xxxxxxxxxx"`))

		assert.Contains(t, e.Payload, "[redacted:default-secrets]")
		assert.NotContains(t, e.Payload, "xxxxxxxxxx")
	})
	t.Run("the mapping's own rule applies", func(t *testing.T) {
		e := one(t, m, r, extra(`"owner":"jo@example.com"`))

		assert.NotContains(t, e.Payload, "jo@example.com")
		assert.Contains(t, e.Payload, "[redacted:customer-data]")
	})
	// head is the encoded payload up to the opening quote of "zz", the last key.
	head := len(one(t, m, r, extra(`"zz":""`)).Payload) - len(`"}`)
	cutAt := mapping.MaxPayloadBytes - len("…")
	t.Run("a match across the boundary is redacted before the cut", func(t *testing.T) {
		// The cut falls four bytes into the secret's value: a build that cut
		// first would keep "password=abcd".
		const lead = " password="
		prefix := strings.Repeat("a", cutAt-head-len(lead)-4)
		e := one(t, m, r, extra(`"zz":"`+prefix+lead+`abcdefghijklmnop"`))

		assert.LessOrEqual(t, len(e.Payload), mapping.MaxPayloadBytes)
		assert.True(t, strings.HasSuffix(e.Payload, "…"))
		assert.NotContains(t, e.Payload, "password=")
		assert.NotContains(t, e.Payload, "abcd")
	})
	t.Run("a cut keeps whole runes", func(t *testing.T) {
		// Two-byte runes start at head; the cut falls on the second byte of one.
		pad := ""
		if (cutAt-head)%2 == 0 {
			pad = "a"
		}
		e := one(t, m, r, extra(`"zz":"`+pad+strings.Repeat("é", 10000)+`"`))

		assert.LessOrEqual(t, len(e.Payload), mapping.MaxPayloadBytes)
		assert.True(t, strings.HasSuffix(e.Payload, "é…"))
		assert.NotContains(t, e.Payload, "\ufffd")
		assert.True(t, utf8.ValidString(e.Payload))
		assert.Equal(t, cutAt-1, len(e.Payload)-len("…"), "the cut backed off one byte")
	})
	t.Run("an escaped secret is withheld", func(t *testing.T) {
		e := one(t, m, r, extra(`"dsn":"postgres:\\/\\/u:pw@h"`))

		assert.Equal(t, "[withheld]", e.Payload)
	})
	t.Run("a title reads on one line", func(t *testing.T) {
		m := *m
		m.Spec.Title = "{annotations.summary}"
		e := one(t, &m, r, `{"alerts":[{"fingerprint":"a","status":"firing","startsAt":"`+start+`","labels":{"service":"web","rule_id":"r1"},"annotations":{"summary":"one\ntwo\tthree"}}]}`)

		assert.Equal(t, "one two three", e.Title)
	})
	t.Run("a title with a secret is redacted", func(t *testing.T) {
		m := *m
		e := one(t, &m, r, `{"alerts":[{"fingerprint":"a","status":"firing","startsAt":"`+start+`","labels":{"service":"web","rule_id":"r1"},"annotations":{"summary":"token AKIAABCDEFGHIJKLMNOP leaked"}}]}`)

		assert.Equal(t, "token [redacted:default-secrets] leaked", e.Title)
	})
}

func TestAMappingWithChecksRefusesItsEvents(t *testing.T) {
	m, r := setup(t)
	m.Spec.Checks = []string{"true"}

	events, err := mapping.Evaluate(m, body(alert("a", "firing", start, "web"), alert("b", "pending", start, "web")), received, r)

	require.NoError(t, err)
	require.Len(t, events, 2)
	for _, e := range events {
		assert.Equal(t, "checks_unsupported", e.Refused)
	}
	assert.Equal(t, "a:firing:"+start, events[0].EventID)
	assert.Equal(t, "status_unknown", events[1].Unroutable)
}

func TestABodyOverTheEventLimitOrNotJSONIsAnError(t *testing.T) {
	m, r := setup(t)
	many := func(n int) []byte {
		alerts := make([]string, n)
		for i := range alerts {
			alerts[i] = alert(fmt.Sprint(i), "firing", start, "web")
		}
		return body(alerts...)
	}

	_, err := mapping.Evaluate(m, many(mapping.MaxEvents+1), received, r)
	require.ErrorIs(t, err, mapping.ErrTooManyEvents)

	events, err := mapping.Evaluate(m, many(mapping.MaxEvents), received, r)
	require.NoError(t, err)
	assert.Len(t, events, mapping.MaxEvents)

	for _, bad := range []string{"{", "", `{} {}`, `{} x`} {
		_, err = mapping.Evaluate(m, []byte(bad), received, r)
		assert.ErrorIs(t, err, mapping.ErrBody, bad)
	}
}

func TestATitleIsFoldedToOneLineAndCapped(t *testing.T) {
	m, r := setup(t)
	title := func(summary string) string {
		return one(t, m, r, `{"alerts":[{"fingerprint":"a","status":"firing","startsAt":"`+start+`","labels":{"service":"web","rule_id":"r1"},"annotations":{"summary":`+summary+`}}]}`).Title
	}

	assert.Equal(t, "a b c d e f", title(`"a\u200bb\u0007c\u007fd\u0000e\rf"`), "every unseen character folds to a space")
	assert.Equal(t, "one  two", title(`"one\r\ntwo"`))
	got := title(`"` + strings.Repeat("é", 300) + `"`)
	assert.Equal(t, 200, utf8.RuneCountInString(got))
	assert.True(t, strings.HasSuffix(got, "…"))
	assert.Equal(t, strings.Repeat("é", 200), title(`"`+strings.Repeat("é", 200)+`"`))
}

func TestTheAlwaysRulesRedactWithoutTheMappingsOwn(t *testing.T) {
	rev, m, _ := setupRevision(t)
	m.Spec.Redact = nil
	r := mapping.Redactor(rev, m)

	e := one(t, m, r, `{"alerts":[{"fingerprint":"a","status":"firing","startsAt":"`+start+`","labels":{"service":"web","rule_id":"r1"},"note":"password=abcdefghijkl","owner":"jo@example.com"}]}`)

	assert.NotContains(t, e.Payload, "abcdefghijkl")
	assert.Contains(t, e.Payload, "jo@example.com", "the mapping's own rule is not in the redactor Redactor was built from")
}

func TestATitleIsRedactedBeforeAndAfterItsFold(t *testing.T) {
	m, r := setup(t)
	title := func(summary string) string {
		return one(t, m, r, `{"alerts":[{"fingerprint":"a","status":"firing","startsAt":"`+start+`","labels":{"service":"web","rule_id":"r1"},"annotations":{"summary":`+summary+`}}]}`).Title
	}

	// The rule needs eight value characters: the zero-width rune counts as one
	// before the fold and splits the value after it.
	got := title(`"password=abc\u200bdefghijkl"`)

	assert.NotContains(t, got, "abc")
	assert.NotContains(t, got, "defghijkl")
	assert.Contains(t, got, "[redacted:default-secrets]")
}
