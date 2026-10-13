package prepare

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/jchen0426/agent-swarm/bench/itbench/raw"
)

// readRows calls fn for each row of a raw table.
func readRows(fsys fs.FS, name string, fn func(raw.Row) error) (err error) {
	f, err := fsys.Open(name)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	if err := raw.ReadTSV(f, fn); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}
