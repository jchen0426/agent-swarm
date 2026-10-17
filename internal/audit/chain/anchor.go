package chain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"time"
)

// MaxAnchorBytes caps an anchor object. An hour's anchor names the runs that
// added records in that hour, which stays far below it.
const MaxAnchorBytes = 4 << 20

// maxRunsBytes caps the runs an anchor names, at half its size limit, so
// that the rest of the anchor always fits beside them.
const maxRunsBytes = MaxAnchorBytes / 2

// tenantName is what a tenant id in an anchor's key may look like.
var tenantName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// sourceName is what an audited table's name in an anchor's counts may look
// like.
var sourceName = regexp.MustCompile(`^[a-z][a-z_]{0,62}$`)

// ValidTenant reports whether a tenant id can name anchors.
func ValidTenant(tenant string) bool { return tenantName.MatchString(tenant) }

// Anchor is what the anchor store's owner writes outside the database every
// hour, all read from one snapshot: the head of a tenant's chain, the
// latest earlier anchor the chain still held, the runs whose records the
// chain gained since, how many rows each audited table held beside how many
// records the chain held of it, and the tables whose state verification
// could not trust then (spec Sections 8.4 and 9.4). An empty chain is
// anchored at Seq 0 with a zero Hash.
type Anchor struct {
	Tenant string `json:"tenant"`
	// Seq and Hash are the chain's last record when the anchor was taken,
	// and ChainedAt its chaining time, zero for an empty chain.
	Seq       int64     `json:"seq"`
	Hash      Sum       `json:"hash"`
	ChainedAt time.Time `json:"chainedAt"`
	// At is when the anchor was taken.
	At time.Time `json:"at"`
	// Base names the latest anchor the chain still held when this one was
	// taken, and After is its Seq; both are empty for the first anchor.
	// After a restore, the anchors between Base and this one are those whose
	// records the restore lost. Runs are the runs of the records after
	// After up to Seq, sorted.
	After int64    `json:"after"`
	Base  string   `json:"base"`
	Runs  []string `json:"runs"`
	// Omitted counts the runs after After up to Seq that Runs leaves out,
	// too long or too many to name within the anchor's size limit, as
	// records appended by hand can make them (FitRuns); it is 0 otherwise.
	Omitted int64 `json:"omitted"`
	// Counts are the rows each audited table held for the tenant, and
	// Records the records the chain held of each, so that rows no record
	// chained show later, even once they are gone.
	Counts  map[string]int64 `json:"counts"`
	Records map[string]int64 `json:"records"`
	// Unsafe names the tables, the chain's own among them, whose state
	// verification could not trust when the anchor was taken, as a trigger
	// that rewrites rows before they are chained; sorted. It keeps what a
	// verification run later might no longer see.
	Unsafe []string `json:"unsafe"`
}

// Key returns the anchor's object name: the tenant, the anchor's time to the
// second and its Seq, so that names sort by time.
func (a *Anchor) Key() string {
	return fmt.Sprintf("%s/%s-%020d.json", a.Tenant, a.At.UTC().Format("20060102T150405Z"), a.Seq)
}

// anchorKey is what an anchor's object name looks like; group 1 is its
// tenant and group 2 its Seq.
var anchorKey = regexp.MustCompile(`^([a-z0-9][a-z0-9-]{0,62})/[0-9]{8}T[0-9]{6}Z-([0-9]{20})\.json$`)

// KeySeq returns the Seq an anchor's object name gives, and false for a name
// that is no anchor's, so that a reader can pass over an anchor without
// reading it.
func KeySeq(key string) (int64, bool) {
	m := anchorKey.FindStringSubmatch(key)
	if len(m) != 3 {
		return 0, false
	}
	seq, err := strconv.ParseInt(m[2], 10, 64)
	return seq, err == nil
}

// Encode writes the anchor in its one canonical form: compact JSON, times in
// UTC, runs as an array.
func (a *Anchor) Encode() []byte {
	c := *a
	c.ChainedAt, c.At = c.ChainedAt.UTC(), c.At.UTC()
	if c.Runs == nil {
		c.Runs = []string{}
	}
	if c.Counts == nil {
		c.Counts = map[string]int64{}
	}
	if c.Records == nil {
		c.Records = map[string]int64{}
	}
	if c.Unsafe == nil {
		c.Unsafe = []string{}
	}
	b, _ := json.Marshal(&c)
	return b
}

// Decode reads an anchor. It refuses anything but the canonical form of a
// valid anchor, so that one anchor has one text.
func Decode(b []byte) (Anchor, error) {
	if len(b) > MaxAnchorBytes {
		return Anchor{}, fmt.Errorf("chain: an anchor is at most %d bytes", MaxAnchorBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var a Anchor
	if err := dec.Decode(&a); err != nil {
		return Anchor{}, fmt.Errorf("chain: anchor: %w", err)
	}
	if err := a.check(); err != nil {
		return Anchor{}, err
	}
	if !bytes.Equal(a.Encode(), b) {
		return Anchor{}, errors.New("chain: the anchor is not in its canonical form")
	}
	return a, nil
}

// check reports what makes an anchor invalid.
func (a *Anchor) check() error {
	switch {
	case !ValidTenant(a.Tenant):
		return fmt.Errorf("chain: %q is not a tenant id", a.Tenant)
	case a.Seq < 0 || a.After < 0 || a.After > a.Seq:
		return fmt.Errorf("chain: an anchor's seq %d must follow the seq %d of its base", a.Seq, a.After)
	case a.At.IsZero():
		return errors.New("chain: an anchor names when it was taken")
	case (a.Seq == 0) != a.ChainedAt.IsZero(), a.Seq == 0 && a.Hash != Sum{}:
		return errors.New("chain: only an anchor of an empty chain has no head")
	}
	if err := a.checkBase(); err != nil {
		return err
	}
	return a.checkLists()
}

// checkBase reports a base that is not an anchor of the tenant at After.
func (a *Anchor) checkBase() error {
	if a.Base == "" {
		if a.After != 0 {
			return errors.New("chain: an anchor that follows another names it as its base")
		}
		return nil
	}
	m := anchorKey.FindStringSubmatch(a.Base)
	if len(m) != 3 || m[1] != a.Tenant || m[2] != fmt.Sprintf("%020d", a.After) {
		return fmt.Errorf("chain: %q is no anchor of the tenant at seq %d", a.Base, a.After)
	}
	return nil
}

// checkLists reports runs that are not distinct, sorted and named, or
// counted below zero where left out, tables that are not distinct, sorted
// and named, and counts that are not of audited tables.
func (a *Anchor) checkLists() error {
	for i, run := range a.Runs {
		if run == "" || i > 0 && a.Runs[i-1] >= run {
			return errors.New("chain: an anchor's runs are distinct, sorted and named")
		}
	}
	if a.Omitted < 0 {
		return errors.New("chain: an anchor leaves out no fewer than 0 runs")
	}
	for i, table := range a.Unsafe {
		if !sourceName.MatchString(table) || i > 0 && a.Unsafe[i-1] >= table {
			return errors.New("chain: an anchor's unsafe tables are distinct, sorted and named")
		}
	}
	for _, counts := range []map[string]int64{a.Counts, a.Records} {
		for source, n := range counts {
			if !sourceName.MatchString(source) || n < 0 {
				return fmt.Errorf("chain: an anchor counts rows and records of audited tables, not %d of %q", n, source)
			}
		}
	}
	return nil
}

// FitRuns returns the runs, in their order, that an anchor can name within
// half its size limit, and how many it leaves out: one record appended by
// hand with a run too long to name, or very many runs, never make an anchor
// too large to read back, which would stop anchoring for good.
func FitRuns(runs []string) (kept []string, omitted int64) {
	size := 0
	for _, run := range runs {
		b, _ := json.Marshal(run)
		if size+len(b)+1 > maxRunsBytes {
			omitted++
			continue
		}
		size += len(b) + 1
		kept = append(kept, run)
	}
	return kept, omitted
}

// Stored is one object, or one version of an object, in an anchor store.
type Stored struct {
	Key string
	// Version names the version, in a store that keeps versions.
	Version string
	// Deleted marks a delete marker: a version that hides the key from a
	// listing without versions.
	Deleted bool
	Body    []byte
	// LockedUntil is when the object's retention in compliance mode ends,
	// zero when it has none.
	LockedUntil time.Time
}

// CheckAnchors reads a tenant's anchors from every object and version a
// store holds. It returns the anchors, one per key, ordered by time and
// sequence, and what it found wrong: an object that is no valid anchor of
// the tenant under its own name, a key hidden by a delete marker, and
// versions of one key that differ. Two anchors of one Seq may hold two
// hashes: after a restore, the chain writes that Seq again.
func CheckAnchors(tenant string, objects []Stored) ([]Anchor, []Finding) {
	var findings []Finding
	byKey := map[string]Anchor{}
	for i := range objects {
		o := &objects[i]
		if o.Deleted {
			findings = append(findings, Finding{Kind: AnchorHidden, Anchor: o.Key, Detail: "a delete marker hides the anchor from a listing without versions"})
			continue
		}
		a, err := Decode(o.Body)
		switch {
		case err != nil:
			findings = append(findings, Finding{Kind: BadAnchor, Anchor: o.Key, Detail: err.Error()})
			continue
		case a.Tenant != tenant || a.Key() != o.Key:
			findings = append(findings, Finding{Kind: BadAnchor, Anchor: o.Key, Detail: "the object holds the anchor named " + a.Key()})
			continue
		}
		if seen, ok := byKey[o.Key]; ok {
			if !bytes.Equal(seen.Encode(), a.Encode()) {
				findings = append(findings, Finding{Kind: AnchorConflict, Seq: a.Seq, Anchor: o.Key, Detail: "two versions of the anchor differ"})
			}
			continue
		}
		byKey[o.Key] = a
	}
	anchors := make([]Anchor, 0, len(byKey))
	for k := range byKey {
		anchors = append(anchors, byKey[k])
	}
	slices.SortFunc(anchors, func(x, y Anchor) int {
		if c := x.At.Compare(y.At); c != 0 {
			return c
		}
		return int(min(max(x.Seq-y.Seq, -1), 1))
	})
	return anchors, findings
}
