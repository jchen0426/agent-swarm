package prepare

import (
	"encoding/json"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
	"github.com/jchen0426/agent-swarm/bench/itbench/raw"
)

// coreEvent is a core/v1 Event as the watch stream records it.
type coreEvent struct {
	APIVersion string `json:"apiVersion"`
	Metadata   struct {
		UID string `json:"uid"`
	} `json:"metadata"`
	InvolvedObject struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Name       string `json:"name"`
		Namespace  string `json:"namespace"`
	} `json:"involvedObject"`
	Reason         string     `json:"reason"`
	Message        string     `json:"message"`
	Type           string     `json:"type"`
	Count          int        `json:"count"`
	FirstTimestamp *time.Time `json:"firstTimestamp"`
	LastTimestamp  *time.Time `json:"lastTimestamp"`
	EventTime      *time.Time `json:"eventTime"`
	Source         struct {
		Component string `json:"component"`
	} `json:"source"`
}

func (e *coreEvent) last() time.Time {
	for _, t := range []*time.Time{e.LastTimestamp, e.EventTime, e.FirstTimestamp} {
		if t != nil && !t.IsZero() {
			return t.UTC()
		}
	}
	return time.Time{}
}

// fromCluster reports whether the event is about the namespace's own objects:
// a core/v1 event, not the stream's events.k8s.io copy, and not about chaos
// resources.
func (e *coreEvent) fromCluster(namespace string) bool {
	return e.APIVersion == "v1" && e.InvolvedObject.Namespace == namespace &&
		!strings.Contains(e.InvolvedObject.APIVersion, "chaos-mesh.org") && !strings.Contains(e.Source.Component, "chaos")
}

func (e *coreEvent) convert() dataset.Event {
	var first time.Time
	if e.FirstTimestamp != nil {
		first = e.FirstTimestamp.UTC()
	}
	return dataset.Event{
		Time: e.last(), First: first, Type: e.Type, Reason: e.Reason,
		ObjectKind: e.InvolvedObject.Kind, ObjectName: e.InvolvedObject.Name,
		Message: scrub(e.Message), Count: e.Count, Source: e.Source.Component,
	}
}

// seenEvent is an event's state as the stream recorded it at a time.
type seenEvent struct {
	at    time.Time
	event coreEvent
}

// readEvents reads the namespace's events, each in its last state recorded
// at or before to, keeping those last seen in [from, to].
func readEvents(fsys fs.FS, name, namespace string, from, to time.Time) ([]dataset.Event, error) {
	latest := map[string]*seenEvent{}
	err := readRows(fsys, name, func(r raw.Row) error {
		at, err := raw.ParseTime(r.Get("Timestamp"))
		if err != nil || at.After(to) {
			return err
		}
		var row struct {
			Object coreEvent `json:"object"`
		}
		if err := json.Unmarshal([]byte(r.Get("Body")), &row); err != nil {
			return err
		}
		if !row.Object.fromCluster(namespace) {
			return nil
		}
		if prev := latest[row.Object.Metadata.UID]; prev == nil || at.After(prev.at) {
			latest[row.Object.Metadata.UID] = &seenEvent{at, row.Object}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var out []dataset.Event
	for _, s := range latest {
		if last := s.event.last(); !last.Before(from) && !last.After(to) {
			out = append(out, s.event.convert())
		}
	}
	sort.Slice(out, func(i, j int) bool { return eventBefore(&out[i], &out[j]) })
	return out, nil
}

// eventBefore orders events by time, then by every other field, so that the
// same download always gives the same order.
func eventBefore(a, b *dataset.Event) bool {
	switch {
	case !a.Time.Equal(b.Time):
		return a.Time.Before(b.Time)
	case a.ObjectKind != b.ObjectKind:
		return a.ObjectKind < b.ObjectKind
	case a.ObjectName != b.ObjectName:
		return a.ObjectName < b.ObjectName
	case a.Reason != b.Reason:
		return a.Reason < b.Reason
	case a.Message != b.Message:
		return a.Message < b.Message
	case a.Type != b.Type:
		return a.Type < b.Type
	case !a.First.Equal(b.First):
		return a.First.Before(b.First)
	case a.Count != b.Count:
		return a.Count < b.Count
	}
	return a.Source < b.Source
}
