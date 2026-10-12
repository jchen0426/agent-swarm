// Package key models hypothesis keys: the fields that say what a hypothesis
// claims, so that equal claims merge into one hypothesis (spec Section 4.3).
package key

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// Other is the value every field accepts besides its closed set. A key that
// contains it never merges with another key.
const Other = "other"

// Errors returned by Schema.Validate.
var (
	ErrMissingField = errors.New("key: missing field")
	ErrUnknownField = errors.New("key: unknown field")
	ErrValue        = errors.New("key: value outside the field's closed set")
)

// Field is one key field and its closed set of values.
type Field struct {
	Name   string   `json:"name"`
	Values []string `json:"values"`
}

// Schema lists the key fields of a work item type. A run locks it when its
// item starts.
type Schema struct {
	Fields []Field `json:"fields"`
}

// Key holds one value per schema field.
type Key map[string]string

// Validate checks that k has exactly the schema's fields, each with a value
// from the field's closed set or Other.
func (s Schema) Validate(k Key) error {
	for _, f := range s.Fields {
		v, ok := k[f.Name]
		if !ok {
			return fmt.Errorf("%w %q", ErrMissingField, f.Name)
		}
		if v != Other && !slices.Contains(f.Values, v) {
			return fmt.Errorf("%w: %s=%q", ErrValue, f.Name, v)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(k)) {
		if !s.hasField(name) {
			return fmt.Errorf("%w %q", ErrUnknownField, name)
		}
	}
	return nil
}

// Mergeable reports whether a hypothesis with key k merges with others that
// have an equal key. Keys that contain Other never merge, and neither do keys
// of a schema without fields.
func (s Schema) Mergeable(k Key) bool {
	if len(s.Fields) == 0 {
		return false
	}
	for _, v := range k {
		if v == Other {
			return false
		}
	}
	return true
}

func (s Schema) hasField(name string) bool {
	return slices.ContainsFunc(s.Fields, func(f Field) bool { return f.Name == name })
}

// Equal reports whether two keys have the same fields with the same values.
func (k Key) Equal(other Key) bool {
	return maps.Equal(k, other)
}

// String renders the key canonically: fields sorted by name and values quoted,
// so that equal keys render identically and different keys differently.
func (k Key) String() string {
	var b strings.Builder
	for i, name := range slices.Sorted(maps.Keys(k)) {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(name)
		b.WriteByte('=')
		b.WriteString(strconv.Quote(k[name]))
	}
	return b.String()
}
