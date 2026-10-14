// Package tools holds the gateway's rules for a tool's definition and for one
// call's arguments (spec Sections 3.3 and 6.2). A definition is listed to
// agents only when a reviewer could see all of it, its input schema is an
// object schema the gateway can check, and its bounds name arguments it has.
// A call's arguments are read strictly, so that the gateway, the audit store
// and the server all read the same call: one JSON object, without repeated
// keys or keys that differ only in case, with only the arguments the pinned
// schema declares, within the tool's bounds. The bytes that were checked are
// the bytes that are recorded and forwarded.
package tools

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/jchen0426/agent-swarm/internal/registry/config"
)

// ErrArguments marks arguments that break a rule. Its message says which, in
// words an agent can act on.
var ErrArguments = errors.New("tools: invalid arguments")

// Bounds on the shape of arguments. Telling keys apart in any case compares
// each key with the others, so members per object are capped too.
const (
	maxDepth   = 32
	maxMembers = 1000
)

// Formats a bounded argument must have. Each has one reading only, so a
// server cannot read more into it than the gateway checked.
var (
	boundedDuration = regexp.MustCompile(`^[1-9]\d{0,6}(ms|s|m|h)$`)
	boundedNumber   = regexp.MustCompile(`^(0|[1-9]\d{0,14})(\.\d{1,15})?$`)
)

// HiddenCharacters reports whether a definition holds characters a reviewer
// could miss (config.Hidden): in its name, title or description, or in any
// string or key of its schemas and annotations, decoded.
func HiddenCharacters(def *config.ToolDefinition) bool {
	for _, s := range []string{def.Name, def.Title, def.Description} {
		if strings.ContainsFunc(s, config.Hidden) {
			return true
		}
	}
	for _, raw := range [][]byte{def.InputSchema, def.OutputSchema, def.Annotations} {
		if len(raw) == 0 {
			continue
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil || anyString(v, func(s string) bool { return strings.ContainsFunc(s, config.Hidden) }) {
			return true
		}
	}
	return false
}

// anyString reports whether f holds for any string or key in v.
func anyString(v any, f func(string) bool) bool {
	switch t := v.(type) {
	case string:
		return f(t)
	case map[string]any:
		for k, e := range t {
			if f(k) || anyString(e, f) {
				return true
			}
		}
	case []any:
		for _, e := range t {
			if anyString(e, f) {
				return true
			}
		}
	}
	return false
}

// Schema is a tool's compiled input schema.
type Schema struct {
	resolved *jsonschema.Resolved
	declared map[string]bool
	types    map[string][]string
}

// CompileSchema compiles a pinned input schema. It refuses a schema whose root
// is not an object schema, one that refers to schemas outside itself (so
// compiling never reaches the network), and one that asks clients to copy
// arguments into HTTP headers (x-mcp-header), which would change what the
// gateway sends.
func CompileSchema(raw []byte) (*Schema, error) {
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, fmt.Errorf("tools: input schema: %w", err)
	}
	if hasKey(generic, "x-mcp-header") {
		return nil, errors.New("tools: input schema: asks clients to copy arguments into HTTP headers")
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("tools: input schema: %w", err)
	}
	if s.Type != "object" {
		return nil, errors.New(`tools: input schema: the root must have type "object"`)
	}
	resolved, err := s.Resolve(nil)
	if err != nil {
		return nil, fmt.Errorf("tools: input schema: %w", err)
	}
	c := &Schema{resolved: resolved, declared: map[string]bool{}, types: map[string][]string{}}
	for _, part := range append([]*jsonschema.Schema{&s}, slices.Concat(s.AllOf, s.AnyOf, s.OneOf)...) {
		if part == nil {
			continue
		}
		for name, p := range part.Properties {
			c.declared[name] = true
			if p != nil {
				c.types[name] = append(c.types[name], p.Type)
				c.types[name] = append(c.types[name], p.Types...)
			}
		}
	}
	return c, nil
}

// hasKey reports whether key is a key anywhere in v.
func hasKey(v any, key string) bool {
	switch t := v.(type) {
	case map[string]any:
		for k, e := range t {
			if k == key || hasKey(e, key) {
				return true
			}
		}
	case []any:
		for _, e := range t {
			if hasKey(e, key) {
				return true
			}
		}
	}
	return false
}

// MissingBounds returns the bounded arguments the schema does not declare,
// sorted.
func (s *Schema) MissingBounds(bounds map[string]config.Bound) []string {
	var out []string
	for _, arg := range slices.Sorted(maps.Keys(bounds)) {
		if !s.declared[arg] {
			out = append(out, arg)
		}
	}
	return out
}

// Args are a call's arguments, read strictly.
type Args struct {
	// Raw is the canonical form that was checked: one compact JSON object
	// with sorted keys, numbers as written. It is what is recorded and sent.
	Raw json.RawMessage
	// values holds the arguments with numbers as json.Number.
	values map[string]any
}

// Values returns the arguments, decoded, numbers as json.Number. The caller
// may change them.
func (a *Args) Values() map[string]any { return a.values }

// Read reads raw as a call's arguments: one JSON object of at most limit
// bytes, whose top-level keys the schema declares, with no key repeated or
// repeated in another case, nested at most 32 deep, without NUL characters,
// and which the schema accepts. Absent arguments are the empty object.
func (s *Schema) Read(raw json.RawMessage, limit int) (*Args, error) {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || bytes.Equal(t, []byte("null")) {
		t = []byte("{}")
	}
	if len(t) > limit {
		return nil, fmt.Errorf("%w: the arguments are larger than %d bytes", ErrArguments, limit)
	}
	if !utf8.Valid(t) {
		return nil, fmt.Errorf("%w: the arguments are not valid UTF-8", ErrArguments)
	}
	dec := json.NewDecoder(bytes.NewReader(t))
	dec.UseNumber()
	v, err := strict(dec, 0)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: the arguments hold more than one JSON value", ErrArguments)
	}
	values, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: the arguments must be a JSON object", ErrArguments)
	}
	for _, k := range slices.Sorted(maps.Keys(values)) {
		if !s.declared[k] {
			return nil, fmt.Errorf("%w: %q is not an argument of this tool", ErrArguments, k)
		}
	}
	canonical, err := encode(values)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrArguments, err)
	}
	var plain any
	if err := json.Unmarshal(canonical, &plain); err != nil {
		return nil, fmt.Errorf("%w: the gateway cannot read the arguments", ErrArguments)
	}
	if err := s.resolved.Validate(plain); err != nil {
		return nil, schemaError(err)
	}
	return &Args{Raw: canonical, values: values}, nil
}

// schemaWhere finds where a schema error is, and which rule it broke, in
// the schema library's message: "validating /properties/flag: type: ...".
var schemaWhere = regexp.MustCompile(`validating (/[^:]*): ([A-Za-z]+):`)

// schemaError says where the arguments break the schema and which rule,
// never the value: the library's message quotes the value, which only the
// tool's redaction paths would have caught.
func schemaError(err error) error {
	m := schemaWhere.FindAllStringSubmatch(err.Error(), -1)
	if len(m) == 0 {
		return fmt.Errorf("%w: the arguments do not match the tool's input schema", ErrArguments)
	}
	last := m[len(m)-1]
	return fmt.Errorf("%w: the arguments do not match the tool's input schema at %s (%s)", ErrArguments, last[1], last[2])
}

// strict decodes one JSON value token by token, refusing repeated keys, keys
// that differ only in case, NUL characters and nesting deeper than maxDepth.
func strict(dec *json.Decoder, depth int) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("%w: the arguments are not JSON", ErrArguments)
	}
	if s, ok := tok.(string); ok && strings.ContainsRune(s, 0) {
		return nil, fmt.Errorf("%w: the arguments hold a NUL character", ErrArguments)
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return tok, nil
	}
	if depth == maxDepth {
		return nil, fmt.Errorf("%w: the arguments nest deeper than %d levels", ErrArguments, maxDepth)
	}
	if delim == '[' {
		return strictArray(dec, depth)
	}
	return strictObject(dec, depth)
}

func strictArray(dec *json.Decoder, depth int) (any, error) {
	list := []any{}
	for dec.More() {
		v, err := strict(dec, depth+1)
		if err != nil {
			return nil, err
		}
		list = append(list, v)
	}
	if _, err := dec.Token(); err != nil {
		return nil, fmt.Errorf("%w: the arguments are not JSON", ErrArguments)
	}
	return list, nil
}

func strictObject(dec *json.Decoder, depth int) (any, error) {
	obj := map[string]any{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("%w: the arguments are not JSON", ErrArguments)
		}
		key, _ := tok.(string)
		if strings.ContainsRune(key, 0) {
			return nil, fmt.Errorf("%w: the arguments hold a NUL character", ErrArguments)
		}
		if len(obj) == maxMembers {
			return nil, fmt.Errorf("%w: an object holds more than %d members", ErrArguments, maxMembers)
		}
		for k := range obj {
			if strings.EqualFold(k, key) {
				return nil, fmt.Errorf("%w: the key %q appears twice, or in two cases", ErrArguments, key)
			}
		}
		v, err := strict(dec, depth+1)
		if err != nil {
			return nil, err
		}
		obj[key] = v
	}
	if _, err := dec.Token(); err != nil {
		return nil, fmt.Errorf("%w: the arguments are not JSON", ErrArguments)
	}
	return obj, nil
}

// encode writes v as compact JSON with sorted keys, numbers as written, and
// no HTML escaping.
func encode(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// CheckBounds checks every bounded argument (spec Section 3.3). It must be
// present, because a server's default may be unbounded. A duration cap takes
// a duration such as 30m, in ms, s, m or h; a number cap takes a plain JSON
// number, with no fraction when the schema says integer. Either must be more
// than zero and at most the cap.
func (s *Schema) CheckBounds(a *Args, bounds map[string]config.Bound) error {
	for _, arg := range slices.Sorted(maps.Keys(bounds)) {
		if err := s.checkBound(arg, a.values[arg], bounds[arg].Max); err != nil {
			return err
		}
	}
	return nil
}

func (s *Schema) checkBound(arg string, v any, limit string) error {
	if d, err := time.ParseDuration(limit); err == nil {
		text, ok := v.(string)
		got, err := time.ParseDuration(text)
		if !ok || !boundedDuration.MatchString(text) || err != nil || got > d {
			return fmt.Errorf("%w: %s must be a duration such as 30m, in ms, s, m or h, more than zero and at most %s", ErrArguments, arg, limit)
		}
		return nil
	}
	maxValue, err := strconv.ParseFloat(limit, 64)
	if err != nil {
		return fmt.Errorf("%w: %s has a cap the gateway cannot read", ErrArguments, arg)
	}
	n, ok := v.(json.Number)
	integer := slices.Contains(s.types[arg], "integer")
	if !ok || !boundedNumber.MatchString(string(n)) || integer && strings.Contains(string(n), ".") {
		return fmt.Errorf("%w: %s must be a plain number, more than zero and at most %s", ErrArguments, arg, limit)
	}
	if got, err := n.Float64(); err != nil || got <= 0 || got > maxValue {
		return fmt.Errorf("%w: %s must be a plain number, more than zero and at most %s", ErrArguments, arg, limit)
	}
	return nil
}

// Coerce turns arguments rendered as text into the types the schema declares
// for them: a number or an integer when the text is a plain number, a boolean
// when it is true or false, and the text otherwise.
func (s *Schema) Coerce(args map[string]string) map[string]any {
	out := make(map[string]any, len(args))
	for name, text := range args {
		out[name] = text
		for _, typ := range s.types[name] {
			switch {
			case (typ == "number" || typ == "integer") && boundedNumber.MatchString(text):
				out[name] = json.Number(text)
			case typ == "boolean" && (text == "true" || text == "false"):
				out[name] = text == "true"
			}
		}
	}
	return out
}
