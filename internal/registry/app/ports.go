// Package app holds the Registry context's use cases and declares the ports
// they need: the configuration source and the record of loaded revisions.
package app

import (
	"context"
	"io/fs"

	"github.com/jchen0426/agent-swarm/internal/registry/config"
)

// Snapshot is the files of one configuration revision.
type Snapshot struct {
	// Commit is the revision's full commit id, or empty when the files come
	// from a directory that is not a commit.
	Commit string
	Files  fs.FS
}

// Source reads configuration revisions (the configuration source port of
// spec Section 2.2).
type Source interface {
	// Read returns the files at ref: a branch, a tag or a commit id.
	Read(ctx context.Context, ref string) (Snapshot, error)
}

// Revisions records each loaded revision with the content hashes of its
// objects (spec Section 3.7). Recording a revision again changes nothing.
type Revisions interface {
	Record(ctx context.Context, rev *config.Revision) error
}

// Limits on one snapshot, which every source keeps. A configuration
// repository is small.
const (
	MaxSnapshotFiles = 20_000
	MaxSnapshotBytes = 256 << 20
)
