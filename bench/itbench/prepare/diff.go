package prepare

import (
	"encoding/json"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
)

// maxValue caps the runes of a value in a change, so a changed blob stays readable.
const maxValue = 300

// missing marks a key or list item that one side does not have.
type missing struct{}

var plainKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

// diffTrees lists the differences between two decoded JSON values, in path
// order. Lists whose items all have a unique name are matched by name, and
// strings that hold JSON objects or arrays are compared by content, so
// reordering or reformatting is not a change.
func diffTrees(path string, a, b any) []dataset.Field {
	a, b = parseJSONString(a), parseJSONString(b)
	if am, ok := a.(map[string]any); ok {
		if bm, ok := b.(map[string]any); ok {
			return diffMaps(path, am, bm, join)
		}
	}
	if al, ok := a.([]any); ok {
		if bl, ok := b.([]any); ok {
			if an, bn := byName(al), byName(bl); an != nil && bn != nil {
				return diffMaps(path, an, bn, item)
			}
			return diffLists(path, al, bl)
		}
	}
	if reflect.DeepEqual(a, b) {
		return nil
	}
	return []dataset.Field{{Path: path, From: render(a), To: render(b)}}
}

func diffMaps(path string, a, b map[string]any, key func(path, k string) string) []dataset.Field {
	keys := make([]string, 0, len(a)+len(b))
	for k := range a {
		keys = append(keys, k)
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var out []dataset.Field
	for _, k := range keys {
		out = append(out, diffTrees(key(path, k), get(a, k), get(b, k))...)
	}
	return out
}

func diffLists(path string, a, b []any) []dataset.Field {
	var out []dataset.Field
	for i := range max(len(a), len(b)) {
		var av, bv any = missing{}, missing{}
		if i < len(a) {
			av = a[i]
		}
		if i < len(b) {
			bv = b[i]
		}
		out = append(out, diffTrees(path+"["+strconv.Itoa(i)+"]", av, bv)...)
	}
	return out
}

// byName indexes a list by its items' names, or returns nil if any item has
// no name or a name repeats.
func byName(list []any) map[string]any {
	out := make(map[string]any, len(list))
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return nil
		}
		name, ok := m["name"].(string)
		if !ok {
			return nil
		}
		if _, dup := out[name]; dup {
			return nil
		}
		out[name] = item
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func get(m map[string]any, k string) any {
	if v, ok := m[k]; ok {
		return v
	}
	return missing{}
}

// item appends a named list item to a path.
func item(path, name string) string { return path + "[" + name + "]" }

// join appends a key to a path, in brackets unless it is a plain identifier.
func join(path, key string) string {
	if plainKey.MatchString(key) {
		if path == "" {
			return key
		}
		return path + "." + key
	}
	return path + "[" + strconv.Quote(key) + "]"
}

func parseJSONString(v any) any {
	s, ok := v.(string)
	if !ok {
		return v
	}
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "{") && !strings.HasPrefix(t, "[") {
		return v
	}
	var parsed any
	if json.Unmarshal([]byte(t), &parsed) != nil {
		return v
	}
	return parsed
}

func render(v any) *string {
	if _, ok := v.(missing); ok {
		return nil
	}
	s, ok := v.(string)
	if !ok {
		b, err := json.Marshal(v)
		if err != nil {
			b = []byte("?")
		}
		s = string(b)
	}
	if r := []rune(s); len(r) > maxValue {
		s = string(r[:maxValue]) + "…"
	}
	return &s
}
