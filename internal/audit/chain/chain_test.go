package chain_test

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/audit/chain"
)

var t0 = time.Date(2026, 10, 15, 9, 0, 0, 123456000, time.UTC)

// link returns a valid record at seq after prev.
func link(seq int64, prev chain.Sum) chain.Link {
	l := chain.Link{
		Seq: seq, Source: "board_events", Key: `["run-1", ` + string(rune('0'+seq%10)) + `]`, Run: "run-1", Form: 1,
		Digest: chain.Digest("row"), Prev: prev, ChainedAt: t0.Add(time.Duration(seq) * time.Second),
	}
	l.Hash = chain.LinkHash(prev, &l)
	return l
}

// links returns a valid chain of n records.
func links(n int) []chain.Link {
	out := make([]chain.Link, 0, n)
	var prev chain.Sum
	for seq := int64(1); seq <= int64(n); seq++ {
		l := link(seq, prev)
		out = append(out, l)
		prev = l.Hash
	}
	return out
}

// The link hash covers every field of a record, so that changing any one
// changes the hash.
func TestTheLinkHashCoversEveryField(t *testing.T) {
	base := link(3, chain.Digest("before"))
	want := chain.LinkHash(base.Prev, &base)
	for name, change := range map[string]func(l *chain.Link){
		"seq":       func(l *chain.Link) { l.Seq++ },
		"source":    func(l *chain.Link) { l.Source = "tool_calls" },
		"key":       func(l *chain.Link) { l.Key += " " },
		"run":       func(l *chain.Link) { l.Run = "run-2" },
		"form":      func(l *chain.Link) { l.Form = 2 },
		"digest":    func(l *chain.Link) { l.Digest[0] ^= 1 },
		"chainedAt": func(l *chain.Link) { l.ChainedAt = l.ChainedAt.Add(time.Microsecond) },
	} {
		l := base
		change(&l)
		assert.NotEqual(t, want, chain.LinkHash(l.Prev, &l), name)
	}
	other := base.Prev
	other[31] ^= 1
	assert.NotEqual(t, want, chain.LinkHash(other, &base), "prev")
}

// Fields are hashed before they are joined, so moving text from one field
// to the next changes the hash.
func TestFieldsCannotRunIntoEachOther(t *testing.T) {
	a := chain.Link{Seq: 1, Source: "ab", Key: "c", ChainedAt: t0}
	b := chain.Link{Seq: 1, Source: "a", Key: "bc", ChainedAt: t0}
	assert.NotEqual(t, chain.LinkHash(chain.Sum{}, &a), chain.LinkHash(chain.Sum{}, &b))
}

// The link hash is SHA-256 over the parts in the order Postgres writes them:
// this vector is what the migration's function computes for the same record.
func TestTheLinkHashIsTheDatabasesEncoding(t *testing.T) {
	l := chain.Link{Seq: 1, Source: "s", Key: "k", Run: "r", Form: 1, Digest: chain.Digest("d"), ChainedAt: time.UnixMicro(1).UTC()}
	part := func(s string) []byte { h := sha256.Sum256([]byte(s)); return h[:] }
	var b []byte
	b = append(b, make([]byte, 32)...)
	b = append(b, 0, 0, 0, 0, 0, 0, 0, 1)
	b = append(b, part("s")...)
	b = append(b, part("k")...)
	b = append(b, part("r")...)
	b = append(b, 0, 1)
	b = append(b, part("d")...)
	b = append(b, 0, 0, 0, 0, 0, 0, 0, 1)
	assert.Equal(t, chain.Sum(sha256.Sum256(b)), chain.LinkHash(chain.Sum{}, &l))
}

// A hash is written as 64 lowercase hex digits, and read only in that form.
func TestAHashIsLowercaseHex(t *testing.T) {
	s := chain.Digest("x")
	text, err := s.MarshalText()
	require.NoError(t, err)
	assert.Equal(t, hex.EncodeToString(s[:]), string(text))
	var back chain.Sum
	require.NoError(t, back.UnmarshalText(text))
	assert.Equal(t, s, back)
	for _, bad := range []string{"", "00", string(text[:63]) + "G", string(text[:63]) + "A", string(text) + "0"} {
		require.Error(t, back.UnmarshalText([]byte(bad)), bad)
	}
}
