// Package checkset writes a scenario set as the platform's retrospective set
// (the agentswarm check command's input): each scenario's task as the agents
// see it, its keys as labels, and the namespace-wide baseline's answer. The
// platform's types are not imported; the JSON is the contract.
package checkset

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/jchen0426/agent-swarm/bench/itbench/baseline"
	"github.com/jchen0426/agent-swarm/bench/itbench/dataset"
	"github.com/jchen0426/agent-swarm/bench/itbench/score"
	"github.com/jchen0426/agent-swarm/bench/itbench/server"
)

// Mode is a failure mode and what it means, as the agents read it.
type Mode struct {
	Name    string `yaml:"name" json:"name"`
	Meaning string `yaml:"meaning" json:"meaning"`
}

// Task is what the agents learn of a scenario.
type Task struct {
	Servers  []string `json:"servers"`
	Alert    string   `json:"alert"`
	Services []string `json:"services"`
}

// Case is one scenario of the retrospective set.
type Case struct {
	ID       string      `json:"id"`
	Task     Task        `json:"task"`
	Change   bool        `json:"change"`
	Labels   []score.Key `json:"labels"`
	Baseline *score.Key  `json:"baseline"`
}

// Set is the retrospective set.
type Set struct {
	FailureModes []Mode `json:"failureModes"`
	Cases        []Case `json:"cases"`
}

// LoadModes reads the failure modes' meanings: one for each of the set's
// failure modes except other, which every key field accepts.
func LoadModes(path string, set *score.Set) ([]Mode, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	var modes []Mode
	if err := yaml.Unmarshal(raw, &modes); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	seen := map[string]bool{}
	for _, m := range modes {
		switch {
		case m.Name == score.Other || !slices.Contains(set.FailureModes, m.Name):
			return nil, fmt.Errorf("%s: %q is not one of the set's failure modes", path, m.Name)
		case seen[m.Name]:
			return nil, fmt.Errorf("%s: %s appears twice", path, m.Name)
		case strings.TrimSpace(m.Meaning) == "":
			return nil, fmt.Errorf("%s: %s has no meaning", path, m.Name)
		}
		seen[m.Name] = true
	}
	for _, name := range set.FailureModes {
		if name != score.Other && !seen[name] {
			return nil, fmt.Errorf("%s: no meaning for %s", path, name)
		}
	}
	return modes, nil
}

// Build turns the scenarios into cases, reading each one's dataset from
// datasets/<id>. Each case's server is serverURL/<id>/mcp, as itbench serve
// --datasets serves it.
func Build(set *score.Set, scenarios []score.Scenario, datasets string, modes []Mode, serverURL string) (*Set, error) {
	out := &Set{FailureModes: modes}
	for _, sc := range scenarios {
		d, err := dataset.Load(filepath.Join(datasets, sc.ID))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", sc.ID, err)
		}
		services := server.NewTools(d).Services()
		_, namespace := baseline.Evaluate(d, set.FlagStore)
		for _, k := range append(slices.Clone(sc.Keys), keys(namespace)...) {
			if k.Service != score.Other && !slices.Contains(services, k.Service) {
				return nil, fmt.Errorf("%s: key %s names a service the data does not", sc.ID, k.String())
			}
		}
		out.Cases = append(out.Cases, Case{
			ID: sc.ID,
			Task: Task{
				Servers:  []string{strings.TrimRight(serverURL, "/") + "/" + sc.ID + "/mcp"},
				Alert:    Alert(&d.Meta),
				Services: services,
			},
			Change:   sc.Change,
			Labels:   sc.Keys,
			Baseline: namespace,
		})
	}
	if len(out.Cases) == 0 {
		return nil, errors.New("no scenarios")
	}
	return out, nil
}

func keys(k *score.Key) []score.Key {
	if k == nil {
		return nil
	}
	return []score.Key{*k}
}

// Alert renders the trigger's alerts as the agents read them.
func Alert(m *dataset.Meta) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Alerts firing at %s:", m.AlertTime.Format(time.RFC3339))
	for i := range m.Trigger {
		a := &m.Trigger[i]
		fmt.Fprintf(&b, "\n- %s", a.Name)
		if a.Severity != "" {
			fmt.Fprintf(&b, " (%s)", a.Severity)
		}
		if a.Service != "" {
			fmt.Fprintf(&b, " on service %s", a.Service)
		}
		if a.Pod != "" {
			fmt.Fprintf(&b, " on pod %s", a.Pod)
		}
		fmt.Fprintf(&b, ", active since %s", a.ActiveAt.UTC().Format(time.RFC3339))
		if a.Description != "" {
			fmt.Fprintf(&b, ": %s", a.Description)
		}
	}
	return b.String()
}
