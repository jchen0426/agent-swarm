package prepare

import (
	"strings"
)

// harnessMark is in every label the benchmark puts on its own objects.
const harnessMark = "it-bench"

// renames replace the names the benchmark gave its faulty values with
// plausible ones. Each keeps the failure the same: an image that cannot be
// pulled, an image name that cannot be parsed, a node that does not exist and
// a command that is not in the image.
var renames = strings.NewReplacer(
	"it-bench/hello-bench-invalid", "open-telemetry/demo-product-catalog",
	"it-bench/hello-bench-arm64", "open-telemetry/demo-checkout",
	"invalid-node", "i-0e4b1c7d9a2f36b58",
	"invalid-command", "product-catalog-server",
)

// scrub applies the renames to one string.
func scrub(s string) string { return renames.Replace(s) }

// scrubTree applies the renames to every string in a decoded JSON value.
func scrubTree(v any) any {
	switch t := v.(type) {
	case string:
		return scrub(t)
	case map[string]any:
		for k, x := range t {
			t[k] = scrubTree(x)
		}
	case []any:
		for i, x := range t {
			t[i] = scrubTree(x)
		}
	}
	return v
}

// isHarness reports whether an object belongs to the benchmark rather than the
// system under test: chaos resources, and objects labeled as the benchmark's.
func isHarness(body map[string]any) bool {
	if api, _ := body["apiVersion"].(string); strings.Contains(api, "chaos-mesh.org") {
		return true
	}
	for _, v := range stringMap(metadata(body)["labels"]) {
		if strings.Contains(v, harnessMark) {
			return true
		}
	}
	return false
}

// stripObject removes the writers' identities and the benchmark's labels, and
// applies the renames.
func stripObject(body map[string]any) {
	md := metadata(body)
	delete(md, "managedFields")
	for _, field := range []string{"labels", "annotations"} {
		m, ok := md[field].(map[string]any)
		if !ok {
			continue
		}
		for k, v := range m {
			if s, _ := v.(string); strings.Contains(k, harnessMark) || strings.Contains(s, harnessMark) {
				delete(m, k)
			}
		}
	}
	scrubTree(body)
}

func metadata(body map[string]any) map[string]any {
	md, _ := body["metadata"].(map[string]any)
	if md == nil {
		return map[string]any{}
	}
	return md
}

func stringMap(v any) map[string]string {
	m, _ := v.(map[string]any)
	out := make(map[string]string, len(m))
	for k, x := range m {
		if s, ok := x.(string); ok {
			out[k] = s
		}
	}
	return out
}
