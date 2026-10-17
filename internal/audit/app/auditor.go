// Package app holds the Audit context's use cases: anchoring the audit
// chain outside the database, and verifying the chain, its rows and its
// anchors (spec Sections 8.4 and 9.4). It declares the ports they need.
package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jchen0426/agent-swarm/internal/audit/chain"
)

// Chain reads a tenant's audit chain, and the rows it covers, from the
// database.
type Chain interface {
	// Read calls fn with the chain and its rows as one snapshot of the
	// database shows them.
	Read(ctx context.Context, fn func(View) error) error
}

// View is a tenant's chain and the rows it covers, as of one moment.
type View interface {
	// Head returns the chain's last record, or nil when the chain is empty.
	Head(ctx context.Context) (*chain.Link, error)
	// HashAt returns the hash of the record at seq, and false when the
	// chain has no record there.
	HashAt(ctx context.Context, seq int64) (chain.Sum, bool, error)
	// Runs returns the distinct runs of the records after seq after, up to
	// and with seq upTo, sorted.
	Runs(ctx context.Context, after, upTo int64) ([]string, error)
	// Counts returns the rows each audited table holds.
	Counts(ctx context.Context) (map[string]int64, error)
	// Records returns the records the chain holds of each audited table.
	Records(ctx context.Context) (map[string]int64, error)
	// Links calls fn with every record, in sequence order.
	Links(ctx context.Context, fn func(*chain.Link) error) error
	// Rows calls fn with every record's row, as the database renders it.
	Rows(ctx context.Context, fn func(*Row) error) error
	// Unchained returns the audited rows that no record names.
	Unchained(ctx context.Context) ([]Row, error)
	// Tables returns how the database holds each audited table.
	Tables(ctx context.Context) ([]Table, error)
}

// Row is an audited row as verification reads it: the record that chains it
// and, when the row still exists and this build knows the record's form,
// the row's columns as the database renders them.
type Row struct {
	Seq         int64
	Source, Key string
	Form        int16
	Digest      chain.Sum
	// Known is false for a form this build cannot render.
	Known bool
	// Rendered is nil when the row is gone.
	Rendered []byte
}

// Table is how the database holds an audited table. Verification trusts
// what it reads only from a plain table that no row security filters, and
// whose inserts its chain trigger chains.
type Table struct {
	Name                    string
	Plain, Filtered, Chains bool
	// Altered is true when another trigger, or a rule, acts on the
	// table's inserts, which could change or drop a row before it is
	// chained.
	Altered bool
}

// Anchors is the store outside the database that keeps anchors write-once
// (spec Section 9.4). Its owner, outside the builders, takes the anchors;
// the platform holds no credential for it.
type Anchors interface {
	// Put writes body under key, once: it returns ErrExists when the key
	// holds an object already.
	Put(ctx context.Context, key string, body []byte) error
	// List returns every object under the tenant's name, every version of
	// each and delete markers included, without their bodies.
	List(ctx context.Context, tenant string) ([]chain.Stored, error)
	// Read fills in the body of an object List returned.
	Read(ctx context.Context, o *chain.Stored) error
	// Retention fills in when an object's retention in compliance mode
	// ends.
	Retention(ctx context.Context, o *chain.Stored) error
	// Locked reports whether the store keeps what it holds write-once, and
	// why not when it does not.
	Locked(ctx context.Context) (bool, string, error)
}

// ErrExists is returned by Anchors.Put for a key that holds an object.
var ErrExists = errors.New("app: the anchor exists")

// Auditor anchors and verifies one tenant's audit chain.
type Auditor struct {
	chain   Chain
	anchors Anchors
	tenant  string
	now     func() time.Time
}

// New returns the auditor of a tenant's chain. now is the clock.
func New(c Chain, a Anchors, tenant string, now func() time.Time) (*Auditor, error) {
	if !chain.ValidTenant(tenant) {
		return nil, fmt.Errorf("app: %q is not a tenant id that can name anchors", tenant)
	}
	return &Auditor{chain: c, anchors: a, tenant: tenant, now: now}, nil
}

// Anchor writes one anchor, from one snapshot: the chain's head; its base,
// the latest anchor, dated before now, whose record the chain still holds
// at its seq; the runs of the records since the base; how many rows each
// audited table holds beside how many records the chain holds of it; and
// the tables whose state verification could not trust now. It reads
// anchors newest first, passes over those whose name puts them past the
// head, and stops at the base. It anchors whatever the chain holds, also
// when earlier anchors disagree with it: verification reports the
// disagreement, and a missing anchor would hide it. It refuses to anchor an
// empty chain once records were anchored, as during a restore, whose losses
// the first anchor after it must see, and it never writes an anchor that
// would not read back.
func (a *Auditor) Anchor(ctx context.Context) (chain.Anchor, error) {
	objects, err := a.anchors.List(ctx, a.tenant)
	if err != nil {
		return chain.Anchor{}, fmt.Errorf("list anchors: %w", err)
	}
	an := chain.Anchor{Tenant: a.tenant, At: a.now().UTC()}
	err = a.chain.Read(ctx, func(v View) error {
		head, err := v.Head(ctx)
		if err != nil {
			return err
		}
		if head != nil {
			an.Seq, an.Hash, an.ChainedAt = head.Seq, head.Hash, head.ChainedAt.UTC()
		}
		base, latest, err := a.findBase(ctx, v, objects, &an)
		if err != nil {
			return err
		}
		if head == nil && latest != nil && latest.Seq > 0 {
			return fmt.Errorf("the chain is empty, but anchor %s holds %d records: anchor once a restore is done", latest.Key(), latest.Seq)
		}
		if base != nil {
			an.Base, an.After = base.Key(), base.Seq
		}
		return describe(ctx, v, &an)
	})
	if err != nil {
		return chain.Anchor{}, fmt.Errorf("read the chain: %w", err)
	}
	if _, err := chain.Decode(an.Encode()); err != nil {
		return chain.Anchor{}, fmt.Errorf("the anchor would not read back: %w", err)
	}
	if err := a.anchors.Put(ctx, an.Key(), an.Encode()); err != nil {
		return chain.Anchor{}, fmt.Errorf("write anchor %s: %w", an.Key(), err)
	}
	return an, nil
}

// describe reads into an what an anchor holds beside its head and base: the
// runs since the base, as many as it can name, the rows and records of each
// table, and the tables verification could not trust now.
func describe(ctx context.Context, v View, an *chain.Anchor) (err error) {
	runs, err := v.Runs(ctx, an.After, an.Seq)
	if err != nil {
		return err
	}
	an.Runs, an.Omitted = chain.FitRuns(runs)
	if an.Counts, err = v.Counts(ctx); err != nil {
		return err
	}
	if an.Records, err = v.Records(ctx); err != nil {
		return err
	}
	tables, err := v.Tables(ctx)
	for _, f := range checkTables(tables) {
		an.Unsafe = append(an.Unsafe, f.Source)
	}
	slices.Sort(an.Unsafe)
	an.Unsafe = slices.Compact(an.Unsafe)
	return err
}

// findBase reads the anchors dated before an, newest first, and returns the
// first whose record the chain still holds at its seq, and the newest. Once
// it has the newest, it passes over, unread, every anchor whose name puts
// its seq past the head: the chain cannot hold it. An object that is no
// anchor of the tenant under its own name is passed over.
func (a *Auditor) findBase(ctx context.Context, v View, objects []chain.Stored, an *chain.Anchor) (base, latest *chain.Anchor, err error) {
	for _, o := range newestFirst(objects) {
		if seq, ok := chain.KeySeq(o.Key); !ok || latest != nil && seq > an.Seq {
			continue
		}
		if err := a.anchors.Read(ctx, &o); err != nil {
			return nil, nil, fmt.Errorf("read anchor %s: %w", o.Key, err)
		}
		got, err := chain.Decode(o.Body)
		if err != nil || got.Tenant != a.tenant || got.Key() != o.Key || !got.At.Before(an.At) {
			continue
		}
		if latest == nil {
			latest = &got
		}
		held, err := holds(ctx, v, &got, an.Seq)
		if err != nil {
			return nil, nil, err
		}
		if held {
			return &got, latest, nil
		}
	}
	return nil, latest, nil
}

// newestFirst returns one version of each key that is no delete marker,
// newest key first: an anchor's key begins with its time.
func newestFirst(objects []chain.Stored) []chain.Stored {
	seen := map[string]bool{}
	var keys []chain.Stored
	for _, o := range objects {
		if !o.Deleted && !seen[o.Key] {
			seen[o.Key] = true
			keys = append(keys, o)
		}
	}
	slices.SortFunc(keys, func(x, y chain.Stored) int { return strings.Compare(y.Key, x.Key) })
	return keys
}

// holds reports whether the chain, whose head is at seq head, still holds
// an anchor's record at its seq. An anchor of the empty chain is held by
// every chain.
func holds(ctx context.Context, v View, an *chain.Anchor, head int64) (bool, error) {
	switch {
	case an.Seq > head:
		return false, nil
	case an.Seq == 0:
		return true, nil
	}
	hash, ok, err := v.HashAt(ctx, an.Seq)
	return ok && hash == an.Hash, err
}

// Verify checks the chain, the rows it covers and its anchors, and returns
// what it found; every is the anchor interval. It lists and reads the
// anchors before it reads the database, so that an anchor taken meanwhile
// never names a record the snapshot lacks, and it reads the whole chain.
func (a *Auditor) Verify(ctx context.Context, every time.Duration) ([]chain.Finding, error) {
	now := a.now()
	objects, err := a.read(ctx)
	if err != nil {
		return nil, err
	}
	anchors, findings := chain.CheckAnchors(a.tenant, objects)
	locked, why, err := a.anchors.Locked(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the anchor store's lock: %w", err)
	}
	if locked {
		for i := range objects {
			if objects[i].Deleted {
				continue
			}
			if err := a.anchors.Retention(ctx, &objects[i]); err != nil {
				return nil, fmt.Errorf("read the retention of anchor %s: %w", objects[i].Key, err)
			}
		}
	}
	findings = append(findings, retention(objects, locked, why, now)...)
	w := chain.NewWalk(anchors)
	err = a.chain.Read(ctx, func(v View) error {
		tables, err := v.Tables(ctx)
		if err != nil {
			return err
		}
		findings = append(findings, checkTables(tables)...)
		if err := v.Links(ctx, func(l *chain.Link) error { w.Next(l); return nil }); err != nil {
			return err
		}
		err = v.Rows(ctx, func(r *Row) error {
			if f, ok := checkRow(r); ok {
				findings = append(findings, f)
			}
			return nil
		})
		if err != nil {
			return err
		}
		unchained, err := v.Unchained(ctx)
		for i := range unchained {
			findings = append(findings, chain.Finding{Kind: chain.UnchainedRow, Source: unchained[i].Source, Key: unchained[i].Key,
				Detail: "no record chains the row"})
		}
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("read the chain: %w", err)
	}
	return append(findings, w.End(now, every)...), nil
}

// read lists every object of the tenant and reads each that is no delete
// marker.
func (a *Auditor) read(ctx context.Context) ([]chain.Stored, error) {
	objects, err := a.anchors.List(ctx, a.tenant)
	if err != nil {
		return nil, fmt.Errorf("list anchors: %w", err)
	}
	for i := range objects {
		if objects[i].Deleted {
			continue
		}
		if err := a.anchors.Read(ctx, &objects[i]); err != nil {
			return nil, fmt.Errorf("read anchor %s: %w", objects[i].Key, err)
		}
	}
	return objects, nil
}

// retention reports a store that is not write-once, or, in one that is,
// each object whose retention in compliance mode has ended or never began.
func retention(objects []chain.Stored, locked bool, why string, now time.Time) []chain.Finding {
	if !locked {
		return []chain.Finding{{Kind: chain.StoreUnlocked, Detail: why}}
	}
	var out []chain.Finding
	for i := range objects {
		if o := &objects[i]; !o.Deleted && !o.LockedUntil.After(now) {
			out = append(out, chain.Finding{Kind: chain.StoreUnlocked, Anchor: o.Key, Detail: "the object is not under retention in compliance mode"})
		}
	}
	return out
}

// checkTables reports each audited table whose rows verification cannot
// trust, or whose inserts are not chained.
func checkTables(tables []Table) []chain.Finding {
	var out []chain.Finding
	for _, t := range tables {
		switch {
		case !t.Plain:
			out = append(out, chain.Finding{Kind: chain.Table, Source: t.Name, Detail: "not a plain table"})
		case t.Filtered:
			out = append(out, chain.Finding{Kind: chain.Table, Source: t.Name,
				Detail: "row security can hide rows from verification, or a table of the same name in another schema can take the platform's writes"})
		case !t.Chains:
			out = append(out, chain.Finding{Kind: chain.Table, Source: t.Name, Detail: "its chain trigger is missing or disabled"})
		case t.Altered:
			out = append(out, chain.Finding{Kind: chain.Table, Source: t.Name, Detail: "another trigger, or a rule, acts on its inserts before they are chained"})
		}
	}
	return out
}

// checkRow compares a row with its record.
func checkRow(r *Row) (chain.Finding, bool) {
	f := chain.Finding{Seq: r.Seq, Source: r.Source, Key: r.Key}
	switch {
	case !r.Known:
		f.Kind, f.Detail = chain.UnknownForm, fmt.Sprintf("this build cannot render form %d", r.Form)
	case r.Rendered == nil:
		f.Kind, f.Detail = chain.MissingRow, "the row is gone"
	case chain.Digest(string(r.Rendered)) != r.Digest:
		f.Kind, f.Detail = chain.ChangedRow, "the row differs from what was chained"
	default:
		return chain.Finding{}, false
	}
	return f, true
}
