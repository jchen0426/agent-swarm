package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jchen0426/agent-swarm/internal/actions/redact"
	"github.com/jchen0426/agent-swarm/internal/actions/tools"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/registry/tmpl"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/key"
)

// hypothesisArg is the argument the gateway adds to every write tool: the
// hypothesis the action addresses. It is stored with the proposal and never
// sent to any server (spec Section 5.3).
const hypothesisArg = "_hypothesis"

// maxRefusedWrites caps the write calls of one session that are refused in
// a round; later ones are refused before any read is made for them.
const maxRefusedWrites = 10

// hypothesisProp describes _hypothesis to agents.
var hypothesisProp = map[string]any{
	"type": "string", "maxLength": 64,
	"description": "The id of the hypothesis this action addresses; you must support it with evidence of your own. " +
		"The call is recorded as a proposal for a person and never executed.",
}

// writeListed returns a write tool as agents see it: its verified
// definition, with the required argument _hypothesis.
func writeListed(name string, v *visibleTool, ref config.ToolRef) (Tool, bool) {
	schema, err := tools.WithArgument(v.def.InputSchema, hypothesisArg, hypothesisProp)
	if err != nil {
		return Tool{}, false
	}
	return Tool{
		Name: name, Title: v.def.Title, Description: v.def.Description, InputSchema: schema,
		Annotations: json.RawMessage(v.def.Annotations), kind: writeTool, ref: ref,
	}, true
}

// extensible reports whether the gateway can add _hypothesis to a write
// tool's input schema: the schema neither declares nor requires an argument
// of that name in any case, and no other keyword at its root could refuse
// it, so that a runtime checking arguments against the listed schema can
// send it.
func extensible(raw []byte, schema *tools.Schema) bool {
	return !schema.Declares(hypothesisArg) && !schema.Requires(hypothesisArg) && tools.OpenRoot(raw)
}

// write records a write call as a proposal for a person; nothing is sent to
// any server (spec Sections 5.3 and 6.3). A session's refused write calls
// are capped per round, counting those in flight as refused until they end,
// so calls made at once cannot pass the cap.
func (g *Gateway) write(ctx context.Context, token string, s *Session, rev *config.Revision, t *Tool, raw json.RawMessage) *Response {
	w := &writeCall{g: g, token: token, s: s, rev: rev, ref: t.ref}
	policy := rev.Tool(t.ref)
	w.rec = &CallRecord{
		ID: g.p.IDs.NewID(), Run: s.Run, Agent: s.Agent, Server: t.ref.Server, Tool: t.ref.Tool,
		SchemaHash: policy.SchemaHash, Args: json.RawMessage("null"), Round: s.Round, At: g.p.Now(),
	}
	if s.DiagnosisOnly {
		return g.end(ctx, w.rec, StatusRefused, "write tools are hidden in this item: its trigger failed a consistency check")
	}
	release, busy := g.flights.take(ctx, token, writeFlight, g.opts.SessionCalls, g.opts.SessionCalls)
	if busy != nil {
		return g.end(ctx, w.rec, StatusReadError, "your other calls did not finish before the round ended")
	}
	defer release()
	if !g.reserveWrite(s) {
		return g.end(ctx, w.rec, StatusRefused, "too many of your write calls were refused in this round; no more are checked until the next round")
	}
	refused := true
	defer func() { g.settleWrite(s, refused) }()
	resp := w.run(ctx, policy, raw)
	refused = resp.Status == StatusRefused
	return resp
}

// writeCall is one write call in flight.
type writeCall struct {
	g     *Gateway
	token string
	s     *Session
	rev   *config.Revision
	ref   config.ToolRef
	rec   *CallRecord
	begun bool // the call's record is committed
}

// run checks a write call and records it, in this order: the arguments,
// read strictly once _hypothesis is split off, and the call's record; the
// board's checks, before any read; the scope; the candidates, read for the
// platform and charged to the session; the facts, until a reserve before the
// call's end; the summary; then the proposal, committed apart from the
// call's time, unless the run stopped. The call ends refused or recorded.
func (w *writeCall) run(ctx context.Context, policy *config.ToolPolicy, raw json.RawMessage) *Response {
	g, s := w.g, w.s
	red := redactor(w.rev, policy, ownToken(w.token)...)
	v, refused := w.gate(ctx, red)
	if refused != nil {
		return refused
	}
	args, cited, err := v.schema.ReadWithout(raw, g.opts.MaxArgsBytes, hypothesisArg)
	if err != nil {
		return w.refuse(ctx, red.Text(err.Error()))
	}
	all := maps.Clone(args.Values())
	all[hypothesisArg] = cited
	w.rec.Args = redactArgs(red, all)
	if err := g.p.Audit.Begin(ctx, w.rec); err != nil {
		g.callNotice(ctx, w.rec, StatusRefused, "")
		return &Response{CallID: w.rec.ID, Source: source(w.ref), Status: StatusRefused, Text: "the call could not be recorded, so it was not made"}
	}
	w.begun = true
	h, ok := cited.(string)
	if !ok || h == "" || utf8.RuneCountInString(h) > 64 {
		return w.refuse(ctx, "_hypothesis must be the id of the hypothesis this action addresses")
	}
	p := &board.Proposal{
		Proposer: s.Agent, Tool: w.ref.String(), SchemaHash: policy.SchemaHash,
		ArgsHash: argsHash(redactArgs(red, args.Values())), Hypothesis: board.EntryID(h), CallID: w.rec.ID,
	}
	pre, err := g.p.Board.Proposable(ctx, s.Run, s.Round, p)
	switch {
	case err != nil && ruleOf(err) == "":
		return w.fail(ctx, "the board could not be reached; try again")
	case err != nil:
		return w.refuse(ctx, red.Text(err.Error()))
	case pre.Earlier != nil:
		return w.commit(ctx, nil, pre.Earlier.ID)
	}
	if p.Scope, err = g.scope(ctx, policy, args.Values(), pre.Key, s.Item); err != nil {
		return w.refuse(ctx, err.Error())
	}
	validated, err := w.candidates(ctx, policy, args.Values(), p)
	if err != nil {
		return w.refuse(ctx, err.Error())
	}
	p.Facts = w.facts(ctx, policy, validated)
	if p.Summary, err = summarize(policy.Summary, validated, p.Facts); err != nil {
		return w.refuse(ctx, "the action's summary could not be written, so it was not recorded")
	}
	return w.commit(ctx, p, "")
}

// gate checks what lets a write call be read at all: a tool whose pinned
// definition the server still offers.
func (w *writeCall) gate(ctx context.Context, red *redact.Redactor) (*visibleTool, *Response) {
	g := w.g
	if err := g.look(ctx, w.rev, w.ref.Server); err != nil {
		return nil, w.fail(ctx, describe(w.ref.Server, cause(ctx, err), red))
	}
	v, ok := g.visible(ctx, w.rev, w.ref, true)
	if !ok {
		return nil, w.refuse(ctx, "this tool is hidden: the server does not offer the definition that was reviewed")
	}
	return &v, nil
}

// refuse records and answers a refused write call.
func (w *writeCall) refuse(ctx context.Context, why string) *Response {
	return w.finish(ctx, StatusRefused, why)
}

// fail records and answers a write call that ended in a read error.
func (w *writeCall) fail(ctx context.Context, why string) *Response {
	return w.finish(ctx, StatusReadError, why)
}

// finish records how a write call ended, leaves a notice when it was
// refused or failed, and answers it.
func (w *writeCall) finish(ctx context.Context, status Status, text string) *Response {
	if !w.begun {
		return w.g.end(ctx, w.rec, status, text)
	}
	w.g.record(ctx, &Outcome{ID: w.rec.ID, Status: status, Output: text, At: w.g.p.Now()})
	w.g.callNotice(ctx, w.rec, status, "")
	return &Response{CallID: w.rec.ID, Source: source(w.ref), Status: status, Text: text}
}

// commit records the proposal, or the proposal the call repeats, apart from
// the call's time: a slow fact server or the round's end must not lose an
// action every check passed. A run that stopped records nothing; neither
// does a session that cannot be read again within the record timeout, or an
// item that turned diagnosis-only meanwhile. The agent learns the proposal's
// id and nothing else: not its summary, not its facts.
func (w *writeCall) commit(ctx context.Context, p *board.Proposal, earlier board.EntryID) *Response {
	g := w.g
	if errors.Is(context.Cause(ctx), errStopped) {
		return w.fail(ctx, "the run stopped")
	}
	check, cancel := context.WithTimeout(context.WithoutCancel(ctx), g.opts.RecordTimeout)
	now, err := g.p.Sessions.Session(check, w.token)
	cancel()
	switch {
	case errors.Is(err, ErrNoSession):
		return w.fail(ctx, "the run stopped")
	case err != nil:
		return w.fail(ctx, "the session could not be checked again, so no proposal was made; try again")
	case now.DiagnosisOnly:
		return w.refuse(ctx, "write tools are hidden in this item: its trigger failed a consistency check")
	}
	id, repeat := earlier, earlier != ""
	if p != nil {
		detached, cancel := context.WithTimeout(context.WithoutCancel(ctx), g.opts.RecordTimeout)
		defer cancel()
		posted, err := g.p.Board.Propose(detached, w.s.Run, w.s.Round, p)
		switch {
		case err != nil && ruleOf(err) == "":
			return w.fail(ctx, "the proposal could not be recorded; try again")
		case err != nil:
			return w.refuse(ctx, redactor(w.rev, w.rev.Tool(w.ref)).Text(err.Error()))
		}
		id, repeat = posted.Event.ID, posted.Repeat
	}
	text := fmt.Sprintf("recorded as proposal %s for a person; nothing was executed", id)
	if repeat {
		text = fmt.Sprintf("this repeats proposal %s, which you made before; nothing new was recorded", id)
	}
	return w.finish(ctx, StatusRecorded, text)
}

// argsHash names redacted arguments by their SHA-256.
func argsHash(args json.RawMessage) string {
	sum := sha256.Sum256(args)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// scope checks each scoped argument against its binding (spec Section
// 3.3): the value the binding gives, from the cited hypothesis's key and the
// item's trusted routing values, must equal the argument, a JSON string.
func (g *Gateway) scope(ctx context.Context, policy *config.ToolPolicy, values map[string]any, hypothesis key.Key, item map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for _, arg := range slices.Sorted(maps.Keys(policy.Scope)) {
		want, err := g.p.Conditions.Scope(ctx, policy.Scope[arg], hypothesis, item)
		if err != nil {
			return nil, fmt.Errorf("the scope of %s cannot be checked against the cited hypothesis, so the action is refused", arg)
		}
		if got, ok := values[arg].(string); !ok || got != want {
			return nil, fmt.Errorf("%s is outside this action's scope: it must act on what the cited hypothesis names", arg)
		}
		out[arg] = want
	}
	return out, nil
}

// candidates checks each argument that has candidates (spec Section 3.3),
// in an order where a read uses only arguments already checked. The argument
// must be present, a string, number or boolean that a reader sees whole, and
// equal one value its read selects, compared as JSON. Each read is a platform
// read charged to the session and named for the write call. A refusal is a
// fixed sentence: it never repeats what a read answered, which may come from
// tools agents cannot call. It fills the proposal's target and candidate
// reads, and returns the validated arguments, which alone fill later reads.
func (w *writeCall) candidates(ctx context.Context, policy *config.ToolPolicy, values map[string]any, p *board.Proposal) (Values, error) {
	validated := Values{}
	p.Candidates = map[string]string{}
	order, err := candidateOrder(policy)
	if err != nil {
		return Values{}, err
	}
	for _, arg := range order {
		v, present := values[arg]
		text, scalar := scalarText(v)
		switch {
		case !present || !scalar:
			return Values{}, fmt.Errorf("%s is required, as a string, number or boolean: its value is checked against its candidates", arg)
		case !plain(text):
			return Values{}, fmt.Errorf("%s holds a character a reader could miss, or more than %d characters", arg, board.MaxValueRunes)
		}
		res, err := w.g.platformRead(ctx, w.read(policy.Candidates[arg], validated))
		if err != nil {
			return Values{}, fmt.Errorf("the candidates of %s could not be read, so the action cannot be checked", arg)
		}
		want := jsonText(v)
		if !slices.ContainsFunc(res.Values, func(c any) bool { return jsonText(c) == want }) {
			return Values{}, fmt.Errorf("%s is not among its candidates", arg)
		}
		validated = validated.with("args."+arg, text)
		p.Candidates[arg] = res.CallID
	}
	for _, arg := range policy.Target {
		p.Target = append(p.Target, board.TargetValue{Arg: arg, Value: validated.m["args."+arg]})
	}
	return validated, nil
}

// read is a platform read made for this write call.
func (w *writeCall) read(pr *config.PlatformRead, values Values) *platformCall {
	return &platformCall{
		rev: w.rev, run: w.s.Run, home: w.ref.Server, pr: pr, values: values,
		class: w.s.Class, token: w.token, cause: w.rec.ID,
	}
}

// candidateOrder orders the arguments that have candidates so that each read
// uses only arguments before it; among those ready, by name.
func candidateOrder(policy *config.ToolPolicy) ([]string, error) {
	deps := map[string][]string{}
	for arg, r := range policy.Candidates {
		for _, a := range r.Args {
			tpl, err := tmpl.Parse(a)
			if err != nil {
				return nil, fmt.Errorf("the candidates of %s cannot be read", arg)
			}
			for _, hole := range tpl.Names() {
				if dep, ok := strings.CutPrefix(hole, "args."); ok {
					deps[arg] = append(deps[arg], dep)
				}
			}
		}
	}
	var order []string
	done := map[string]bool{}
	for len(order) < len(policy.Candidates) {
		ready := ""
		for _, arg := range slices.Sorted(maps.Keys(policy.Candidates)) {
			if !done[arg] && !slices.ContainsFunc(deps[arg], func(d string) bool { return !done[d] }) {
				ready = arg
				break
			}
		}
		if ready == "" {
			return nil, errors.New("the candidates of this tool depend on each other, so the action cannot be checked")
		}
		order, done[ready] = append(order, ready), true
	}
	return order, nil
}

// factReserve is the time the facts leave before the call's end, so that
// the agent is answered while its round lasts.
const factReserve = 5 * time.Second

// facts reads each fact (spec Section 3.3), until factReserve before the
// call's end. A fact keeps its first board.MaxFactValues values, as JSON,
// and counts the rest. One that cannot be read, selects nothing, comes too
// late, or holds a value a reader could not see whole is unavailable: the
// recorded action stands without it.
func (w *writeCall) facts(ctx context.Context, policy *config.ToolPolicy, validated Values) map[string]board.Fact {
	out := map[string]board.Fact{}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = w.g.p.Now().Add(w.g.opts.CallTimeout)
	}
	ctx, cancel := context.WithDeadline(ctx, deadline.Add(-factReserve))
	defer cancel()
	for _, name := range slices.Sorted(maps.Keys(policy.Facts)) {
		if ctx.Err() != nil {
			out[name] = board.Fact{Unavailable: "no_time"}
			continue
		}
		res, err := w.g.platformRead(ctx, w.read(policy.Facts[name], validated))
		switch {
		case errors.Is(err, ErrNothingRead):
			out[name] = board.Fact{Unavailable: "nothing"}
		case err != nil:
			out[name] = board.Fact{Unavailable: "unreadable"}
		default:
			out[name] = fact(res)
		}
	}
	return out
}

// fact keeps a fact's values as a proposal holds them. A value is kept only
// if a reader sees all of it: every string and key in it plain, and its JSON
// at most board.MaxValueRunes characters.
func fact(res *PlatformResult) board.Fact {
	f := board.Fact{CallID: res.CallID}
	for i, v := range res.Values {
		if i == board.MaxFactValues {
			f.More = len(res.Values) - i
			break
		}
		text := jsonText(v)
		if !plainValue(v) || utf8.RuneCountInString(text) > board.MaxValueRunes {
			return board.Fact{CallID: res.CallID, Unavailable: "not_plain"}
		}
		f.Values = append(f.Values, text)
	}
	return f
}

// summarize renders a write tool's summary by rules, from validated
// arguments and facts, keeping the policy's text apart from the values
// (spec Section 3.3): a screen shows values as data, never as the
// platform's words. A fact with several values joins them with ", " and
// says how many more there are; an unavailable one shows as [unavailable].
// Each value shows at most board.MaxValueRunes characters, and the summary
// at most board.MaxSummaryRunes, a cut marked with an ellipsis: display data never
// refuses an action.
func summarize(text string, validated Values, facts map[string]board.Fact) ([]board.SummaryPart, error) {
	tpl, err := tmpl.Parse(text)
	if err != nil {
		return nil, err
	}
	parts, err := tpl.RenderParts(func(hole string) (string, bool) {
		if strings.HasPrefix(hole, "args.") {
			v, ok := validated.m[hole]
			return v, ok
		}
		name, _ := strings.CutPrefix(hole, "facts.")
		f, ok := facts[name]
		if !ok {
			return "", false
		}
		return factShown(&f), true
	})
	if err != nil {
		return nil, err
	}
	out := make([]board.SummaryPart, 0, len(parts))
	room := board.MaxSummaryRunes
	for _, p := range parts {
		shown := p.Text
		if p.Hole {
			shown = cutRunes(shown, board.MaxValueRunes)
		}
		shown = cutRunes(shown, room)
		room -= utf8.RuneCountInString(shown)
		out = append(out, board.SummaryPart{Text: shown, Value: p.Hole})
		if room == 0 {
			break
		}
	}
	return out, nil
}

// factShown is how a summary shows a fact.
func factShown(f *board.Fact) string {
	if f.Unavailable != "" {
		return "[unavailable]"
	}
	var shown []string
	for _, v := range f.Values {
		var s string
		if json.Unmarshal([]byte(v), &s) != nil {
			s = v
		}
		shown = append(shown, s)
	}
	if f.More > 0 {
		shown = append(shown, fmt.Sprintf("and %d more", f.More))
	}
	return strings.Join(shown, ", ")
}

// cutRunes shortens s to at most n characters, the last of them an
// ellipsis when it cuts.
func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	if n <= 0 {
		return ""
	}
	return string([]rune(s)[:n-1]) + "\u2026"
}

// plain reports whether a value shows whole to a reader: no control or
// hidden character, and at most board.MaxValueRunes characters.
func plain(s string) bool {
	return utf8.RuneCountInString(s) <= board.MaxValueRunes && !strings.ContainsFunc(s, unseen)
}

// plainValue reports whether every string and key in a decoded value is
// free of control and hidden characters.
func plainValue(v any) bool {
	switch t := v.(type) {
	case string:
		return !strings.ContainsFunc(t, unseen)
	case map[string]any:
		for k, e := range t {
			if strings.ContainsFunc(k, unseen) || !plainValue(e) {
				return false
			}
		}
	case []any:
		return !slices.ContainsFunc(t, func(e any) bool { return !plainValue(e) })
	}
	return true
}

// unseen reports whether a reader could miss r: a control or hidden
// character.
func unseen(r rune) bool { return r < ' ' || r == 0x7f || config.Hidden(r) }

// scalarText returns a scalar argument as text: a string as itself, a number
// as written, a boolean as true or false.
func scalarText(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case json.Number:
		return t.String(), true
	case bool:
		if t {
			return "true", true
		}
		return "false", true
	}
	return "", false
}

// jsonText writes a value as compact JSON, numbers as written.
func jsonText(v any) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return ""
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// with returns the values and one more: an argument the candidate check
// validated, which only this file adds.
func (v Values) with(name, value string) Values {
	m := maps.Clone(v.m)
	if m == nil {
		m = map[string]string{}
	}
	m[name] = value
	return Values{m: m}
}

// reserveWrite takes one of the round's refusals for a session's write call
// that starts, unless its refused and running write calls of the round reach
// maxRefusedWrites. A session's count starts again with each round. A call
// that still holds a round that ended, which the board refuses before any
// read, counts against no round.
func (g *Gateway) reserveWrite(s *Session) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	k := listKey{s.Run, s.Agent}
	r, ok := g.refused[k]
	switch {
	case ok && r.round > s.Round:
		return true
	case !ok || r.round < s.Round:
		r = refusals{round: s.Round}
	}
	if r.refused+r.running >= maxRefusedWrites {
		return false
	}
	if !ok && len(g.refused) >= maxLists {
		clear(g.refused)
	}
	r.running++
	g.refused[k] = r
	return true
}

// settleWrite ends a write call's reservation, counting it when the call was
// refused, unless the session's round moved on meanwhile.
func (g *Gateway) settleWrite(s *Session, refused bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	k := listKey{s.Run, s.Agent}
	r, ok := g.refused[k]
	if !ok || r.round != s.Round {
		return
	}
	if r.running > 0 {
		r.running--
	}
	if refused {
		r.refused++
	}
	g.refused[k] = r
}

// refusals counts a session's write calls of one round that were refused,
// and those still running.
type refusals struct {
	round, refused, running int
}
