package board_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jchen0426/agent-swarm/internal/work/board"
)

// StatusOf names a hypothesis's standing by the table: converged, rejected,
// refuted while not rejected, leading at rank 0 with none of those, and
// trailing for the rest.
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

	assert.Equal(t, board.StatusConverged, board.StatusOf(a, 0))
	assert.Equal(t, board.StatusRejected, board.StatusOf(a, 1), "rejected wins over refuted")
	assert.Equal(t, board.StatusRefuted, board.StatusOf(a, 2))
	assert.Equal(t, board.StatusTrailing, board.StatusOf(a, 3), "rank 3 with none of the others")
	assert.Equal(t, board.StatusConverged, board.StatusOf(a, 4), "converged is checked first")
	assert.Equal(t, board.StatusLeading, board.StatusOf(lead, 0))
	assert.Equal(t, board.StatusTrailing, board.StatusOf(lead, 1))
	assert.Equal(t, board.StatusRefuted, board.StatusOf(refutedLead, 0), "refuted beats leading")
	assert.Equal(t, board.StatusWord(""), board.StatusOf(lead, 2), "no such hypothesis")
	assert.Equal(t, board.StatusWord(""), board.StatusOf(lead, -1))
}
