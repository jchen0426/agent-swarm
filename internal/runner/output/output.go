// Package output is what a turn leaves its runner (Plan 7d-1, Decision 12):
// its output file, <runner.output>/<turn id>.out, which keeps the first
// bytes of the turn's standard output and standard error, and the class its
// exit code gives (Contract C7). Both runners, the process runner and the
// Docker runner, use it, so that one fix to it reaches both (Plan 7d-2).
package output

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jchen0426/agent-swarm/internal/outcome"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// CheckID refuses a turn's id that cannot name its output file, <id>.out,
// in the output directory: an empty one, or one that holds a path.
func CheckID(id string) error {
	if id == "" || filepath.Base(id) != id {
		return fmt.Errorf("a turn's id names its output file, which %q cannot", id)
	}
	return nil
}

// File is a turn's output file, open for writing. It keeps the first bytes
// written to it, up to its cap, and drops what follows, as it drops all that
// follows a write that failed: it never fails its writer, so that a turn
// neither blocks on its output nor fails for it.
type File struct {
	file *os.File
	left int64
	err  error
}

// Create creates a turn's output file, <dir>/<id>.out, empty, mode 0600,
// which keeps the turn's first limit bytes. An id CheckID refuses is
// refused.
func Create(dir, id string, limit int64) (*File, error) {
	if err := CheckID(id); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(dir, id+".out"), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // G304: the output directory's file of a turn, named by its id.
	if err != nil {
		return nil, err
	}
	return &File{file: file, left: limit}, nil
}

func (f *File) Write(b []byte) (int, error) {
	if f.err == nil && f.left > 0 {
		n := min(int64(len(b)), f.left)
		_, f.err = f.file.Write(b[:n])
		f.left -= n
	}
	return len(b), nil
}

// Name returns the file's path.
func (f *File) Name() string { return f.file.Name() }

// Close closes the file, and returns the failure of the write that failed,
// if one did, with the close's.
func (f *File) Close() error { return errors.Join(f.err, f.file.Close()) }

// classes gives the class of each exit code of agentswarm turn (Decision 13;
// Contract C7).
var classes = map[int]item.TurnClass{
	outcome.ExitEnded:       item.TurnEnded,
	outcome.ExitFinished:    item.TurnFinished,
	outcome.ExitRefused:     item.TurnRefused,
	outcome.ExitVendor:      item.TurnVendor,
	outcome.ExitInterrupted: item.TurnInterrupted,
	outcome.ExitUnavailable: item.TurnUnavailable,
}

// Class returns the class an exit code of agentswarm turn gives, by Contract
// C7's table, and false for a code outside it, which each runner reads by
// its own rules.
func Class(code int) (item.TurnClass, bool) {
	class, ok := classes[code]
	return class, ok
}
