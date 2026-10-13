package raw

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"time"
)

// Row is one record of a table, read by column name.
type Row struct {
	cols   map[string]int
	fields []string
}

// Get returns the named column, or "" when the table has no such column.
func (r Row) Get(name string) string {
	i, ok := r.cols[name]
	if !ok {
		return ""
	}
	return r.fields[i]
}

// ReadTSV calls fn for each record of a pandas-exported table: tab
// separators, a header row, and CSV quoting that allows embedded newlines.
// It stops at the first error, including one returned by fn.
func ReadTSV(r io.Reader, fn func(Row) error) error {
	cr := csv.NewReader(r)
	cr.Comma = '\t'
	cr.ReuseRecord = true
	header, err := cr.Read()
	if err != nil {
		return fmt.Errorf("read header: %w", err)
	}
	cols := make(map[string]int, len(header))
	for i, name := range header {
		cols[name] = i
	}
	for {
		fields, err := cr.Read()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := fn(Row{cols: cols, fields: fields}); err != nil {
			return err
		}
	}
}

// ParseTime reads a timestamp as the export writes it, in UTC.
func ParseTime(s string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02 15:04:05.999999999", time.RFC3339Nano} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized time %q", s)
}
