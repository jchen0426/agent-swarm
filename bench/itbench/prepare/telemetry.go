package prepare

import (
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"time"

	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
	"github.com/jchen0426/agent-swarm/bench/itbench/raw"
)

// readTable calls fn for each row of a raw table whose timestamp is in
// [from, to] and whose resource is in the namespace, with that resource.
func readTable(fsys fs.FS, name, namespace string, from, to time.Time, fn func(raw.Row, time.Time, map[string]any) error) error {
	return readRows(fsys, name, func(r raw.Row) error {
		at, err := raw.ParseTime(r.Get("Timestamp"))
		if err != nil {
			return err
		}
		if at.Before(from) || at.After(to) {
			return nil
		}
		resource, err := pyMap(r.Get("ResourceAttributes"))
		if err != nil {
			return fmt.Errorf("column ResourceAttributes: %w", err)
		}
		if resource["k8s.namespace.name"] != namespace {
			return nil
		}
		return fn(r, at, resource)
	})
}

// readLogs reads the namespace's log records in [from, to].
func readLogs(fsys fs.FS, name, namespace string, from, to time.Time) ([]dataset.Log, error) {
	var out []dataset.Log
	err := readTable(fsys, name, namespace, from, to, func(r raw.Row, at time.Time, resource map[string]any) error {
		out = append(out, dataset.Log{
			Time: at, Service: r.Get("ServiceName"), Pod: str0(resource["k8s.pod.name"]),
			Severity: r.Get("SeverityText"), Body: scrub(r.Get("Body")), TraceID: r.Get("TraceId"),
		})
		return nil
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out, err
}

// readSpans reads the namespace's spans in [from, to]. It keeps the spans
// that ended in error, counts the calls between services from all spans (a
// span whose parent is in another service is a call from that service), and
// returns the time of the last span.
func readSpans(fsys fs.FS, name, namespace string, from, to time.Time) ([]dataset.Span, []dataset.Edge, time.Time, error) {
	type link struct {
		parent, service string
		failed          bool
	}
	serviceOfSpan := map[string]string{}
	var links []link
	var out []dataset.Span
	var last time.Time
	err := readTable(fsys, name, namespace, from, to, func(r raw.Row, at time.Time, _ map[string]any) error {
		if at.After(last) {
			last = at
		}
		service, status := r.Get("ServiceName"), r.Get("StatusCode")
		serviceOfSpan[r.Get("SpanId")] = service
		links = append(links, link{r.Get("ParentSpanId"), service, status == "Error"})
		if status != "Error" {
			return nil
		}
		span, err := errorSpan(r, at)
		if err != nil {
			return err
		}
		out = append(out, span)
		return nil
	})
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	type pair struct{ from, to string }
	counts := map[pair]*dataset.Edge{}
	for i := range links {
		l := &links[i]
		caller, ok := serviceOfSpan[l.parent]
		if !ok || caller == l.service {
			continue
		}
		p := pair{caller, l.service}
		if counts[p] == nil {
			counts[p] = &dataset.Edge{From: caller, To: l.service}
		}
		counts[p].Calls++
		if l.failed {
			counts[p].Errors++
		}
	}
	edges := make([]dataset.Edge, 0, len(counts))
	for _, e := range counts {
		edges = append(edges, *e)
	}
	sort.Slice(edges, func(i, j int) bool { return edges[i].From+"\x00"+edges[i].To < edges[j].From+"\x00"+edges[j].To })
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out, edges, last, nil
}

func errorSpan(r raw.Row, at time.Time) (dataset.Span, error) {
	attrs, err := pyMap(r.Get("SpanAttributes"))
	if err != nil {
		return dataset.Span{}, fmt.Errorf("column SpanAttributes: %w", err)
	}
	attributes := make(map[string]string, len(attrs)+1)
	for k, v := range attrs {
		attributes[k] = *render(scrubTree(v))
	}
	if events, err := raw.ParsePython(r.Get("Events.Attributes")); err == nil {
		list, _ := events.([]any)
		for _, e := range list {
			m, _ := e.(map[string]any)
			if msg, ok := m["exception.message"].(string); ok {
				attributes["exception.message"] = *render(scrub(msg))
				break
			}
		}
	}
	ns, err := strconv.ParseFloat(r.Get("Duration"), 64)
	if err != nil {
		return dataset.Span{}, fmt.Errorf("column Duration: %w", err)
	}
	return dataset.Span{
		Time: at, TraceID: r.Get("TraceId"), SpanID: r.Get("SpanId"), ParentID: r.Get("ParentSpanId"),
		Service: r.Get("ServiceName"), Name: r.Get("SpanName"), Kind: r.Get("SpanKind"), DurationMS: ns / 1e6,
		Status: r.Get("StatusCode"), Message: *render(scrub(r.Get("StatusMessage"))), Attributes: attributes,
	}, nil
}

// pyMap parses a Python dict literal; an empty string is an empty dict.
func pyMap(s string) (map[string]any, error) {
	if s == "" {
		return map[string]any{}, nil
	}
	v, err := raw.ParsePython(s)
	if err != nil {
		return nil, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("not a dict: %.40q", s)
	}
	return m, nil
}
