package app_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/actions/limits"
	"github.com/jchen0426/agent-swarm/internal/llm/app"
	"github.com/jchen0426/agent-swarm/internal/llm/cost"
)

// errorType reads the Messages API's error type from a reply.
func errorType(t *testing.T, r *app.Reply) string {
	t.Helper()
	var body struct {
		Error struct{ Type string } `json:"error"`
	}
	require.NoError(t, json.Unmarshal(r.Body, &body), string(r.Body))
	return body.Error.Type
}

// A call is recorded before it reaches the vendor, in the form it is sent
// in, then its outcome: the tokens it used, what they cost and how the
// vendor served it. The runtime gets the vendor's answer as it was sent.
func TestACallIsRecordedBeforeItIsForwardedThenItsOutcome(t *testing.T) {
	e := newEnv(t)

	r := e.post(t, request)

	require.Equal(t, 200, r.Status, string(r.Body))
	assert.Equal(t, string(ok().Body), string(r.Body))
	assert.Equal(t, "req_1", r.Header["Request-Id"])
	assert.Equal(t, []bool{true}, e.vendor.recorded, "the record existed when the vendor was called")
	rec, o := e.last(t)
	sum := sha256.Sum256([]byte(e.vendor.bodies[0]))
	assert.Equal(t, e.vendor.bodies[0], rec.Request, "the record holds what was sent")
	assert.Equal(t, hex.EncodeToString(sum[:]), rec.RequestDigest)
	assert.Equal(t, int64(len(e.vendor.bodies[0])), rec.RequestBytes)
	assert.Equal(t, model, rec.Model)
	assert.Equal(t, "run-1", rec.Run)
	assert.Equal(t, "flag-investigator", rec.Agent)
	assert.Equal(t, 2, rec.Round)
	assert.Equal(t, cost.Primary, rec.Class)
	assert.Equal(t, app.StatusOK, o.Status)
	assert.Equal(t, cost.Usage{Input: 1000, CacheWrite5m: 400, CacheRead: 20000, Output: 300}, o.Usage)
	assert.Equal(t, int64(500_000_000), o.Spend, "pico-USD")
	assert.Equal(t, int64(5000), o.Weighted)
	assert.Equal(t, 1, o.Attempts)
	assert.Contains(t, o.Details, `"id":"msg_1"`)
	assert.NotContains(t, o.Completion, "sk-")
}

// The vendor gets the gateway's own encoding of what it checked, never the
// client's bytes, at the standard service tier.
func TestTheVendorGetsTheGatewaysEncoding(t *testing.T) {
	e := newEnv(t)

	e.post(t, request)

	assert.NotEqual(t, request, e.vendor.bodies[0])
	assert.True(t, strings.HasPrefix(e.vendor.bodies[0], `{"max_tokens":1024,"messages":`), e.vendor.bodies[0])
	assert.Contains(t, e.vendor.bodies[0], `"service_tier":"standard_only"`)
}

// Secrets never reach the vendor, the session's own token included, and
// the record holds the completion redacted; the runtime gets the
// completion as the vendor wrote it.
func TestSecretsNeverReachTheVendor(t *testing.T) {
	e := newEnv(t)
	body := strings.NewReplacer("You investigate incidents.", "Use sk-aaaaaaaa.", "Find why checkout fails.", "My token is "+token+"?").Replace(request)

	r := e.post(t, body)

	require.Equal(t, 200, r.Status, string(r.Body))
	assert.NotContains(t, e.vendor.bodies[0], "sk-")
	assert.NotContains(t, e.vendor.bodies[0], token)
	assert.Contains(t, e.vendor.bodies[0], "[redacted:api-keys]")
	assert.Contains(t, e.vendor.bodies[0], "[redacted:session-token]")
	rec, o := e.last(t)
	assert.NotContains(t, rec.Request, "sk-")
	assert.NotContains(t, o.Completion, "sk-")
	assert.Contains(t, string(r.Body), "sk-zzzzzzzz", "the runtime gets the answer as sent")
}

// A tool's JSON output reaches the vendor as the MCP gateway showed it, and
// the record holds what was sent. Read as text, the output's escaped quote
// closes a key, so that api_key and what follows read as a member that the
// output does not hold; the gateway reads it as JSON, value by value, as the
// MCP gateway does.
func TestAJSONOutputIsForwardedAndRecordedAsShown(t *testing.T) {
	e := newEnv(t)
	e.configure(t, defaultSecrets)
	shown, err := json.Marshal(`{"lines":["level=info","api_key\": \"abcd1234efgh"]}`)
	require.NoError(t, err)
	require.Equal(t, 200, e.post(t, request).Status)

	r := e.post(t, strings.Replace(next, `"content": "errors rose"`, `"content": `+string(shown), 1))

	require.Equal(t, 200, r.Status, string(r.Body))
	assert.Contains(t, e.vendor.bodies[1], `"content":`+string(shown))
	rec, _ := e.last(t)
	assert.Equal(t, e.vendor.bodies[1], rec.Request, "the record holds what was sent")
}

// A thinking block the gateway gave a session comes back with the session's
// next request; another session, or the same after the gateway restarted,
// cannot send it.
func TestAThinkingBlockGoesBackOnlyFromTheSessionThatGotIt(t *testing.T) {
	e := newEnv(t)
	require.Equal(t, 200, e.post(t, request).Status)
	other := e.session
	other.Run = "run-other"
	e.sessions.Put("other-token", &other)

	back := e.post(t, next)
	stolen := e.send(t, "other-token", next)
	e.restart(t)
	afterRestart := e.post(t, next)

	assert.Equal(t, 200, back.Status, string(back.Body))
	assert.Contains(t, e.vendor.bodies[1], `"signature":"EqQBCkgIBRAB"`, "the block goes back unchanged")
	assert.Equal(t, 400, stolen.Status)
	assert.Equal(t, 400, afterRestart.Status)
	assert.Contains(t, string(afterRestart.Body), "thinking block")
}

// The ids of the tool calls an answer holds come back with their results in
// the session's next request whatever a rule matches in them: the vendor
// wrote them, and an id it wrote is no channel for the agent's text. Another
// session, or the same after the gateway restarted, cannot send an id that
// holds what a rule hides. The record of the request redacts the ids too.
func TestAToolCallsIdTheGatewayGaveComesBackWhateverARuleMatchesInIt(t *testing.T) {
	const id = "toolu_sk-aaaaaaaa"
	e := newEnv(t)
	e.vendor.answers = []*app.Answer{withToolCall(ok(), id)}
	require.Equal(t, 200, e.post(t, request).Status)
	other := e.session
	other.Run = "run-other"
	e.sessions.Put("other-token", &other)

	back := e.post(t, echoing(id))
	rec, _ := e.last(t)
	stolen := e.send(t, "other-token", echoing(id))
	e.restart(t)
	afterRestart := e.post(t, echoing(id))

	require.Equal(t, 200, back.Status, string(back.Body))
	assert.Contains(t, e.vendor.bodies[1], `"id":"`+id+`"`, "the vendor gets the id it gave")
	assert.Contains(t, e.vendor.bodies[1], `"tool_use_id":"`+id+`"`)
	assert.NotContains(t, rec.Request, "sk-", "the record redacts the id")
	assert.Equal(t, 400, stolen.Status)
	assert.Equal(t, 400, afterRestart.Status)
	for _, r := range []*app.Reply{stolen, afterRestart} {
		assert.Contains(t, string(r.Body), "redaction rule")
		assert.NotContains(t, string(r.Body), "sk-aaaaaaaa", "the refusal does not repeat the id")
	}
	assert.Equal(t, 2, e.vendor.attempts())
}

// An answer that is withheld gives the session nothing to send back: not the
// ids of its tool calls, as not its thinking blocks.
func TestTheIdsOfAWithheldAnswerAreNotRemembered(t *testing.T) {
	const id = "toolu_sk-aaaaaaaa"
	for name, c := range map[string]struct {
		answer    *app.Answer
		storeDown bool
		status    int
	}{
		"an answer that cannot be recorded":  {withToolCall(ok(), id), true, 503},
		"an answer whose text hides a match": {withToolCall(saying(`Saw s\\u006b-zzzzzzzz in the logs.`), id), false, 502},
	} {
		e := newEnv(t)
		e.vendor.answers = []*app.Answer{c.answer}
		if c.storeDown {
			e.calls.SetFailEnd(errors.New("the store is down"))
		}

		r := e.post(t, request)
		e.calls.SetFailEnd(nil)
		back := e.post(t, echoing(id))

		assert.Equal(t, c.status, r.Status, name)
		assert.Equal(t, 400, back.Status, name)
		assert.Contains(t, string(back.Body), "redaction rule", name)
	}
}

// The gateway keeps the ids of the tool calls it gave a session as it keeps
// its thinking blocks: at most 1,024 of them, the oldest forgotten first.
func TestASessionKeepsOnlyTheLastToolCallIdsItWasGiven(t *testing.T) {
	id := func(i int) string { return fmt.Sprintf("toolu_sk-%08d", i) }
	var ids []string
	for i := range 1025 {
		ids = append(ids, id(i))
	}
	e := newEnv(t)
	e.vendor.answers = []*app.Answer{toolCalls(ids...), finished()}
	require.Equal(t, 200, e.post(t, request).Status)

	forgotten := e.post(t, echoing(id(0)))
	oldest := e.post(t, echoing(id(1)))
	newest := e.post(t, echoing(id(1024)))

	assert.Equal(t, 400, forgotten.Status, "the oldest of 1,025 is forgotten")
	assert.Equal(t, 200, oldest.Status, string(oldest.Body))
	assert.Equal(t, 200, newest.Status, string(newest.Body))
}

// An id that no request could send back is not remembered: one longer than
// 128 bytes, empty, or with a character outside letters, digits, _ and -. It
// would take the place of an id that can come back, and the bytes the gateway
// keeps for a session would depend on the vendor alone.
func TestAnIdNoRequestCouldSendBackIsNotRemembered(t *testing.T) {
	id := func(i int) string { return fmt.Sprintf("toolu_sk-%08d", i) }
	var ids []string
	for i := range 1024 {
		ids = append(ids, id(i))
	}
	// Remembered, these would push the first three ids out.
	ids = append(ids, strings.Repeat("x", 129), "toolu.9", "")
	e := newEnv(t)
	e.vendor.answers = []*app.Answer{toolCalls(ids...), finished()}
	require.Equal(t, 200, e.post(t, request).Status)

	first := e.post(t, echoing(id(0)))
	third := e.post(t, echoing(id(2)))
	last := e.post(t, echoing(id(1023)))

	assert.Equal(t, 200, first.Status, string(first.Body))
	assert.Equal(t, 200, third.Status, string(third.Body))
	assert.Equal(t, 200, last.Status, string(last.Body))
}

// Every request a session sends is recorded, also one the gateway refuses,
// with its reason; nothing refused reaches the vendor.
func TestEveryRefusalIsRecorded(t *testing.T) {
	cases := []struct {
		name                string
		method, path, query string
		header              map[string][]string
		body                string
		length              int64
		status              int
		kind                string
	}{
		{name: "another path", method: "POST", path: "/v1/files", body: request, status: 404, kind: "not_found_error"},
		{name: "count tokens", method: "POST", path: "/v1/messages/count_tokens", body: request, status: 404, kind: "not_found_error"},
		{name: "another method", method: "GET", path: "/v1/messages", status: 404, kind: "not_found_error"},
		{name: "a query", method: "POST", path: "/v1/messages", query: "beta=true", body: request, status: 404, kind: "not_found_error"},
		{name: "too large", method: "POST", path: "/v1/messages", body: strings.Repeat(" ", 64<<10+1), length: 1 << 20, status: 413, kind: "request_too_large"},
		{name: "a secret in an id", method: "POST", path: "/v1/messages", body: strings.Replace(request, `"content": "Find why checkout fails."}]`,
			`"content": "Find why checkout fails."}, {"role": "assistant", "content": [{"type": "tool_use", "id": "`+token+`", "name": "metrics__query", "input": {}}]},
			 {"role": "user", "content": [{"type": "tool_result", "tool_use_id": "`+token+`", "content": "none"}]}]`, 1), status: 400, kind: "invalid_request_error"},
		{name: "another model", method: "POST", path: "/v1/messages", body: strings.Replace(request, model, "claude-opus-5-5", 1), status: 400, kind: "invalid_request_error"},
		{name: "a vendor tool", method: "POST", path: "/v1/messages", body: strings.Replace(request, `"tools": [{`, `"tools": [{"type": "web_search_20250305", `, 1), status: 400, kind: "invalid_request_error"},
		{name: "a field outside the list", method: "POST", path: "/v1/messages", body: strings.Replace(request, `{"model"`, `{"mcp_servers": [], "model"`, 1), status: 400, kind: "invalid_request_error"},
		{name: "a beta", method: "POST", path: "/v1/messages", header: map[string][]string{"Anthropic-Beta": {"files-api-2025-04-14"}}, body: request, status: 400, kind: "invalid_request_error"},
		{name: "a compressed body", method: "POST", path: "/v1/messages", header: map[string][]string{"Content-Encoding": {"gzip"}}, body: request, status: 400, kind: "invalid_request_error"},
	}
	for _, c := range cases {
		e := newEnv(t)
		h := headers()
		for k, v := range c.header {
			h[k] = v
		}

		r := e.gw.Messages(t.Context(), &app.Inbound{Method: c.method, Path: c.path, Query: c.query, Token: token, Header: h, Body: []byte(c.body), Length: c.length})

		assert.Equal(t, c.status, r.Status, c.name)
		assert.Equal(t, c.kind, errorType(t, r), c.name)
		assert.Equal(t, "false", r.Header["X-Should-Retry"], c.name)
		assert.Zero(t, e.vendor.attempts(), c.name)
		rec, o := e.last(t)
		assert.Equal(t, app.StatusRefused, o.Status, c.name)
		assert.NotEmpty(t, o.Reason, c.name)
		assert.NotContains(t, rec.Request+o.Reason, token, c.name)
		assert.Zero(t, rec.Estimate, c.name)
		if c.length > 0 {
			assert.Equal(t, c.length, rec.RequestBytes, c.name)
		}
	}
}

// A refused request's record keeps at most its first 4 KiB, redacted, with
// the size it came in and the digest of its redacted JSON in full: a
// sandbox cannot fill the audit store with refused requests.
func TestARefusedRequestIsRecordedShort(t *testing.T) {
	e := newEnv(t)
	body := strings.Replace(request, `{"model"`, `{"metadata": {"user_id": "sk-cccccccc", "zz_pad": "`+strings.Repeat("x", 20000)+`"}, "model"`, 1)

	e.post(t, body)

	rec, _ := e.last(t)
	assert.LessOrEqual(t, len(rec.Request), 4096+3)
	assert.Contains(t, rec.Request, `"user_id":"[redacted:api-keys]"`)
	assert.NotContains(t, rec.Request, "sk-")
	assert.Equal(t, int64(len(body)), rec.RequestBytes)
	assert.Len(t, rec.RequestDigest, 64)
}

// A refusal of a path quotes the path and query only when they are whole, at
// most 64 bytes and plain; otherwise it says how long they are and quotes
// none of them. The record redacts the reason as written, and a cut copy
// leaves most of a secret in it, as does an escaped one.
func TestARefusedPathIsQuotedOnlyWhole(t *testing.T) {
	for name, c := range map[string]struct{ path, query, says string }{
		"a short path":  {"/v1/files", "", `"/v1/files?"`},
		"a short query": {"/v1/messages", "beta=true", `"/v1/messages?beta=true"`},
		// A cut at 64 bytes leaves sk-aaaaaaa, which no rule matches.
		"a long path":     {"/v1/" + strings.Repeat("x", 50) + "sk-aaaaaaaa", "", "a value of 66 bytes"},
		"a long query":    {"/v1/messages", strings.Repeat("y", 41) + "sk-bbbbbbbb", "a value of 65 bytes"},
		"an escaped path": {`/v1/s\u006b-cccccccc`, "", "a value of 21 bytes"},
	} {
		e := newEnv(t)

		r := e.gw.Messages(t.Context(), &app.Inbound{Method: "POST", Path: c.path, Query: c.query, Token: token, Header: headers(), Body: []byte(request)})

		assert.Equal(t, 404, r.Status, name)
		assert.Equal(t, "not_found_error", errorType(t, r), name)
		assert.Zero(t, e.vendor.attempts(), name)
		_, o := e.last(t)
		assert.Equal(t, app.StatusRefused, o.Status, name)
		assert.Contains(t, o.Reason, c.says, name)
		for _, part := range []string{"sk-", "xxxx", "yyyy", "u006b"} {
			assert.NotContains(t, o.Reason, part, name)
		}
	}
}

// A refusal quotes the method as it quotes the path: only when it is whole, at
// most 64 bytes and plain; otherwise it says how long the method is. A client
// can send a method as long as a request line may be, and the reason is
// recorded, so a method written whole would put a megabyte into the audit
// store for a request that costs nothing.
func TestARefusedMethodIsQuotedOnlyWhole(t *testing.T) {
	for name, c := range map[string]struct{ method, says string }{
		"a short method":     {"GET", `not "GET" "/v1/messages?"`},
		"a long method":      {strings.Repeat("X", 60) + "sk-aaaaaaaa", "a value of 71 bytes"},
		"a very long method": {strings.Repeat("Y", 1<<20) + "sk-bbbbbbbb", "a value of 1048587 bytes"},
		"an escaped method":  {"GET\n", "a value of 4 bytes"},
	} {
		e := newEnv(t)

		r := e.gw.Messages(t.Context(), &app.Inbound{Method: c.method, Path: "/v1/messages", Token: token, Header: headers(), Body: []byte(request)})

		assert.Equal(t, 404, r.Status, name)
		assert.Equal(t, "not_found_error", errorType(t, r), name)
		assert.Less(t, len(r.Body), 512, name)
		assert.Zero(t, e.vendor.attempts(), name)
		_, o := e.last(t)
		assert.Equal(t, app.StatusRefused, o.Status, name)
		assert.Contains(t, o.Reason, c.says, name)
		assert.Less(t, len(o.Reason), 256, name)
		for _, part := range []string{"sk-", "XXXX", "YYYY"} {
			assert.NotContains(t, o.Reason, part, name)
		}
	}
}

// A refusal's reason quotes a value of the client's only when the session's
// redaction rules leave it as it is on its own. The reason is stored redacted
// as a whole sentence, in a record kept for a year, and a rule anchored to a
// whole value, such as ^AKIA[A-Z0-9]{16}$, matches the value alone but not
// inside the sentence: such a value is given by its length, as the record's
// request hides it. A short plain value no rule hides is still quoted whole.
func TestARefusalQuotesNoValueTheSessionsRulesHide(t *testing.T) {
	e := newEnv(t)
	e.configure(t, anchoredKeys)
	const key = "AKIAABCDEFGHIJKLMNOP"
	for name, in := range map[string]*app.Inbound{
		"a field":   {Method: "POST", Path: app.Path, Body: []byte(strings.Replace(request, `{"model"`, `{"`+key+`": 1, "model"`, 1))},
		"the model": {Method: "POST", Path: app.Path, Body: []byte(strings.Replace(request, `"model": "claude-haiku-5-5"`, `"model": "`+key+`"`, 1))},
		"a method":  {Method: key, Path: app.Path},
	} {
		in.Token, in.Header = token, headers()

		r := e.gw.Messages(t.Context(), in)

		assert.NotContains(t, string(r.Body), key, name)
		rec, o := e.last(t)
		assert.Equal(t, app.StatusRefused, o.Status, name)
		assert.Contains(t, o.Reason, "a value of 20 bytes", name)
		assert.NotContains(t, o.Reason, key, name)
		assert.NotContains(t, rec.Request, key, name)
	}
	e.post(t, strings.Replace(request, `{"model"`, `{"metadata": {}, "model"`, 1))
	_, o := e.last(t)
	assert.Contains(t, o.Reason, `"metadata"`, "a value no rule hides is quoted whole")
}

// A session refused too often in a round is shut out until the next: one
// more refusal is recorded, then its requests are refused unrecorded.
func TestASessionRefusedTooOftenIsShutOutForTheRound(t *testing.T) {
	e := newEnv(t, func(o *app.Options) { o.MaxRefusals = 3 })
	bad := strings.Replace(request, `{"model"`, `{"metadata": {}, "model"`, 1)
	for range 3 {
		require.Equal(t, 400, e.post(t, bad).Status)
	}

	first := e.post(t, request)
	recorded := len(e.calls.Records())
	second := e.post(t, request)
	e.session.Round++
	e.sessions.Put(token, &e.session)
	nextRound := e.post(t, request)

	assert.Equal(t, 429, first.Status)
	assert.Equal(t, 429, second.Status)
	assert.Equal(t, 4, recorded, "three refusals and the shut-out")
	assert.Equal(t, 200, nextRound.Status, string(nextRound.Body))
	assert.Equal(t, 1, e.vendor.attempts())
}

// A request sent after the session's round ended is refused.
func TestACallAfterTheRoundEndedIsRefused(t *testing.T) {
	e := newEnv(t)
	e.session.Deadline = time.Now().Add(-time.Second)
	e.sessions.Put(token, &e.session)

	r := e.post(t, request)

	assert.Equal(t, 403, r.Status)
	assert.Contains(t, string(r.Body), "round has ended")
	assert.Zero(t, e.vendor.attempts())
}

// A token that names no live session is refused, and nothing is recorded:
// nothing names a run to record it under.
func TestATokenOfNoLiveSessionIsRefused(t *testing.T) {
	e := newEnv(t)
	e.sessions.End(token)

	r := e.post(t, request)

	assert.Equal(t, 401, r.Status)
	assert.Equal(t, "authentication_error", errorType(t, r))
	assert.Empty(t, e.calls.Records())
	assert.Zero(t, e.vendor.attempts())
}

// Once a run has spent its budget, its next call is refused and recorded,
// while another run's calls go on.
func TestARunsBudgetStopsItsCalls(t *testing.T) {
	e := newEnv(t)
	e.session.Budget = 5000
	e.sessions.Put(token, &e.session)
	require.Equal(t, 200, e.post(t, request).Status)

	r := e.post(t, request)

	assert.Equal(t, 403, r.Status)
	assert.Equal(t, "permission_error", errorType(t, r))
	assert.Contains(t, string(r.Body), "budget")
	assert.Equal(t, 1, e.vendor.attempts())
	_, o := e.last(t)
	assert.Equal(t, app.StatusRefused, o.Status)
	other := e.session
	other.Run, other.Budget = "run-2", 1_000_000
	e.sessions.Put("other-token", &other)
	assert.Equal(t, 200, e.send(t, "other-token", request).Status)
}

// Comparison and probation runs are cut at 80% of the daily cap; primary
// runs go on until the cap (spec Section 7).
func TestShadowRunsAreCutFirst(t *testing.T) {
	e := newEnv(t, func(o *app.Options) { o.Limits.DailyCap = 12_000 })
	require.Equal(t, 200, e.post(t, request).Status, "5,000 spent today")
	shadow := e.session
	shadow.Run, shadow.Class = "run-shadow", cost.Shadow
	e.sessions.Put("shadow-token", &shadow)
	second := e.session
	second.Run = "run-second"
	e.sessions.Put("second-token", &second)

	require.Equal(t, 200, e.send(t, "shadow-token", request).Status, "under 80%: 10,000 spent then")
	cut := e.send(t, "shadow-token", request)
	primary := e.send(t, "second-token", request)
	capped := e.send(t, "second-token", request)

	assert.Equal(t, 403, cut.Status)
	assert.Contains(t, string(cut.Body), "80%")
	assert.Equal(t, 200, primary.Status, "a primary run goes on past 80%")
	assert.Equal(t, 403, capped.Status)
	assert.Contains(t, string(capped.Body), "daily token cap")
}

// A session makes one model call at a time: another sent while one is in
// flight is refused and recorded.
func TestASessionMakesOneCallAtATime(t *testing.T) {
	e := newEnv(t)
	e.vendor.block = make(chan struct{})
	var wg sync.WaitGroup
	var first *app.Reply
	wg.Go(func() { first = e.post(t, request) })
	require.Eventually(t, func() bool { return e.vendor.sent.Load() == 1 }, 5*time.Second, time.Millisecond)

	second := e.post(t, request)
	close(e.vendor.block)
	wg.Wait()

	assert.Equal(t, 429, second.Status)
	assert.Equal(t, "rate_limit_error", errorType(t, second))
	assert.Equal(t, 200, first.Status)
	assert.Equal(t, 200, e.post(t, request).Status, "the next call goes once the first ended")
}

// A failure another attempt may get past is retried, after the wait the
// vendor asks for when it is longer, within the round.
func TestTransientFailuresAreRetriedWithinTheRound(t *testing.T) {
	e := newEnv(t)
	e.vendor.answers = []*app.Answer{{Status: 529, Body: []byte(`{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`), RetryAfter: 30 * time.Millisecond}, ok()}
	start := time.Now()

	r := e.post(t, request)

	assert.Equal(t, 200, r.Status)
	assert.GreaterOrEqual(t, time.Since(start), 30*time.Millisecond, "the vendor's wait")
	_, o := e.last(t)
	assert.Equal(t, 2, o.Attempts)
	assert.Equal(t, app.StatusOK, o.Status)
}

// When the round leaves no time for the next wait, the last failure is
// answered at once, and recorded.
func TestNoAttemptOutlastsTheRound(t *testing.T) {
	e := newEnv(t)
	e.session.Deadline = time.Now().Add(300 * time.Millisecond)
	e.sessions.Put(token, &e.session)
	e.vendor.answers = []*app.Answer{{Status: 529, Body: []byte(`{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`), RetryAfter: 2 * time.Second}}
	start := time.Now()

	r := e.post(t, request)

	assert.Less(t, time.Since(start), 200*time.Millisecond, "no wait the round cannot hold")
	assert.Equal(t, 529, r.Status)
	assert.Equal(t, "false", r.Header["X-Should-Retry"])
	_, o := e.last(t)
	assert.Equal(t, 1, o.Attempts)
	assert.Equal(t, app.StatusError, o.Status)
	assert.Contains(t, o.Reason, "overloaded_error")
}

// No attempt is made once the request has ended: its client went during the
// wait before the next attempt, and a request that ended is not sent, as
// Decision 8 has it, though the vendor might answer the retry, and charge for
// it, with no one to read the answer. The call ends at its last failure,
// which cost nothing.
func TestNoAttemptIsMadeOnceTheRequestEnded(t *testing.T) {
	e := newEnv(t, func(o *app.Options) { o.Backoff = []time.Duration{300 * time.Millisecond} })
	e.vendor.answers = []*app.Answer{{Status: 529, Body: []byte(`{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`)}, ok()}
	ctx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	var r *app.Reply
	wg.Go(func() { r = e.sendWith(ctx, token, request) })
	require.Eventually(t, func() bool { return e.vendor.attempts() == 1 }, 5*time.Second, time.Millisecond)

	cancel()
	wg.Wait()

	assert.Equal(t, 502, r.Status)
	require.Eventually(t, func() bool { _, _, _, ended := e.calls.Record("m-1"); return ended }, 5*time.Second, 5*time.Millisecond)
	_, o, _, _ := e.calls.Record("m-1")
	assert.Equal(t, 1, e.vendor.attempts(), "no attempt after the request ended")
	assert.Equal(t, app.StatusError, o.Status)
	assert.Equal(t, 1, o.Attempts)
	assert.Zero(t, o.Weighted)
}

// A request that never left is tried again: it cost nothing.
func TestARequestThatNeverLeftIsTriedAgain(t *testing.T) {
	e := newEnv(t)
	e.vendor.errs = []error{fmt.Errorf("%w: connection refused", app.ErrNotSent)}

	r := e.post(t, request)

	assert.Equal(t, 200, r.Status)
	_, o := e.last(t)
	assert.Equal(t, 2, o.Attempts)
}

// An answer lost after the request reached the vendor is never tried
// again, and counts the call's worst case: the vendor may have charged
// for it.
func TestALostAnswerCountsItsWorstCase(t *testing.T) {
	e := newEnv(t)
	e.vendor.errs = []error{fmt.Errorf("%w: connection reset", app.ErrLost)}

	r := e.post(t, request)

	assert.Equal(t, 502, r.Status)
	assert.Equal(t, 1, e.vendor.attempts())
	rec, o := e.last(t)
	assert.Equal(t, app.StatusUnknown, o.Status)
	assert.Equal(t, cost.Estimate(rec.RequestBytes, 1024), o.Usage)
	assert.Equal(t, cost.Weighted(card.Spend(o.Usage), 100_000), o.Weighted)
	assert.Positive(t, o.Weighted)
}

// An answer whose tokens cannot be read counts the call's worst case too.
func TestAnAnswerWithoutUsageCountsItsWorstCase(t *testing.T) {
	e := newEnv(t)
	e.vendor.answers = []*app.Answer{{Status: 200, Body: []byte(`{"content": [], "stop_reason": "end_turn"}`)}}

	r := e.post(t, request)

	assert.Equal(t, 502, r.Status)
	_, o := e.last(t)
	assert.Equal(t, app.StatusUnknown, o.Status)
	assert.Positive(t, o.Weighted)
}

// The vendor's refusal is answered as the vendor sent it, and recorded
// redacted; it cost nothing.
func TestTheVendorsRefusalIsAnsweredAsSent(t *testing.T) {
	e := newEnv(t)
	vendorError := `{"type":"error","error":{"type":"invalid_request_error","message":"bad sk-dddddddd"}}`
	e.vendor.answers = []*app.Answer{{Status: 400, Body: []byte(vendorError)}}

	r := e.post(t, request)

	assert.Equal(t, 400, r.Status)
	assert.Equal(t, vendorError, string(r.Body))
	_, o := e.last(t)
	assert.Equal(t, app.StatusError, o.Status)
	assert.Contains(t, o.Reason, "the vendor answered 400")
	assert.NotContains(t, o.Reason, "sk-")
	assert.Zero(t, o.Weighted)
}

// An answer that cannot be recorded is not returned: it would be model
// output the audit store does not hold. The runtime is told not to repeat
// the call, which was made, and cannot send its thinking block back.
func TestAnAnswerThatCannotBeRecordedIsWithheld(t *testing.T) {
	e := newEnv(t)
	e.calls.SetFailEnd(errors.New("the store is down"))

	r := e.post(t, request)
	e.calls.SetFailEnd(nil)
	back := e.post(t, next)

	assert.Equal(t, 503, r.Status)
	assert.NotContains(t, string(r.Body), "toolu_9")
	assert.Equal(t, "false", r.Header["X-Should-Retry"])
	assert.Equal(t, 400, back.Status, "the withheld answer's thinking block is not the session's")
}

// An answer whose text hides what a rule matches behind an escape has no
// record that is safe to keep, so it is withheld as one that cannot be
// recorded is: the runtime is told not to repeat the call, which was made;
// the store holds the tokens the vendor reported, and none of the answer; and
// the answer's thinking block is not the session's, so it cannot come back.
func TestAnAnswerWhoseRecordWouldHideAMatchIsWithheld(t *testing.T) {
	e := newEnv(t)
	e.vendor.answers = []*app.Answer{saying(`Saw s\\u006b-zzzzzzzz in the logs.`)}

	r := e.post(t, request)
	_, o := e.last(t)
	spent, err := e.calls.Spent(t.Context(), "run-1")
	require.NoError(t, err)
	back := e.post(t, next)

	assert.Equal(t, 502, r.Status)
	assert.Equal(t, "api_error", errorType(t, r))
	assert.Equal(t, "false", r.Header["X-Should-Retry"])
	assert.NotContains(t, string(r.Body), "toolu_9")
	assert.NotContains(t, string(r.Body), "zzzzzzzz")
	assert.Equal(t, app.StatusError, o.Status)
	assert.Equal(t, cost.Usage{Input: 1000, CacheWrite5m: 400, CacheRead: 20000, Output: 300}, o.Usage)
	assert.Equal(t, int64(500_000_000), o.Spend, "pico-USD")
	assert.Equal(t, int64(5000), o.Weighted)
	assert.Equal(t, int64(5000), spent, "the run spent what the vendor reported")
	assert.Equal(t, 1, o.Attempts)
	assert.Empty(t, o.Completion)
	assert.Contains(t, o.Reason, "withheld")
	assert.NotContains(t, o.Reason, "zzzzzzzz")
	assert.Contains(t, o.Details, `"id":"msg_1"`)
	assert.Equal(t, 400, back.Status, "the withheld answer's thinking block is not the session's")
	assert.Contains(t, string(back.Body), "thinking block")
	assert.Equal(t, 1, e.vendor.attempts())
}

// An answer whose text holds escapes that hide nothing, a literal \n or a
// Windows path, is returned and recorded as any other, its thinking block
// remembered.
func TestAnAnswerWithOrdinaryEscapesIsReturnedAndRecorded(t *testing.T) {
	e := newEnv(t)
	e.vendor.answers = []*app.Answer{saying(`Saw C:\\Users\\new\\table end with \\n in the logs.`)}

	r := e.post(t, request)
	_, o := e.last(t)
	back := e.post(t, next)

	require.Equal(t, 200, r.Status, string(r.Body))
	assert.Equal(t, string(e.vendor.answers[0].Body), string(r.Body))
	assert.Equal(t, app.StatusOK, o.Status)
	assert.Contains(t, o.Completion, `C:\\Users\\new\\table end with \\n in the logs.`)
	assert.Equal(t, 200, back.Status, string(back.Body))
}

// While the audit store is down, nothing reaches the vendor (spec Section
// 8.2), and the runtime may try again.
func TestNothingIsForwardedWhileTheAuditStoreIsDown(t *testing.T) {
	e := newEnv(t)
	e.calls.SetFail(errors.New("the store is down"))

	r := e.post(t, request)

	assert.Equal(t, 503, r.Status)
	assert.Equal(t, "true", r.Header["X-Should-Retry"])
	assert.Zero(t, e.vendor.attempts())
}

// A model the deployment set no card for is refused: its spend could not
// be counted.
func TestAModelWithoutACardIsRefused(t *testing.T) {
	e := newEnv(t, func(o *app.Options) { o.Cards = map[string]cost.Card{} })

	r := e.post(t, request)

	assert.Equal(t, 403, r.Status)
	assert.Contains(t, string(r.Body), "price")
	assert.Zero(t, e.vendor.attempts())
}

// When the round ends before the vendor answers, the request is answered
// at once, and the call goes on until the vendor answers, so that what it
// cost is recorded.
func TestACallGoesOnPastTheRoundUntilItsCostIsKnown(t *testing.T) {
	e := newEnv(t)
	e.session.Deadline = time.Now().Add(100 * time.Millisecond)
	e.sessions.Put(token, &e.session)
	e.vendor.block = make(chan struct{})

	ctx, cancel := context.WithCancel(t.Context())
	r := e.gw.Messages(ctx, &app.Inbound{Method: "POST", Path: "/v1/messages", Token: token, Header: headers(), Body: []byte(request)})
	cancel() // as an HTTP server does once it has answered
	_, _, _, endedBefore := e.calls.Record("m-1")
	close(e.vendor.block)

	assert.Equal(t, 504, r.Status)
	assert.False(t, endedBefore)
	require.Eventually(t, func() bool { _, _, _, ended := e.calls.Record("m-1"); return ended }, 5*time.Second, 5*time.Millisecond)
	_, o, _, _ := e.calls.Record("m-1")
	assert.Equal(t, app.StatusOK, o.Status)
	assert.Equal(t, int64(5000), o.Weighted)
}

// When the run stops, the request is answered at once; the call's cost is
// still recorded when the vendor answers.
func TestARequestIsAnsweredWhenTheRunStops(t *testing.T) {
	e := newEnv(t)
	done := make(chan struct{})
	e.session.Done = done
	e.sessions.Put(token, &e.session)
	e.vendor.block = make(chan struct{})
	var r *app.Reply
	var wg sync.WaitGroup
	wg.Go(func() { r = e.post(t, request) })
	require.Eventually(t, func() bool { return e.vendor.sent.Load() == 1 }, 5*time.Second, time.Millisecond)
	start := time.Now()

	close(done)
	wg.Wait()
	close(e.vendor.block)

	assert.Less(t, time.Since(start), 2*time.Second)
	assert.Equal(t, 502, r.Status)
	require.Eventually(t, func() bool { _, _, _, ended := e.calls.Record("m-1"); return ended }, 5*time.Second, 5*time.Millisecond)
}

// When the vendor keeps failing, comparison and probation runs pause and
// primary runs fail at once (spec Sections 6.2 and 12.1).
func TestComparisonRunsPauseFirstWhenTheVendorFails(t *testing.T) {
	e := newEnv(t, func(o *app.Options) {
		o.Vendor = limits.Settings{Slots: 10, Failures: 1, CoolDown: time.Minute}
		o.Backoff = nil
	})
	e.vendor.answers = []*app.Answer{{Status: 529, Body: []byte(`{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`)}}
	require.Equal(t, 529, e.post(t, request).Status, "one failure degrades the vendor")
	shadow := e.session
	shadow.Run, shadow.Class = "run-shadow", cost.Shadow
	e.sessions.Put("shadow-token", &shadow)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()

	paused := e.gw.Messages(ctx, &app.Inbound{Method: "POST", Path: "/v1/messages", Token: "shadow-token", Header: headers(), Body: []byte(request)})
	waited := time.Since(start)
	failed := e.post(t, request)

	assert.GreaterOrEqual(t, waited, 100*time.Millisecond, "it waited until its request ended")
	assert.Equal(t, 502, paused.Status)
	assert.Contains(t, string(paused.Body), "before the call was made")
	assert.Equal(t, 503, failed.Status)
	assert.Equal(t, "overloaded_error", errorType(t, failed))
	assert.Equal(t, 1, e.vendor.attempts())
}

// Options that cannot hold are refused.
func TestOptionsMustHold(t *testing.T) {
	for name, change := range map[string]func(*app.Options){
		"no cap":       func(o *app.Options) { o.Limits.DailyCap = 0 },
		"no version":   func(o *app.Options) { o.Version = "" },
		"no body":      func(o *app.Options) { o.MaxBodyBytes = 0 },
		"no tokens":    func(o *app.Options) { o.MaxTokens = 0 },
		"no reference": func(o *app.Options) { o.Reference = 0 },
		"a bad card":   func(o *app.Options) { o.Cards = map[string]cost.Card{model: {}} },
		"no refusals":  func(o *app.Options) { o.MaxRefusals = 0 },
		"a bad wait":   func(o *app.Options) { o.Backoff = []time.Duration{-time.Second} },
		"a dear token": func(o *app.Options) { o.Reference = cost.MaxRate + 1 },
		"no last wait": func(o *app.Options) { o.LastCallWait = 0 },
	} {
		o := options()
		change(&o)

		_, err := app.New(&app.Ports{Now: time.Now}, &o)

		require.Error(t, err, name)
	}
}

// A call that goes on after its request was answered keeps its session's
// one call in flight: the session's next request, in the next round, waits
// for it to end, and the vendor never has two of the session's calls at
// once. A request sent while another waits is refused.
func TestTheNextRoundWaitsForTheCallTheLastRoundLeft(t *testing.T) {
	e := newEnv(t)
	e.session.Deadline = time.Now().Add(100 * time.Millisecond)
	e.sessions.Put(token, &e.session)
	e.vendor.block = make(chan struct{})
	require.Equal(t, 504, e.post(t, request).Status, "the round ended before the model answered")
	e.session.Round, e.session.Deadline = 3, time.Now().Add(5*time.Second)
	e.sessions.Put(token, &e.session)

	var next *app.Reply
	var wg sync.WaitGroup
	wg.Go(func() { next = e.post(t, request) })
	assert.Never(t, func() bool { return e.vendor.sent.Load() > 1 }, 100*time.Millisecond, 5*time.Millisecond, "the next call waits")
	busy := e.post(t, request)
	close(e.vendor.block)
	wg.Wait()

	assert.Equal(t, 429, busy.Status, "one request waits at a time")
	assert.Equal(t, 200, next.Status, string(next.Body))
	assert.Equal(t, int32(1), e.vendor.most.Load(), "never two of the session's calls at once")
	assert.Equal(t, 2, e.vendor.attempts())
}

// A request that ended before its call could start is not sent: nobody
// would read the answer. It is recorded, and costs nothing.
func TestARequestThatEndedIsNotSent(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	r := e.sendWith(ctx, token, request)

	assert.Equal(t, 502, r.Status)
	assert.Zero(t, e.vendor.attempts())
	_, o := e.last(t)
	assert.Equal(t, app.StatusError, o.Status)
	assert.Zero(t, o.Weighted)
}

// A comparison run's call paused while the vendor is failing waits no
// longer than its round.
func TestAPausedCallEndsWithItsRound(t *testing.T) {
	e := newEnv(t, func(o *app.Options) {
		o.Vendor = limits.Settings{Slots: 10, Failures: 1, CoolDown: 3 * time.Second}
		o.Backoff = nil
	})
	e.vendor.answers = []*app.Answer{{Status: 529, Body: []byte(`{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`)}}
	require.Equal(t, 529, e.post(t, request).Status, "one failure degrades the vendor")
	shadow := e.session
	shadow.Run, shadow.Class, shadow.Deadline = "run-shadow", cost.Shadow, time.Now().Add(100*time.Millisecond)
	e.sessions.Put("shadow-token", &shadow)
	start := time.Now()

	r := e.send(t, "shadow-token", request)

	assert.Less(t, time.Since(start), 2*time.Second, "not the three seconds the vendor is paused for")
	assert.Equal(t, 504, r.Status)
	assert.Equal(t, 1, e.vendor.attempts())
}

// A session lasts across rounds (spec Section 5.2): a thinking block the
// gateway gave it in one round may come back in a later one.
func TestAThinkingBlockGoesBackInALaterRound(t *testing.T) {
	e := newEnv(t)
	require.Equal(t, 200, e.post(t, request).Status)
	e.session.Round++
	e.sessions.Put(token, &e.session)

	r := e.post(t, next)

	assert.Equal(t, 200, r.Status, string(r.Body))
}

// The gateway keeps what it gave a session in use, however many other
// sessions come and go: past MaxSessions it forgets the session used least
// recently, never all at once.
func TestASessionInUseKeepsItsThinkingBlocks(t *testing.T) {
	e := newEnv(t)
	require.Equal(t, 200, e.post(t, request).Status)
	for i := range app.MaxSessions {
		other := e.session
		other.Run = fmt.Sprintf("other-%d", i)
		tok := fmt.Sprintf("token-%d", i)
		e.sessions.Put(tok, &other)
		e.stray(t, tok)
		if i == app.MaxSessions/2 {
			e.stray(t, token)
		}
	}

	r := e.post(t, next)

	assert.Equal(t, 200, r.Status, string(r.Body))
}

// A session's refusals are counted before they are recorded, so that the
// cap holds for requests sent at once.
func TestRefusalsAreCappedWhenSentAtOnce(t *testing.T) {
	e := newEnv(t, func(o *app.Options) { o.MaxRefusals = 3 })
	release := e.calls.HoldBegins()
	var answered atomic.Int32
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			e.stray(t, token)
			answered.Add(1)
		})
	}
	require.Eventually(t, func() bool { return int(answered.Load())+e.calls.Held() == 50 }, 5*time.Second, time.Millisecond)

	release()
	wg.Wait()

	assert.Len(t, e.calls.Records(), 4, "three refusals and the shut-out")
}

// A request the vendor refuses as invalid counts against the session's
// round, as the gateway's own refusals do.
func TestTheVendorsRefusalsCountAgainstTheSession(t *testing.T) {
	e := newEnv(t, func(o *app.Options) { o.MaxRefusals = 3 })
	e.vendor.answers = []*app.Answer{{Status: 400, Body: []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`)}}
	for range 3 {
		require.Equal(t, 400, e.post(t, request).Status)
	}

	shut := e.post(t, request)

	assert.Equal(t, 429, shut.Status)
	assert.Equal(t, 3, e.vendor.attempts())
}

// A call counts at its worst case until its outcome is recorded, in its
// run's spend and the day's, so that a budget holds while calls are in
// flight, and if an outcome is never recorded.
func TestACallCountsItsWorstCaseUntilItsOutcomeIsRecorded(t *testing.T) {
	e := newEnv(t)
	e.vendor.block = make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() { e.post(t, request) })
	require.Eventually(t, func() bool { return e.vendor.sent.Load() == 1 }, 5*time.Second, time.Millisecond)

	inFlight, err := e.calls.Spent(t.Context(), "run-1")
	require.NoError(t, err)
	today, err := e.calls.SpentSince(t.Context(), cost.DayStart(t0))
	require.NoError(t, err)
	close(e.vendor.block)
	wg.Wait()
	after, err := e.calls.Spent(t.Context(), "run-1")
	require.NoError(t, err)

	rec, _ := e.last(t)
	worst := cost.Weighted(card.Spend(cost.Estimate(rec.RequestBytes, 1024)), 100_000)
	assert.Equal(t, worst, rec.Estimate)
	assert.Equal(t, worst, inFlight)
	assert.Equal(t, worst, today)
	assert.Equal(t, int64(5000), after)
}

// An outcome that could not be recorded is tried again until it is; the
// answer stays withheld.
func TestAnOutcomeThatCannotBeRecordedIsTriedAgain(t *testing.T) {
	e := newEnv(t, func(o *app.Options) { o.RecordTimeout = 10 * time.Millisecond })
	e.calls.SetFailEnd(errors.New("the store is down"))

	r := e.post(t, request)
	e.calls.SetFailEnd(nil)

	assert.Equal(t, 503, r.Status)
	require.Eventually(t, func() bool { _, _, _, ended := e.calls.Record("m-1"); return ended }, 5*time.Second, 5*time.Millisecond)
	_, o := e.last(t)
	assert.Equal(t, app.StatusOK, o.Status)
	assert.Equal(t, int64(5000), o.Weighted)
}

// A wait between attempts ends when the call's time is up, in a round with
// no end too.
func TestNoWaitOutlastsTheCallsTimeout(t *testing.T) {
	e := newEnv(t, func(o *app.Options) { o.VendorTimeout = 100 * time.Millisecond })
	e.session.Deadline = time.Time{}
	e.sessions.Put(token, &e.session)
	e.vendor.answers = []*app.Answer{{Status: 529, Body: []byte(`{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`), RetryAfter: 3 * time.Second}}
	start := time.Now()

	r := e.post(t, request)

	assert.Less(t, time.Since(start), 2*time.Second)
	assert.Equal(t, 529, r.Status)
}

// The vendor takes as many calls at once as it has slots: it serves agents
// only, so none is kept for the platform's reads.
func TestTheVendorTakesAsManyCallsAtOnceAsItHasSlots(t *testing.T) {
	e := newEnv(t, func(o *app.Options) { o.Vendor.Slots = 2 })
	e.vendor.block = make(chan struct{})
	other := e.session
	other.Run = "run-2"
	e.sessions.Put("other-token", &other)
	var wg sync.WaitGroup
	wg.Go(func() { e.post(t, request) })
	wg.Go(func() { e.send(t, "other-token", request) })

	require.Eventually(t, func() bool { return e.vendor.sent.Load() == 2 }, 5*time.Second, time.Millisecond)
	close(e.vendor.block)
	wg.Wait()
}

// A request the gateway does not forward is recorded short, whatever
// stopped it: the allowlist, the budget, or the request ending before its
// call could start.
func TestARequestNotForwardedIsRecordedShort(t *testing.T) {
	long := strings.Replace(request, "Find why checkout fails.", strings.Repeat("x", 20000), 1)
	e := newEnv(t)
	e.session.Budget = 0
	e.sessions.Put(token, &e.session)
	require.Equal(t, 403, e.post(t, long).Status)
	e.session.Budget = 2_000_000
	e.sessions.Put(token, &e.session)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.Equal(t, 502, e.sendWith(ctx, token, long).Status)

	records := e.calls.Records()
	require.Len(t, records, 2)
	for _, rec := range records {
		assert.LessOrEqual(t, len(rec.Request), 4096+3, rec.ID)
		assert.True(t, strings.HasPrefix(rec.Request, `{"max_tokens":1024,"messages":[{"content":"xxx`), rec.ID)
		assert.Len(t, rec.RequestDigest, 64, rec.ID)
		assert.Zero(t, rec.Estimate, rec.ID)
	}
	assert.Zero(t, e.vendor.attempts())
}

// Requests the gateway drops because they ended before their call could
// start count against the session's round, as refusals do, so that a
// sandbox cannot fill the audit store with them.
func TestRequestsDroppedAtOnceCountAgainstTheSession(t *testing.T) {
	e := newEnv(t, func(o *app.Options) { o.MaxRefusals = 3 })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for range 3 {
		require.Equal(t, 502, e.sendWith(ctx, token, request).Status)
	}

	shut := e.sendWith(ctx, token, request)
	again := e.sendWith(ctx, token, request)

	assert.Equal(t, 429, shut.Status)
	assert.Equal(t, 429, again.Status)
	assert.Len(t, e.calls.Records(), 4, "three dropped and the shut-out")
}

// A request that ends while its call is being recorded is not sent: its
// record says the call was not made, which costs nothing.
func TestARequestThatEndsWhileItsCallIsRecordedIsNotSent(t *testing.T) {
	e := newEnv(t)
	release := e.calls.HoldBegins()
	ctx, cancel := context.WithCancel(t.Context())
	var r *app.Reply
	var wg sync.WaitGroup
	wg.Go(func() { r = e.sendWith(ctx, token, request) })
	require.Eventually(t, func() bool { return e.calls.Held() == 1 }, 5*time.Second, time.Millisecond)

	cancel()
	release()
	wg.Wait()

	assert.Equal(t, 502, r.Status)
	assert.Zero(t, e.vendor.attempts())
	rec, o := e.last(t)
	assert.Equal(t, app.StatusError, o.Status)
	assert.Zero(t, o.Weighted)
	assert.Positive(t, rec.Estimate)
	spent, err := e.calls.Spent(t.Context(), "run-1")
	require.NoError(t, err)
	assert.Zero(t, spent, "a call not made costs nothing")
}

// block makes the vendor hold every attempt from now on until hold is
// closed, or answer at once when hold is nil.
func (v *vendor) hold(hold chan struct{}) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.block = hold
}

// A call that goes on after its request was answered holds its session for
// at most LastCallWait: past it, once the vendor has answered another call
// since, the session's next request goes on beside it, and the call that
// hangs counts at its worst case until it ends.
func TestANextCallGoesOnBesideACallThatHangs(t *testing.T) {
	e := newEnv(t, func(o *app.Options) { o.LastCallWait = 50 * time.Millisecond })
	e.session.Deadline = time.Now().Add(100 * time.Millisecond)
	e.sessions.Put(token, &e.session)
	hang := make(chan struct{})
	defer close(hang)
	e.vendor.hold(hang)
	require.Equal(t, 504, e.post(t, request).Status)
	e.vendor.hold(nil)
	other := e.session
	other.Run, other.Deadline = "run-2", time.Now().Add(5*time.Second)
	e.sessions.Put("other-token", &other)
	require.Equal(t, 200, e.send(t, "other-token", request).Status, "the vendor answers others")
	e.session.Round, e.session.Deadline = 3, time.Now().Add(5*time.Second)
	e.sessions.Put(token, &e.session)
	start := time.Now()

	r := e.post(t, request)

	assert.Equal(t, 200, r.Status, string(r.Body))
	assert.GreaterOrEqual(t, time.Since(start), 50*time.Millisecond, "it waited for the call first")
	assert.Equal(t, int32(2), e.vendor.most.Load())
	first, _, _, _ := e.calls.Record("m-1")
	spent, err := e.calls.Spent(t.Context(), "run-1")
	require.NoError(t, err)
	assert.Equal(t, first.Estimate+5000, spent, "the call that hangs counts at its worst case")
}

// While the vendor answers no call, a session's next request does not go
// on beside the call that hangs, which would only add another; an answer
// from before the call began does not count. It goes on once the vendor
// answers again.
func TestANextCallWaitsWhileTheVendorAnswersNoOne(t *testing.T) {
	e := newEnv(t, func(o *app.Options) { o.LastCallWait = 50 * time.Millisecond })
	require.Equal(t, 200, e.post(t, request).Status, "an answer before the call that hangs")
	e.session.Deadline = time.Now().Add(100 * time.Millisecond)
	e.sessions.Put(token, &e.session)
	hang := make(chan struct{})
	defer close(hang)
	e.vendor.hold(hang)
	require.Equal(t, 504, e.post(t, request).Status)
	e.session.Round, e.session.Deadline = 3, time.Now().Add(5*time.Second)
	e.sessions.Put(token, &e.session)
	other := e.session
	other.Run = "run-2"
	e.sessions.Put("other-token", &other)
	var next *app.Reply
	var wg sync.WaitGroup
	wg.Go(func() { next = e.post(t, request) })
	assert.Never(t, func() bool { return e.vendor.sent.Load() > 2 }, 300*time.Millisecond, 5*time.Millisecond, "no call beside one that hangs")

	e.vendor.hold(nil)
	require.Equal(t, 200, e.send(t, "other-token", request).Status)
	wg.Wait()

	assert.Equal(t, 200, next.Status, string(next.Body))
}

// A session never has more than two calls at the vendor: one that hangs,
// and one beside it. When both hang, its next request waits until one
// ends.
func TestASessionHasAtMostTwoCallsAtTheVendor(t *testing.T) {
	e := newEnv(t, func(o *app.Options) { o.LastCallWait = 20 * time.Millisecond })
	other := e.session
	other.Run = "run-2"
	e.sessions.Put("other-token", &other)
	hang := make(chan struct{})
	for round := 3; round <= 4; round++ {
		e.vendor.hold(hang)
		e.session.Round, e.session.Deadline = round, time.Now().Add(100*time.Millisecond)
		e.sessions.Put(token, &e.session)
		require.Equal(t, 504, e.post(t, request).Status, "round %d", round)
		e.vendor.hold(nil)
		require.Equal(t, 200, e.send(t, "other-token", request).Status, "the vendor answers others")
	}
	e.session.Round, e.session.Deadline = 5, time.Now().Add(5*time.Second)
	e.sessions.Put(token, &e.session)

	var third *app.Reply
	var wg sync.WaitGroup
	wg.Go(func() { third = e.post(t, request) })
	assert.Never(t, func() bool { return e.vendor.sent.Load() > 4 }, 200*time.Millisecond, 5*time.Millisecond,
		"the session's third call waits while two hang")
	close(hang)
	wg.Wait()

	assert.Equal(t, 200, third.Status, string(third.Body))
}

// Drain waits for the calls that outlived their requests to end and their
// outcomes to be recorded, as the gateway stops, or for its context.
func TestDrainWaitsForCallsThatOutliveTheirRequests(t *testing.T) {
	e := newEnv(t)
	e.session.Deadline = time.Now().Add(100 * time.Millisecond)
	e.sessions.Put(token, &e.session)
	e.vendor.block = make(chan struct{})
	require.Equal(t, 504, e.post(t, request).Status)
	short, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	early := e.gw.Drain(short)
	close(e.vendor.block)
	err := e.gw.Drain(t.Context())

	require.ErrorIs(t, early, context.DeadlineExceeded)
	require.NoError(t, err)
	_, o, _, ended := e.calls.Record("m-1")
	require.True(t, ended)
	assert.Equal(t, app.StatusOK, o.Status)
}

// A request's record holds it redacted, a thinking block's text too, while
// the vendor gets the block as the gateway gave it; the record's digest is
// of the record, so that it cannot give back what the record hides.
func TestAThinkingBlockIsRecordedRedacted(t *testing.T) {
	e := newEnv(t)
	thought := `{"type": "thinking", "thinking": "the key is sk-hhhhhhhh", "signature": "EqQBCkgIBRAB"}`
	answer := ok()
	answer.Body = []byte(strings.Replace(string(answer.Body), thinking, thought, 1))
	e.vendor.answers = []*app.Answer{answer, ok()}
	require.Equal(t, 200, e.post(t, request).Status)

	r := e.post(t, strings.Replace(next, thinking, thought, 1))

	require.Equal(t, 200, r.Status, string(r.Body))
	assert.Contains(t, e.vendor.bodies[1], "sk-hhhhhhhh", "the vendor gets the block unchanged")
	rec, _ := e.last(t)
	assert.NotContains(t, rec.Request, "sk-hhhhhhhh")
	sum := sha256.Sum256([]byte(rec.Request))
	assert.Equal(t, hex.EncodeToString(sum[:]), rec.RequestDigest)
}

// Drain waits for the requests in the gateway's hands as well, a request
// waiting for its session's last call among them, and the gateway takes no
// new request once it drains.
func TestDrainWaitsForRequestsInHandAndRefusesNewOnes(t *testing.T) {
	e := newEnv(t)
	e.session.Deadline = time.Now().Add(100 * time.Millisecond)
	e.sessions.Put(token, &e.session)
	first := make(chan struct{})
	e.vendor.block = first
	require.Equal(t, 504, e.post(t, request).Status)
	second := make(chan struct{})
	e.vendor.mu.Lock()
	e.vendor.block = second
	e.vendor.mu.Unlock()
	e.session.Round, e.session.Deadline = 3, time.Now().Add(5*time.Second)
	e.sessions.Put(token, &e.session)
	other := e.session
	other.Run = "run-2"
	e.sessions.Put("other-token", &other)
	var next *app.Reply
	var wg sync.WaitGroup
	wg.Go(func() { next = e.post(t, request) })
	require.Eventually(t, func() bool { return e.ports.IDs.(*ids).n.Load() == 2 }, 5*time.Second, time.Millisecond, "the next request is in hand")
	drained := make(chan error, 1)
	go func() { drained <- e.gw.Drain(t.Context()) }()
	require.Eventually(t, func() bool { return e.stray(t, "other-token").Status == 503 }, 5*time.Second, time.Millisecond, "no new request")

	close(first)
	require.Eventually(t, func() bool { return e.vendor.sent.Load() == 2 }, 5*time.Second, time.Millisecond)
	select {
	case err := <-drained:
		t.Fatalf("Drain returned (%v) with a call at the vendor", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(second)
	wg.Wait()

	require.NoError(t, <-drained)
	assert.Equal(t, 200, next.Status, string(next.Body))
}

// A request dropped while its call is being recorded counts against the
// session's round, as one dropped before does.
func TestRequestsDroppedWhileRecordedCountAgainstTheSession(t *testing.T) {
	e := newEnv(t, func(o *app.Options) { o.MaxRefusals = 3 })
	for range 3 {
		release := e.calls.HoldBegins()
		ctx, cancel := context.WithCancel(t.Context())
		var r *app.Reply
		var wg sync.WaitGroup
		wg.Go(func() { r = e.sendWith(ctx, token, request) })
		require.Eventually(t, func() bool { return e.calls.Held() == 1 }, 5*time.Second, time.Millisecond)
		cancel()
		release()
		wg.Wait()
		require.Equal(t, 502, r.Status)
	}

	shut := e.post(t, request)

	assert.Equal(t, 429, shut.Status)
	assert.Zero(t, e.vendor.attempts())
}

// A call recorded and then not made, whose outcome could not be recorded,
// has its outcome tried again, so that it does not count at its worst case
// for good.
func TestANotMadeOutcomeIsTriedAgain(t *testing.T) {
	e := newEnv(t, func(o *app.Options) { o.RecordTimeout = 50 * time.Millisecond })
	release := e.calls.HoldBegins()
	ctx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	wg.Go(func() { e.sendWith(ctx, token, request) })
	require.Eventually(t, func() bool { return e.calls.Held() == 1 }, 5*time.Second, time.Millisecond)
	cancel()
	e.calls.SetFailEnd(errors.New("the store is down"))
	release()
	wg.Wait()
	e.calls.SetFailEnd(nil)

	require.Eventually(t, func() bool { _, _, _, ended := e.calls.Record("m-1"); return ended }, 5*time.Second, 5*time.Millisecond)
	spent, err := e.calls.Spent(t.Context(), "run-1")
	require.NoError(t, err)
	assert.Zero(t, spent)
}
