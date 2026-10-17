// Package chain holds the rules of the audit chain (spec Section 9.4): how
// each audited row links to the one before it, what an anchor outside the
// database holds, and what a verification finds when the two disagree.
package chain

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// Sum is a SHA-256 hash.
type Sum [sha256.Size]byte

// String returns the hash in lowercase hex.
func (s Sum) String() string { return hex.EncodeToString(s[:]) }

// MarshalText writes the hash in lowercase hex.
func (s Sum) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// UnmarshalText reads a hash written in lowercase hex.
func (s *Sum) UnmarshalText(text []byte) error {
	if len(text) != 2*len(s) {
		return fmt.Errorf("chain: a hash is %d hex digits, not %d", 2*len(s), len(text))
	}
	for _, c := range text {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return errors.New("chain: a hash is written in lowercase hex")
		}
	}
	_, err := hex.Decode(s[:], text)
	return err
}

// Link is one record of the chain: an audited row, named by its table and
// its key, with the digest of the row as the database renders it.
type Link struct {
	Seq int64
	// Source is the audited table; Key is the row's primary key within the
	// tenant, as a JSON array; Run is the row's run, or empty.
	Source, Key, Run string
	// Form numbers the list of columns the digest covers, per source.
	Form   int16
	Digest Sum
	// Prev is the hash of the record before, zero for the first.
	Prev Sum
	Hash Sum
	// ChainedAt is when the record was chained, to the microsecond.
	ChainedAt time.Time
}

// LinkHash returns the hash that links l after the record whose hash is
// prev. It covers every field of l in fixed-width parts, so that no field
// can run into the next: prev, the sequence number, the hashes of the
// source, the key and the run, the form, the digest, and the chaining time
// in microseconds since the epoch. Integers are big-endian, as Postgres's
// int8send and int2send write them.
func LinkHash(prev Sum, l *Link) Sum {
	b := make([]byte, 0, 4*sha256.Size+sha256.Size+8+2+sha256.Size+8)
	b = append(b, prev[:]...)
	b, _ = binary.Append(b, binary.BigEndian, l.Seq)
	for _, s := range []string{l.Source, l.Key, l.Run} {
		h := sha256.Sum256([]byte(s))
		b = append(b, h[:]...)
	}
	b, _ = binary.Append(b, binary.BigEndian, l.Form)
	b = append(b, l.Digest[:]...)
	b, _ = binary.Append(b, binary.BigEndian, l.ChainedAt.UnixMicro())
	return sha256.Sum256(b)
}

// Digest returns the digest of a row as the database rendered it: its
// columns as one JSON array, in the text Postgres writes for it.
func Digest(rendered string) Sum { return sha256.Sum256([]byte(rendered)) }
