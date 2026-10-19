package app_test

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/actions/app"
	"github.com/jchen0426/agent-swarm/internal/actions/redact"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
)

// sessionToken is a token of a session at the LLM gateway, as an agent could
// paste one anywhere.
const sessionToken = "aswl_0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefg"

// What the gateway keeps in place of text that holds an escape which may hide
// a secret: the reason of a refusal, and the name of a tool.
const (
	escapedReason = "the arguments hold an escape that may hide a secret; write them plainly"
	escapedName   = "[redacted:escaped]"
)

// The letters and digits of the tokens the tests use: the caller's own, and
// the session token of the LLM gateway.
var (
	ownBody   = token[len("aswm_"):]
	otherBody = sessionToken[len("aswl_"):]
)

// escape writes r as a JSON escape: a backslash, u and four hex digits.
func escape(r rune) string { return fmt.Sprintf(`\u%04x`, r) }

// behindAnEscape holds a token each way a backslash escape can hide one: the
// caller's own with one of its characters escaped, and another session's with
// its underscore escaped. written is JSON text, with a backslash before the
// escape; the gateway decodes it once, to decoded, which still holds the
// escape: the token appears when that is decoded again.
var behindAnEscape = []struct{ name, written, decoded string }{
	{"the caller's own token with one character escaped", ownBody[:5] + `\` + escape(rune(ownBody[5])) + ownBody[6:], ownBody[:5] + escape(rune(ownBody[5])) + ownBody[6:]},
	{"another session's token with its underscore escaped", `aswl\` + escape('_') + otherBody, "aswl" + escape('_') + otherBody},
}

// hiddenRules holds the match of a rule that is no token rule, behind a
// backslash escape, for each way a rule reaches a call: a rule of the tool
// (the AKIA key of default-secrets, which get_flag names) and a rule that
// applies always (a private key header). written and decoded are as in
// behindAnEscape.
var hiddenRules = []struct{ name, written, decoded string }{
	{"a match of the tool's rule", `AKI\` + escape('A') + `0123456789ABCDEF`, "AKI" + escape('A') + "0123456789ABCDEF"},
	{"a match of a rule that applies always", `-----BEGIN RSA PRIVATE KE\` + escape('Y') + `-----`, "-----BEGIN RSA PRIVATE KE" + escape('Y') + "-----"},
}

// assertNoToken asserts that none of kept, text the gateway stored or
// answered, holds a session token as written or one decode away: the
// gateway's own rules find nothing in it to change, behind an escape or not,
// and none of the spellings that were sent is in it.
func assertNoToken(t *testing.T, kept []string, sent ...string) {
	t.Helper()
	own := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(ownBody))
	red := redact.New(redact.SessionTokens(), redact.Rule{Patterns: []*regexp.Regexp{own}, Replacement: config.SessionTokenReplacement})
	for _, k := range kept {
		assert.Equal(t, k, red.Text(k), "a token as written")
		assert.False(t, red.Escaped(k), "a token behind an escape: %s", k)
		for _, s := range sent {
			assert.NotContains(t, k, s)
		}
	}
}

// A session's token is hidden wherever the gateway keeps text: a tool's
// output, shown and stored, and an agent's text on the board, whatever
// rules the configuration holds.
func TestASessionTokenIsHiddenWhereverTheGatewayKeepsText(t *testing.T) {
	e := newEnv(t)
	e.up.set(func(u *upstream) {
		u.replies["get_flag"] = text(`{"flag":"x","on":true,"note":"` + sessionToken + `","truncated":false}`)
	})

	read := e.call(t, "flags__get_flag", `{"flag":"x","window":"1h"}`)
	post := e.call(t, "post_task", `{"title":"check `+sessionToken+`"}`)

	require.Equal(t, app.StatusOK, read.Status, read.Text)
	assert.Contains(t, read.Text, config.SessionTokenReplacement)
	_, out, _, _ := e.audit.Record(read.CallID)
	require.Equal(t, app.StatusOK, post.Status, post.Text)
	for _, kept := range []string{read.Text, out.Output, e.events(t)[0].Body.Task.Title} {
		assert.NotContains(t, kept, sessionToken)
	}
	assert.Equal(t, "check "+config.SessionTokenReplacement, e.events(t)[0].Body.Task.Title)
}

// A read call whose arguments hold a session token is refused: the gateway
// sends a server the arguments as the agent wrote them. A JSON escape does
// not hide the token, which the gateway decodes; a backslash escape that may
// hide it is refused for what it may hide.
func TestAReadCallWhoseArgumentsHoldASessionTokenIsRefused(t *testing.T) {
	e := newEnv(t)
	for _, c := range []struct{ args, reason string }{
		{`{"flag":"` + sessionToken + `","window":"1h"}`, "hold a session token"},
		// The underscore as a JSON escape: the decoded string holds the token.
		{`{"flag":"aswl` + escape('_') + otherBody + `","window":"1h"}`, "hold a session token"},
		// A backslash before that escape, which JSON decodes to a backslash: the
		// decoded string still holds the escape.
		{`{"flag":"aswl\` + escape('_') + otherBody + `","window":"1h"}`, "an escape that may hide a session token"},
	} {
		r := e.call(t, "flags__get_flag", c.args)

		assert.Equal(t, app.StatusRefused, r.Status, c.args)
		assert.Contains(t, r.Text, c.reason, c.args)
		rec, _, _, _ := e.audit.Record(r.CallID)
		assert.NotContains(t, string(rec.Args), otherBody, c.args)
	}
	assert.Empty(t, e.up.sent())
}

// A read call whose arguments hold the caller's own token is refused, in any
// case, without its prefix, or with its prefix written otherwise; one whose
// escapes go deeper than the gateway decodes is refused for what they may
// hide.
func TestAReadCallWhoseArgumentsHoldTheCallersTokenIsRefused(t *testing.T) {
	e := newEnv(t)
	body := token[len("aswm_"):]
	for _, args := range []string{
		`{"flag":"` + body + `","window":"1h"}`,
		`{"flag":"ASWM_` + body + `","window":"1h"}`,
		`{"flag":"` + strings.ToLower(body) + `","window":"1h"}`,
		`{"flag":"aswm%5F` + body + `","window":"1h"}`,
	} {
		r := e.call(t, "flags__get_flag", args)

		assert.Equal(t, app.StatusRefused, r.Status, args)
		assert.Contains(t, r.Text, "hold a session token", args)
	}
	deep := e.call(t, "flags__get_flag", `{"flag":"x`+strings.Repeat(`\\`, 16)+`n","window":"1h"}`)
	assert.Equal(t, app.StatusRefused, deep.Status)
	assert.Contains(t, deep.Text, "an escape that may hide a session token")
	assert.Empty(t, e.up.sent())
}

// The caller's own token is hidden wherever the gateway keeps text, in any
// case and without its prefix: a tool's output, the arguments of a read it
// refused and of a write call, the name of a tool it refused, before and
// after the round's deadline, and the agent's text on the board, which every
// agent of the run reads.
func TestTheCallersOwnTokenIsHiddenWhereverTheGatewayKeepsText(t *testing.T) {
	e := newEnv(t)
	body := token[len("aswm_"):]
	e.up.set(func(u *upstream) {
		u.replies["get_flag"] = text(`{"flag":"x","on":true,"note":"` + strings.ToLower(body) + `","truncated":false}`)
	})

	read := e.call(t, "flags__get_flag", `{"flag":"x","window":"1h"}`)
	refused := e.call(t, "flags__get_flag", `{"flag":"`+body+`","window":"1h"}`)
	post := e.call(t, "post_task", `{"title":"check ASWM_`+body+`"}`)
	write := e.call(t, "flags__set_flag", `{"flag":"`+body+`","on":false,"_hypothesis":"h-1"}`)
	unknown := e.call(t, "tool_"+body, `{}`)
	e.clock.advance(151 * time.Second)
	late := e.call(t, "tool_"+body, `{}`)

	require.Equal(t, app.StatusOK, read.Status, read.Text)
	require.Equal(t, app.StatusRefused, refused.Status, refused.Text)
	require.Equal(t, app.StatusOK, post.Status, post.Text)
	_, out, _, _ := e.audit.Record(read.CallID)
	rec, _, _, _ := e.audit.Record(refused.CallID)
	written, _, _, _ := e.audit.Record(write.CallID)
	named, _, _, _ := e.audit.Record(unknown.CallID)
	named2, _, _, _ := e.audit.Record(late.CallID)
	kept := []string{read.Text, out.Output, string(rec.Args), string(written.Args), named.Tool, named2.Tool}
	for _, ev := range e.events(t) {
		if ev.Body.Task != nil {
			kept = append(kept, ev.Body.Task.Title)
		}
	}
	require.Len(t, kept, 7)
	for _, k := range kept {
		assert.NotContains(t, strings.ToLower(k), strings.ToLower(body))
	}
}

// A quote that spans what replaced a session token is refused, as one that
// spans any rule's replacement: the token could have been anything.
func TestAQuoteSpanningASessionTokensReplacementIsRefused(t *testing.T) {
	e := newEnv(t)
	e.up.set(func(u *upstream) {
		u.replies["get_flag"] = text(`{"flag":"x","on":true,"note":"key ` + sessionToken + ` used","truncated":false}`)
	})
	read := e.call(t, "flags__get_flag", `{"flag":"x","window":"1h"}`)
	require.Equal(t, app.StatusOK, read.Status, read.Text)

	r := e.call(t, "post_evidence", fmt.Sprintf(`{"claim":"a key","call_id":%q,"quote":"key %s used"}`, read.CallID, config.SessionTokenReplacement))

	assert.Equal(t, app.StatusRefused, r.Status)
	assert.Contains(t, r.Text, "spans text the redaction rules hid")
}

// A read call whose arguments hide a session token behind a backslash escape
// is refused, and its record keeps no arguments: the gateway cannot redact
// what an escape hides, and a stored copy would show the token to whoever
// decodes it again.
func TestAReadCallWhoseArgumentsHideATokenBehindAnEscapeKeepsNoArguments(t *testing.T) {
	e := newEnv(t)
	for _, c := range behindAnEscape {
		r := e.call(t, "flags__get_flag", `{"flag":"`+c.written+`","window":"1h"}`)

		assert.Equal(t, app.StatusRefused, r.Status, c.name)
		assert.Contains(t, r.Text, "an escape that may hide a session token", c.name)
		rec, out, _, _ := e.audit.Record(r.CallID)
		assert.Equal(t, "null", string(rec.Args), c.name)
		assertNoToken(t, []string{string(rec.Args), out.Output, r.Text}, c.decoded)
	}
	assert.Empty(t, e.up.sent())
}

// A board call whose arguments hide a session token behind a backslash escape
// is refused, and its record keeps no arguments, though the board refuses the
// text only after the record is made.
func TestABoardCallWhoseArgumentsHideATokenBehindAnEscapeKeepsNoArguments(t *testing.T) {
	e := newEnv(t)
	for _, c := range behindAnEscape {
		r := e.call(t, "post_task", `{"title":"check `+c.written+`"}`)

		assert.Equal(t, app.StatusRefused, r.Status, c.name)
		ids := e.audit.Calls()
		rec, out, _, _ := e.audit.Record(ids[len(ids)-1])
		assert.Equal(t, "post_task", rec.Tool, c.name)
		assert.Equal(t, "null", string(rec.Args), c.name)
		assertNoToken(t, []string{string(rec.Args), out.Output, r.Text}, c.decoded)
	}
}

// A write call whose arguments hide a session token behind a backslash escape
// keeps no arguments in its record either.
func TestAWriteCallWhoseArgumentsHideATokenBehindAnEscapeKeepsNoArguments(t *testing.T) {
	e := newEnv(t)
	for _, c := range behindAnEscape {
		r := e.call(t, "flags__set_flag", `{"flag":"`+c.written+`","on":false,"_hypothesis":"h-1"}`)

		assert.Equal(t, app.StatusRefused, r.Status, c.name)
		rec, out, _, _ := e.audit.Record(r.CallID)
		assert.Equal(t, "null", string(rec.Args), c.name)
		assertNoToken(t, []string{string(rec.Args), out.Output, r.Text}, c.decoded)
	}
}

// A write call whose arguments hide what a redaction rule matches behind a
// backslash escape, a session token or any other match, is refused, and no
// proposal is made: a person would be shown an action whose arguments the
// record cannot show. The record keeps no arguments, and no read is made for
// the call.
func TestAWriteCallWhoseArgumentsHideAMatchBehindAnEscapeIsRefused(t *testing.T) {
	e := newEnv(t)
	withPolicy(t, e, `    set_flag:
      schemaHash: "`+hash(t, setFlagLimit)+`"
      effect: write
      redact: [default-secrets]
      target: [flag]
      scope: {flag: hypothesis.flag}
      candidates:
        flag: {tool: list_flags, field: "$[*].name"}
      summary: Set {args.flag}
`)
	e.up.set(func(u *upstream) { u.defs[2] = setFlagLimit })
	e.flags("x")
	h := e.hypothesis(t, "x")
	plain := e.call(t, "flags__set_flag", `{"flag":"x","limit":"7","_hypothesis":"`+h+`"}`)
	require.Equal(t, app.StatusRecorded, plain.Status, plain.Text)
	sent := len(e.up.sent())

	for _, c := range slices.Concat(hiddenRules, behindAnEscape) {
		r := e.call(t, "flags__set_flag", `{"flag":"x","limit":"`+c.written+`","_hypothesis":"`+h+`"}`)

		assert.Equal(t, app.StatusRefused, r.Status, c.name)
		assert.Equal(t, escapedReason, r.Text, c.name)
		rec, out, _, _ := e.audit.Record(r.CallID)
		assert.Equal(t, "null", string(rec.Args), c.name)
		assert.Equal(t, escapedReason, out.Output, c.name)
	}
	cited := e.call(t, "flags__set_flag", `{"flag":"x","limit":"7","_hypothesis":["`+hiddenRules[1].written+`"]}`)
	assert.Equal(t, app.StatusRefused, cited.Status, "the hypothesis the call cites is one of its arguments")
	assert.Equal(t, escapedReason, cited.Text)
	assert.Len(t, e.proposals(t), 1, "the plain call's, and none for a call that hides a match")
	assert.Len(t, e.up.sent(), sent, "no read is made for a call that is refused")
}

// The name of a tool the session cannot call is stored as a placeholder when
// it holds a session token behind an escape: the name is the agent's own
// text, which the gateway cannot redact then. A name nothing answers to, and
// one asked for after the round's deadline, are both stored so.
func TestTheNameOfAToolHoldingATokenBehindAnEscapeIsNotStored(t *testing.T) {
	e := newEnv(t)
	var unknown, late []*app.Response
	for _, c := range behindAnEscape {
		unknown = append(unknown, e.call(t, "tool_"+c.decoded, `{}`))
	}
	e.clock.advance(151 * time.Second)
	for _, c := range behindAnEscape {
		late = append(late, e.call(t, "tool_"+c.decoded, `{}`))
	}

	for i, c := range behindAnEscape {
		assert.Contains(t, unknown[i].Text, "no tool by that name", c.name)
		assert.Contains(t, late[i].Text, "the round has ended", c.name)
		for _, r := range []*app.Response{unknown[i], late[i]} {
			rec, out, _, _ := e.audit.Record(r.CallID)
			assert.Equal(t, escapedName, rec.Tool, c.name)
			assertNoToken(t, []string{rec.Tool, string(rec.Args), out.Output, r.Text}, c.decoded)
		}
	}
}

// A refusal never quotes text that hides a session token behind an escape: an
// argument's key, the key of a repeat, an id the board looked for, or the
// hypothesis a write call cites. Its reason is one sentence of the gateway's
// own, stored and answered.
func TestARefusalNeverQuotesATokenBehindAnEscape(t *testing.T) {
	e := newEnv(t)
	var sent []string
	for _, c := range behindAnEscape {
		sent = append(sent, c.decoded)
		for _, call := range []struct{ site, tool, args string }{
			{"an undeclared key of a read call", "flags__get_flag", `{"` + c.written + `":"x","flag":"x","window":"1h"}`},
			{"a repeated key of a read call", "flags__get_flag", `{"` + c.written + `":"x","` + strings.ToLower(c.written) + `":"y","flag":"x","window":"1h"}`},
			{"an undeclared key of a board call", "post_task", `{"` + c.written + `":"x","title":"check"}`},
			{"an undeclared key of a write call", "flags__set_flag", `{"` + c.written + `":"x","flag":"x","_hypothesis":"h-1"}`},
			{"an id the board looked for", "retract_evidence", `{"evidence_id":"` + c.written + `"}`},
			{"the hypothesis a write call cites", "flags__set_flag", `{"flag":"x","_hypothesis":"` + c.written + `"}`},
		} {
			what := c.name + ", in " + call.site

			r := e.call(t, call.tool, call.args)

			assert.Equal(t, app.StatusRefused, r.Status, what)
			assert.Equal(t, escapedReason, r.Text, what)
			ids := e.audit.Calls()
			_, out, _, _ := e.audit.Record(ids[len(ids)-1])
			assert.Equal(t, escapedReason, out.Output, what)
		}
	}
	for _, id := range e.audit.Calls() {
		rec, out, _, _ := e.audit.Record(id)
		assertNoToken(t, []string{string(rec.Args), rec.Tool, out.Output}, sent...)
	}
}

// An escape that hides nothing a rule matches costs a record nothing: the
// arguments and the name of a tool are stored, as they were written.
func TestAnEscapeThatHidesNothingKeepsTheRecordWhole(t *testing.T) {
	e := newEnv(t)

	read := e.call(t, "flags__get_flag", `{"flag":"a\\nb","window":"1h"}`)
	named := e.call(t, `tool_a\n`, `{}`)

	require.Equal(t, app.StatusOK, read.Status, read.Text)
	rec, _, _, _ := e.audit.Record(read.CallID)
	assert.JSONEq(t, `{"flag":"a\\nb","window":"1h"}`, string(rec.Args))
	rec, _, _, _ = e.audit.Record(named.CallID)
	assert.Equal(t, `tool_a\n`, rec.Tool)
}

// A read call whose arguments hide what a redaction rule matches behind a
// backslash escape is refused, whether the rule is one of the tool's or
// applies always: the gateway sends a server the arguments as the agent wrote
// them, and cannot redact what an escape hides. The server gets no call, and
// the record keeps no arguments.
func TestAReadCallWhoseArgumentsHideAMatchBehindAnEscapeIsRefused(t *testing.T) {
	e := newEnv(t)
	for _, c := range hiddenRules {
		r := e.call(t, "flags__get_flag", `{"flag":"`+c.written+`","window":"1h"}`)

		assert.Equal(t, app.StatusRefused, r.Status, c.name)
		assert.Equal(t, escapedReason, r.Text, c.name)
		rec, out, _, _ := e.audit.Record(r.CallID)
		assert.Equal(t, "null", string(rec.Args), c.name)
		assert.Equal(t, escapedReason, out.Output, c.name)
	}
	assert.Empty(t, e.up.sent(), "no call reaches the server")
}

// A quote of what a read was given cannot be posted as evidence when the read
// hid a rule's match behind an escape: the read is refused, so it never
// happened, and the server's answer to it, which repeats the agent's words,
// does not exist to cite.
func TestAQuoteOfWhatARefusedReadWasGivenIsNoEvidence(t *testing.T) {
	e := newEnv(t)
	e.up.set(func(u *upstream) {
		u.replies["get_flag"] = text(`{"flag":"x","on":false,"note":"the cart service leaks memory","truncated":false}`)
	})
	read := e.call(t, "flags__get_flag", `{"flag":"the cart service leaks memory `+hiddenRules[0].written+`","window":"1h"}`)
	assert.Equal(t, app.StatusRefused, read.Status, read.Text)

	r := e.call(t, "post_evidence", fmt.Sprintf(`{"claim":"the cart service leaks memory","call_id":%q,"quote":"the cart service leaks memory"}`, read.CallID))

	assert.Equal(t, app.StatusRefused, r.Status, r.Text)
	assert.Contains(t, r.Text, "cannot be cited")
	for _, ev := range e.events(t) {
		assert.Nil(t, ev.Body.Evidence)
	}
}
