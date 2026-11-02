// Package control holds the platform-wide controls' state: the kill switch
// (spec Section 8.3; Plan 7b-1, Decision 5). It is as pure as item and board,
// so that the stores return its types without importing internal/work/app,
// which re-exports them.
package control

import (
	"errors"
	"time"

	"github.com/jchen0426/agent-swarm/internal/work/board"
)

// KillSwitch is the kill switch's state: its latest row, and what the rules
// read of the rows before it (Decision 5).
type KillSwitch struct {
	On     bool
	Seq    int64        // the latest row's number, 0 before the first
	OnSeq  int64        // the latest on-row's number, 0 before the first on
	OffAt  time.Time    // the latest off-row's time, zero before the first off
	By     string       // who wrote the latest row
	Reason board.Reason // the latest row's reason, its text redacted
	Since  time.Time    // the latest row's time
}

// ErrStale marks a request to the kill switch whose After is not the latest
// row's number, while the state it asks does not hold. The store returns it
// with the state, which the coordinator's view follows.
var ErrStale = errors.New("control: the kill switch has a newer row")
