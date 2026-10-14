package tools

import (
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// errCycle is the error of a schema that validation would follow forever.
var errCycle = errors.New("tools: input schema: its references form a cycle that never steps into the value")

var (
	// inPlace names the keywords that apply their schemas to the same value
	// as the schema that holds them.
	inPlace = []string{"allOf", "anyOf", "oneOf", "not", "if", "then", "else", "dependentSchemas", "dependencies"}
	// everywhere names every keyword that holds schemas: those of inPlace,
	// those that apply their schemas to parts of the value, and those that
	// keep schemas to be referred to.
	everywhere = slices.Concat(inPlace, []string{
		"properties", "patternProperties", "additionalProperties", "propertyNames", "items", "prefixItems",
		"additionalItems", "contains", "unevaluatedProperties", "unevaluatedItems", "contentSchema", "$defs", "definitions",
	})
	// named names the keywords that hold schemas by name.
	named = map[string]bool{
		"properties": true, "patternProperties": true, "$defs": true, "definitions": true, "dependentSchemas": true, "dependencies": true,
	}
)

// checkReferences refuses a schema that validation could follow forever: a
// cycle of $ref and of the keywords of inPlace, none of which steps into the
// value. It also refuses what it cannot follow as the validator does: a
// reference other than "#" or "#/" and a JSON pointer, $dynamicRef and
// $recursiveRef, and an $id below the root, which changes how references
// below it resolve.
func checkReferences(root any) error {
	c := &references{root: root, state: map[string]int{}}
	return c.all(root, "")
}

// references follows a schema's references. A schema is known by its JSON
// pointer.
type references struct {
	root  any
	state map[string]int
}

// States of a schema while its references are followed.
const (
	following = 1
	followed  = 2
)

// all follows the schema at path and every schema below it.
func (c *references) all(node any, path string) error {
	if err := c.follow(node, path); err != nil {
		return err
	}
	m, _ := node.(map[string]any)
	return each(m, path, everywhere, c.all)
}

// follow follows the schemas that apply to the same value as the schema at
// path, and reports a cycle among them.
func (c *references) follow(node any, path string) error {
	m, ok := node.(map[string]any)
	if !ok {
		return nil
	}
	switch c.state[path] {
	case following:
		return errCycle
	case followed:
		return nil
	}
	c.state[path] = following
	if _, ok := m["$id"]; ok && path != "" {
		return refused("an $id below the root")
	}
	for _, k := range []string{"$dynamicRef", "$recursiveRef"} {
		if _, ok := m[k]; ok {
			return refused(k)
		}
	}
	if ref, ok := m["$ref"]; ok {
		s, _ := ref.(string)
		target, at, err := c.resolve(s)
		if err != nil {
			return err
		}
		if err := c.follow(target, at); err != nil {
			return err
		}
	}
	if err := each(m, path, inPlace, c.follow); err != nil {
		return err
	}
	c.state[path] = followed
	return nil
}

// resolve returns the schema a reference names, and its JSON pointer.
func (c *references) resolve(ref string) (node any, path string, err error) {
	if ref == "#" {
		return c.root, "", nil
	}
	pointer, err := url.PathUnescape(strings.TrimPrefix(ref, "#"))
	if err != nil || !strings.HasPrefix(ref, "#/") {
		return nil, "", refused(fmt.Sprintf("the reference %q", ref))
	}
	node = c.root
	var at strings.Builder
	for _, token := range strings.Split(pointer[1:], "/") {
		token = strings.NewReplacer("~1", "/", "~0", "~").Replace(token)
		var ok bool
		switch t := node.(type) {
		case map[string]any:
			node, ok = t[token]
		case []any:
			i, err := strconv.Atoi(token)
			if ok = err == nil && i >= 0 && i < len(t); ok {
				node = t[i]
			}
		}
		if !ok {
			return nil, "", refused(fmt.Sprintf("the reference %q", ref))
		}
		at.WriteString("/" + escapeToken(token))
	}
	return node, at.String(), nil
}

// each calls fn on every schema m holds under keywords, with its JSON
// pointer, in a fixed order.
func each(m map[string]any, path string, keywords []string, fn func(any, string) error) error {
	for _, k := range keywords {
		at := path + "/" + escapeToken(k)
		switch t := m[k].(type) {
		case map[string]any:
			if !named[k] {
				if err := fn(t, at); err != nil {
					return err
				}
				continue
			}
			for _, name := range slices.Sorted(maps.Keys(t)) {
				if err := fn(t[name], at+"/"+escapeToken(name)); err != nil {
					return err
				}
			}
		case []any:
			for i, e := range t {
				if err := fn(e, at+"/"+strconv.Itoa(i)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// escapeToken writes a key as a JSON pointer token.
func escapeToken(s string) string { return strings.NewReplacer("~", "~0", "/", "~1").Replace(s) }

func refused(what string) error {
	return fmt.Errorf("tools: input schema: %s cannot be followed here", what)
}
