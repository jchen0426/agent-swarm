// Package dataset is the prepared form of one benchmark scenario: what the
// read-only tools serve, cut at the alert time and stripped of the harness.
package dataset

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"time"
)

// Meta describes the scenario as the responders would see it. TracesUntil
// is the last span in the window: in several scenarios the benchmark's
// traces end before the fault.
type Meta struct {
	Namespace   string    `json:"namespace"`
	AlertTime   time.Time `json:"alert_time"`
	WindowStart time.Time `json:"window_start"`
	TracesUntil time.Time `json:"traces_until,omitzero"`
	Trigger     []Alert   `json:"trigger"`
}

// Alert is one alert in one poll of the alerting system.
type Alert struct {
	Polled      time.Time `json:"polled,omitzero"`
	Name        string    `json:"name"`
	Service     string    `json:"service,omitempty"`
	Pod         string    `json:"pod,omitempty"`
	Namespace   string    `json:"namespace,omitempty"`
	Severity    string    `json:"severity,omitempty"`
	State       string    `json:"state"`
	ActiveAt    time.Time `json:"active_at"`
	Description string    `json:"description,omitempty"`
}

// Change is one change to an object's desired state.
type Change struct {
	Time    time.Time `json:"time"`
	Kind    string    `json:"kind"`
	Name    string    `json:"name"`
	Service string    `json:"service,omitempty"`
	Action  string    `json:"action"`
	Fields  []Field   `json:"fields,omitempty"`
}

// Field is one changed value. A nil From means added, a nil To removed.
type Field struct {
	Path string  `json:"path"`
	From *string `json:"from,omitempty"`
	To   *string `json:"to,omitempty"`
}

// Object is the last snapshot of an object at or before the alert.
type Object struct {
	Kind     string          `json:"kind"`
	Name     string          `json:"name"`
	Service  string          `json:"service,omitempty"`
	Observed time.Time       `json:"observed"`
	Body     json.RawMessage `json:"body"`
}

// Event is one Kubernetes event: its last occurrence, and its first.
type Event struct {
	Time       time.Time `json:"time"`
	First      time.Time `json:"first,omitzero"`
	Type       string    `json:"type"`
	Reason     string    `json:"reason"`
	ObjectKind string    `json:"object_kind"`
	ObjectName string    `json:"object_name"`
	Message    string    `json:"message"`
	Count      int       `json:"count,omitempty"`
	Source     string    `json:"source,omitempty"`
}

// Log is one log record.
type Log struct {
	Time     time.Time `json:"time"`
	Service  string    `json:"service"`
	Pod      string    `json:"pod,omitempty"`
	Severity string    `json:"severity,omitempty"`
	Body     string    `json:"body"`
	TraceID  string    `json:"trace_id,omitempty"`
}

// Span is one span that ended in error.
type Span struct {
	Time       time.Time         `json:"time"`
	TraceID    string            `json:"trace_id"`
	SpanID     string            `json:"span_id"`
	ParentID   string            `json:"parent_id,omitempty"`
	Service    string            `json:"service"`
	Name       string            `json:"name"`
	Kind       string            `json:"kind"`
	DurationMS float64           `json:"duration_ms"`
	Status     string            `json:"status"`
	Message    string            `json:"message,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

// Edge counts the calls from one service to another, from all spans.
type Edge struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Calls  int    `json:"calls"`
	Errors int    `json:"errors"`
}

// ServicePoint is one minute of one operation's request metrics.
type ServicePoint struct {
	Time      time.Time `json:"time"`
	Service   string    `json:"service"`
	Operation string    `json:"operation"`
	Kind      string    `json:"kind"`
	Requests  float64   `json:"requests"`
	Errors    float64   `json:"errors"`
	P95MS     float64   `json:"p95_ms,omitempty"`
}

// PodPoint is one sample of one pod's resource use.
type PodPoint struct {
	Time        time.Time `json:"time"`
	Pod         string    `json:"pod"`
	Service     string    `json:"service,omitempty"`
	CPUCores    float64   `json:"cpu_cores"`
	MemoryBytes float64   `json:"memory_bytes"`
	CPULimit    float64   `json:"cpu_limit_cores,omitempty"`
	MemoryLimit float64   `json:"memory_limit_bytes,omitempty"`
}

// Dataset is one prepared scenario. Digest, set by Load, is the SHA-256 of
// the files it read.
type Dataset struct {
	Digest   string
	Meta     Meta
	Alerts   []Alert
	Changes  []Change
	Objects  []Object
	Events   []Event
	Logs     []Log
	Spans    []Span
	Edges    []Edge
	Services []ServicePoint
	Pods     []PodPoint
}

// Save writes the dataset into dir, one file per stream.
func (d *Dataset) Save(dir string) (err error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	meta, err := json.MarshalIndent(d.Meta, "", "  ")
	if err != nil {
		return err
	}
	return errors.Join(
		root.WriteFile("meta.json", meta, 0o600),
		writeLines(root, "alerts.jsonl", d.Alerts),
		writeLines(root, "changes.jsonl", d.Changes),
		writeLines(root, "objects.jsonl", d.Objects),
		writeLines(root, "events.jsonl", d.Events),
		writeLines(root, "logs.jsonl", d.Logs),
		writeLines(root, "spans.jsonl", d.Spans),
		writeLines(root, "edges.jsonl", d.Edges),
		writeLines(root, "service_metrics.jsonl", d.Services),
		writeLines(root, "pod_metrics.jsonl", d.Pods),
	)
}

// Load reads a dataset that Save wrote.
func Load(dir string) (_ *Dataset, err error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	meta, err := root.ReadFile("meta.json")
	if err != nil {
		return nil, err
	}
	d := &Dataset{}
	if err := json.Unmarshal(meta, &d.Meta); err != nil {
		return nil, fmt.Errorf("meta.json: %w", err)
	}
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "meta.json %d\n", len(meta))
	_, _ = h.Write(meta)
	err = errors.Join(
		readLines(root, h, "alerts.jsonl", &d.Alerts),
		readLines(root, h, "changes.jsonl", &d.Changes),
		readLines(root, h, "objects.jsonl", &d.Objects),
		readLines(root, h, "events.jsonl", &d.Events),
		readLines(root, h, "logs.jsonl", &d.Logs),
		readLines(root, h, "spans.jsonl", &d.Spans),
		readLines(root, h, "edges.jsonl", &d.Edges),
		readLines(root, h, "service_metrics.jsonl", &d.Services),
		readLines(root, h, "pod_metrics.jsonl", &d.Pods),
	)
	d.Digest = hex.EncodeToString(h.Sum(nil))
	return d, err
}

func writeLines[T any](root *os.Root, name string, items []T) (err error) {
	f, err := root.Create(name)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	for i := range items {
		if err := enc.Encode(&items[i]); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return w.Flush()
}

// readLines decodes one stream's file, and adds its name and bytes to h.
func readLines[T any](root *os.Root, h hash.Hash, name string, out *[]T) (err error) {
	f, err := root.Open(name)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	_, _ = fmt.Fprintf(h, "\n%s\n", name)
	dec := json.NewDecoder(bufio.NewReader(io.TeeReader(f, h)))
	for dec.More() {
		var item T
		if err := dec.Decode(&item); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		*out = append(*out, item)
	}
	return nil
}
