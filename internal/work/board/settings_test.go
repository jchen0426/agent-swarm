package board_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/board"
)

func TestReplayChecksConvergenceSettings(t *testing.T) {
	tests := []struct {
		name string
		conv board.Convergence
		ok   bool
	}{
		{"the defaults", board.DefaultConvergence, true},
		{"a margin of zero", board.Convergence{MinContributors: 1, MinSources: 1, RefutationMargin: 0}, true},
		{"missing settings", board.Convergence{}, false},
		{"no contributor needed", board.Convergence{MinContributors: 0, MinSources: 2, RefutationMargin: 2}, false},
		{"no source needed", board.Convergence{MinContributors: 2, MinSources: 0, RefutationMargin: 2}, false},
		{"a negative margin", board.Convergence{MinContributors: 2, MinSources: 2, RefutationMargin: -1}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			settings := incidentSettings()
			settings.Convergence = tt.conv

			_, err := board.Replay(settings, nil)

			if tt.ok {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, board.ErrInvalid)
		})
	}
}
