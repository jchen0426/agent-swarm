package chain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/audit/chain"
)

// anchor returns a valid anchor of tenant-a's chain at l, taken at at,
// whose base, when after is not 0, is an anchor at seq after an hour before.
func anchor(l *chain.Link, at time.Time, after int64, runs ...string) chain.Anchor {
	a := chain.Anchor{Tenant: "tenant-a", Seq: l.Seq, Hash: l.Hash, ChainedAt: l.ChainedAt, At: at, After: after, Runs: runs}
	if after > 0 {
		base := chain.Anchor{Tenant: "tenant-a", Seq: after, At: at.Add(-time.Hour)}
		a.Base = base.Key()
	}
	return a
}

func stored(a *chain.Anchor) chain.Stored { return chain.Stored{Key: a.Key(), Body: a.Encode()} }

// An anchor is written in one canonical form, and read back as written.
func TestAnAnchorRoundTrips(t *testing.T) {
	l := links(3)[2]
	a := anchor(&l, t0.Add(time.Hour), 1, "run-1", "run-2")
	a.Counts = map[string]int64{"tool_calls": 2, "board_events": 1}
	a.Records = map[string]int64{"tool_calls": 2, "board_events": 1}

	got, err := chain.Decode(a.Encode())

	require.NoError(t, err)
	assert.Equal(t, a.Encode(), got.Encode())
	assert.Equal(t, a.Hash, got.Hash)
	assert.True(t, a.ChainedAt.Equal(got.ChainedAt))
	assert.Equal(t, "tenant-a/20261015T100000Z-00000000000000000003.json", got.Key())
	assert.Equal(t, a.Counts, got.Counts)
	assert.Equal(t, a.Records, got.Records)
	assert.Contains(t, string(a.Encode()), `"counts":{"board_events":1,"tool_calls":2}`, "tables are counted in sorted order")
}

// An empty chain is anchored at seq 0 with a zero hash and no head time.
func TestAnEmptyChainIsAnchoredAtZero(t *testing.T) {
	a := chain.Anchor{Tenant: "tenant-a", At: t0}

	got, err := chain.Decode(a.Encode())

	require.NoError(t, err)
	assert.Zero(t, got.Seq)
	assert.Equal(t, `{"tenant":"tenant-a","seq":0,"hash":"0000000000000000000000000000000000000000000000000000000000000000","chainedAt":"0001-01-01T00:00:00Z","at":"2026-10-15T09:00:00.123456Z","after":0,"base":"","runs":[],"counts":{},"records":{},"unsafe":[]}`, //nolint:testifylint // the canonical form is compared byte for byte
		string(a.Encode()))
}

// Only the canonical form of a valid anchor is read: anything else in the
// store is no anchor.
func TestDecodeRefusesAnythingButAValidCanonicalAnchor(t *testing.T) {
	l := links(3)[2]
	a := anchor(&l, t0.Add(time.Hour), 1, "run-1")
	good := string(a.Encode())
	for name, body := range map[string]string{
		"spaces":          strings.Replace(good, `"seq":3`, `"seq": 3`, 1),
		"unknown field":   strings.Replace(good, `{"tenant"`, `{"extra":1,"tenant"`, 1),
		"trailing data":   good + "{}",
		"field order":     strings.Replace(strings.Replace(good, `"tenant":"tenant-a",`, "", 1), `"runs"`, `"tenant":"tenant-a","runs"`, 1),
		"upper hex":       strings.Replace(good, `"hash":"`+l.Hash.String(), `"hash":"`+strings.ToUpper(l.Hash.String()), 1),
		"bad tenant":      strings.Replace(good, `"tenant-a"`, `"Tenant/A"`, 1),
		"after past seq":  strings.Replace(good, `"after":1`, `"after":4`, 1),
		"negative seq":    strings.Replace(good, `"seq":3`, `"seq":-3`, 1),
		"unsorted runs":   strings.Replace(good, `["run-1"]`, `["run-2","run-1"]`, 1),
		"repeated runs":   strings.Replace(good, `["run-1"]`, `["run-1","run-1"]`, 1),
		"empty run":       strings.Replace(good, `["run-1"]`, `[""]`, 1),
		"no time":         strings.Replace(good, `"at":"2026-10-15T10:00:00.123456Z"`, `"at":"0001-01-01T00:00:00Z"`, 1),
		"local time":      strings.Replace(good, `"at":"2026-10-15T10:00:00.123456Z"`, `"at":"2026-10-15T21:00:00.123456+11:00"`, 1),
		"head of empty":   `{"tenant":"tenant-a","seq":0,"hash":"` + l.Hash.String() + `","chainedAt":"0001-01-01T00:00:00Z","at":"2026-10-15T09:00:00Z","after":0,"runs":[]}`,
		"no head":         strings.Replace(good, `"chainedAt":"2026-10-15T09:00:03.123456Z"`, `"chainedAt":"0001-01-01T00:00:00Z"`, 1),
		"negative count":  strings.Replace(good, `"counts":{}`, `"counts":{"tool_calls":-1}`, 1),
		"bad record":      strings.Replace(good, `"records":{}`, `"records":{"Tool Calls":1}`, 1),
		"unsorted tables": strings.Replace(good, `"unsafe":[]`, `"unsafe":["tool_hides","board_events"]`, 1),
		"bad table":       strings.Replace(good, `"unsafe":[]`, `"unsafe":["Tool Hides"]`, 1),
		"no base":         strings.Replace(good, `"base":"tenant-a/20261015T090000Z-00000000000000000001.json"`, `"base":""`, 1),
		"base elsewhere":  strings.Replace(good, `"base":"tenant-a/`, `"base":"tenant-b/`, 1),
		"base at a seq":   strings.Replace(good, `-00000000000000000001.json"`, `-00000000000000000002.json"`, 1),
		"bad table name":  strings.Replace(good, `"counts":{}`, `"counts":{"Tool Calls":1}`, 1),
		"not json":        "anchor",
		"too large":       good + strings.Repeat(" ", chain.MaxAnchorBytes),
	} {
		_, err := chain.Decode([]byte(body))
		require.Error(t, err, name)
	}
	_, err := chain.Decode([]byte(good))
	require.NoError(t, err)
}

// A tenant id names anchors only in a form that is safe in an object's name.
func TestATenantIDIsSafeInAnObjectName(t *testing.T) {
	assert.True(t, chain.ValidTenant("tenant-a"))
	for _, bad := range []string{"", "Tenant", "a/b", "../a", "-a", strings.Repeat("a", 64), "a b"} {
		assert.False(t, chain.ValidTenant(bad), bad)
	}
}

// What a store holds is read as the tenant's anchors, once per key and in
// time order; what is no anchor of the tenant under its own name, a delete
// marker, and versions of one key that differ are found.
func TestCheckAnchorsFindsWhatTheStoreHoldsWrong(t *testing.T) {
	ls := links(3)
	a1 := anchor(&ls[0], t0.Add(time.Hour), 0, "run-1")
	a2 := anchor(&ls[2], t0.Add(2*time.Hour), 1, "run-1")
	forged := a2
	forged.Hash = chain.Digest("forged")
	forged.At = t0.Add(3 * time.Hour)
	other := a1
	other.Tenant = "tenant-b"
	renamed := stored(&a1)
	renamed.Key = "tenant-a/20990101T000000Z-00000000000000000001.json"
	rewritten := a2
	rewritten.Hash = chain.Digest("rewritten")
	second := stored(&rewritten)
	second.Version = "v2"

	anchors, findings := chain.CheckAnchors("tenant-a", []chain.Stored{
		stored(&a2), stored(&a1), stored(&a1), second, stored(&forged),
		{Key: a1.Key(), Version: "v3", Deleted: true}, stored(&other), renamed, {Key: "tenant-a/junk", Body: []byte("junk")},
	})

	require.Len(t, anchors, 3)
	assert.Equal(t, []int64{1, 3, 3}, []int64{anchors[0].Seq, anchors[1].Seq, anchors[2].Seq}, "after a restore, two anchors of one seq may hold two hashes")
	var kinds []chain.Kind
	for i := range findings {
		kinds = append(kinds, findings[i].Kind)
	}
	assert.ElementsMatch(t, []chain.Kind{chain.BadAnchor, chain.AnchorConflict, chain.AnchorHidden, chain.BadAnchor, chain.BadAnchor}, kinds)
}

// Reading an anchor never fails on any bytes, and what it accepts it writes
// back unchanged.
func FuzzDecodeNeverFails(f *testing.F) {
	l := links(2)[1]
	a := anchor(&l, t0, 1, "run-1")
	f.Add(a.Encode())
	f.Add([]byte(`{"tenant":"a","seq":0}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		got, err := chain.Decode(b)
		if err != nil {
			return
		}
		assert.Equal(t, string(b), string(got.Encode()))
	})
}
