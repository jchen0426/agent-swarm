package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// ToolSchemaHash returns the hash that a registration pins for a tool, and
// that the MCP gateway compares with what the server lists (spec Section
// 3.3): the SHA-256 of a canonical JSON object holding the tool's name,
// description, input schema and output schema. Keys are sorted and numbers
// keep their literal text, so equal schemas give equal hashes. A nil output
// schema is JSON null.
func ToolSchemaHash(name, description string, inputSchema, outputSchema []byte) (string, error) {
	in, err := canonicalJSON(inputSchema)
	if err != nil {
		return "", fmt.Errorf("tool %s: input schema: %w", name, err)
	}
	out, err := canonicalJSON(outputSchema)
	if err != nil {
		return "", fmt.Errorf("tool %s: output schema: %w", name, err)
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(map[string]any{
		"name": name, "description": description, "inputSchema": in, "outputSchema": out,
	}); err != nil {
		return "", fmt.Errorf("tool %s: %w", name, err)
	}
	return hashBytes(bytes.TrimSuffix(b.Bytes(), []byte("\n"))), nil
}

// canonicalJSON decodes one JSON document, keeping numbers as written, for
// re-encoding with sorted keys. Empty input is null.
func canonicalJSON(raw []byte) (any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("more than one JSON value")
	}
	return v, nil
}
