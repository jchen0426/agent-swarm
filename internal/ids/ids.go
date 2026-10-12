// Package ids issues identifiers for platform records.
package ids

import "github.com/google/uuid"

// UUIDv7 issues time-ordered UUIDs (RFC 9562, version 7). Within one process
// each new id sorts after the previous one.
type UUIDv7 struct{}

// NewID returns a new version 7 UUID in its canonical text form.
func (UUIDv7) NewID() string {
	return uuid.Must(uuid.NewV7()).String()
}
