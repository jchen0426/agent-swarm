// Package server serves one prepared scenario through read-only MCP tools.
// Every read ends at the alert time, because the dataset does.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
)

const (
	defaultLimit = 50
	maxLimit     = 200
	maxBody      = 1000
)

// Tools answers the read tools' queries over one dataset.
type Tools struct {
	d        *dataset.Dataset
	services []string          // every service the data names, sorted
	kinds    map[string]string // object kinds by their lower case
	objects  map[string]bool   // names of objects and of the objects events are about
	pods     map[string]bool   // names of pods in objects or in pod metrics
}

// NewTools returns the tools over a dataset.
func NewTools(d *dataset.Dataset) *Tools {
	t := &Tools{d: d, kinds: map[string]string{}, objects: map[string]bool{}, pods: map[string]bool{}}
	names := map[string]bool{}
	for i := range d.Objects {
		o := &d.Objects[i]
		names[o.Service] = true
		t.kinds[strings.ToLower(o.Kind)] = o.Kind
		t.objects[o.Name] = true
		if o.Kind == "Pod" {
			t.pods[o.Name] = true
		}
	}
	for i := range d.Events {
		t.objects[d.Events[i].ObjectName] = true
	}
	for i := range d.Changes {
		names[d.Changes[i].Service] = true
	}
	for i := range d.Logs {
		names[d.Logs[i].Service] = true
	}
	for i := range d.Spans {
		names[d.Spans[i].Service] = true
	}
	for i := range d.Services {
		names[d.Services[i].Service] = true
	}
	for i := range d.Pods {
		names[d.Pods[i].Service] = true
		t.pods[d.Pods[i].Pod] = true
	}
	for _, e := range d.Edges {
		names[e.From], names[e.To] = true, true
	}
	delete(names, "")
	for name := range names {
		t.services = append(t.services, name)
	}
	sort.Strings(t.services)
	return t
}

// service checks a service name, if one is given, against the data.
func (t *Tools) service(name string) error {
	if name == "" || slices.Contains(t.services, name) {
		return nil
	}
	return fmt.Errorf("no service named %q; the services are %s", name, strings.Join(t.services, ", "))
}

// kind returns an object kind as the data spells it, matching any case.
func (t *Tools) kind(kind string) (string, error) {
	if k, ok := t.kinds[strings.ToLower(kind)]; ok {
		return k, nil
	}
	known := make([]string, 0, len(t.kinds))
	for _, k := range t.kinds {
		known = append(known, k)
	}
	sort.Strings(known)
	return "", fmt.Errorf("no objects of kind %q; the kinds are %s", kind, strings.Join(known, ", "))
}

// Window is a time range within the dataset's half hour before the alert.
type Window struct {
	Since string `json:"since,omitempty" jsonschema:"start of the range, RFC 3339; default and earliest: 30 minutes before the alert"`
	Until string `json:"until,omitempty" jsonschema:"end of the range, RFC 3339; default and latest: the alert time"`
}

func (t *Tools) window(w Window) (since, until time.Time, err error) {
	if since, err = t.clamp(w.Since, t.d.Meta.WindowStart); err != nil {
		return since, until, err
	}
	until, err = t.clamp(w.Until, t.d.Meta.AlertTime)
	return since, until, err
}

// clamp parses a time, defaulting to def, and keeps it within the window.
func (t *Tools) clamp(s string, def time.Time) (time.Time, error) {
	if s == "" {
		return def, nil
	}
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("time %q is not RFC 3339, for example 2025-12-15T17:50:00Z", s)
	}
	if v.Before(t.d.Meta.WindowStart) {
		return t.d.Meta.WindowStart, nil
	}
	if v.After(t.d.Meta.AlertTime) {
		return t.d.Meta.AlertTime, nil
	}
	return v.UTC(), nil
}

func within(at, since, until time.Time) bool { return !at.Before(since) && !at.After(until) }

func limitOf(n int) int {
	if n <= 0 {
		return defaultLimit
	}
	return min(n, maxLimit)
}

// AlertsInput selects a poll of the alerting system.
type AlertsInput struct {
	At string `json:"at,omitempty" jsonschema:"RFC 3339 time; the last poll at or before it is returned; default and latest: the alert time"`
}

// AlertsOutput is one poll of the alerting system.
type AlertsOutput struct {
	Polled time.Time       `json:"polled"`
	Alerts []dataset.Alert `json:"alerts"`
}

// ListAlerts returns the alerts firing at the last poll at or before a time.
func (t *Tools) ListAlerts(in *AlertsInput) (AlertsOutput, error) {
	at, err := t.clamp(in.At, t.d.Meta.AlertTime)
	if err != nil {
		return AlertsOutput{}, err
	}
	out := AlertsOutput{Alerts: []dataset.Alert{}}
	for i := range t.d.Alerts {
		if p := t.d.Alerts[i].Polled; !p.After(at) && p.After(out.Polled) {
			out.Polled = p
		}
	}
	for i := range t.d.Alerts {
		if a := t.d.Alerts[i]; a.Polled.Equal(out.Polled) {
			a.Polled = time.Time{}
			out.Alerts = append(out.Alerts, a)
		}
	}
	return out, nil
}

// ListServicesInput takes no arguments.
type ListServicesInput struct{}

// Call counts the calls between this service and another, from traces.
type Call struct {
	Service string `json:"service"`
	Calls   int    `json:"calls"`
	Errors  int    `json:"errors,omitempty"`
}

// Service is one service: whom it calls and who calls it.
type Service struct {
	Name     string `json:"name"`
	Calls    []Call `json:"calls,omitempty"`
	CalledBy []Call `json:"called_by,omitempty"`
}

// ListServicesOutput lists the namespace's services, and where the traces
// that the calls come from end.
type ListServicesOutput struct {
	Services    []Service `json:"services"`
	TracesUntil time.Time `json:"traces_until"`
}

// ListServices lists every service the data names, with the calls between
// them seen in traces.
func (t *Tools) ListServices(*ListServicesInput) (ListServicesOutput, error) {
	byName := map[string]*Service{}
	out := ListServicesOutput{Services: make([]Service, len(t.services)), TracesUntil: t.d.Meta.TracesUntil}
	for i, name := range t.services {
		out.Services[i] = Service{Name: name}
		byName[name] = &out.Services[i]
	}
	for _, e := range t.d.Edges {
		byName[e.From].Calls = append(byName[e.From].Calls, Call{Service: e.To, Calls: e.Calls, Errors: e.Errors})
		byName[e.To].CalledBy = append(byName[e.To].CalledBy, Call{Service: e.From, Calls: e.Calls, Errors: e.Errors})
	}
	return out, nil
}

// ChangesInput selects changes.
type ChangesInput struct {
	Service string `json:"service,omitempty" jsonschema:"only changes to this service's objects"`
	Window
}

// ChangesOutput lists changes, newest first.
type ChangesOutput struct {
	Changes []dataset.Change `json:"changes"`
}

// GetChanges lists changes to desired state, newest first.
func (t *Tools) GetChanges(in *ChangesInput) (ChangesOutput, error) {
	if err := t.service(in.Service); err != nil {
		return ChangesOutput{}, err
	}
	since, until, err := t.window(in.Window)
	if err != nil {
		return ChangesOutput{}, err
	}
	out := ChangesOutput{Changes: []dataset.Change{}}
	for _, c := range slices.Backward(t.d.Changes) {
		if within(c.Time, since, until) && (in.Service == "" || c.Service == in.Service) {
			out.Changes = append(out.Changes, c)
		}
	}
	return out, nil
}

// EventsInput selects Kubernetes events.
type EventsInput struct {
	Object string `json:"object,omitempty" jsonschema:"only events about the object with this name"`
	Type   string `json:"type,omitempty" jsonschema:"Warning or Normal"`
	Limit  int    `json:"limit,omitempty" jsonschema:"at most this many of the most recent events; default 50, at most 200"`
	Window
}

// EventsOutput lists events, newest first.
type EventsOutput struct {
	Events []dataset.Event `json:"events"`
	Total  int             `json:"total"`
}

// GetEvents lists Kubernetes events by their last occurrence, newest first.
func (t *Tools) GetEvents(in *EventsInput) (EventsOutput, error) {
	if in.Object != "" && !t.objects[in.Object] {
		return EventsOutput{}, fmt.Errorf("no object named %q; list_objects lists them", in.Object)
	}
	if in.Type != "" && !strings.EqualFold(in.Type, "Warning") && !strings.EqualFold(in.Type, "Normal") {
		return EventsOutput{}, fmt.Errorf("type %q is neither Warning nor Normal; use Warning or Normal", in.Type)
	}
	since, until, err := t.window(in.Window)
	if err != nil {
		return EventsOutput{}, err
	}
	out := EventsOutput{Events: []dataset.Event{}}
	for i := len(t.d.Events) - 1; i >= 0; i-- {
		e := &t.d.Events[i]
		if !within(e.Time, since, until) || (in.Object != "" && e.ObjectName != in.Object) || (in.Type != "" && !strings.EqualFold(e.Type, in.Type)) {
			continue
		}
		out.Total++
		if len(out.Events) < limitOf(in.Limit) {
			out.Events = append(out.Events, *e)
		}
	}
	return out, nil
}

// LogsInput selects a service's log records.
type LogsInput struct {
	Service  string `json:"service" jsonschema:"the service whose logs to search"`
	Contains string `json:"contains,omitempty" jsonschema:"only records containing this text, ignoring case"`
	Limit    int    `json:"limit,omitempty" jsonschema:"at most this many of the most recent matches; default 50, at most 200"`
	Window
}

// LogsOutput holds the most recent matching records, oldest first.
type LogsOutput struct {
	Logs      []dataset.Log `json:"logs"`
	Total     int           `json:"total"`
	Truncated bool          `json:"truncated"`
}

// QueryLogs searches a service's logs and returns the most recent matches.
func (t *Tools) QueryLogs(in *LogsInput) (LogsOutput, error) {
	if in.Service == "" {
		return LogsOutput{}, errors.New("name a service")
	}
	if err := t.service(in.Service); err != nil {
		return LogsOutput{}, err
	}
	since, until, err := t.window(in.Window)
	if err != nil {
		return LogsOutput{}, err
	}
	needle := strings.ToLower(in.Contains)
	var matches []dataset.Log
	for _, l := range t.d.Logs {
		if l.Service == in.Service && within(l.Time, since, until) && strings.Contains(strings.ToLower(l.Body), needle) {
			matches = append(matches, l)
		}
	}
	keep := limitOf(in.Limit)
	out := LogsOutput{Logs: []dataset.Log{}, Total: len(matches), Truncated: len(matches) > keep}
	for _, l := range matches[max(0, len(matches)-keep):] {
		if r := []rune(l.Body); len(r) > maxBody {
			l.Body = string(r[:maxBody]) + "…"
		}
		out.Logs = append(out.Logs, l)
	}
	return out, nil
}

// SpansInput selects spans that ended in error.
type SpansInput struct {
	Service string `json:"service,omitempty" jsonschema:"only spans of this service"`
	Limit   int    `json:"limit,omitempty" jsonschema:"at most this many of the most recent spans; default 50, at most 200"`
	Window
}

// SpansOutput lists error spans, newest first, and where the traces end.
type SpansOutput struct {
	Spans       []dataset.Span `json:"spans"`
	Total       int            `json:"total"`
	TracesUntil time.Time      `json:"traces_until"`
}

// GetErrorSpans lists the spans that ended in error, newest first. Traces
// may end before the alert; TracesUntil says where.
func (t *Tools) GetErrorSpans(in *SpansInput) (SpansOutput, error) {
	if err := t.service(in.Service); err != nil {
		return SpansOutput{}, err
	}
	since, until, err := t.window(in.Window)
	if err != nil {
		return SpansOutput{}, err
	}
	out := SpansOutput{Spans: []dataset.Span{}, TracesUntil: t.d.Meta.TracesUntil}
	for i := len(t.d.Spans) - 1; i >= 0; i-- {
		s := &t.d.Spans[i]
		if !within(s.Time, since, until) || (in.Service != "" && s.Service != in.Service) {
			continue
		}
		out.Total++
		if len(out.Spans) < limitOf(in.Limit) {
			out.Spans = append(out.Spans, *s)
		}
	}
	return out, nil
}

// ServiceMetricsInput selects a service's request metrics.
type ServiceMetricsInput struct {
	Service   string `json:"service" jsonschema:"the service"`
	Kind      string `json:"kind,omitempty" jsonschema:"server (requests it serves, the default) or client (calls it makes)"`
	Operation string `json:"operation,omitempty" jsonschema:"one operation (span name); default: all operations together"`
	Window
}

// MetricPoint is one minute of requests.
type MetricPoint struct {
	Time     time.Time `json:"time"`
	Requests float64   `json:"requests"`
	Errors   float64   `json:"errors"`
	MaxP95MS float64   `json:"max_p95_ms"`
}

// ServiceMetricsOutput is a service's requests per minute.
type ServiceMetricsOutput struct {
	Points []MetricPoint `json:"points"`
}

// GetServiceMetrics returns a service's requests, errors and p95 latency per
// minute. Over several operations it sums requests and errors and gives the
// highest operation's p95.
func (t *Tools) GetServiceMetrics(in *ServiceMetricsInput) (ServiceMetricsOutput, error) {
	if in.Service == "" {
		return ServiceMetricsOutput{}, errors.New("name a service")
	}
	if err := t.service(in.Service); err != nil {
		return ServiceMetricsOutput{}, err
	}
	since, until, err := t.window(in.Window)
	if err != nil {
		return ServiceMetricsOutput{}, err
	}
	kind := strings.ToLower(in.Kind)
	switch kind {
	case "":
		kind = "server"
	case "server", "client":
	default:
		return ServiceMetricsOutput{}, fmt.Errorf("kind %q is neither server nor client; use server or client", in.Kind)
	}
	byMinute := map[time.Time]*MetricPoint{}
	for _, p := range t.d.Services {
		if p.Service != in.Service || p.Kind != kind || (in.Operation != "" && p.Operation != in.Operation) || !within(p.Time, since, until) {
			continue
		}
		m := byMinute[p.Time]
		if m == nil {
			m = &MetricPoint{Time: p.Time}
			byMinute[p.Time] = m
		}
		m.Requests += p.Requests
		m.Errors += p.Errors
		m.MaxP95MS = max(m.MaxP95MS, p.P95MS)
	}
	out := ServiceMetricsOutput{Points: []MetricPoint{}}
	for _, m := range byMinute {
		out.Points = append(out.Points, *m)
	}
	sort.Slice(out.Points, func(i, j int) bool { return out.Points[i].Time.Before(out.Points[j].Time) })
	return out, nil
}

// PodMetricsInput selects pods' resource use.
type PodMetricsInput struct {
	Service string `json:"service,omitempty" jsonschema:"the service whose pods to show"`
	Pod     string `json:"pod,omitempty" jsonschema:"one pod"`
	Window
}

// PodMetricsOutput is pods' resource use per minute.
type PodMetricsOutput struct {
	Points []dataset.PodPoint `json:"points"`
}

// GetPodMetrics returns CPU and memory use and limits per pod and minute.
func (t *Tools) GetPodMetrics(in *PodMetricsInput) (PodMetricsOutput, error) {
	if in.Service == "" && in.Pod == "" {
		return PodMetricsOutput{}, errors.New("name a service or a pod")
	}
	if err := t.service(in.Service); err != nil {
		return PodMetricsOutput{}, err
	}
	if in.Pod != "" && !t.pods[in.Pod] {
		return PodMetricsOutput{}, fmt.Errorf("no pod named %q; list_objects with kind Pod lists the pods", in.Pod)
	}
	since, until, err := t.window(in.Window)
	if err != nil {
		return PodMetricsOutput{}, err
	}
	out := PodMetricsOutput{Points: []dataset.PodPoint{}}
	for _, p := range t.d.Pods {
		if (in.Service == "" || p.Service == in.Service) && (in.Pod == "" || p.Pod == in.Pod) && within(p.Time, since, until) {
			out.Points = append(out.Points, p)
		}
	}
	return out, nil
}

// ListObjectsInput selects a kind of object.
type ListObjectsInput struct {
	Kind string `json:"kind" jsonschema:"for example Pod, Deployment, Service, ConfigMap, ResourceQuota"`
}

// ObjectSummary is one object in a list.
type ObjectSummary struct {
	Name     string    `json:"name"`
	Service  string    `json:"service,omitempty"`
	Observed time.Time `json:"observed"`
	Status   string    `json:"status,omitempty"`
}

// ListObjectsOutput lists objects by name.
type ListObjectsOutput struct {
	Objects []ObjectSummary `json:"objects"`
}

// ListObjects lists the objects of one kind as last observed, with a status
// line for pods.
func (t *Tools) ListObjects(in *ListObjectsInput) (ListObjectsOutput, error) {
	kind, err := t.kind(in.Kind)
	if err != nil {
		return ListObjectsOutput{}, err
	}
	out := ListObjectsOutput{Objects: []ObjectSummary{}}
	for i := range t.d.Objects {
		o := &t.d.Objects[i]
		if o.Kind != kind {
			continue
		}
		s := ObjectSummary{Name: o.Name, Service: o.Service, Observed: o.Observed}
		if o.Kind == "Pod" {
			s.Status = podStatus(o.Body)
		}
		out.Objects = append(out.Objects, s)
	}
	sort.Slice(out.Objects, func(i, j int) bool { return out.Objects[i].Name < out.Objects[j].Name })
	return out, nil
}

// podStatus summarizes a pod: its phase, readiness, restarts and the reasons
// its containers wait or it is not scheduled.
func podStatus(body json.RawMessage) string {
	var pod struct {
		Status struct {
			Phase      string `json:"phase"`
			Conditions []struct {
				Type, Status, Reason string
			} `json:"conditions"`
			ContainerStatuses []struct {
				Ready        bool `json:"ready"`
				RestartCount int  `json:"restartCount"`
				State        struct {
					Waiting *struct {
						Reason string `json:"reason"`
					} `json:"waiting"`
				} `json:"state"`
			} `json:"containerStatuses"`
		} `json:"status"`
	}
	if json.Unmarshal(body, &pod) != nil {
		return ""
	}
	parts := []string{pod.Status.Phase}
	for _, c := range pod.Status.Conditions {
		if c.Type == "PodScheduled" && c.Status == "False" && c.Reason != "" {
			parts = append(parts, c.Reason)
		}
	}
	if n := len(pod.Status.ContainerStatuses); n > 0 {
		ready, restarts := 0, 0
		var waiting []string
		for _, c := range pod.Status.ContainerStatuses {
			if c.Ready {
				ready++
			}
			restarts += c.RestartCount
			if c.State.Waiting != nil && c.State.Waiting.Reason != "" {
				waiting = append(waiting, c.State.Waiting.Reason)
			}
		}
		parts = append(parts, fmt.Sprintf("ready %d/%d", ready, n), fmt.Sprintf("restarts %d", restarts))
		parts = append(parts, waiting...)
	}
	return strings.Join(parts, "; ")
}

// GetObjectInput names one object.
type GetObjectInput struct {
	Kind string `json:"kind" jsonschema:"the object's kind, for example Deployment"`
	Name string `json:"name" jsonschema:"the object's name"`
}

// GetObjectOutput is an object as last observed at or before the alert. The
// object is decoded: as raw bytes, its schema would be an array.
type GetObjectOutput struct {
	Observed time.Time      `json:"observed"`
	Object   map[string]any `json:"object"`
}

// GetObject returns one object as the last snapshot at or before the alert
// saw it. Snapshots are about five minutes apart.
func (t *Tools) GetObject(in *GetObjectInput) (GetObjectOutput, error) {
	kind, err := t.kind(in.Kind)
	if err != nil {
		return GetObjectOutput{}, err
	}
	for i := range t.d.Objects {
		if o := &t.d.Objects[i]; o.Kind == kind && o.Name == in.Name {
			out := GetObjectOutput{Observed: o.Observed}
			return out, json.Unmarshal(o.Body, &out.Object)
		}
	}
	return GetObjectOutput{}, fmt.Errorf("no %s named %s", kind, in.Name)
}
