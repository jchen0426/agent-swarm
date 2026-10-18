package redact_test

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

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

// Redacting a value never changes the value: the gateway stores arguments
// redacted, but checks and sends them as written.
func TestValueNeverChangesWhatItRedacts(t *testing.T) {
	r := redact.New(rule(t, "[secret]", []string{`hunter2`}, "$.window"))
	doc := decode(t, `{"window":"1h","note":["hunter2"],"nested":{"window":"2h","list":[{"pw":"hunter2"}]}}`)
	before := encode(t, doc)

	got := r.Value(doc)

	assert.JSONEq(t, `{"window":"[secret]","note":["[secret]"],"nested":{"window":"2h","list":[{"pw":"[secret]"}]}}`, encode(t, got))
	assert.JSONEq(t, before, encode(t, doc))
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

// A member rule matches a key and its value together, so an escape in the
// key can hide the match: EscapedValue flags such a member, and not one that
// Value has already redacted, nor an ordinary one.
func TestEscapedValueChecksAMemberWrittenAsKeyEqualsValue(t *testing.T) {
	r := redact.New(rule(t, "[secret]", []string{`(?i)password\s*[=:]\s*\S+`}))

	assert.True(t, r.EscapedValue(decode(t, `{"pass\\u0077ord":"hunter2"}`)), "the key hides password=")
	assert.False(t, r.EscapedValue(r.Value(decode(t, `{"password":"hunter2"}`))), "Value has redacted the member")
	assert.False(t, r.EscapedValue(decode(t, `{"user":"ana"}`)))
}

// A member Value has redacted is not flagged, even when its key holds a JSON
// escape, as a Windows path does: the member rule matches the replacement
// again, but no match is hidden there. A key whose escape does hide the
// keyword is still flagged.
func TestAMemberRedactedUnderAnEscapedKeyIsNotFlagged(t *testing.T) {
	r := redact.New(rule(t, "[secret]", []string{`(?i)password\s*[=:]\s*\S+`}))

	for _, doc := range []string{
		`{"C:\\temp\\password":"hunter2hunter2"}`,
		`{"HOME\\nPASSWORD":"hunter2hunter2"}`,
		`{"t\\tpassword":"hunter2hunter2"}`,
		`{"dir\\\\password":"hunter2hunter2"}`,
	} {
		assert.False(t, r.EscapedValue(r.Value(decode(t, doc))), doc)
	}
	assert.True(t, r.EscapedValue(decode(t, `{"pass\\u0077ord":"hunter2"}`)), "an escape that hides the keyword")
}

// A key may open a written member before an escaped keyword, as in
// "x":"pass\u0077ord. Text decodes that member and changes it, though Value
// leaves the member as written, so a changed member is not a redacted one:
// EscapedValue still reads the member and flags its escaped keyword. A value
// that is a replacement holds nothing to hide, and is not checked that way.
func TestADecoyBeforeAnEscapedKeywordDoesNotHideAMember(t *testing.T) {
	r := redact.New(rule(t, "[secret]", []string{`(?i)password\s*[=:]\s*\S+`}))

	assert.True(t, r.EscapedValue(r.Value(decode(t, `{"\"x\":\"pass\\u0077ord":"hunter2"}`))), "a double-quoted decoy")
	assert.True(t, r.EscapedValue(r.Value(decode(t, `{"'x':'pass\\u0077ord":"hunter2"}`))), "a single-quoted decoy")
	assert.False(t, r.EscapedValue(r.Value(decode(t, `{"pass\\u0077ord":"[secret]"}`))), "a value that is the replacement")
}

// Only a value that is exactly a rule's replacement holds nothing to hide. A
// value that only begins or ends with one is checked as a member all the
// same, so that a key whose escape hides a member rule's keyword is still
// flagged: a value that held a replacement anywhere would otherwise let any
// such member through.
func TestOnlyAValueThatIsExactlyAReplacementIsNotCheckedAsAMember(t *testing.T) {
	r := redact.New(rule(t, "[secret]", []string{`(?i)password\s*[=:]\s*\S+`}))

	assert.True(t, r.EscapedValue(r.Value(decode(t, `{"pass\\u0077ord":"[secret]hunter2"}`))), "a value that begins with the replacement")
	assert.True(t, r.EscapedValue(r.Value(decode(t, `{"pass\\u0077ord":"hunter2[secret]"}`))), "a value that ends with the replacement")
	assert.False(t, r.EscapedValue(r.Value(decode(t, `{"pass\\u0077ord":"[secret]"}`))), "a value that is the replacement")
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

// A value that runs on, as raw JSON written inside a string or a JSON line
// cut inside a string, never hides the member after it.
func TestAValueThatRunsOnNeverHidesTheNextMember(t *testing.T) {
	r := redact.New(rule(t, "[secret]", secrets))

	for in, want := range map[string]string{
		`{"msg": "request body: {"password":"hunter2hunter2"}"}`:                  `{"msg": "request body: {"password":"[secret]"}"}`,
		"{\"msg\":\"cut here\n{\"password\":\"hunter2hunter2\",\"user\":\"ana\"}": "{\"msg\":\"cut here\n{\"password\":\"[secret]\",\"user\":\"ana\"}",
		`2026-10-14 INFO {"msg": "body: {"api_key":"k3y-abcdefgh"}"}`:             `2026-10-14 INFO {"msg": "body: {"api_key":"[secret]"}"}`,
		`{"rows":[{"user":"ana","password":"hunter2hunt`:                          `{"rows":[{"user":"ana","password":"[secret]"`,
	} {
		assert.Equal(t, want, r.Text(in), in)
	}
}

// Text never fails on what members are written like, however they are cut.
func TestTextReadsAnyCutOfAMemberWithoutFailing(t *testing.T) {
	r := redact.New(rule(t, "[secret]", secrets))

	for _, in := range []string{`"a":"b":1`, `'a':'b':1`, `"a":"`, `"a":'`, `"a":`, `"a": "b"=>"c"`, `"a":"\`, `"a":"\"b":1`} {
		assert.NotPanics(t, func() { r.Text(in) }, in)
	}
}

func FuzzTextNeverFails(f *testing.F) {
	for _, s := range []string{`"a":"b":1`, `{"password":"hunter2hunter2"}`, `'a': 'b\'c', "d"=>"e"`, `"k":"v\nw","x":"\u00`, `\\\"`} {
		f.Add(s)
	}
	r := redact.New(redact.Rule{Replacement: "[secret]", Patterns: []*regexp.Regexp{regexp.MustCompile(secrets[0]), regexp.MustCompile(secrets[1])}})
	f.Fuzz(func(_ *testing.T, s string) {
		_ = r.Escaped(r.Text(s))
	})
}

// Changes names every string, key and number that redaction changes, with
// what the redacted value shows instead: the text redacted, or the
// replacement of a path that replaced the value around it. A string whose
// key alone changed is left out, as is what redaction leaves alone.
func TestChangesNamesEveryStringKeyAndNumberRedactionChanges(t *testing.T) {
	r := redact.New(
		rule(t, "[hidden]", nil, "$.query", "$.filter"),
		rule(t, "[token]", []string{`tok_[a-z0-9]+`}),
		rule(t, "[card]", []string{`\b4[0-9]{15}\b`}),
	)
	doc := decode(t, `{"query":"the cart service leaks memory","filter":{"who":"alice","ids":["x1"]},
		"note":"used tok_abc123 here","tok_key":"kept","card":4111111111111111,"plain":"as written"}`)

	got := r.Changes(doc)

	assert.ElementsMatch(t, []redact.Change{
		{From: "the cart service leaks memory", To: "[hidden]"},
		{From: "who", To: "[hidden]"},
		{From: "alice", To: "[hidden]"},
		{From: "ids", To: "[hidden]"},
		{From: "x1", To: "[hidden]"},
		{From: "used tok_abc123 here", To: "used [token] here"},
		{From: "tok_key", To: "[token]"},
		{From: "4111111111111111", To: "[card]"},
	}, got)
}

// Rules put before a redactor's own apply first.
func TestBeforePutsRulesFirst(t *testing.T) {
	r := redact.New(rule(t, "[word]", []string{`secret`}))

	both := r.Before(rule(t, "[phrase]", []string{`top secret plan`}))

	assert.Equal(t, "the [phrase], a [word]", both.Text("the top secret plan, a secret"))
	assert.Equal(t, "the top [word] plan, a [word]", r.Text("the top secret plan, a secret"), "the redactor itself is unchanged")
}

// A literal is found in any case and with any run of white space where it
// has one, also white space beyond ASCII, and the text around it stays as
// it was, invalid bytes included.
func TestALiteralIsFoundInAnyCaseAndSpacing(t *testing.T) {
	r := redact.New(redact.Rule{Literals: []string{" The Cart  leaks "}, Replacement: "[x]"})

	assert.Equal(t, "a [x] b [x].", r.Text("a THE\u00a0cart\n\t\u3000leaks b the cart leaks."))
	assert.Equal(t, "no match here", r.Text("no match here"))
	assert.Equal(t, "\xff [x] \u0130", r.Text("\xff the cart LEAKS \u0130"))
	assert.Equal(t, "x", redact.New(redact.Rule{Literals: []string{"  "}, Replacement: "[x]"}).Text("x"), "a literal of white space alone matches nothing")
}

// A long literal costs time linear in the text it is matched against.
func TestALongLiteralIsMatchedInLinearTime(t *testing.T) {
	value := strings.Repeat("a ", 30000) + "end"
	r := redact.New(redact.Rule{Literals: []string{value}, Replacement: "[x]"})
	text := strings.Repeat(value+" ", 4)

	start := time.Now()
	got := r.Text(text)

	assert.Equal(t, "[x] [x] [x] [x] ", got)
	assert.Less(t, time.Since(start), time.Second)
}

// Letters whose cases map unevenly meet their plain forms: the long s, the
// Kelvin sign, the final sigma.
func TestALiteralFoldsCaseBeyondASCII(t *testing.T) {
	r := redact.New(redact.Rule{Literals: []string{"the \u017fervice of \u212aelvin \u03bb\u03bf\u03b3\u03bf\u03c2"}, Replacement: "[x]"})

	assert.Equal(t, "a [x].", r.Text("a THE SERVICE OF kelvin \u039b\u039f\u0393\u039f\u03a3."))
	assert.Equal(t, "ok", redact.Fold("O\u212a"))
}

// A long literal costs little against an output of many short strings: it
// is folded once, and never against a string too short to hold it.
func TestManyShortStringsWithALongLiteralCostLittle(t *testing.T) {
	r := redact.New(redact.Rule{Literals: []string{strings.Repeat("a ", 15000) + "end"}, Replacement: "[x]"})
	numbers := make([]any, 60000)
	for i := range numbers {
		numbers[i] = json.Number(strconv.Itoa(i))
	}

	start := time.Now()
	got := r.Value(map[string]any{"numbers": numbers})

	assert.Len(t, got.(map[string]any)["numbers"], 60000)
	assert.Less(t, time.Since(start), time.Second)
}

// Spellings of one value that overlap are all replaced, leftmost first and
// longest where two start together, so that no repeat is left in between.
func TestOverlappingSpellingsAreAllReplaced(t *testing.T) {
	r := redact.New(redact.Rule{Literals: []string{"/tmp/tmp", `\/tmp\/tmp`}, Replacement: "[x]"})

	assert.Equal(t, "[x][x]", r.Text(`\/tmp\/tmp/tmp/tmp`))
	assert.Equal(t, "a [x] b [x]", r.Text("a /TMP/tmp b /tmp/tmp"))
}

// Two keys that redaction makes equal are stored as the redacted value
// places them, the later in sorted order with " (2)", and each key's value is
// compared with what is stored under that key: neither value changed here.
func TestChangesPairsEachValueWithTheKeyItIsStoredUnder(t *testing.T) {
	for name, replacement := range map[string]string{"sorted after the key": "[key]", "sorted before the key": "#key"} {
		t.Run(name, func(t *testing.T) {
			r := redact.New(rule(t, replacement, []string{`AKIA[0-9A-Z]{16}`}))
			doc := decode(t, `{"labels":{"AKIAABCDEFGHIJKLMNOP":"the cart service leaks memory","`+replacement+`":"healthy, no errors"}}`)

			assert.Equal(t, []redact.Change{{From: "AKIAABCDEFGHIJKLMNOP", To: replacement}}, r.Changes(doc))
		})
	}
}
