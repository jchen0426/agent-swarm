package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"

	"github.com/jchen0426/agent-swarm/internal/registry/config"
)

// ErrNoCommit is returned when a running registry is given files that are not
// a commit: a work item can lock only a commit.
var ErrNoCommit = errors.New("registry: the configuration source returned no commit")

// RefusedError is the registry's refusal of the commit it read: a revision
// that breaks one of its rules, or that names a runtime without an image. It
// says which commit, beside the reason, so that the refusal is logged with the
// commit a person pushed, once for as long as the ref stays at it. A revision
// that cannot be recorded, and a source that cannot be read, are no refusal:
// they are tried again.
type RefusedError struct {
	Commit string
	Err    error
}

func (e *RefusedError) Error() string { return "revision " + e.Commit + ": " + e.Err.Error() }

func (e *RefusedError) Unwrap() error { return e.Err }

// maxCached bounds the revisions kept in memory. A revision that falls out is
// read again when a work item asks for it.
const maxCached = 32

// Registry serves configuration to the platform: the revision at its ref,
// which new work items lock, and any earlier revision that a work item locked.
type Registry struct {
	source     Source
	conditions config.ConditionChecker
	records    Revisions
	ref        string
	// runtimes maps each runtime name to its image digest, as the deployment
	// pins them.
	runtimes map[string]string

	// refreshing runs refreshes one at a time.
	refreshing sync.Mutex

	mu      sync.Mutex
	current *config.Revision
	cached  map[string]*config.Revision
	order   []string
}

// New returns a registry that reads ref, such as refs/heads/main, from
// source. runtimes maps each runtime name to the image digest the deployment
// pins for it; a revision that names a runtime without one is refused.
func New(source Source, conditions config.ConditionChecker, records Revisions, ref string, runtimes map[string]string) *Registry {
	return &Registry{
		source: source, conditions: conditions, records: records, ref: ref,
		runtimes: maps.Clone(runtimes), cached: map[string]*config.Revision{},
	}
}

// Refresh reads the revision at the registry's ref and makes it current. A
// revision that breaks a rule, that names a runtime without an image, or
// that cannot be recorded, is refused, and the registry keeps serving the
// revision it had: the platform loads only revisions that keep every rule
// and that every work item can lock. The error then says why, as a
// RefusedError, which names the commit, for a revision the rules refuse.
//
// Refreshes run one at a time, in the order they read the ref. A slow
// refresh that read an older commit therefore never replaces the revision a
// later one loaded, which could undo a yank.
func (r *Registry) Refresh(ctx context.Context) (*config.Revision, error) {
	r.refreshing.Lock()
	defer r.refreshing.Unlock()
	snap, err := r.source.Read(ctx, r.ref)
	if err != nil {
		return r.Current(), fmt.Errorf("read %s: %w", r.ref, err)
	}
	if cur := r.Current(); cur != nil && cur.Commit() == snap.Commit {
		return cur, nil
	}
	rev, err := r.load(ctx, snap)
	if err != nil {
		return r.Current(), err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.current = rev
	return rev, nil
}

// Current returns the revision that new work items lock, or nil before the
// first refresh that succeeded.
func (r *Registry) Current() *config.Revision {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.current
}

// At returns the revision of a full commit id, such as one a work item
// locked. It reads the commit again when it is not in memory.
func (r *Registry) At(ctx context.Context, commit string) (*config.Revision, error) {
	r.mu.Lock()
	rev, ok := r.cached[commit]
	r.mu.Unlock()
	if ok {
		return rev, nil
	}
	snap, err := r.source.Read(ctx, commit)
	if err != nil {
		return nil, fmt.Errorf("read commit %s: %w", commit, err)
	}
	if snap.Commit != commit {
		return nil, fmt.Errorf("registry: %q resolved to commit %q; ask for a full commit id", commit, snap.Commit)
	}
	return r.load(ctx, snap)
}

// load builds, records and caches one snapshot's revision.
func (r *Registry) load(ctx context.Context, snap Snapshot) (*config.Revision, error) {
	if snap.Commit == "" {
		return nil, ErrNoCommit
	}
	rev, err := config.Build(snap.Files, snap.Commit, r.conditions)
	if err != nil {
		return nil, &RefusedError{Commit: snap.Commit, Err: err}
	}
	// A revision that no work item could lock would stop every item, so it
	// is refused like one that breaks a rule.
	if _, err := rev.Lock(r.runtimes); err != nil {
		return nil, &RefusedError{Commit: snap.Commit, Err: err}
	}
	if err := r.records.Record(ctx, rev); err != nil {
		return nil, fmt.Errorf("record revision %s: %w", snap.Commit, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.cached[rev.Commit()]; !ok {
		r.cached[rev.Commit()] = rev
		r.order = append(r.order, rev.Commit())
	}
	for len(r.order) > maxCached {
		oldest := r.order[0]
		r.order = r.order[1:]
		if r.current == nil || oldest != r.current.Commit() {
			delete(r.cached, oldest)
		} else {
			r.order = append(r.order, oldest)
		}
	}
	return rev, nil
}
