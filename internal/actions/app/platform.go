package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jchen0426/agent-swarm/internal/actions/limits"
	"github.com/jchen0426/agent-swarm/internal/actions/output"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/registry/selector"
	"github.com/jchen0426/agent-swarm/internal/registry/tmpl"
	"github.com/jchen0426/agent-swarm/internal/work/board"
)

// Errors of a platform read.
var (
	// ErrReadFailed is returned when a platform read was refused or ended in
	// a read error.
	ErrReadFailed = errors.New("app: the platform read failed")
	// ErrNothingRead is returned when a platform read's field selects
	// nothing but redacted values.
	ErrNothingRead = errors.New("app: the platform read selected nothing")
)

// Origin says what a platform read is for.
type Origin struct {
	// Run is the run the read serves, or empty for a read when an item opens.
	Run board.RunID
	// Commit is the locked revision.
	Commit string
}

// Values fill the holes of a platform read's argument templates, each with
// one whole value that is never parsed again. Only an inbound event's
// trusted fields (Trusted) and the arguments the gateway validated against
// their candidates fill them, never an agent's own text (spec Section 3.3).
type Values struct {
	m map[string]string
}

// Trusted returns an inbound event's trusted fields as values (spec Section
// 3.6). Names under args. are the gateway's own, for validated arguments,
// and are refused.
func Trusted(fields map[string]string) (Values, error) {
	for name := range fields {
		if strings.HasPrefix(name, "args.") {
			return Values{}, fmt.Errorf("app: %s is not a trusted field; an argument fills a read only once it is validated", name)
		}
	}
	return Values{m: maps.Clone(fields)}, nil
}

// PlatformResult is what a platform read selected.
type PlatformResult struct {
	CallID string
	Values []any
}

// PlatformRead reads for the platform, under its own identity (spec Section
// 3.3, platform reads): candidates, facts, routing and identity. Each
// argument is a template whose holes take whole values from values, never
// parsed again. The read runs the read path with the platform's reserved
// capacity, and its field is selected from the redacted output; a value that
// redaction changed is not a value. A read that fails or selects nothing is
// an error, so a caller refuses rather than guesses.
func (g *Gateway) PlatformRead(ctx context.Context, o Origin, home string, pr *config.PlatformRead, values Values) (*PlatformResult, error) {
	rev, err := g.p.Revisions.At(ctx, o.Commit)
	if err != nil {
		return nil, fmt.Errorf("app: revision %s: %w", o.Commit, err)
	}
	ctx, cancel := g.callContext(ctx, time.Time{}, nil)
	defer cancel()
	return g.platformRead(ctx, &platformCall{rev: rev, run: o.Run, home: home, pr: pr, values: values, class: limits.Platform})
}

// platformCall is a platform read: for the platform itself, or for an
// agent's write call, which pays for it with its session's class and calls
// in flight.
type platformCall struct {
	rev    *config.Revision
	run    board.RunID
	home   string
	pr     *config.PlatformRead
	values Values
	class  limits.Class
	// token names the session the read is charged to, or is empty. Its
	// caller already counts among the session's calls in flight, so the read
	// counts only among the session's calls to the server.
	token string
	// cause names the write call the read is made for, or is empty.
	cause string
}

func (g *Gateway) platformRead(ctx context.Context, c *platformCall) (*PlatformResult, error) {
	ref, err := config.ResolveToolRef(c.pr.Tool, c.home)
	if err != nil {
		return nil, fmt.Errorf("app: platform read: %w", err)
	}
	policy := c.rev.Tool(ref)
	if policy == nil || policy.Effect != config.EffectRead {
		return nil, fmt.Errorf("app: platform read: %s is not a listed read tool", ref)
	}
	sel, err := selector.Parse(c.pr.Field)
	if err != nil {
		return nil, fmt.Errorf("app: platform read: %w", err)
	}
	rendered, err := render(c.pr.Args, c.values)
	if err != nil {
		return nil, fmt.Errorf("app: platform read: %w", err)
	}
	if c.token != "" {
		release, err := g.flights.takeServer(ctx, c.token, ref.Server, g.opts.ServerCalls)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: no call could start before the round ended", ErrReadFailed, ref)
		}
		defer release()
	}
	if err := g.look(ctx, c.rev, ref.Server); err != nil {
		return nil, fmt.Errorf("%w: %s: %s", ErrReadFailed, ref, describe(ref.Server, err, redactor(c.rev, policy)))
	}
	v, ok := g.visible(ctx, c.rev, ref, true)
	if !ok {
		return nil, fmt.Errorf("%w: %s is hidden", ErrReadFailed, ref)
	}
	args, err := json.Marshal(v.schema.Coerce(rendered))
	if err != nil {
		return nil, fmt.Errorf("app: platform read: %w", err)
	}
	resp := g.read(ctx, &read{rev: c.rev, ref: ref, class: c.class, run: c.run, args: args, cause: c.cause})
	if resp.Status != StatusOK {
		return nil, fmt.Errorf("%w: %s: %s", ErrReadFailed, ref, resp.Text)
	}
	doc, ok := output.Decode(resp.Text)
	if !ok {
		return nil, fmt.Errorf("%w: %s did not answer with one JSON document", ErrReadFailed, ref)
	}
	selected := unredacted(sel.Select(doc), redactor(c.rev, policy).Replacements())
	if len(selected) == 0 {
		return nil, fmt.Errorf("%w: %s selects nothing from %s", ErrNothingRead, c.pr.Field, ref)
	}
	return &PlatformResult{CallID: resp.CallID, Values: selected}, nil
}

// render fills each argument's template with whole values, never parsed
// again.
func render(args map[string]string, values Values) (map[string]string, error) {
	out := map[string]string{}
	for _, name := range slices.Sorted(maps.Keys(args)) {
		tpl, err := tmpl.Parse(args[name])
		if err != nil {
			return nil, err
		}
		v, err := tpl.Render(func(hole string) (string, bool) {
			v, ok := values.m[hole]
			return v, ok
		})
		if err != nil {
			return nil, err
		}
		out[name] = v
	}
	return out, nil
}

// unredacted drops every value redaction changed: one that holds a
// replacement anywhere, in a string, a key or a value inside it, is not the
// value the server holds.
func unredacted(values []any, replacements []string) []any {
	var out []any
	for _, v := range values {
		if !holds(v, replacements) {
			out = append(out, v)
		}
	}
	return out
}

// holds reports whether any string, key or number in v contains one of subs.
func holds(v any, subs []string) bool {
	contains := func(s string) bool {
		return slices.ContainsFunc(subs, func(sub string) bool { return strings.Contains(s, sub) })
	}
	switch t := v.(type) {
	case string:
		return contains(t)
	case json.Number:
		return contains(t.String())
	case map[string]any:
		for k, e := range t {
			if contains(k) || holds(e, subs) {
				return true
			}
		}
	case []any:
		return slices.ContainsFunc(t, func(e any) bool { return holds(e, subs) })
	}
	return false
}
