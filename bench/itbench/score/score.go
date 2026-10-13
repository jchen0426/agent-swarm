// Package score holds the mechanism check's frozen answers and scores
// conclusions against them by key.
package score

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"go.yaml.in/yaml/v3"
)

// Other is the value every key field accepts. It is never a hit: the spec
// leaves keys containing it to raters (Section 13.4).
const Other = "other"

// Key is a hypothesis key of the example profile: the service where the
// cause lies, and the kind of failure.
type Key struct {
	Service     string `yaml:"service" json:"service"`
	FailureMode string `yaml:"failureMode" json:"failureMode"`
}

// String renders a key, or "none" for no conclusion.
func (k *Key) String() string {
	if k == nil {
		return "none"
	}
	return k.Service + "/" + k.FailureMode
}

// Scenario is one benchmark scenario and the keys that count as a hit.
type Scenario struct {
	ID     string `yaml:"id"`
	Dir    string `yaml:"dir"`
	Change bool   `yaml:"change"`
	Keys   []Key  `yaml:"keys"`
	Why    string `yaml:"why"`
}

// Excluded is a scenario left out, with the reason.
type Excluded struct {
	ID  string `yaml:"id"`
	Why string `yaml:"why"`
}

// Set is the frozen scenario set: where the data comes from, the closed list
// of failure modes, the flag store the baseline recognizes, and each
// scenario's answer.
type Set struct {
	Benchmark    string     `yaml:"benchmark"`
	Source       string     `yaml:"source"`
	Revision     string     `yaml:"revision"`
	Prefix       string     `yaml:"prefix"`
	Namespace    string     `yaml:"namespace"`
	FlagStore    string     `yaml:"flagStore"`
	FailureModes []string   `yaml:"failureModes"`
	Scenarios    []Scenario `yaml:"scenarios"`
	Excluded     []Excluded `yaml:"excluded"`
}

// Load reads and checks a scenario set.
func Load(path string) (_ *Set, err error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	var set Set
	if err := dec.Decode(&set); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &set, set.check()
}

func (s *Set) check() error {
	seen := map[string]bool{}
	for _, sc := range s.Scenarios {
		if seen[sc.ID] {
			return fmt.Errorf("scenario %s appears twice", sc.ID)
		}
		seen[sc.ID] = true
		if len(sc.Keys) == 0 {
			return fmt.Errorf("scenario %s has no keys", sc.ID)
		}
		for _, k := range sc.Keys {
			if k.Service == "" || k.Service == Other || k.FailureMode == Other || !slices.Contains(s.FailureModes, k.FailureMode) {
				return fmt.Errorf("scenario %s: key %s is not a closed-list key", sc.ID, k.String())
			}
		}
	}
	return nil
}

// Hit reports whether a conclusion equals one of the scenario's keys. No
// conclusion is never a hit.
func (s *Scenario) Hit(k *Key) bool {
	return k != nil && slices.Contains(s.Keys, *k)
}

// Answers maps a scenario id to one method's conclusion. A scenario that is
// not in the map has no conclusion.
type Answers map[string]Key

func (a Answers) get(id string) *Key {
	if k, ok := a[id]; ok {
		return &k
	}
	return nil
}

// Row is one scenario's result.
type Row struct {
	ID          string
	Change      bool
	Answer      string
	Hit         bool
	Baseline    string
	BaselineHit bool
}

// Report compares a method's answers with the baseline's, overall and on the
// scenarios whose fault is a change.
type Report struct {
	Rows                           []Row
	Scenarios, ChangeScenarios     int
	Hits, BaselineHits             int
	ChangeHits, ChangeBaselineHits int
	OnlyAnswers, OnlyBaseline      int
}

// Compare scores answers and the baseline on every scenario of the set.
func Compare(set *Set, answers, baseline Answers) Report {
	var r Report
	for i := range set.Scenarios {
		sc := &set.Scenarios[i]
		a, b := answers.get(sc.ID), baseline.get(sc.ID)
		row := Row{ID: sc.ID, Change: sc.Change, Answer: a.String(), Hit: sc.Hit(a), Baseline: b.String(), BaselineHit: sc.Hit(b)}
		r.Rows = append(r.Rows, row)
		r.Scenarios++
		r.Hits += btoi(row.Hit)
		r.BaselineHits += btoi(row.BaselineHit)
		r.OnlyAnswers += btoi(row.Hit && !row.BaselineHit)
		r.OnlyBaseline += btoi(row.BaselineHit && !row.Hit)
		if sc.Change {
			r.ChangeScenarios++
			r.ChangeHits += btoi(row.Hit)
			r.ChangeBaselineHits += btoi(row.BaselineHit)
		}
	}
	return r
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}
