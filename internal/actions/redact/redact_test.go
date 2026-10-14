package redact_test

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
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

// A pattern that matches the key alone still hides the value when its match
// in key=value runs on past the equals sign.
func TestAMatchAcrossTheEqualsSignReplacesTheValue(t *testing.T) {
	r := redact.New(rule(t, "[pw]", []string{`(?i)password\S*`}))

	got := r.Value(decode(t, `{"password":"hunter2hunter2","card":"4111"}`))

	assert.JSONEq(t, `{"[pw]":"[pw]","card":"4111"}`, encode(t, got))
}

var secrets = []string{
	`(?i)\b(password|passwd|secret|api[_-]?key)\s*[:=]\s*[^\s,;]{8,}`,
	`\bAKIA[0-9A-Z]{16}\b`,
	`(?i)\b[a-z]+://[^\s:/@]+:[^\s@/]+@`,
}

// JSON written inside text, such as a log line, a JSON line or JSON that was
// cut short, gets the member rule too.
func TestTextFindsMembersWrittenAsJSON(t *testing.T) {
	r := redact.New(rule(t, "[secret]", secrets))

	for in, want := range map[string]string{
		`Found: {"user":"ana","password":"hunter2hunter2"}`:    `Found: {"user":"ana","password":"[secret]"}`,
		"{\"a\":1,\"password\":\"hunter2hunter2\"}\n{\"b\":2}": "{\"a\":1,\"password\":\"[secret]\"}\n{\"b\":2}",
		`{"rows":[{"p\u0061ssword" : "hunter2hunter2"},{"x`:    `{"rows":[{"p\u0061ssword" : "[secret]"},{"x`,
		`{"pin":12345678,"ok":true}`:                           `{"pin":12345678,"ok":true}`,
	} {
		assert.Equal(t, want, r.Text(in), in)
	}
}

// A string value written in text is redacted as text of its own, decoded, so
// an escape inside it hides nothing.
func TestTextRedactsStringValuesWrittenInText(t *testing.T) {
	r := redact.New(rule(t, "[secret]", secrets))

	for in, want := range map[string]string{
		`log "msg":"loaded key\nAKIAABCDEFGHIJKLMNOP"`:              `log "msg":"loaded key\n[secret]"`,
		`{"msg":"{\"password\":\"hunter2hunter2\"}"}`:               `{"msg":"{\"password\":\"[secret]\"}"}`,
		`{"url":"https://h/p?user=x\u0026password=hunter2hunter2"}`: `{"url":"https://h/p?user=x&[secret]"}`,
	} {
		assert.Equal(t, want, r.Text(in), in)
	}
}

func TestEscapesAreDecodedLevelByLevel(t *testing.T) {
	r := redact.New(rule(t, "[dsn]", []string{`[a-z]+://[^\s:/@]+:[^\s@/]+@`}))

	assert.True(t, r.Escaped(`dsn postgres:\\/\\/admin:pw@db`), "two levels down")
	assert.True(t, r.Escaped(strings.Repeat(`\`, 32)+"n"), "deeper than the gateway decodes")
	assert.False(t, r.Escaped(`C:\new\table`))
	assert.False(t, r.Escaped(strings.Repeat(`\`, 16)+"x"))
}

func TestEscapedValueChecksEveryStringAndKey(t *testing.T) {
	r := redact.New(rule(t, "[dsn]", []string{`[a-z]+://[^\s:/@]+:[^\s@/]+@`}))

	assert.True(t, r.EscapedValue(r.Value(decode(t, `{"lines":["postgres:\\/\\/admin:pw@db"]}`))))
	assert.True(t, r.EscapedValue(r.Value(decode(t, `{"postgres:\\/\\/admin:pw@db":1}`))))
	assert.False(t, r.EscapedValue(r.Value(decode(t, `{"lines":["C:\\new"],"n":1}`))))
}

// Python and Ruby write members too, and a broken JSON string may hold a
// line break; text that is not JSON gets the member rule all the same.
func TestTextFindsMembersWrittenInOtherForms(t *testing.T) {
	r := redact.New(rule(t, "[secret]", secrets))

	for in, want := range map[string]string{
		`{'user': 'ana', 'password': 'hunter2hunter2'}`: `{'user': 'ana', 'password': "[secret]"}`,
		`{"password"=>"hunter2hunter2", "n"=>1}`:        `{"password"=>"[secret]", "n"=>1}`,
		"{\"password\":\"hunter2hunter2\nmore\"}":       `{"password":"[secret]"}`,
	} {
		assert.Equal(t, want, r.Text(in), in)
	}
}

// A match that needs both the key and the value hides the value, even when
// it does not run on into the value.
func TestAMatchThatNeedsTheKeyAndTheValueReplacesTheValue(t *testing.T) {
	r := redact.New(rule(t, "[pw]", []string{`(?i)^password=`}))

	assert.JSONEq(t, `{"password":"[pw]"}`, encode(t, r.Value(decode(t, `{"password":"hunter2"}`))))
	assert.JSONEq(t, `{"password":"[pw]"}`, r.Text(`{"password":"hunter2"}`))
}
