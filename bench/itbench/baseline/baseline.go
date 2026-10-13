// Package baseline is the mechanism check's deterministic baseline: the most
// recent change before the alert, mapped to a key. It is the benchmark's
// version of the example profile's "last release or flag change on the
// service".
package baseline

import (
	"slices"
	"sort"
	"strings"

	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
	"github.com/jchen0426/agent-swarm/bench/itbench/score"
)

const restartedAt = `annotations["kubectl.kubernetes.io/restartedAt"]`

// Evaluate returns the baseline's answer in two scopes: the latest change to
// a service the trigger's alerts name, and the latest change in the
// namespace. Either is nil when no change is in its scope.
func Evaluate(d *dataset.Dataset, flagStore string) (service, namespace *score.Key) {
	alerting := AlertingServices(&d.Meta)
	if c := latest(d.Changes, func(c *dataset.Change) bool { return slices.Contains(alerting, c.Service) }); c != nil {
		k := KeyOf(c, flagStore)
		service = &k
	}
	if c := latest(d.Changes, func(*dataset.Change) bool { return true }); c != nil {
		k := KeyOf(c, flagStore)
		namespace = &k
	}
	return service, namespace
}

// latest returns the latest change that keep accepts, or nil. A restart is
// not a change: it sets nothing but the restart annotation.
func latest(changes []dataset.Change, keep func(*dataset.Change) bool) *dataset.Change {
	var best *dataset.Change
	for i := range changes {
		c := &changes[i]
		if isRestart(c) || !keep(c) {
			continue
		}
		if best == nil || !c.Time.Before(best.Time) {
			best = c
		}
	}
	return best
}

func isRestart(c *dataset.Change) bool {
	if len(c.Fields) == 0 {
		return false
	}
	for _, f := range c.Fields {
		if !strings.HasSuffix(f.Path, restartedAt) {
			return false
		}
	}
	return true
}

// KeyOf maps a change to a key. A change to the flag store is a flag change, a
// change to a container image is a release, and any other change is a
// configuration change. A change with no service names the service "other".
func KeyOf(c *dataset.Change, flagStore string) score.Key {
	service := c.Service
	if service == "" {
		service = score.Other
	}
	mode := "config-change"
	switch {
	case c.Kind == "ConfigMap" && c.Name == flagStore:
		mode = "flag-change"
	case slices.ContainsFunc(c.Fields, func(f dataset.Field) bool { return strings.HasSuffix(f.Path, ".image") }):
		mode = "bad-release"
	}
	return score.Key{Service: service, FailureMode: mode}
}

// AlertingServices are the services the trigger's alerts name, sorted.
func AlertingServices(m *dataset.Meta) []string {
	var out []string
	for i := range m.Trigger {
		if s := m.Trigger[i].Service; s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
