package redact_test

import (
	"bytes"
	"encoding/json"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/actions/redact"
	"github.com/jchen0426/agent-swarm/internal/registry/selector"
)

func rule(t *testing.T, replacement string, patterns []string, paths ...string) redact.Rule {
	t.Helper()
	r := redact.Rule{Replacement: replacement}
	for _, p := range patterns {
		r.Patterns = append(r.Patterns, regexp.MustCompile(p))
	}
	for _, p := range paths {
		s, err := selector.Parse(p)
		require.NoError(t, err)
		r.Paths = append(r.Paths, s)
	}
	return r
}

func decode(t *testing.T, s string) any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader([]byte(s)))
	dec.UseNumber()
	var v any
	require.NoError(t, dec.Decode(&v))
	return v
}

func encode(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return string(raw)
}

func TestTextReplacesEveryMatchLiterally(t *testing.T) {
	r := redact.New(rule(t, "[key $1]", []string{`AKIA([0-9A-Z]{16})`}))

	got := r.Text("a AKIAABCDEFGHIJKLMNOP and AKIAQRSTUVWXYZ234567.")

	assert.Equal(t, "a [key $1] and [key $1].", got)
}

func TestValueRedactsKeysValuesAndNumbers(t *testing.T) {
	r := redact.New(rule(t, "[card]", []string{`\b4[0-9]{15}\b`}))
	doc := decode(t, `{"4111111111111111":"owner","card":4111111111111111,"note":["paid with 4111111111111111"],"amount":12.5}`)

	got := r.Value(doc)

	assert.JSONEq(t, `{"[card]":"owner","card":"[card]","note":["paid with [card]"],"amount":12.5}`, encode(t, got))
}

// A rule written for "password: ..." in plain text also finds the member
// {"password": "..."}, where key and value are separate strings.
func TestAPatternFindsAMemberWrittenAsKeyEqualsValue(t *testing.T) {
	r := redact.New(rule(t, "[secret]", []string{`(?i)\b(password|api[_-]?key)\s*[:=]\s*[^\s,;]{8,}`}))

	got := r.Value(decode(t, `{"password":"hunter2hunter2","user":"ana","nested":[{"api_key":"k3y-abcdefgh"}],"pin":12345678}`))

	assert.JSONEq(t, `{"password":"[secret]","user":"ana","nested":[{"api_key":"[secret]"}],"pin":12345678}`, encode(t, got))
}

func TestPathsReplaceWholeValuesBeforePatternsRun(t *testing.T) {
	r := redact.New(
		rule(t, "[env]", nil, "$..env[*].value"),
		rule(t, "[token]", []string{`tok_[a-z0-9]+`}),
	)
	doc := decode(t, `{"env":[{"name":"DB","value":{"password":"hunter2"}}],"log":"used tok_abc123"}`)

	got := r.Value(doc)

	assert.JSONEq(t, `{"env":[{"name":"DB","value":"[env]"}],"log":"used [token]"}`, encode(t, got))
	assert.True(t, r.HasPaths())
	assert.False(t, redact.New(rule(t, "x", []string{"y"})).HasPaths())
}

func TestKeysThatRedactionMakesEqualAreToldApart(t *testing.T) {
	r := redact.New(rule(t, "[email]", []string{`[a-z]+@example.com`}))
	for range 20 {
		got := r.Value(decode(t, `{"ana@example.com":1,"bo@example.com":2,"cy@example.com":3}`))

		assert.Equal(t, map[string]any{"[email]": json.Number("1"), "[email] (2)": json.Number("2"), "[email] (3)": json.Number("3")}, got)
	}
}

func TestAnEscapedMatchInTextIsFound(t *testing.T) {
	r := redact.New(rule(t, "[dsn]", []string{`[a-z]+://[^\s:/@]+:[^\s@/]+@`}))

	assert.True(t, r.Escaped(`payload={"dsn":"postgres:\/\/u:pw@db"}`))
	assert.False(t, r.Escaped(`key \u0041KIA`), "no rule matches the decoded text")
	assert.False(t, r.Escaped(`no escapes here`))
	assert.False(t, r.Escaped(`dsn postgres://[dsn]db`))
}

func TestNoRulesChangeNothing(t *testing.T) {
	r := redact.New()

	assert.Equal(t, "AKIAABCDEFGHIJKLMNOP", r.Text("AKIAABCDEFGHIJKLMNOP"))
	assert.JSONEq(t, `{"n":12345678901234567890}`, encode(t, r.Value(decode(t, `{"n":12345678901234567890}`))))
	assert.False(t, r.Escaped(`postgres:\/\/u:pw@h`))
}
