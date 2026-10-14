package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
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
// redaction replaced is not a value. A read that fails or selects nothing is
// an error, so a caller refuses rather than guesses.
func (g *Gateway) PlatformRead(ctx context.Context, o Origin, home string, pr *config.PlatformRead, values map[string]string) (*PlatformResult, error) {
	rev, err := g.p.Revisions.At(ctx, o.Commit)
	if err != nil {
		return nil, fmt.Errorf("app: revision %s: %w", o.Commit, err)
	}
	ref, err := config.ResolveToolRef(pr.Tool, home)
	if err != nil {
		return nil, fmt.Errorf("app: platform read: %w", err)
	}
	policy := rev.Tool(ref)
	if policy == nil || policy.Effect != config.EffectRead {
		return nil, fmt.Errorf("app: platform read: %s is not a listed read tool", ref)
	}
	sel, err := selector.Parse(pr.Field)
	if err != nil {
		return nil, fmt.Errorf("app: platform read: %w", err)
	}
	rendered, err := render(pr.Args, values)
	if err != nil {
		return nil, fmt.Errorf("app: platform read: %w", err)
	}
	ctx, cancel := g.callContext(ctx, time.Time{}, nil)
	defer cancel()
	if err := g.look(ctx, rev, ref.Server); err != nil {
		return nil, fmt.Errorf("%w: %s: %s", ErrReadFailed, ref, describe(ref.Server, err, redactor(rev, policy)))
	}
	v, ok := g.visible(ctx, rev, ref, true)
	if !ok {
		return nil, fmt.Errorf("%w: %s is hidden", ErrReadFailed, ref)
	}
	args, err := json.Marshal(v.schema.Coerce(rendered))
	if err != nil {
		return nil, fmt.Errorf("app: platform read: %w", err)
	}
	resp := g.read(ctx, &read{rev: rev, ref: ref, class: limits.Platform, run: o.Run, args: args})
	if resp.Status != StatusOK {
		return nil, fmt.Errorf("%w: %s: %s", ErrReadFailed, ref, resp.Text)
	}
	doc, ok := output.Decode(resp.Text)
	if !ok {
		return nil, fmt.Errorf("%w: %s did not answer with one JSON document", ErrReadFailed, ref)
	}
	selected := unredacted(sel.Select(doc), redactor(rev, policy).Replacements())
	if len(selected) == 0 {
		return nil, fmt.Errorf("%w: %s selects nothing from %s", ErrNothingRead, pr.Field, ref)
	}
	return &PlatformResult{CallID: resp.CallID, Values: selected}, nil
}

// render fills each argument's template with whole values, never parsed
// again.
func render(args, values map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for _, name := range slices.Sorted(maps.Keys(args)) {
		tpl, err := tmpl.Parse(args[name])
		if err != nil {
			return nil, err
		}
		v, err := tpl.Render(func(hole string) (string, bool) {
			v, ok := values[hole]
			return v, ok
		})
		if err != nil {
			return nil, err
		}
		out[name] = v
	}
	return out, nil
}

// unredacted drops the values redaction replaced.
func unredacted(values []any, replacements []string) []any {
	var out []any
	for _, v := range values {
		if s, ok := v.(string); !ok || !slices.Contains(replacements, s) {
			out = append(out, v)
		}
	}
	return out
}
