package board_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jchen0426/agent-swarm/internal/work/board"
)

// StatusOf names a hypothesis's standing by the table: converged, and
// confirming while the open wrap-up round names it; rejected; refuted while
// not rejected; leading at rank 0 with none of those; and trailing for the
// rest.
func TestStatusWordsForEachHypothesisState(t *testing.T) {
	refuted := []board.EntryID{"e1"}
	a := &board.Assessment{Hypotheses: []board.HypothesisView{
		0: {Converged: true},
		1: {Rejected: true, StandingRefutations: refuted},
		2: {StandingRefutations: refuted},
		3: {},
		4: {Converged: true, StandingRefutations: refuted},
	}}
	lead := &board.Assessment{Hypotheses: []board.HypothesisView{{}, {}}}
	refutedLead := &board.Assessment{Hypotheses: []board.HypothesisView{{StandingRefutations: refuted}}}

	assert.Equal(t, board.StatusConverged, board.StatusOf(a, 0, ""))
	assert.Equal(t, board.StatusRejected, board.StatusOf(a, 1, ""), "rejected wins over refuted")
	assert.Equal(t, board.StatusRefuted, board.StatusOf(a, 2, ""))
	assert.Equal(t, board.StatusTrailing, board.StatusOf(a, 3, ""), "rank 3 with none of the others")
	assert.Equal(t, board.StatusConverged, board.StatusOf(a, 4, ""), "converged is checked first")
	assert.Equal(t, board.StatusLeading, board.StatusOf(lead, 0, ""))
	assert.Equal(t, board.StatusTrailing, board.StatusOf(lead, 1, ""))
	assert.Equal(t, board.StatusRefuted, board.StatusOf(refutedLead, 0, ""), "refuted beats leading")
	assert.Equal(t, board.StatusWord(""), board.StatusOf(lead, 2, ""), "no such hypothesis")
	assert.Equal(t, board.StatusWord(""), board.StatusOf(lead, -1, ""))

	named := &board.Assessment{Hypotheses: []board.HypothesisView{
		0: {ID: "h1"},
		1: {ID: "h2", Converged: true},
		2: {ID: "h3", Converged: true},
		3: {ID: "h4", Rejected: true},
		4: {ID: "h5", StandingRefutations: refuted},
		5: {ID: "h6"},
	}}
	assert.Equal(t, board.StatusConfirming, board.StatusOf(named, 1, "h2"), "converged and named by the wrap-up round")
	assert.Equal(t, board.StatusConverged, board.StatusOf(named, 2, "h2"), "converged while the wrap-up round names another")
	assert.Equal(t, board.StatusLeading, board.StatusOf(named, 0, "h1"), "named and not converged: its word as before")
	assert.Equal(t, board.StatusRejected, board.StatusOf(named, 3, "h4"), "named and not converged: its word as before")
	assert.Equal(t, board.StatusRefuted, board.StatusOf(named, 4, "h5"), "named and not converged: its word as before")
	assert.Equal(t, board.StatusTrailing, board.StatusOf(named, 5, "h6"), "named and not converged: its word as before")
}

// A suggestion is superseded when its hypothesis is rejected now, or is not
// converged now and converged at some round's end: spec 6.3's "rejected or
// lost convergence", read as written.
func TestTheSupersededMarkIsSpec63s(t *testing.T) {
	for _, tc := range []struct {
		name                              string
		rejected, converged, wasConverged bool
		superseded                        bool
	}{
		{name: "rejected", rejected: true, superseded: true},
		{name: "rejected after it converged", rejected: true, wasConverged: true, superseded: true},
		{name: "lost convergence", wasConverged: true, superseded: true},
		{name: "converged now", converged: true, wasConverged: true},
		{name: "converged now, at no round's end yet", converged: true},
		{name: "never converged"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := board.HypothesisView{Rejected: tc.rejected, Converged: tc.converged}
			assert.Equal(t, tc.superseded, board.Superseded(&h, tc.wasConverged))
		})
	}
}
