// Package holding binds what an instance does to its holding of the
// platform's lease (spec Section 8.2; ADR-004). A holding is one term of an
// instance as the lease's holder, named by its epoch, which only grows.
//
// Every guarded write runs under a context that carries the epoch of the
// holding it acts for, and the store refuses it, in the write's transaction,
// once that holding is over. A context without an epoch is a bug: the write
// fails with ErrNoEpoch, which never fences. Before every request to the
// model vendor or to an MCP server, the caller asks the Holding whether its
// epoch still holds.
//
// The package imports nothing beyond the standard library.
package holding

import (
	"context"
	"errors"
)

var (
	// ErrNoEpoch marks a guarded write whose context carries no epoch: a
	// bug, logged as "write without an epoch", which never fences.
	ErrNoEpoch = errors.New("holding: the context carries no epoch")
	// ErrLeaseLost marks a write refused, or a renewal failed, because the
	// holding it acts for is over: it expired, or another instance took the
	// lease over. Every guard's refusal wraps it.
	ErrLeaseLost = errors.New("holding: the lease is lost")
)

// epochKey is the context key of the epoch.
type epochKey struct{}

// WithEpoch returns ctx carrying epoch, the holding that the writes under
// it, and under the contexts derived from it, act for.
func WithEpoch(ctx context.Context, epoch int64) context.Context {
	return context.WithValue(ctx, epochKey{}, epoch)
}

// EpochFrom returns the context's epoch, or ErrNoEpoch when it has none,
// or one below 1.
func EpochFrom(ctx context.Context) (int64, error) {
	epoch, ok := ctx.Value(epochKey{}).(int64)
	if !ok || epoch < 1 {
		return 0, ErrNoEpoch
	}
	return epoch, nil
}

// Holding tells whether a holding of the lease still holds.
type Holding interface {
	// Valid reports whether the holding of epoch still holds: it is the
	// leader's current one, and its last good renewal, or its take, was
	// sent recently enough that it cannot have expired. It takes no lock.
	Valid(epoch int64) bool
}
