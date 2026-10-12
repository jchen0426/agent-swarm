package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/jchen0426/agent-swarm/internal/work/board"
)

// boardFile is the input of the conclude command: a run's board settings and
// its events in commit order. Provenance in it is trusted, as in a replay.
type boardFile struct {
	Settings board.Settings `json:"settings"`
	Events   []board.Event  `json:"events"`
}

func newConcludeCommand() *cobra.Command {
	var path, at string
	cmd := &cobra.Command{
		Use:   "conclude --board FILE [--at TIME]",
		Short: "Assess a recorded board and print its hypotheses and conclusion as JSON",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			assessment, err := conclude(path, at)
			if err != nil {
				return failure{err}
			}
			return writeJSON(c.OutOrStdout(), assessment)
		},
	}
	cmd.Flags().StringVar(&path, "board", "", "board file to assess (JSON)")
	cmd.Flags().StringVar(&at, "at", "", "assess as of this time, in RFC 3339 (default: every event)")
	_ = cmd.MarkFlagRequired("board")
	return cmd
}

// conclude replays the board in the file and assesses it, as of at when set.
func conclude(path, at string) (board.Assessment, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return board.Assessment{}, fmt.Errorf("read board file: %w", err)
	}
	var f boardFile
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return board.Assessment{}, fmt.Errorf("parse board file: %w", err)
	}
	events := f.Events
	if at != "" {
		t, err := time.Parse(time.RFC3339, at)
		if err != nil {
			return board.Assessment{}, fmt.Errorf("parse --at: %w", err)
		}
		events = board.AsOf(events, t)
	}
	s, err := board.Replay(f.Settings, events)
	if err != nil {
		return board.Assessment{}, err
	}
	return s.Assess(), nil
}
