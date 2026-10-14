package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jchen0426/agent-swarm/internal/actions/limits"
	"github.com/jchen0426/agent-swarm/internal/actions/output"
	"github.com/jchen0426/agent-swarm/internal/actions/redact"
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
}

// errStopped ends the calls of a session whose run stopped.
var errStopped = errors.New("app: the run stopped")

// recordAttempts is how often the gateway tries to record a call's outcome.
const recordAttempts = 3

// Call makes one agent's read call (spec Section 6.2). A call the gateway
// refuses, or that ends in a read error, is still a Response; the error is
// for a session that does not exist, or a revision that cannot be read.
func (g *Gateway) Call(ctx context.Context, token, name string, args json.RawMessage) (*Response, error) {
	s, err := g.p.Sessions.Session(ctx, token)
	if err != nil {
		return nil, err
	}
	list, err := g.list(ctx, &s)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(list, func(t Tool) bool { return t.Name == name })
	if i < 0 {
		rec := &CallRecord{ID: g.p.IDs.NewID(), Run: s.Run, Agent: s.Agent, Tool: truncate(name, 128), Args: json.RawMessage("null"), Round: s.Round, At: g.p.Now()}
		return g.end(ctx, rec, StatusRefused, "there is no tool by that name in your list"), nil
	}
	rev, err := g.p.Revisions.At(ctx, s.Commit)
	if err != nil {
		return nil, fmt.Errorf("app: revision %s: %w", s.Commit, err)
	}
	ctx, cancel := g.callContext(ctx, s.Deadline, s.Done)
	defer cancel()
	ref := list[i].ref
	release, err := g.flights.take(ctx, token, ref.Server, g.opts.SessionCalls, g.opts.ServerCalls)
	if err != nil {
		rec := &CallRecord{ID: g.p.IDs.NewID(), Run: s.Run, Agent: s.Agent, Server: ref.Server, Tool: ref.Tool, Args: json.RawMessage("null"), Round: s.Round, At: g.p.Now()}
		return g.end(ctx, rec, StatusReadError, "your other calls did not finish before the round ended"), nil
	}
	defer release()
	return g.read(ctx, &read{rev: rev, ref: ref, class: s.Class, run: s.Run, agent: s.Agent, round: s.Round, args: args}), nil
}

// callContext ends at the earliest of the round's end, the call timeout and
// the session's end, which it gives as the cause.
func (g *Gateway) callContext(ctx context.Context, roundEnd time.Time, done <-chan struct{}) (context.Context, context.CancelFunc) {
	deadline := g.p.Now().Add(g.opts.CallTimeout)
	if !roundEnd.IsZero() && roundEnd.Before(deadline) {
		deadline = roundEnd
	}
	ctx, stop := context.WithCancelCause(ctx)
	ctx, cancel := context.WithDeadline(ctx, deadline)
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

// read is one read call, by an agent or by the platform.
type read struct {
	rev   *config.Revision
	ref   config.ToolRef
	class limits.Class
	run   board.RunID
	agent string
	round int
	args  json.RawMessage
}

// read runs the read path in the order of spec Section 6.2: policy, server
// limits, the audit record, forwarding, partialWhen and redaction, the
// stored outcome, then the answer.
func (g *Gateway) read(ctx context.Context, r *read) *Response {
	srv := r.rev.Server(r.ref.Server)
	policy := srv.Spec.Tools[r.ref.Tool]
	red := redactor(r.rev, policy)
	rec := &CallRecord{
		ID: g.p.IDs.NewID(), Run: r.run, Agent: r.agent, Server: r.ref.Server, Tool: r.ref.Tool,
		SchemaHash: policy.SchemaHash, Args: json.RawMessage("null"), Round: r.round, At: g.p.Now(),
	}
	lim := g.p.Limits.Server(r.ref.Server)
	if err := g.look(ctx, r.rev, r.ref.Server); err != nil {
		if errors.Is(err, ErrConnection) {
			lim.Failed()
		}
		return g.end(ctx, rec, StatusReadError, describe(r.ref.Server, err, red))
	}
	v, ok := g.visible(ctx, r.rev, r.ref, true)
	if !ok {
		return g.end(ctx, rec, StatusRefused, "this tool is hidden: the server does not offer the definition that was reviewed")
	}
	args, err := v.schema.Read(r.args, g.opts.MaxArgsBytes)
	if err == nil {
		rec.Args = redactArgs(red, args.Values())
		err = v.schema.CheckBounds(args, policy.Bounds)
	}
	if err != nil {
		return g.end(ctx, rec, StatusRefused, red.Text(err.Error()))
	}
	release, err := lim.Acquire(ctx, r.class)
	if err != nil {
		msg := r.ref.Server + " is degraded after repeated connection failures; try again later"
		if !errors.Is(err, limits.ErrDegraded) {
			msg = "no capacity on " + r.ref.Server + " before the round ended"
		}
		return g.end(ctx, rec, StatusReadError, msg)
	}
	defer release()
	if err := g.p.Audit.Begin(ctx, rec); err != nil {
		return &Response{CallID: rec.ID, Source: source(r.ref), Status: StatusRefused, Text: "the call could not be recorded, so it was not made"}
	}
	conn := &Conn{Server: Server{Name: r.ref.Server, Endpoint: srv.Spec.Endpoint, CredentialRef: srv.Spec.CredentialRef}}
	if r.agent != "" {
		conn.Run, conn.Agent = r.run, r.agent
	}
	res, err := g.forward(ctx, conn, r.ref.Tool, args.Raw, lim)
	status, text, notes := g.outcome(ctx, r.ref, policy, &v, red, res, err)
	if !g.record(ctx, &Outcome{ID: rec.ID, Status: status, Output: text, At: g.p.Now()}) {
		return &Response{CallID: rec.ID, Source: source(r.ref), Status: StatusReadError, Text: "the output could not be stored, so it cannot be used"}
	}
	return &Response{CallID: rec.ID, Source: source(r.ref), Status: status, Text: text, Notes: notes}
}

// forward sends the call. A connection failure is retried while the server
// is not degraded, and counts against the server once, after the retries
// (spec Section 6.2, steps 2 and 4).
func (g *Gateway) forward(ctx context.Context, conn *Conn, tool string, args json.RawMessage, lim *limits.Server) (*output.Result, error) {
	for attempt := 0; ; attempt++ {
		res, err := g.p.Upstream.Call(ctx, conn, tool, args)
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

// outcome turns what the server answered into a status and the text the
// gateway stores and shows.
func (g *Gateway) outcome(ctx context.Context, ref config.ToolRef, policy *config.ToolPolicy, v *visibleTool, red *redact.Redactor, res *output.Result, err error) (status Status, text string, notes []string) {
	if err != nil {
		return StatusReadError, describe(ref.Server, cause(ctx, err), red), nil
	}
	if res.IsError {
		c, err := output.Read(res, false)
		if err != nil {
			return StatusReadError, ref.Server + " answered with an error that could not be read", nil
		}
		shown, err := c.Show(red)
		if err != nil {
			return StatusReadError, ref.Server + " answered with an error that could not be redacted", nil
		}
		return StatusReadError, shown, c.Notes
	}
	c, err := output.Read(res, len(v.def.OutputSchema) > 0)
	if err != nil {
		return StatusReadError, red.Text(err.Error()), nil
	}
	if policy.PartialWhen != "" {
		plain, ok := c.Plain()
		if !ok {
			return StatusReadError, "the output is not JSON, so its completeness cannot be checked", nil
		}
		partial, err := g.p.Conditions.PartialWhen(ctx, policy.PartialWhen, plain)
		switch {
		case err != nil:
			return StatusReadError, "the output could not be checked for completeness, so it cannot be used", nil
		case partial:
			return StatusReadError, "the output is partial, so it cannot be used: narrow the query", nil
		}
	}
	shown, err := c.Show(red)
	if err != nil {
		return StatusReadError, red.Text(err.Error()), nil
	}
	if len(shown) > g.opts.MaxOutputBytes {
		return StatusReadError, fmt.Sprintf("the output is larger than %d bytes, so it cannot be used: narrow the query", g.opts.MaxOutputBytes), nil
	}
	return StatusOK, shown, c.Notes
}

// cause returns why ctx ended in place of a context error, so that a stopped
// run is told apart from a slow server.
func cause(ctx context.Context, err error) error {
	if c := context.Cause(ctx); errors.Is(c, errStopped) {
		return errStopped
	}
	return err
}

// describe says what went wrong, in the gateway's own words: never an error
// string of the client library, which may hold URLs or payloads.
func describe(server string, err error, red *redact.Redactor) string {
	var se *ServerError
	switch {
	case errors.As(err, &se):
		return server + " answered with an error: " + red.Text(se.Message)
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

// end records a call that ended before it was forwarded, and answers it. The
// call stands as answered even if it cannot be recorded.
func (g *Gateway) end(ctx context.Context, rec *CallRecord, status Status, why string) *Response {
	if err := g.p.Audit.Begin(context.WithoutCancel(ctx), rec); err == nil {
		g.record(ctx, &Outcome{ID: rec.ID, Status: status, Output: why, At: g.p.Now()})
	}
	src := ""
	if rec.Server != "" {
		src = rec.Server + "/" + rec.Tool
	}
	return &Response{CallID: rec.ID, Source: src, Status: status, Text: why}
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

// redactor returns the redaction rules for a tool: the ones its policy
// names, then the ones that apply always.
func redactor(rev *config.Revision, policy *config.ToolPolicy) *redact.Redactor {
	var rules []redact.Rule
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

// redactArgs returns the arguments as the audit store keeps them, redacted.
func redactArgs(red *redact.Redactor, values map[string]any) json.RawMessage {
	raw, err := json.Marshal(red.Value(values))
	if err != nil {
		return json.RawMessage("null")
	}
	return raw
}

func source(ref config.ToolRef) string { return ref.Server + "/" + ref.Tool }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
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
