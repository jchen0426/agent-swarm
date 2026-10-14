package config

import (
	"cmp"
	"fmt"
	"slices"
)

// Problem is one reason a configuration revision is refused: the file, the
// field within it when there is one, and what is wrong.
type Problem struct {
	File    string `json:"file"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

func (p Problem) String() string {
	if p.Field == "" {
		return p.File + ": " + p.Message
	}
	return p.File + ": " + p.Field + ": " + p.Message
}

// Problems is the error of a refused revision: every problem found, sorted by
// file and field.
type Problems []Problem

func (ps Problems) Error() string {
	if len(ps) == 1 {
		return "configuration refused: " + ps[0].String()
	}
	return fmt.Sprintf("configuration refused: %d problems; the first: %s", len(ps), ps[0])
}

// problems collects the problems of one revision.
type problems struct{ list Problems }

func (ps *problems) add(file, field, format string, args ...any) {
	ps.list = append(ps.list, Problem{File: file, Field: field, Message: fmt.Sprintf(format, args...)})
}

// sorted returns the problems in a stable order, or nil when there are none.
func (ps *problems) sorted() Problems {
	if len(ps.list) == 0 {
		return nil
	}
	slices.SortStableFunc(ps.list, func(a, b Problem) int {
		return cmp.Or(cmp.Compare(a.File, b.File), cmp.Compare(a.Field, b.Field))
	})
	return ps.list
}
