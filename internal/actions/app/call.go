package app

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/jchen0426/agent-swarm/internal/actions/limits"
	"github.com/jchen0426/agent-swarm/internal/actions/output"
	"github.com/jchen0426/agent-swarm/internal/actions/redact"
	"github.com/jchen0426/agent-swarm/internal/holding"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/work/board"
)

// Response is what a caller is told of one call.
type Response struct {
	// CallID names the call in the audit store; evidence cites it.
	CallID string
	// Source is the server and tool, as server/tool.
	Source string
	Status Status
	// Text is the canonical output when Status is ok, and otherwise what
	// went wrong, redacted.
	Text string
	// Notes say what was left out of the output.
	Notes []string
	// TurnOver is set on the answer to a board call that its round's end or
	// its run's stop refused or cut short: the session can post nothing more
	// in its round, and the endpoint answers it as a session gone (Plan
	// 7d-1, Decision 13).
	TurnOver bool

	// failure is the side the call failed on, which the write call a
	// platform read is made for records as its own.
	failure Failure
}

// ErrUnavailable answers a call the gateway cannot serve now: its revision
// unreadable, its record not begun, its holding refused, the gateway
// draining, or, for a board call, the board unreachable, which is recorded.
var ErrUnavailable = errors.New("app: the gateway cannot serve this call now")

// The causes of a call cut short, which leaves no failure (Decision 20):
// errStopped ends the calls of a session whose run stopped, and
// errRoundEnded those of a session whose round ended.
var (
	errStopped    = errors.New("app: the run stopped")
	errRoundEnded = errors.New("app: the round ended")
)

// recordAttempts is how often the gateway tries to record a call's outcome.
const recordAttempts = 3

// Call makes one agent's call (spec Section 6.2). A call the gateway
// refuses, or that ends in a read error, is still a Response, whose record
// names the side its failure was on (Decision 20). The error is for a
// session that does not exist, and ErrUnavailable for a call the gateway
// cannot serve now: its revision cannot be read, its record cannot be begun,
// the holding it acts for is over, or the gateway drains, so that a failure
// the records would lack is never answered as a call. Every write the call
// makes, those after its context ended included, acts for the holding that
// issued the session.
func (g *Gateway) Call(ctx context.Context, token, name string, args json.RawMessage) (*Response, error) {
	if err := g.enter(); err != nil {
		return nil, err
	}
	defer g.leave()
	resp, err := g.call(ctx, token, name, args)
	if resp != nil && isBoardTool(name) {
		// A board call that failed on no side was refused, or cut short, by
		// its round's end or its run's stop (Decision 20).
		resp.TurnOver = resp.Status != StatusOK && resp.failure == ""
	}
	return resp, err
}

func (g *Gateway) call(ctx context.Context, token, name string, args json.RawMessage) (*Response, error) {
	s, err := g.p.Sessions.Session(ctx, token)
	if err != nil {
		return nil, err
	}
	g.see(s.Epoch)
	ctx = holding.WithEpoch(ctx, s.Epoch)
	rev, err := g.p.Revisions.At(ctx, s.Commit)
	if err != nil {
		return nil, fmt.Errorf("%w: revision %s: %w", ErrUnavailable, s.Commit, err)
	}
	if !s.Deadline.IsZero() && !g.p.Now().Before(s.Deadline) {
		// What the call did would count in no round, and the round's end
		// may be recorded already.
		rec := &CallRecord{ID: g.p.IDs.NewID(), Run: s.Run, Agent: s.Agent, Tool: storedName(rev, token, name), Args: json.RawMessage("null"), Round: s.Round, At: g.p.Now()}
		return g.end(ctx, rec, StatusRefused, "", "the round has ended: your turn is over")
	}
	ctx, cancel := g.callContext(ctx, s.Deadline, s.Done)
	defer cancel()
	list, err := g.list(ctx, &s)
	if err != nil && (ctx.Err() == nil || unheld(err)) {
		// A listing that failed once the call's context ended was cut short,
		// unless the holding refused it: the call is recorded as cut short
		// below.
		return nil, err
	}
	i := slices.IndexFunc(list, func(t Tool) bool { return t.Name == name })
	if i < 0 {
		// The name is the agent's own text, so it is stored as storedName
		// gives it: redacted, or a placeholder.
		rec := &CallRecord{ID: g.p.IDs.NewID(), Run: s.Run, Agent: s.Agent, Tool: storedName(rev, token, name), Args: json.RawMessage("null"), Round: s.Round, At: g.p.Now()}
		if err != nil {
			// The call's time ran out while its tools were listed, as when a
			// look hangs, unless the round's end or the run's stop cut it.
			return g.end(ctx, rec, StatusReadError, cutOr(ctx, FailureServer), ended(ctx))
		}
		release, busy := g.flights.take(ctx, token, unknownFlight, g.opts.SessionCalls, g.opts.SessionCalls)
		if busy != nil {
			return g.end(ctx, rec, StatusReadError, cutOr(ctx, FailureAgent), "your other calls did not finish before the round ended")
		}
		defer release()
		return g.end(ctx, rec, StatusRefused, FailureAgent, "there is no tool by that name in your list")
	}
	switch list[i].kind {
	case boardTool:
		return g.boardCall(ctx, token, &s, rev, &list[i], args)
	case writeTool:
		return g.write(ctx, token, &s, rev, &list[i], args)
	}
	ref := list[i].ref
	release, err := g.flights.take(ctx, token, ref.Server, g.opts.SessionCalls, g.opts.ServerCalls)
	if err != nil {
		rec := &CallRecord{ID: g.p.IDs.NewID(), Run: s.Run, Agent: s.Agent, Server: ref.Server, Tool: ref.Tool, Args: json.RawMessage("null"), Round: s.Round, At: g.p.Now()}
		return g.end(ctx, rec, StatusReadError, cutOr(ctx, FailureAgent), "your other calls did not finish before the round ended")
	}
	defer release()
	return g.read(ctx, &read{rev: rev, ref: ref, class: s.Class, run: s.Run, agent: s.Agent, round: s.Round, args: args, token: token})
}

// callContext ends at the earliest of the round's end, the call timeout and
// the session's end, and gives the round's end and the session's as its
// cause, so that a call they cut short is told from one that timed out.
func (g *Gateway) callContext(ctx context.Context, roundEnd time.Time, done <-chan struct{}) (context.Context, context.CancelFunc) {
	deadline, why := g.p.Now().Add(g.opts.CallTimeout), error(nil)
	if !roundEnd.IsZero() && !roundEnd.After(deadline) {
		deadline, why = roundEnd, errRoundEnded
	}
	ctx, stop := context.WithCancelCause(ctx)
	ctx, cancel := context.WithDeadlineCause(ctx, deadline, why)
	if done != nil {
		go func() {
			select {
			case <-done:
				stop(errStopped)
			case <-ctx.Done():
			}
		}()
	}
	return ctx, func() {
		cancel()
		stop(nil)
	}
}

// ended says why a call's context ended before its tools were listed.
func ended(ctx context.Context) string {
	if errors.Is(context.Cause(ctx), errStopped) {
		return "the run stopped"
	}
	return "your tools could not be listed before the call's time ran out"
}

// read is one read call, by an agent or by the platform.
type read struct {
	rev   *config.Revision
	ref   config.ToolRef
	class limits.Class
	run   board.RunID
	agent string
	round int
	args  json.RawMessage
	cause string // the write call a platform read is made for
	// token is the token of the session the read is made for, or is empty.
	token string
}

// read runs the read path in the order of spec Section 6.2: policy, which
// refuses arguments past a bound, with a session token, or with an escape
// that may hide what a rule matches; server limits, the audit record,
// forwarding, partialWhen and redaction, the stored outcome, then the answer.
// Each way it ends records the side its failure was on (Decision 20). A read
// whose record cannot be begun is not made: it fails wrapping
// ErrUnavailable, as does one the holding refuses, before its record is
// begun or after, which records no failure.
func (g *Gateway) read(ctx context.Context, r *read) (*Response, error) {
	srv := r.rev.Server(r.ref.Server)
	policy := srv.Spec.Tools[r.ref.Tool]
	red := redactor(r.rev, policy, ownToken(r.token)...)
	rec := &CallRecord{
		ID: g.p.IDs.NewID(), Run: r.run, Agent: r.agent, Server: r.ref.Server, Tool: r.ref.Tool,
		SchemaHash: policy.SchemaHash, Args: json.RawMessage("null"), Round: r.round, At: g.p.Now(), Cause: r.cause,
	}
	if policy.Effect != config.EffectRead {
		// Only read tools are ever forwarded: in Phase 0 no write reaches a
		// server (spec Section 6.3), whatever list a caller holds.
		return g.end(ctx, rec, StatusRefused, FailureAgent, "only read tools are forwarded")
	}
	lim := g.p.Limits.Server(r.ref.Server)
	if err := g.look(ctx, r.rev, r.ref.Server); err != nil {
		if unheld(err) {
			return nil, err
		}
		if errors.Is(err, ErrConnection) {
			lim.Failed()
		}
		return g.end(ctx, rec, StatusReadError, serverFailure(ctx, err), describe(r.ref.Server, cause(ctx, err), red))
	}
	v, ok, err := g.visible(ctx, r.rev, r.ref)
	switch {
	case err != nil:
		return g.end(ctx, rec, StatusReadError, cutOr(ctx, FailurePlatform), hidesUnread)
	case !ok:
		return g.end(ctx, rec, StatusRefused, FailureServer, "this tool is hidden: the server does not offer the definition that was reviewed")
	}
	args, err := v.schema.Read(r.args, g.opts.MaxArgsBytes)
	if err == nil {
		rec.Args = redactArgs(red, args.Values())
		err = v.schema.CheckBounds(args, policy.Bounds)
	}
	if err != nil {
		return g.end(ctx, rec, StatusRefused, FailureAgent, reasonOf(red, err))
	}
	if why := argsRefusal(red, args.Values(), r.token); why != "" {
		return g.end(ctx, rec, StatusRefused, FailureAgent, why)
	}
	if r.agent != "" {
		if red, err = repeats(red, args.Values()); err != nil {
			return g.end(ctx, rec, StatusRefused, FailureAgent, err.Error())
		}
	}
	release, err := lim.Acquire(ctx, r.class)
	if err != nil {
		msg, failure := r.ref.Server+" is degraded after repeated connection failures; try again later", FailureServer
		if !errors.Is(err, limits.ErrDegraded) {
			msg, failure = "no capacity on "+r.ref.Server+" before the round ended", cutOr(ctx, FailureServer)
		}
		return g.end(ctx, rec, StatusReadError, failure, msg)
	}
	defer release()
	return g.send(ctx, r, rec, &v, red, args.Raw, lim)
}

// send makes a read that passed its checks, once the holding allows it: it
// records the call, forwards it and records how it ended, then answers it. A
// read whose record cannot be begun is not made; one the holding refuses,
// before its record is begun or after, records no failure. The record is
// begun apart from the call's context, so a call cut short by then is
// recorded, and ends as a cut when it is forwarded.
func (g *Gateway) send(ctx context.Context, r *read, rec *CallRecord, v *visibleTool, red *redact.Redactor, args json.RawMessage, lim *limits.Server) (*Response, error) {
	if err := g.holds(ctx); err != nil {
		return nil, err
	}
	if err := g.begin(ctx, rec); err != nil {
		g.callNotice(ctx, rec, StatusRefused, "")
		return nil, unrecorded(err)
	}
	conn := &Conn{Server: Server{Name: r.ref.Server, Endpoint: v.srv.Spec.Endpoint, CredentialRef: v.srv.Spec.CredentialRef}}
	if r.agent != "" {
		conn.Run, conn.Agent = r.run, r.agent
	}
	res, err := g.forward(ctx, conn, r.ref.Tool, args, lim)
	if unheld(err) {
		// The call stays without an outcome: the holding it acts for is over.
		return nil, err
	}
	o := g.outcome(ctx, r.ref, v.policy, v, red, res, err)
	if !g.record(ctx, &Outcome{ID: rec.ID, Status: o.status, Failure: o.failure, Output: o.text, At: g.p.Now()}) {
		// The call stays without an outcome, which its round counts as the
		// platform's failure.
		g.callNotice(ctx, rec, StatusReadError, "")
		return &Response{CallID: rec.ID, Source: source(r.ref), Status: StatusReadError, Text: "the output could not be stored, so it cannot be used",
			failure: FailurePlatform}, nil
	}
	g.callNotice(ctx, rec, o.status, "")
	return &Response{CallID: rec.ID, Source: source(r.ref), Status: o.status, Text: o.text, Notes: o.notes, failure: o.failure}, nil
}

// forward sends the call, each attempt once the holding allows it, and
// while the call's context lives. A connection failure is retried while the
// server is not degraded, and counts against the server once, after the
// retries (spec Section 6.2, steps 2 and 4). A refusal of the holding's is
// no failure of the server's.
func (g *Gateway) forward(ctx context.Context, conn *Conn, tool string, args json.RawMessage, lim *limits.Server) (*output.Result, error) {
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := g.holds(ctx); err != nil {
			return nil, err
		}
		res, err := g.p.Upstream.Call(ctx, conn, tool, args)
		if errors.Is(err, ErrUnavailable) {
			return nil, g.holdingRefused(ctx)
		}
		if !errors.Is(err, ErrConnection) {
			if err == nil {
				lim.Succeeded()
			}
			return res, err
		}
		if attempt == g.opts.Retries || lim.Degraded() || sleep(ctx, g.opts.Backoff<<attempt) != nil {
			lim.Failed()
			return nil, err
		}
	}
}

// answered is how a forwarded call ended: its status, the side its failure
// was on, the text the gateway stores and shows, and what was left out of
// the output.
type answered struct {
	status  Status
	failure Failure
	text    string
	notes   []string
}

// failedOn is a forwarded call that ended in a read error on side f.
func failedOn(f Failure, text string) answered {
	return answered{status: StatusReadError, failure: f, text: text}
}

// outcome turns what the server answered into how the call ended. An output
// the gateway cannot read, check or redact is the server's failure; one too
// large to use, or partial, is the agent's, which a narrower query avoids.
func (g *Gateway) outcome(ctx context.Context, ref config.ToolRef, policy *config.ToolPolicy, v *visibleTool, red *redact.Redactor, res *output.Result, err error) answered {
	if err != nil {
		return failedOn(callFailure(ctx, err), describe(ref.Server, cause(ctx, err), red))
	}
	if res.IsError {
		return g.toolError(ref, red, res)
	}
	c, err := output.Read(res, len(v.def.OutputSchema) > 0)
	if err != nil {
		return failedOn(FailureServer, red.Text(err.Error()))
	}
	// The cap applies to the canonical form, so an output too large to use
	// is refused before partialWhen and redaction run on it.
	if len(c.Text) > g.opts.MaxOutputBytes {
		return failedOn(FailureAgent, g.outputTooLarge())
	}
	if policy.PartialWhen != "" {
		plain, ok := c.Plain()
		if !ok {
			return failedOn(FailureServer, "the output is not JSON, so its completeness cannot be checked")
		}
		partial, err := g.p.Conditions.PartialWhen(ctx, policy.PartialWhen, plain)
		switch {
		case err != nil:
			return failedOn(FailureServer, "the output could not be checked for completeness, so it cannot be used")
		case partial:
			return failedOn(FailureAgent, "the output is partial, so it cannot be used: narrow the query")
		}
	}
	shown, err := c.Show(red)
	if err != nil {
		return failedOn(FailureServer, red.Text(err.Error()))
	}
	if len(shown) > g.opts.MaxOutputBytes {
		return failedOn(FailureAgent, g.outputTooLarge())
	}
	return answered{status: StatusOK, text: shown, notes: c.Notes}
}

// toolError turns a tool's own error into how the call ended: the tool's
// failure (R1), which is how a server tells both its backend's outage and
// the agent's bad arguments; but the server's, when the error cannot be read
// or redacted.
func (g *Gateway) toolError(ref config.ToolRef, red *redact.Redactor, res *output.Result) answered {
	tooLarge := fmt.Sprintf("%s answered with an error larger than %d bytes", ref.Server, g.opts.MaxOutputBytes)
	c, err := output.Read(res, false)
	if err != nil {
		return failedOn(FailureServer, ref.Server+" answered with an error that could not be read")
	}
	if len(c.Text) > g.opts.MaxOutputBytes {
		return failedOn(FailureTool, tooLarge)
	}
	shown, err := c.Show(red)
	if err != nil {
		return failedOn(FailureServer, ref.Server+" answered with an error that could not be redacted")
	}
	if len(shown) > g.opts.MaxOutputBytes {
		return failedOn(FailureTool, tooLarge)
	}
	return answered{status: StatusReadError, failure: FailureTool, text: shown, notes: c.Notes}
}

func (g *Gateway) outputTooLarge() string {
	return fmt.Sprintf("the output is larger than %d bytes, so it cannot be used: narrow the query", g.opts.MaxOutputBytes)
}

// cause returns why ctx ended in place of a context error, so that a stopped
// run is told apart from a slow server.
func cause(ctx context.Context, err error) error {
	if c := context.Cause(ctx); errors.Is(c, errStopped) {
		return errStopped
	}
	return err
}

// cutShort reports whether a call's context ended because its round ended
// or its run stopped: a call cut short so failed on no side (Decision 20).
func cutShort(ctx context.Context) bool {
	c := context.Cause(ctx)
	return errors.Is(c, errRoundEnded) || errors.Is(c, errStopped)
}

// cutOr is the side of a call that failed once its context ended: none when
// its round's end or its run's stop cut it short, and f when its own time
// ran out.
func cutOr(ctx context.Context, f Failure) Failure {
	if cutShort(ctx) {
		return ""
	}
	return f
}

// serverFailure is the side of an error in reaching a server or reading its
// answer: the server's, but none for a call whose round's end or run's stop
// ended it.
func serverFailure(ctx context.Context, err error) Failure {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return cutOr(ctx, FailureServer)
	}
	return FailureServer
}

// callFailure is the side of an error a call to a server ended in: the
// agent's, for arguments the server answers are invalid and for an answer
// larger than the gateway reads, which a narrower query avoids; otherwise
// as serverFailure says.
func callFailure(ctx context.Context, err error) Failure {
	var se *ServerError
	switch {
	case errors.As(err, &se) && se.Code == invalidParams, errors.Is(err, ErrTooLarge):
		return FailureAgent
	}
	return serverFailure(ctx, err)
}

// hidesUnread says why a call whose hides could not be read was not made.
const hidesUnread = "the tools hidden for review could not be read, so the call was not made; try again"

// unrecorded is the error of a call whose record could not be begun: it was
// not made, and is answered unavailable, since the records would lack how it
// ended (Decision 20).
func unrecorded(err error) error {
	return fmt.Errorf("%w: the call could not be recorded: %w", ErrUnavailable, err)
}

// describe says what went wrong, in the gateway's own words: never an error
// string of the client library, which may hold URLs or payloads.
func describe(server string, err error, red *redact.Redactor) string {
	var se *ServerError
	switch {
	case errors.As(err, &se):
		return serverError(server, se.Message, red)
	case errors.Is(err, errStopped):
		return "the run stopped"
	case errors.Is(err, ErrConnection):
		return server + " could not be reached"
	case errors.Is(err, ErrTooLarge):
		return server + "'s answer is larger than the gateway reads: narrow the query"
	case errors.Is(err, ErrNeedsInput):
		return server + " asked for more input, which the gateway does not give"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return server + " gave no answer in time"
	}
	return server + "'s answer could not be read"
}

// serverError tells a server's error message redacted as an output is, as
// one text block, or that it could not be redacted.
func serverError(server, message string, red *redact.Redactor) string {
	c, err := output.Read(&output.Result{Blocks: []output.Block{{Kind: output.Text, Text: message}}}, false)
	if err == nil {
		var shown string
		if shown, err = c.Show(red); err == nil {
			return server + " answered with an error: " + cut(shown, maxServerMessage)
		}
	}
	return server + " answered with an error that could not be redacted"
}

// maxServerMessage caps the part of a server's error message an agent is
// told, after redaction, so that a cut never leaves part of a secret.
const maxServerMessage = 4 << 10

// cut shortens s to at most n bytes, on a character boundary.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// end records a call that ended before it was forwarded, with the side its
// failure was on, and answers it; it leaves the call's notice either way. A
// call that cannot be recorded is answered unavailable.
func (g *Gateway) end(ctx context.Context, rec *CallRecord, status Status, failure Failure, why string) (*Response, error) {
	err := g.begin(ctx, rec)
	if err == nil {
		g.record(ctx, &Outcome{ID: rec.ID, Status: status, Failure: failure, Output: why, At: g.p.Now()})
	}
	g.callNotice(ctx, rec, status, "")
	if err != nil {
		return nil, unrecorded(err)
	}
	src := ""
	if rec.Server != "" {
		src = rec.Server + "/" + rec.Tool
	}
	return &Response{CallID: rec.ID, Source: src, Status: status, Text: why, failure: failure}, nil
}

// begin records a call apart from the call's context, within the record
// timeout, so that a call its context ended is still recorded.
func (g *Gateway) begin(ctx context.Context, rec *CallRecord) error {
	begin, cancel := context.WithTimeout(context.WithoutCancel(ctx), g.opts.RecordTimeout)
	defer cancel()
	return g.p.Audit.Begin(begin, rec)
}

// record records an outcome, also after the call's context ended, trying a
// few times. It reports whether the outcome was recorded.
func (g *Gateway) record(ctx context.Context, o *Outcome) bool {
	ctx = context.WithoutCancel(ctx)
	for range recordAttempts {
		attempt, cancel := context.WithTimeout(ctx, g.opts.RecordTimeout)
		err := g.p.Audit.End(attempt, o)
		cancel()
		if err == nil {
			return true
		}
	}
	return false
}

// redactor returns the redaction rules for a tool: the session tokens' rule
// and those of extra, the ones its policy names, then the ones that apply
// always.
func redactor(rev *config.Revision, policy *config.ToolPolicy, extra ...redact.Rule) *redact.Redactor {
	rules := append([]redact.Rule{redact.SessionTokens()}, extra...)
	named := map[string]bool{}
	for _, name := range policy.Redact {
		if r := rev.RedactionRule(name); r != nil {
			rules = append(rules, redact.FromConfig(r))
			named[name] = true
		}
	}
	for _, r := range rev.AlwaysRedactionRules() {
		if !named[r.Metadata.Name] {
			rules = append(rules, redact.FromConfig(r))
		}
	}
	return redact.New(rules...)
}

// Limits of the values whose repeats an output shows as stored. A value
// that folds to fewer bytes is left alone: replacing it would mangle the
// output, and it cannot carry a claim. More values than maxRepeats refuse
// the call, which bounds the work redaction does on the output.
const (
	minRepeatBytes = 4
	maxRepeats     = 32
)

// repeats returns red with rules before its own for each string, key and
// number of an agent's read arguments that red changes in the stored
// arguments: where the output repeats the value as the agent wrote it, it
// shows it as the stored arguments do. Otherwise a read that echoes its
// arguments would let an agent cite its own words, which neither the echo
// check nor a rater, who sees the stored arguments, could tell from the
// server's answer (spec Section 3.3). A value is found as a literal, in any
// case and with any run of white space where it has one, as quotes are
// compared, and in the spellings JSON gives it; longer values come first,
// so that a value inside another never splits it.
func repeats(red *redact.Redactor, values map[string]any) (*redact.Redactor, error) {
	var changes []redact.Change
	seen := map[string]bool{}
	for _, c := range red.Changes(values) {
		if len(redact.Fold(c.From)) < minRepeatBytes || seen[c.From] {
			continue
		}
		seen[c.From] = true
		changes = append(changes, c)
	}
	if len(changes) > maxRepeats {
		return nil, fmt.Errorf("the redaction rules change %d of the arguments' values, more than the %d the output can be checked for; narrow the call", len(changes), maxRepeats)
	}
	slices.SortFunc(changes, func(a, b redact.Change) int { return cmp.Compare(len(b.From), len(a.From)) })
	rules := make([]redact.Rule, 0, len(changes))
	for _, c := range changes {
		rules = append(rules, redact.Rule{Literals: spellings(c.From), Replacement: c.To})
	}
	return red.Before(rules...), nil
}

// spellings returns how an output may write a value: as it is, and escaped
// as JSON writes it, by default, with every character beyond ASCII escaped,
// and with each slash escaped too, as some encoders write it.
func spellings(v string) []string {
	raw, _ := json.Marshal(v)
	escaped := string(raw[1 : len(raw)-1])
	var ascii strings.Builder
	for _, r := range escaped {
		switch {
		case r < utf8.RuneSelf:
			ascii.WriteRune(r)
		case r > 0xFFFF:
			hi, lo := utf16.EncodeRune(r)
			fmt.Fprintf(&ascii, "\\u%04x\\u%04x", hi, lo)
		default:
			fmt.Fprintf(&ascii, "\\u%04x", r)
		}
	}
	out := []string{v}
	for _, e := range []string{escaped, ascii.String()} {
		for _, s := range []string{e, strings.ReplaceAll(e, "/", `\/`)} {
			if !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
	}
	return out
}

// redactArgs returns the arguments as the audit store keeps them, redacted.
// Arguments that hold an escape that may hide what a rule matches are kept as
// null, in the record of a call that is refused: redaction cannot reach what
// the escape hides, and a stored copy would show it to whoever decodes the
// copy again.
func redactArgs(red *redact.Redactor, values map[string]any) json.RawMessage {
	redacted := red.Value(values)
	if red.EscapedValue(redacted) {
		return json.RawMessage("null")
	}
	return encodeArgs(redacted)
}

// escapedArgs reports whether the arguments hold an escape that may hide what
// a rule of red matches. A read or write call whose arguments do is refused,
// and its record keeps them as null.
func escapedArgs(red *redact.Redactor, values map[string]any) bool {
	return red.EscapedValue(red.Value(values))
}

// encodeArgs writes redacted arguments as JSON, or as null if they cannot be
// written.
func encodeArgs(redacted any) json.RawMessage {
	raw, err := json.Marshal(redacted)
	if err != nil {
		return json.RawMessage("null")
	}
	return raw
}

// escapedReason is what a refusal says when the arguments hold an escape that
// may hide a secret, and in place of the text of an error that quotes what the
// agent wrote, when that holds one.
const escapedReason = "the arguments hold an escape that may hide a secret; write them plainly"

// reasonOf returns the text of err, which may quote what the agent wrote, as a
// refusal states it: redacted, or, if it holds an escape that may hide what a
// rule matches, escapedReason, because redaction cannot reach what the escape
// hides.
func reasonOf(red *redact.Redactor, err error) string {
	text := err.Error()
	if red.Escaped(text) {
		return escapedReason
	}
	return red.Text(text)
}

// escapedName is what the audit store keeps in place of the name of a tool
// that holds an escape that may hide a secret.
const escapedName = "[redacted:escaped]"

// storedName returns the name of a tool that the agent asked for but cannot
// call, as the audit store keeps it. The name is the agent's own text: it is
// redacted and cut, or, if it holds an escape that may hide what a rule
// matches, kept as escapedName. The whole name is looked at for the escape,
// and the cut is made after the redaction, so that it never leaves part of a
// secret.
func storedName(rev *config.Revision, token, name string) string {
	red := always(rev, ownToken(token)...)
	if red.Escaped(name) {
		return escapedName
	}
	return cut(red.Text(name), maxNameBytes)
}

func source(ref config.ToolRef) string { return ref.Server + "/" + ref.Tool }

// maxNameBytes caps the stored name of a tool an agent asked for but cannot
// see.
const maxNameBytes = 128

// tokenForm matches a session token whole.
var tokenForm = regexp.MustCompile(`^` + config.SessionTokenPattern + `$`)

// ownToken returns the rule that hides the letters and digits of the
// caller's own token, in any case, which no prefix written otherwise hides;
// none for a token not in a session token's form.
func ownToken(token string) []redact.Rule {
	if !tokenForm.MatchString(token) {
		return nil
	}
	own := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(token[len("aswm_"):]))
	return []redact.Rule{{Patterns: []*regexp.Regexp{own}, Replacement: config.SessionTokenReplacement}}
}

// tokensOf returns the rules that find a session token in arguments: any
// session's, and the caller's own, as ownToken finds it.
func tokensOf(token string) *redact.Redactor {
	return redact.New(append([]redact.Rule{redact.SessionTokens()}, ownToken(token)...)...)
}

// tokenRefusal says why arguments are not sent when they hold a session
// token, in any string, key or number, or an escape that may hide one: the
// gateway sends a server the arguments as the agent wrote them, unredacted.
// It returns nothing for arguments that hold neither; token is the caller's.
func tokenRefusal(v any, token string) string {
	red := tokensOf(token)
	switch {
	case !reflect.DeepEqual(red.Value(v), v):
		return "the arguments hold a session token, which no server is sent"
	case red.EscapedValue(v):
		return "the arguments hold an escape that may hide a session token; write them plainly"
	}
	return ""
}

// argsRefusal says why a read call's arguments are not sent: they hold a
// session token, or an escape that may hide one, which tokenRefusal tells
// first, with a reason of its own; or an escape that may hide what another
// rule of red matches, which escapedReason tells. It returns nothing for
// arguments that hold none of them; token is the caller's.
func argsRefusal(red *redact.Redactor, values map[string]any, token string) string {
	if why := tokenRefusal(values, token); why != "" {
		return why
	}
	if escapedArgs(red, values) {
		return escapedReason
	}
	return ""
}

// always returns the redaction rules that apply always, for text that no
// tool's rules cover: the session tokens' rule and those of extra, then the
// configuration's.
func always(rev *config.Revision, extra ...redact.Rule) *redact.Redactor {
	rules := append([]redact.Rule{redact.SessionTokens()}, extra...)
	for _, r := range rev.AlwaysRedactionRules() {
		rules = append(rules, redact.FromConfig(r))
	}
	return redact.New(rules...)
}

// sleep waits d, or until ctx ends.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
