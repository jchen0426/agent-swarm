// Package current is the registry at runtime (Plan 7d-1, Decision 11). It
// refreshes the registry every period, makes a revision current only once
// it passes the checks Decision 1 makes and the registry cannot, and serves
// the revisions items locked, with their agents' skills, to the coordinator,
// both gateways and the runner, off the coordinator's lock.
package current

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	registryapp "github.com/jchen0426/agent-swarm/internal/registry/app"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
)

// defaultRefresh is how often the registry is refreshed when Options says
// not [1 min].
const defaultRefresh = time.Minute

// skillEntry is the file of a skill version that a runtime reads first: the
// skill's text, which a turn's system prompt holds (Contract C6).
const skillEntry = "SKILL.md"

// Options are the registry at runtime's settings. Priced and CheckSkill are
// required.
type Options struct {
	// Refresh is how often the registry is refreshed [1 min].
	Refresh time.Duration
	// Images are the deployment's runtime images, which a revision is locked
	// with.
	Images map[string]string
	// Priced reports whether the deployment has a price card for a model.
	Priced func(model string) bool
	// CheckSkill applies to a skill's text the LLM gateway's checks of a
	// system prompt under a revision: llmapp.CheckSkill.
	CheckSkill func(rev *config.Revision, text string) error
	// Log receives "revision current" and "revision refused" (Contract C3),
	// and each refresh of the registry that failed; nil logs nothing.
	Log *slog.Logger
}

// Revisions is the registry at runtime: the current revision, and those
// items locked. It serves the coordinator's Revisions port, both gateways',
// and the runner's skills.
type Revisions struct {
	reg *registryapp.Registry
	o   Options
	log *slog.Logger

	current atomic.Pointer[config.Revision]
	// refused is the commit the last refresh refused: a refresh that finds
	// the registry's ref at it again neither checks nor logs it again. Only
	// Run's refreshes read and write it.
	refused string
}

// New returns the registry at runtime over reg. No revision is current
// until Run's first refresh makes one so.
func New(reg *registryapp.Registry, o Options) *Revisions {
	if o.Refresh <= 0 {
		o.Refresh = defaultRefresh
	}
	log := o.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Revisions{reg: reg, o: o, log: log}
}

// Run refreshes the registry at once, then every Refresh, until ctx ends. A
// refresh that fails, or whose revision is refused, leaves the last current
// revision current, or none, and the next refresh tries again (Decision
// 11). Run is called once.
func (r *Revisions) Run(ctx context.Context) {
	tick := time.NewTicker(r.o.Refresh)
	defer tick.Stop()
	for {
		r.refresh(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// refresh refreshes the registry, and makes the revision at its ref current
// once it passes the checks. A revision it refuses is logged with its commit
// and the reason, once for as long as the refreshes find the ref at it.
func (r *Revisions) refresh(ctx context.Context) {
	last := r.refused
	r.refused = ""
	rev, err := r.reg.Refresh(ctx)
	switch {
	case ctx.Err() != nil:
		return
	case err != nil:
		r.log.WarnContext(ctx, "the registry could not be refreshed; the next refresh tries again", "err", err)
		return
	}
	commit := rev.Commit()
	if cur := r.current.Load(); cur != nil && cur.Commit() == commit {
		return
	}
	if commit == last {
		r.refused = last
		return
	}
	if err := r.check(rev); err != nil {
		r.refused = commit
		r.log.WarnContext(ctx, "revision refused", "commit", commit, "reason", err.Error())
		return
	}
	r.current.Store(rev)
	r.log.InfoContext(ctx, "revision current", "commit", commit)
}

// check makes the checks Decision 1 makes before a revision is current,
// which the registry cannot: every agent's model has a price card in the
// deployment, and every skill an agent pins passes the LLM gateway's checks
// as a system prompt, under the revision's own rules. It reports every
// failure, each with the agent's name.
func (r *Revisions) check(rev *config.Revision) error {
	var problems []string
	for _, a := range rev.Agents() {
		name := a.Metadata.Name
		if !r.o.Priced(a.Spec.Model) {
			problems = append(problems, fmt.Sprintf("agent %s's model %s has no price card in the deployment", name, a.Spec.Model))
		}
		for _, ref := range a.Skills() {
			text, err := skillText(rev, ref)
			if err == nil {
				err = r.o.CheckSkill(rev, text)
			}
			if err != nil {
				problems = append(problems, fmt.Sprintf("agent %s's skill %s: %v", name, ref, err))
			}
		}
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

// Current returns the revision new items start from, or nil while none has
// passed the checks.
func (r *Revisions) Current() *config.Revision { return r.current.Load() }

// At returns the revision of a full commit id, as an item locked it, read
// again when the registry no longer holds it. A commit the local repository
// lacks fails, and the step that needed it is tried again. At returns once
// ctx ends, whether the read did or not: the runner waits for an agent's
// skills with no bound of its own, and a fenced coordinator waits for
// nothing.
func (r *Revisions) At(ctx context.Context, commit string) (*config.Revision, error) {
	type read struct {
		rev *config.Revision
		err error
	}
	done := make(chan read, 1)
	go func() {
		rev, err := r.reg.At(ctx, commit)
		done <- read{rev: rev, err: err}
	}()
	select {
	case got := <-done:
		return got.rev, got.err
	case <-ctx.Done():
		return nil, fmt.Errorf("read commit %s: %w", commit, ctx.Err())
	}
}

// Lock locks a revision with the deployment's runtime images.
func (r *Revisions) Lock(rev *config.Revision) (config.Lock, error) {
	return rev.Lock(r.o.Images)
}

// Skills returns the texts of the skill versions an agent pins at a commit,
// in the order it pins them: each version's SKILL.md, which the turn's
// system prompt holds, each as its own block (Contract C6). It reads off the
// coordinator's lock, and returns once ctx ends, as At does.
func (r *Revisions) Skills(ctx context.Context, commit, agent string) ([]string, error) {
	rev, err := r.At(ctx, commit)
	if err != nil {
		return nil, err
	}
	a := rev.Agent(agent)
	if a == nil {
		return nil, fmt.Errorf("current: commit %s has no agent %q", commit, agent)
	}
	texts := make([]string, 0, len(a.Skills()))
	for _, ref := range a.Skills() {
		text, err := skillText(rev, ref)
		if err != nil {
			return nil, fmt.Errorf("current: commit %s: %w", commit, err)
		}
		texts = append(texts, text)
	}
	return texts, nil
}

// skillText returns a skill version's text: its SKILL.md.
func skillText(rev *config.Revision, ref config.SkillRef) (string, error) {
	bundle, err := rev.SkillBundle(ref)
	if err != nil {
		return "", fmt.Errorf("skill %s: %w", ref, err)
	}
	text, err := fs.ReadFile(bundle, skillEntry)
	if err != nil {
		return "", fmt.Errorf("skill %s: %w", ref, err)
	}
	return string(text), nil
}
