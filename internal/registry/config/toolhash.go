package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

// ToolDefinition is what a server lists for one tool, as a client may show it
// to a model or a person: its name, title, description, schemas and
// annotations. Each JSON part holds one JSON value; an empty part is absent.
type ToolDefinition struct {
	Name         string
	Title        string
	Description  string
	InputSchema  []byte
	OutputSchema []byte
	Annotations  []byte
}

// ToolSchemaHash returns the hash that a registration pins for a tool, and
// that the MCP gateway compares with what the server lists (spec Section
// 3.3): the SHA-256 of a canonical JSON object of the whole definition, so a
// changed title or annotation is a change too. Keys are sorted and numbers
// keep their literal text, so equal definitions give equal hashes. JSON that
// parsers could read differently is refused: a repeated key, more than one
// value, or text that is not UTF-8. An absent part is null.
func ToolSchemaHash(def *ToolDefinition) (string, error) {
	parts := map[string]any{"name": def.Name, "title": def.Title, "description": def.Description}
	for _, p := range []struct {
		key, label string
		raw        []byte
	}{
		{"inputSchema", "input schema", def.InputSchema},
		{"outputSchema", "output schema", def.OutputSchema},
		{"annotations", "annotations", def.Annotations},
	} {
		v, err := canonicalJSON(p.raw)
		if err != nil {
			return "", fmt.Errorf("tool %s: %s: %w", def.Name, p.label, err)
		}
		parts[p.key] = v
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(parts); err != nil {
		return "", fmt.Errorf("tool %s: %w", def.Name, err)
	}
	return hashBytes(bytes.TrimSuffix(b.Bytes(), []byte("\n"))), nil
}

// Limits on one JSON part of a tool definition. Real schemas are far smaller
// and shallower; the limits keep a hostile server from exhausting memory or
// the stack.
const (
	maxDefinitionPartBytes = 1 << 20
	maxDefinitionDepth     = 128
)

// canonicalJSON decodes exactly one JSON value, keeping numbers as written,
// for re-encoding with sorted keys. Empty input is null.
func canonicalJSON(raw []byte) (any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if len(raw) > maxDefinitionPartBytes {
		return nil, fmt.Errorf("is larger than %d bytes", maxDefinitionPartBytes)
	}
	if !utf8.Valid(raw) {
		return nil, errors.New("is not valid UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	v, err := strictValue(dec, 0)
	if errors.Is(err, io.EOF) {
		return nil, errors.New("holds no complete JSON value")
	}
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("holds more than one JSON value, or text after it")
	}
	return v, nil
}

// strictValue decodes one JSON value token by token, inside depth arrays and
// objects. It refuses an object that repeats a key, because parsers disagree
// on which of its values counts, and nesting deeper than maxDefinitionDepth.
func strictValue(dec *json.Decoder, depth int) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return tok, nil // a string, a json.Number, a bool or nil
	}
	if depth == maxDefinitionDepth {
		return nil, fmt.Errorf("nests deeper than %d levels", maxDefinitionDepth)
	}
	if delim == '[' {
		list := []any{}
		for dec.More() {
			v, err := strictValue(dec, depth+1)
			if err != nil {
				return nil, err
			}
			list = append(list, v)
		}
		_, err := dec.Token()
		return list, err
	}
	obj := map[string]any{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, errors.New("an object key is not a string")
		}
		if _, repeated := obj[key]; repeated {
			return nil, fmt.Errorf("repeats the key %q", key)
		}
		v, err := strictValue(dec, depth+1)
		if err != nil {
			return nil, err
		}
		obj[key] = v
	}
	_, err = dec.Token()
	return obj, err
}
