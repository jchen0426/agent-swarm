package app_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/actions/app"
	"github.com/jchen0426/agent-swarm/internal/actions/output"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/key"
)

// setFlagLimit is set_flag with a limit, whose candidates are read with the
// flag once the flag is validated.
var setFlagLimit = config.ToolDefinition{
	Name: "set_flag", Description: "Set a flag's limit.",
	InputSchema: []byte(`{"type":"object","properties":{"flag":{"type":"string"},"limit":{}}}`),
}

// flags makes list_flags answer with the given flags.
func (e *env) flags(names ...string) {
	var list []string
	for _, n := range names {
		list = append(list, fmt.Sprintf(`{"name":%q}`, n))
	}
	e.up.set(func(u *upstream) { u.replies["list_flags"] = text("[" + strings.Join(list, ",") + "]") })
}

// hypothesis posts evidence from the agent's own read and a hypothesis on
// it, keyed by flag, and returns the hypothesis's id.
func (e *env) hypothesis(t *testing.T, flag string) string {
	t.Helper()
	ev := e.evidence(t)
	r := e.call(t, "post_hypothesis", fmt.Sprintf(`{"key":{"flag":%q},"statement":"flag %s broke checkout","evidence_ids":[%q]}`, flag, flag, ev))
	require.Equal(t, app.StatusOK, r.Status, r.Text)
	return strings.TrimPrefix(r.Text, "posted hypothesis ")
}

// proposals returns the run's proposals.
func (e *env) proposals(t *testing.T) []board.Proposal {
	t.Helper()
	var out []board.Proposal
	events := e.events(t)
	for i := range events {
		if p := events[i].Body.Proposal; p != nil {
			out = append(out, *p)
		}
	}
	return out
}

// use makes the session run under a revision built of files.
func (e *env) use(t *testing.T, files map[string][]byte, commit string) {
	t.Helper()
	rev := build(t, e.cc, commit, files)
	e.revs.add(rev)
	e.session.Commit = rev.Commit()
	e.sessions.Put(token, &e.session)
}

// withPolicy replaces set_flag's policy.
func withPolicy(t *testing.T, e *env, policy string) {
	t.Helper()
	files := revisionFiles(t, defaultPins(), "reviewed")
	server := string(files["servers/flags.yaml"])
	start := strings.Index(server, "    set_flag:")
	end := strings.Index(server, "    owners:")
	files["servers/flags.yaml"] = []byte(server[:start] + policy + server[end:])
	e.use(t, files, "f00df00df00df00df00df00df00df00df00df00d")
}

// A write call is recorded as a proposal for a person and never sent (spec
// Sections 5.3 and 6.3). Its candidates are read for the platform, in the
// run; the agent learns the proposal's id and nothing else.
func TestAWriteCallIsRecordedAsAProposalAndNeverSent(t *testing.T) {
	e := newEnv(t)
	e.flags("x", "y")
	h := e.hypothesis(t, "x")

	r := e.call(t, "flags__set_flag", fmt.Sprintf(`{"flag":"x","on":false,"_hypothesis":%q}`, h))

	require.Equal(t, app.StatusRecorded, r.Status, r.Text)
	assert.Regexp(t, `^recorded as proposal e-\d+ for a person; nothing was executed$`, r.Text)
	for _, c := range e.up.sent() {
		assert.NotEqual(t, "set_flag", c.tool, "no write reaches a server")
	}
	proposals := e.proposals(t)
	require.Len(t, proposals, 1)
	p := proposals[0]
	assert.Equal(t, "flag-investigator", p.Proposer)
	assert.Equal(t, "flags.set_flag", p.Tool)
	sum := sha256.Sum256([]byte(`{"flag":"x","on":false}`))
	assert.Equal(t, "sha256:"+hex.EncodeToString(sum[:]), p.ArgsHash)
	assert.Equal(t, board.EntryID(h), p.Hypothesis)
	assert.Equal(t, []board.TargetValue{{Arg: "flag", Value: "x"}}, p.Target)
	assert.Equal(t, map[string]string{"flag": "x"}, p.Scope)
	assert.Equal(t, []board.SummaryPart{{Text: "Set "}, {Text: "x", Value: true}}, p.Summary)
	assert.Equal(t, r.CallID, p.CallID)
	read, _, _, _ := e.audit.Record(p.Candidates["flag"])
	calls := e.audit.Calls()
	assert.Less(t, slices.Index(calls, r.CallID), slices.Index(calls, p.Candidates["flag"]), "the write call is recorded before the reads made for it")
	assert.Equal(t, "list_flags", read.Tool)
	assert.Empty(t, read.Agent, "a platform read")
	assert.Equal(t, r.CallID, read.Cause, "made for the write call")
	assert.Equal(t, board.RunID("run-1"), read.Run)
	rec, out, _, _ := e.audit.Record(r.CallID)
	assert.Equal(t, app.StatusRecorded, out.Status)
	assert.JSONEq(t, fmt.Sprintf(`{"flag":"x","on":false,"_hypothesis":%q}`, h), string(rec.Args))

	cite := e.call(t, "post_evidence", fmt.Sprintf(`{"claim":"set","call_id":%q,"quote":"recorded"}`, r.CallID))
	assert.Equal(t, app.StatusRefused, cite.Status, "a recorded write call is no read to cite")
}

// A proposer must support the hypothesis it cites with evidence of its own
// (spec Section 9.2); the board refuses before any read is made.
func TestAProposerMustSupportTheHypothesisWithItsOwnEvidence(t *testing.T) {
	e := newEnv(t)
	e.flags("x")
	ev := e.evidence(t)
	theirs, err := e.boards.PostHypothesis(t.Context(), "run-1", e.bc.Round, colleague, &board.HypothesisInput{
		Key: key.Key{"flag": "x"}, Statement: "flag x broke checkout", Evidence: []board.EntryID{board.EntryID(ev)},
	})
	require.NoError(t, err)
	sent := len(e.up.sent())

	r := e.call(t, "flags__set_flag", fmt.Sprintf(`{"flag":"x","_hypothesis":%q}`, theirs.Event.ID))

	assert.Equal(t, app.StatusRefused, r.Status)
	assert.Contains(t, r.Text, "no live evidence of your own")
	assert.Len(t, e.up.sent(), sent, "no candidate read for a call the board refuses")
	assert.Contains(t, e.notices(t), board.Notice{Code: board.NoticeRefusedCall, Agent: "flag-investigator", Tool: "flags__set_flag", CallID: r.CallID})

	read := e.call(t, "flags__get_flag", `{"flag":"x","window":"1h"}`)
	support := e.call(t, "post_evidence", fmt.Sprintf(`{"claim":"x is on","call_id":%q,"quote":"\"on\":true","hypothesis_id":%q,"stance":"supports"}`, read.CallID, theirs.Event.ID))
	require.Equal(t, app.StatusOK, support.Status, support.Text)
	r = e.call(t, "flags__set_flag", fmt.Sprintf(`{"flag":"x","_hypothesis":%q}`, theirs.Event.ID))
	assert.Equal(t, app.StatusRecorded, r.Status, r.Text)
}

// _hypothesis is required, and is a hypothesis's id.
func TestAWriteCallNamesTheHypothesisItAddresses(t *testing.T) {
	e := newEnv(t)
	e.flags("x")
	for _, args := range []string{
		`{"flag":"x"}`, `{"flag":"x","_hypothesis":7}`, `{"flag":"x","_hypothesis":""}`, `{"flag":"x","_Hypothesis":"e-1"}`,
		`{"flag":"x","_hypothesis":"` + strings.Repeat("e", 65) + `"}`,
	} {
		r := e.call(t, "flags__set_flag", args)
		assert.Equal(t, app.StatusRefused, r.Status, args)
	}
	long := e.call(t, "flags__set_flag", `{"flag":"x","_hypothesis":"`+strings.Repeat("e", 65)+`"}`)
	assert.Contains(t, long.Text, "_hypothesis must be the id", "an id longer than any the board issues")
	assert.Empty(t, e.up.sent())
	assert.Empty(t, e.proposals(t))
}

// An argument is checked against its candidates exactly, as JSON: a number is
// not a string, and a dependent read takes the validated value of the
// argument it depends on.
func TestCandidatesAreComparedExactlyAndReadInOrder(t *testing.T) {
	e := newEnv(t)
	withPolicy(t, e, `    set_flag:
      schemaHash: "`+hash(t, setFlagLimit)+`"
      effect: write
      target: [flag, limit]
      scope: {flag: hypothesis.flag}
      candidates:
        flag: {tool: list_flags, field: "$[*].name"}
        limit: {tool: get_flag, args: {flag: "{args.flag}", window: 1h}, field: "$.limits[*]"}
      summary: Set {args.flag} to {args.limit}
`)
	e.up.set(func(u *upstream) {
		u.defs[2] = setFlagLimit
		u.replies["get_flag"] = text(`{"flag":"x","on":true,"truncated":false,"limits":[1,"2",3.50,{"n":4}]}`)
	})
	e.flags("x")
	h := e.hypothesis(t, "x")

	for _, limit := range []string{`"1"`, `2`, `3.5`, `{"n":4}`, `null`} {
		r := e.call(t, "flags__set_flag", fmt.Sprintf(`{"flag":"x","limit":%s,"_hypothesis":%q}`, limit, h))
		assert.Equal(t, app.StatusRefused, r.Status, limit)
	}
	r := e.call(t, "flags__set_flag", fmt.Sprintf(`{"flag":"x","limit":3.50,"_hypothesis":%q}`, h))
	require.Equal(t, app.StatusRecorded, r.Status, r.Text)
	p := e.proposals(t)[0]
	assert.Equal(t, []board.TargetValue{{Arg: "flag", Value: "x"}, {Arg: "limit", Value: "3.50"}}, p.Target)
	assert.Equal(t, "Set x to 3.50", board.SummaryText(p.Summary))
	dependent, _, _, _ := e.audit.Record(p.Candidates["limit"])
	assert.JSONEq(t, `{"flag":"x","window":"1h"}`, string(dependent.Args), "the validated flag, never the agent's text")
}

// A candidate read that fails, selects nothing or selects only values that
// redaction changed refuses the call; the refusal never names a candidate.
func TestACandidateReadThatFailsRefusesTheCall(t *testing.T) {
	e := newEnv(t)
	e.flags("x")
	h := e.hypothesis(t, "x")
	for _, reply := range []*output.Result{
		{IsError: true, Blocks: []output.Block{{Kind: output.Text, Text: "catalog down"}}},
		text(`[]`),
		text(`[{"name":"x -----BEGIN RSA PRIVATE KEY-----"}]`),
		text(`[{"name":"y"},{"name":"z"}]`),
	} {
		e.up.set(func(u *upstream) { u.replies["list_flags"] = reply })

		r := e.call(t, "flags__set_flag", fmt.Sprintf(`{"flag":"x","_hypothesis":%q}`, h))

		assert.Equal(t, app.StatusRefused, r.Status)
		assert.NotContains(t, r.Text, `"y"`)
		assert.NotContains(t, r.Text, "z")
	}
	assert.Empty(t, e.proposals(t))
}

// The scope binds an argument to the cited hypothesis's key (spec Section
// 3.3): another flag, even one among the candidates, is refused, and so is a
// hypothesis keyed "other".
func TestTheScopeBindsTheArgumentToTheHypothesis(t *testing.T) {
	e := newEnv(t)
	e.flags("x", "y", "other")
	h := e.hypothesis(t, "x")
	other := e.hypothesis(t, "other")

	r := e.call(t, "flags__set_flag", fmt.Sprintf(`{"flag":"y","_hypothesis":%q}`, h))
	assert.Equal(t, app.StatusRefused, r.Status)
	assert.Contains(t, r.Text, "outside this action's scope")
	r = e.call(t, "flags__set_flag", fmt.Sprintf(`{"flag":"x","_hypothesis":%q}`, other))
	assert.Equal(t, app.StatusRefused, r.Status)
	assert.Empty(t, e.proposals(t))
}

// A write call repeated after a lost answer returns the first proposal,
// without reading its candidates again.
func TestARepeatedWriteCallReturnsTheFirstProposal(t *testing.T) {
	e := newEnv(t)
	e.flags("x")
	h := e.hypothesis(t, "x")
	args := fmt.Sprintf(`{"flag":"x","on":true,"_hypothesis":%q}`, h)
	first := e.call(t, "flags__set_flag", args)
	require.Equal(t, app.StatusRecorded, first.Status, first.Text)
	sent := len(e.up.sent())

	again := e.call(t, "flags__set_flag", args)

	require.Equal(t, app.StatusRecorded, again.Status)
	id := strings.Fields(first.Text)[3]
	assert.Contains(t, again.Text, "this repeats proposal "+id)
	assert.Len(t, e.up.sent(), sent)
	assert.Len(t, e.proposals(t), 1)
	_, out, _, ended := e.audit.Record(again.CallID)
	assert.True(t, ended, "the repeated call is audited too")
	assert.Equal(t, app.StatusRecorded, out.Status)
}

// A server that declares _hypothesis itself would be told what the platform
// keeps from servers; its tool is hidden.
func TestAWriteToolThatDeclaresTheHypothesisArgumentIsHidden(t *testing.T) {
	e := newEnv(t)
	sneaky := setFlag
	sneaky.InputSchema = []byte(`{"type":"object","properties":{"flag":{"type":"string"},"_HYPOTHESIS":{"type":"string"}}}`)
	pins := defaultPins()
	pins["set_flag"] = sneaky
	e.use(t, revisionFiles(t, pins, "reviewed"), "5ea4c5ea4c5ea4c5ea4c5ea4c5ea4c5ea4c5ea4")
	e.up.set(func(u *upstream) { u.defs[2] = sneaky })

	list, err := e.gw.Tools(t.Context(), token)

	require.NoError(t, err)
	assert.NotContains(t, names(list), "flags__set_flag")
	hides, err := e.hides.Hidden(t.Context())
	require.NoError(t, err)
	require.Len(t, hides, 1)
	assert.Equal(t, app.ReasonSchema, hides[0].Reason)
}

// In a diagnosis-only item write tools are hidden from every run, and a
// call by name is refused at once, even in a round whose list showed the
// tool (spec Sections 3.6 and 6.1).
func TestInADiagnosisOnlyItemWriteToolsAreHiddenAndRefused(t *testing.T) {
	e := newEnv(t)
	e.flags("x")
	h := e.hypothesis(t, "x")
	listed, err := e.gw.Tools(t.Context(), token)
	require.NoError(t, err)
	require.Contains(t, names(listed), "flags__set_flag")

	e.session.DiagnosisOnly = true
	e.sessions.Put(token, &e.session)
	r := e.call(t, "flags__set_flag", fmt.Sprintf(`{"flag":"x","_hypothesis":%q}`, h))

	assert.Equal(t, app.StatusRefused, r.Status)
	assert.Empty(t, e.proposals(t))
	e.nextRound(t)
	list, err := e.gw.Tools(t.Context(), token)
	require.NoError(t, err)
	assert.NotContains(t, names(list), "flags__set_flag")
}

// Facts are read for the proposal, also through tools only the platform may
// call; a fact that cannot be read is recorded unavailable. Agents never see
// facts or summaries (spec Section 3.3).
func TestFactsAreRecordedAndNeverShownToAgents(t *testing.T) {
	e := newEnv(t)
	withPolicy(t, e, `    set_flag:
      schemaHash: "`+hash(t, setFlag)+`"
      effect: write
      target: [flag]
      scope: {flag: hypothesis.flag}
      candidates:
        flag: {tool: list_flags, field: "$[*].name"}
      facts:
        owner: {tool: owners, args: {flag: "{args.flag}"}, field: "$.owners[*].user"}
        state: {tool: get_flag, args: {flag: "{args.flag}", window: 1h}, field: "$.missing"}
      summary: "Set {args.flag}, owned by {facts.owner}, now {facts.state}"
`)
	e.flags("x")
	e.up.set(func(u *upstream) {
		u.replies["owners"] = text(`{"owners":[{"user":"ana@example.com"},{"user":"bo@example.com"}]}`)
	})
	h := e.hypothesis(t, "x")

	r := e.call(t, "flags__set_flag", fmt.Sprintf(`{"flag":"x","_hypothesis":%q}`, h))

	require.Equal(t, app.StatusRecorded, r.Status, r.Text)
	p := e.proposals(t)[0]
	assert.Equal(t, []string{`"ana@example.com"`, `"bo@example.com"`}, p.Facts["owner"].Values)
	assert.Equal(t, "nothing", p.Facts["state"].Unavailable)
	assert.Equal(t, "Set x, owned by ana@example.com, bo@example.com, now [unavailable]", board.SummaryText(p.Summary))
	assert.NotContains(t, r.Text, "ana@example.com")
	view := e.call(t, "read_board", `{}`)
	require.Equal(t, app.StatusOK, view.Status)
	assert.NotContains(t, view.Text, "ana@example.com")
	assert.NotContains(t, view.Text, "owned by")
}

// The reads a write call triggers are paid for by the agent's session: on a
// degraded server they fail fast, as the agent's own reads do, where the
// platform's own reads would go on (spec Section 6.2).
func TestCandidateReadsArePaidForByTheSession(t *testing.T) {
	e := newEnv(t)
	e.flags("x")
	h := e.hypothesis(t, "x")
	conn := fmt.Errorf("%w: refused", app.ErrConnection)
	for range 3 {
		e.up.set(func(u *upstream) { u.errs = []error{conn, conn, conn} })
		e.call(t, "flags__list_flags", `{}`)
	}

	r := e.call(t, "flags__set_flag", fmt.Sprintf(`{"flag":"x","_hypothesis":%q}`, h))

	assert.Equal(t, app.StatusRefused, r.Status)
	assert.Contains(t, r.Text, "candidates of flag could not be read")
	_, err := e.gw.PlatformRead(t.Context(), app.Origin{Run: "run-1", Commit: e.rev.Commit()}, "flags",
		&config.PlatformRead{Tool: "list_flags", Field: "$[*].name"}, app.Values{})
	require.NoError(t, err, "the platform's own read goes on")
}

// In the wrap-up round, a write call must cite the converged hypothesis
// (spec Section 5.6).
func TestInTheWrapUpRoundAnActionMustCiteTheConvergedHypothesis(t *testing.T) {
	e := newEnv(t)
	e.flags("x", "y")
	h := e.hypothesis(t, "x")
	other := e.hypothesis(t, "y")
	e.wrapUp(t, h)

	r := e.call(t, "flags__set_flag", fmt.Sprintf(`{"flag":"y","_hypothesis":%q}`, other))
	assert.Equal(t, app.StatusRefused, r.Status)
	r = e.call(t, "flags__set_flag", fmt.Sprintf(`{"flag":"x","_hypothesis":%q}`, h))
	assert.Equal(t, app.StatusRecorded, r.Status, r.Text)
}

// A write call counts among the session's calls in flight, and its reads
// only among the session's calls to their server, so a call never waits for
// a slot it holds itself: many at once on one server all finish (spec
// Section 6.1).
func TestManyWriteCallsAtOnceOnOneServerAllFinish(t *testing.T) {
	e := newEnv(t)
	e.flags("x")
	h := e.hypothesis(t, "x")
	results := make([]*app.Response, 6)
	var wg sync.WaitGroup
	for i := range results {
		wg.Go(func() {
			r, err := e.gw.Call(t.Context(), token, "flags__set_flag", json.RawMessage(fmt.Sprintf(`{"flag":"x","on":%t,"_hypothesis":%q}`, i%2 == 0, h)))
			if err == nil {
				results[i] = r
			}
		})
	}
	wg.Wait()

	for i, r := range results {
		require.NotNil(t, r, i)
		assert.Equal(t, app.StatusRecorded, r.Status, r.Text)
	}
	assert.Len(t, e.proposals(t), 2, "each distinct call once")
}

// A candidate value a reader could not see whole is refused, not altered:
// a summary would show it as data that looks like something else.
func TestACandidateValueAReaderCouldMissIsRefused(t *testing.T) {
	e := newEnv(t)
	withPolicy(t, e, `    set_flag:
      schemaHash: "`+hash(t, setFlagLimit)+`"
      effect: write
      target: [flag, limit]
      scope: {flag: hypothesis.flag}
      candidates:
        flag: {tool: list_flags, field: "$[*].name"}
        limit: {tool: get_flag, args: {flag: "{args.flag}", window: 1h}, field: "$.limits[*]"}
      summary: Set {args.flag} to {args.limit}
`)
	long := strings.Repeat("9", 201)
	e.up.set(func(u *upstream) {
		u.defs[2] = setFlagLimit
		u.replies["get_flag"] = text(`{"flag":"x","on":true,"truncated":false,"limits":["5\u200b","` + long + `"]}`)
	})
	e.flags("x")
	h := e.hypothesis(t, "x")

	for _, limit := range []string{`"5\u200b"`, `"` + long + `"`} {
		r := e.call(t, "flags__set_flag", fmt.Sprintf(`{"flag":"x","limit":%s,"_hypothesis":%q}`, limit, h))
		assert.Equal(t, app.StatusRefused, r.Status)
		assert.Contains(t, r.Text, "a reader could miss")
	}
	assert.Empty(t, e.proposals(t))
}

// A fact server that does not answer leaves its fact unavailable before the
// call's end, and the proposal is recorded all the same.
func TestASlowFactServerNeverLosesTheProposal(t *testing.T) {
	e := newEnv(t, func(p *app.Ports, _ *app.Options) { p.Board = timedBoard{p.Board} })
	withPolicy(t, e, `    set_flag:
      schemaHash: "`+hash(t, setFlag)+`"
      effect: write
      target: [flag]
      scope: {flag: hypothesis.flag}
      candidates:
        flag: {tool: list_flags, field: "$[*].name"}
      facts:
        state: {tool: get_flag, args: {flag: "{args.flag}", window: 1h}, field: "$.on"}
      summary: "Set {args.flag}, now {facts.state}"
`)
	e.flags("x")
	h := e.hypothesis(t, "x")
	e.session.Deadline = time.Now().Add(5*time.Second + 300*time.Millisecond)
	e.sessions.Put(token, &e.session)
	e.up.set(func(u *upstream) { u.stall = map[string]bool{"get_flag": true} })

	r := e.call(t, "flags__set_flag", fmt.Sprintf(`{"flag":"x","_hypothesis":%q}`, h))

	require.Equal(t, app.StatusRecorded, r.Status, r.Text)
	p := e.proposals(t)[0]
	assert.NotEmpty(t, p.Facts["state"].Unavailable)
	assert.Equal(t, "Set x, now [unavailable]", board.SummaryText(p.Summary))
}

// A session's refused write calls are capped per round; then no read is
// made for its write calls until the next round.
func TestRefusedWriteCallsAreCappedPerRound(t *testing.T) {
	e := newEnv(t)
	e.flags("x")
	h := e.hypothesis(t, "x")
	for range 10 {
		require.Equal(t, app.StatusRefused, e.call(t, "flags__set_flag", `{"flag":"x"}`).Status)
	}
	sent := len(e.up.sent())

	r := e.call(t, "flags__set_flag", fmt.Sprintf(`{"flag":"x","_hypothesis":%q}`, h))

	assert.Equal(t, app.StatusRefused, r.Status)
	assert.Contains(t, r.Text, "no more are checked until the next round")
	assert.Len(t, e.up.sent(), sent, "no read for a call over the cap")
	e.nextRound(t)
	assert.Equal(t, app.StatusRecorded, e.call(t, "flags__set_flag", fmt.Sprintf(`{"flag":"x","_hypothesis":%q}`, h)).Status)
}

// A write tool whose input schema's root could refuse _hypothesis is
// hidden: a runtime that checks arguments against the listed schema could
// never send it.
func TestAWriteToolWhoseSchemaCouldRefuseTheHypothesisIsHidden(t *testing.T) {
	for i, schema := range []string{
		`{"type":"object","properties":{"flag":{"type":"string"}},"allOf":[{"additionalProperties":false,"properties":{"flag":{}}}]}`,
		`{"type":"object","properties":{"flag":{"type":"string"}},"NOT":{"required":["_hypothesis"]}}`,
		`{"type":"object","properties":{"flag":{"type":"string"}},"enum":[{"flag":"x"}]}`,
		`{"type":"object","properties":{"flag":{"type":"string"}},"dependencies":{"_hypothesis":{"not":{}}}}`,
	} {
		e := newEnv(t)
		strict := setFlag
		strict.InputSchema = []byte(schema)
		pins := defaultPins()
		pins["set_flag"] = strict
		e.use(t, revisionFiles(t, pins, "reviewed"), fmt.Sprintf("a110fa110fa110fa110fa110fa110fa110fa110%d", i))
		e.up.set(func(u *upstream) { u.defs[2] = strict })

		list, err := e.gw.Tools(t.Context(), token)

		require.NoError(t, err)
		assert.NotContains(t, names(list), "flags__set_flag", schema)
	}
}

// A write tool whose input schema requires _hypothesis, in any case, without
// declaring it is hidden: listed, it would require the argument twice, and
// the pinned schema would refuse every call once the gateway split it off.
func TestAWriteToolThatRequiresTheHypothesisArgumentIsHidden(t *testing.T) {
	for i, schema := range []string{
		`{"type":"object","properties":{"flag":{"type":"string"}},"required":["_hypothesis"]}`,
		`{"type":"object","properties":{"flag":{"type":"string"}},"required":["flag","_Hypothesis"]}`,
	} {
		e := newEnv(t)
		strict := setFlag
		strict.InputSchema = []byte(schema)
		pins := defaultPins()
		pins["set_flag"] = strict
		e.use(t, revisionFiles(t, pins, "reviewed"), fmt.Sprintf("2e9112e9112e9112e9112e9112e9112e9112e91%d", i))
		e.up.set(func(u *upstream) { u.defs[2] = strict })

		list, err := e.gw.Tools(t.Context(), token)

		require.NoError(t, err)
		assert.NotContains(t, names(list), "flags__set_flag", schema)
	}
}

// An item that turns diagnosis-only while a write call reads its candidates
// records no proposal.
func TestAnItemThatTurnsDiagnosisOnlyDuringACallRecordsNoProposal(t *testing.T) {
	e := newEnv(t)
	e.flags("x")
	h := e.hypothesis(t, "x")
	e.up.set(func(u *upstream) {
		u.onCall = func(c call) {
			if c.tool == "list_flags" {
				flagged := e.session
				flagged.DiagnosisOnly = true
				e.sessions.Put(token, &flagged)
			}
		}
	})

	r := e.call(t, "flags__set_flag", fmt.Sprintf(`{"flag":"x","_hypothesis":%q}`, h))

	assert.Equal(t, app.StatusRefused, r.Status)
	assert.Empty(t, e.proposals(t))
}

// A fact long enough to fill the summary never refuses the proposal: the
// summary shows each value cut, and is cut itself, never the action.
func TestALongFactNeverRefusesTheProposal(t *testing.T) {
	e := newEnv(t)
	withPolicy(t, e, `    set_flag:
      schemaHash: "`+hash(t, setFlag)+`"
      effect: write
      target: [flag]
      scope: {flag: hypothesis.flag}
      candidates:
        flag: {tool: list_flags, field: "$[*].name"}
      facts:
        a: {tool: owners, args: {flag: "{args.flag}"}, field: "$.owners[*].user"}
        b: {tool: owners, args: {flag: "{args.flag}"}, field: "$.owners[*].user"}
        c: {tool: owners, args: {flag: "{args.flag}"}, field: "$.owners[*].user"}
      summary: "Set {args.flag}: {facts.a} {facts.b} {facts.c} {facts.a} {facts.b} {facts.c}"
`)
	e.flags("x")
	var owners []string
	for range 6 {
		owners = append(owners, fmt.Sprintf(`{"user":%q}`, strings.Repeat("o", 196)))
	}
	e.up.set(func(u *upstream) { u.replies["owners"] = text(`{"owners":[` + strings.Join(owners, ",") + `]}`) })
	h := e.hypothesis(t, "x")

	r := e.call(t, "flags__set_flag", fmt.Sprintf(`{"flag":"x","_hypothesis":%q}`, h))

	require.Equal(t, app.StatusRecorded, r.Status, r.Text)
	p := e.proposals(t)[0]
	assert.Equal(t, 1, p.Facts["a"].More)
	assert.LessOrEqual(t, utf8.RuneCountInString(board.SummaryText(p.Summary)), board.MaxSummaryRunes)
	require.Equal(t, board.SummaryPart{Text: "x", Value: true}, p.Summary[1])
	assert.True(t, strings.HasSuffix(p.Summary[3].Text, "\u2026"), "a value cut is marked")
}

// A fact value with a character a reader could miss anywhere in it, in a
// string, or in a key or string inside an object, is unavailable.
func TestAFactValueWithAHiddenCharacterAnywhereIsUnavailable(t *testing.T) {
	for name, owners := range map[string]string{
		"a string":  `[{"user":"ana\u200b"}]`,
		"an object": `[{"user":{"name":"\u202eregnad ton"}}]`,
		"a key":     `[{"user":{"name\u200b":"ana"}}]`,
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			withPolicy(t, e, `    set_flag:
      schemaHash: "`+hash(t, setFlag)+`"
      effect: write
      target: [flag]
      scope: {flag: hypothesis.flag}
      candidates:
        flag: {tool: list_flags, field: "$[*].name"}
      facts:
        owner: {tool: owners, args: {flag: "{args.flag}"}, field: "$.owners[*].user"}
      summary: "Set {args.flag}, owned by {facts.owner}"
`)
			e.flags("x")
			e.up.set(func(u *upstream) { u.replies["owners"] = text(`{"owners":` + owners + `}`) })
			h := e.hypothesis(t, "x")

			r := e.call(t, "flags__set_flag", fmt.Sprintf(`{"flag":"x","_hypothesis":%q}`, h))

			require.Equal(t, app.StatusRecorded, r.Status, r.Text)
			p := e.proposals(t)[0]
			assert.Equal(t, "not_plain", p.Facts["owner"].Unavailable)
			assert.Equal(t, "Set x, owned by [unavailable]", board.SummaryText(p.Summary))
		})
	}
}

// Write calls made at once cannot pass the refusal cap: a call counts as
// refused from the moment it is checked until it is recorded.
func TestWriteCallsAtOnceCannotPassTheRefusalCap(t *testing.T) {
	e := newEnv(t, func(_ *app.Ports, o *app.Options) { o.SessionCalls = 64 })
	e.flags("x")
	h := e.hypothesis(t, "x")
	release := make(chan struct{})
	e.up.set(func(u *upstream) {
		u.replies["list_flags"] = text(`[{"name":"y"}]`)
		u.onCall = func(c call) {
			if c.tool == "list_flags" {
				<-release
			}
		}
	})
	sent := len(e.up.sent())
	var wg sync.WaitGroup
	for range 40 {
		wg.Go(func() {
			_, _ = e.gw.Call(t.Context(), token, "flags__set_flag", json.RawMessage(fmt.Sprintf(`{"flag":"x","_hypothesis":%q}`, h)))
		})
	}
	time.AfterFunc(300*time.Millisecond, func() { close(release) })
	wg.Wait()

	assert.LessOrEqual(t, len(e.up.sent())-sent, 10, "at most the cap's worth of candidate reads")
}

// A session that cannot be read again before the proposal is committed
// records nothing: the check fails closed.
func TestASessionThatCannotBeReadAgainRecordsNoProposal(t *testing.T) {
	var sessions *failingSessions
	e := newEnv(t, func(p *app.Ports, _ *app.Options) {
		sessions = &failingSessions{Sessions: p.Sessions}
		p.Sessions = sessions
	})
	e.flags("x")
	h := e.hypothesis(t, "x")
	e.up.set(func(u *upstream) {
		u.onCall = func(c call) {
			if c.tool == "list_flags" {
				sessions.failing.Store(true)
			}
		}
	})

	r := e.call(t, "flags__set_flag", fmt.Sprintf(`{"flag":"x","_hypothesis":%q}`, h))

	assert.Equal(t, app.StatusReadError, r.Status)
	assert.Empty(t, e.proposals(t))
}

// A fact server that hangs leaves the call time to answer before its end.
func TestAHangingFactServerLeavesTimeToAnswer(t *testing.T) {
	e := newEnv(t)
	withPolicy(t, e, `    set_flag:
      schemaHash: "`+hash(t, setFlag)+`"
      effect: write
      target: [flag]
      scope: {flag: hypothesis.flag}
      candidates:
        flag: {tool: list_flags, field: "$[*].name"}
      facts:
        state: {tool: get_flag, args: {flag: "{args.flag}", window: 1h}, field: "$.on"}
      summary: "Set {args.flag}, now {facts.state}"
`)
	e.flags("x")
	h := e.hypothesis(t, "x")
	e.session.Deadline = time.Now().Add(5*time.Second + 500*time.Millisecond)
	e.sessions.Put(token, &e.session)
	e.up.set(func(u *upstream) { u.stall = map[string]bool{"get_flag": true} })
	start := time.Now()

	r := e.call(t, "flags__set_flag", fmt.Sprintf(`{"flag":"x","_hypothesis":%q}`, h))

	require.Equal(t, app.StatusRecorded, r.Status, r.Text)
	assert.Less(t, time.Since(start), 3*time.Second, "answered 5 s before the call's end")
}

// A write call, or a call by a name nothing answers to, that finds no slot
// among the session's calls before its time runs out is still audited and
// leaves a notice, as every call does (spec Section 8.1).
func TestACallThatFindsNoSlotIsStillAudited(t *testing.T) {
	e := newEnv(t, func(_ *app.Ports, o *app.Options) { o.SessionCalls = 1 })
	e.flags("x")
	h := e.hypothesis(t, "x")
	sent := len(e.up.sent())
	block := make(chan struct{})
	e.up.set(func(u *upstream) { u.onCall = func(call) { <-block } })
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = e.gw.Call(context.Background(), token, "flags__list_flags", json.RawMessage(`{}`))
	}()
	require.Eventually(t, func() bool { return len(e.up.sent()) == sent+1 }, time.Second, time.Millisecond)

	for _, c := range []struct{ name, args string }{
		{"flags__set_flag", fmt.Sprintf(`{"flag":"x","_hypothesis":%q}`, h)},
		{"no_such_tool", `{}`},
	} {
		e.session.Deadline = time.Now().Add(200 * time.Millisecond)
		e.sessions.Put(token, &e.session)
		r := e.call(t, c.name, c.args)
		assert.Equal(t, app.StatusReadError, r.Status, c.name)
		assert.Contains(t, r.Text, "did not finish", c.name)
		_, out, _, ended := e.audit.Record(r.CallID)
		assert.True(t, ended, c.name)
		assert.Equal(t, app.StatusReadError, out.Status, c.name)
	}
	close(block)
	<-done
	assert.Subset(t, e.rules(t), []board.Notice{
		{Code: board.NoticeFailedRead, Agent: "flag-investigator", Tool: "flags__set_flag"},
		{Code: board.NoticeFailedRead, Agent: "flag-investigator"},
	})
}

// A session store that hangs when the proposal is about to be committed
// costs the call no more than the record timeout, and records nothing: the
// check fails closed.
func TestASessionStoreThatHangsBeforeTheCommitRecordsNoProposal(t *testing.T) {
	var sessions *failingSessions
	e := newEnv(t, func(p *app.Ports, o *app.Options) {
		sessions = &failingSessions{Sessions: p.Sessions, hang: true}
		p.Sessions = sessions
		o.RecordTimeout = 100 * time.Millisecond
	})
	e.flags("x")
	h := e.hypothesis(t, "x")
	e.up.set(func(u *upstream) {
		u.onCall = func(c call) {
			if c.tool == "list_flags" {
				sessions.failing.Store(true)
			}
		}
	})
	done := make(chan *app.Response, 1)
	go func() {
		r, _ := e.gw.Call(context.Background(), token, "flags__set_flag", json.RawMessage(fmt.Sprintf(`{"flag":"x","_hypothesis":%q}`, h)))
		done <- r
	}()

	select {
	case r := <-done:
		assert.Equal(t, app.StatusReadError, r.Status)
	case <-time.After(5 * time.Second):
		t.Fatal("the call waited for the session store")
	}
	assert.Empty(t, e.proposals(t))
}

// A write call of a round that ended counts against no later round, even
// when it ends after the next round's calls began.
func TestAWriteCallOfAnEndedRoundCountsAgainstNoLaterRound(t *testing.T) {
	e := newEnv(t)
	e.flags("x")
	h := e.hypothesis(t, "x")
	release, started := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.up.set(func(u *upstream) {
		u.onCall = func(c call) {
			if c.tool == "list_flags" {
				once.Do(func() { close(started) })
				<-release
			}
		}
	})
	done := make(chan *app.Response, 1)
	go func() {
		r, _ := e.gw.Call(context.Background(), token, "flags__set_flag", json.RawMessage(fmt.Sprintf(`{"flag":"x","_hypothesis":%q}`, h)))
		done <- r
	}()
	<-started
	e.nextRound(t)
	for range 9 {
		require.Equal(t, app.StatusRefused, e.call(t, "flags__set_flag", `{"flag":"x"}`).Status)
	}
	close(release)
	ended := <-done
	require.Equal(t, app.StatusRefused, ended.Status, "its round ended")
	require.Contains(t, ended.Text, "round 1")

	r := e.call(t, "flags__set_flag", fmt.Sprintf(`{"flag":"x","_hypothesis":%q}`, h))

	assert.Equal(t, app.StatusRecorded, r.Status, r.Text)
}

// A write call that still holds a round that ended neither resets the
// current round's count of refusals nor counts against it.
func TestAWriteCallOfAnEndedRoundLeavesTheRoundsCapAlone(t *testing.T) {
	e := newEnv(t)
	e.flags("x")
	h := e.hypothesis(t, "x")
	stale := e.session
	e.nextRound(t)
	e.sessions.Put("stale", &stale)
	for range 10 {
		require.Equal(t, app.StatusRefused, e.call(t, "flags__set_flag", `{"flag":"x"}`).Status)
	}
	old, err := e.gw.Call(t.Context(), "stale", "flags__set_flag", json.RawMessage(fmt.Sprintf(`{"flag":"x","_hypothesis":%q}`, h)))
	require.NoError(t, err)
	require.Equal(t, app.StatusRefused, old.Status)
	require.Contains(t, old.Text, "round 1", "the board refused it: its round ended")

	r := e.call(t, "flags__set_flag", fmt.Sprintf(`{"flag":"x","_hypothesis":%q}`, h))

	assert.Equal(t, app.StatusRefused, r.Status)
	assert.Contains(t, r.Text, "no more are checked until the next round", "the round's cap still holds")
}
