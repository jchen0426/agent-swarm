package app_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/actions/app"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
)

// sessionToken is a token of a session at the LLM gateway, as an agent could
// paste one anywhere.
const sessionToken = "aswl_0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefg"

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
// sends a server the arguments as the agent wrote them, and an escape does
// not hide the token.
func TestAReadCallWhoseArgumentsHoldASessionTokenIsRefused(t *testing.T) {
	e := newEnv(t)
	for _, args := range []string{
		`{"flag":"` + sessionToken + `","window":"1h"}`,
		`{"flag":"aswl_0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefg","window":"1h"}`,
	} {
		r := e.call(t, "flags__get_flag", args)

		assert.Equal(t, app.StatusRefused, r.Status, args)
		assert.Contains(t, r.Text, "session token", args)
		rec, _, _, _ := e.audit.Record(r.CallID)
		assert.NotContains(t, string(rec.Args), sessionToken, args)
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
