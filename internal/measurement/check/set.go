// Package check runs the retrospective check (spec Sections 14.1 and 14.4).
// The planned agents work each case of a set on their own, without the
// coordinator, through read-only MCP servers. Their hypotheses are aggregated
// by key as in the independent run, and the conclusion is scored against the
// case's labels and its baseline.
package check

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/jchen0426/agent-swarm/internal/work/key"
)

// The key fields of a check: the service where the cause lies, and the kind
// of failure (spec Section 3.5).
const (
	ServiceField     = "service"
	FailureModeField = "failureMode"
)

var (
	caseID    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	agentName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
)

// FailureMode is one value of the closed list, with what it means.
type FailureMode struct {
	Name    string `json:"name"`
	Meaning string `json:"meaning"`
}

// Task is all that the agents learn of a case.
type Task struct {
	// Servers are MCP endpoints whose reads end at the alert time.
	Servers  []string `json:"servers"`
	Alert    string   `json:"alert"`
	Services []string `json:"services"`
}

// Case is one past incident: the agents' task, and what scores their
// conclusion. Only the task ever reaches an agent.
type Case struct {
	ID   string `json:"id"`
	Task Task   `json:"task"`
	// Change is true when the cause was a change, the subset the "most recent
	// change" baseline is meant for.
	Change bool `json:"change"`
	// Labels are the keys that count as a hit: the ground truth, framed one or
	// more ways (spec Section 13.4).
	Labels []key.Key `json:"labels"`
	// Baseline is the baseline's answer, or nil when it has none.
	Baseline key.Key `json:"baseline"`
}

// Set is a retrospective set: the closed list of failure modes and the cases.
type Set struct {
	FailureModes []FailureMode `json:"failureModes"`
	Cases        []Case        `json:"cases"`
}

// FailureModeNames lists the failure modes' names in the set's order.
func (s *Set) FailureModeNames() []string {
	names := make([]string, 0, len(s.FailureModes))
	for _, m := range s.FailureModes {
		names = append(names, m.Name)
	}
	return names
}

// Schema is the key schema of a case: its services and the set's failure modes.
func (s *Set) Schema(c *Case) key.Schema {
	return key.Schema{Fields: []key.Field{
		{Name: ServiceField, Values: c.Task.Services},
		{Name: FailureModeField, Values: s.FailureModeNames()},
	}}
}

// LoadSet reads a set from a JSON file and checks that every case can be run
// and scored.
func LoadSet(path string) (*Set, error) {
	var set Set
	if err := decodeFile(path, &set); err != nil {
		return nil, err
	}
	if err := set.check(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &set, nil
}

func (s *Set) check() error {
	modes := map[string]bool{}
	for _, m := range s.FailureModes {
		switch {
		case m.Name == key.Other:
			return fmt.Errorf("%q is not a failure mode: every key field accepts it", key.Other)
		case strings.TrimSpace(m.Name) == "":
			return errors.New("a failure mode has no name")
		case modes[m.Name]:
			return fmt.Errorf("failure mode %s appears twice", m.Name)
		case strings.TrimSpace(m.Meaning) == "":
			return fmt.Errorf("failure mode %s has no meaning", m.Name)
		}
		modes[m.Name] = true
	}
	if len(s.Cases) == 0 {
		return errors.New("the set has no cases")
	}
	seen := map[string]bool{}
	for i := range s.Cases {
		c := &s.Cases[i]
		if seen[c.ID] {
			return fmt.Errorf("case %s appears twice", c.ID)
		}
		seen[c.ID] = true
		if err := s.checkCase(c); err != nil {
			return err
		}
	}
	return nil
}

func (s *Set) checkCase(c *Case) error {
	switch {
	case !caseID.MatchString(c.ID):
		return fmt.Errorf("case id %q: use letters, digits, dots, dashes and underscores", c.ID)
	case len(c.Task.Servers) == 0:
		return fmt.Errorf("case %s has no server", c.ID)
	case strings.TrimSpace(c.Task.Alert) == "":
		return fmt.Errorf("case %s has no alert", c.ID)
	case len(c.Task.Services) == 0:
		return fmt.Errorf("case %s has no services", c.ID)
	case slices.Contains(c.Task.Services, key.Other):
		return fmt.Errorf("case %s: %q is not a service: every key field accepts it", c.ID, key.Other)
	case len(c.Labels) == 0:
		return fmt.Errorf("case %s has no labels", c.ID)
	}
	schema := s.Schema(c)
	for _, l := range c.Labels {
		if err := schema.Validate(l); err != nil {
			return fmt.Errorf("case %s: label %s: %w", c.ID, l, err)
		}
		if hasOther(l) {
			return fmt.Errorf("case %s: label %s: a label containing %q could never be a hit", c.ID, l, key.Other)
		}
	}
	if c.Baseline != nil {
		if err := schema.Validate(c.Baseline); err != nil {
			return fmt.Errorf("case %s: baseline %s: %w", c.ID, c.Baseline, err)
		}
	}
	return nil
}

func hasOther(k key.Key) bool {
	for _, v := range k {
		if v == key.Other {
			return true
		}
	}
	return false
}

// Agent is one planned agent: its name and its skill's text.
type Agent struct {
	Name  string
	Skill string
}

// agentFile is one agent in an agents file. Skill is a path relative to the
// file's directory.
type agentFile struct {
	Name  string `json:"name"`
	Skill string `json:"skill"`
}

// LoadAgents reads an agents file: a JSON list of names and skill files, each
// skill in the file's directory or below it.
func LoadAgents(path string) ([]Agent, error) {
	var list []agentFile
	if err := decodeFile(path, &list); err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("%s: no agents", path)
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	agents := make([]Agent, 0, len(list))
	seen := map[string]bool{}
	for _, a := range list {
		switch {
		case !agentName.MatchString(a.Name):
			return nil, fmt.Errorf("%s: agent name %q: use lower-case letters, digits and dashes", path, a.Name)
		case seen[a.Name]:
			return nil, fmt.Errorf("%s: agent %s appears twice", path, a.Name)
		}
		seen[a.Name] = true
		skill, err := root.ReadFile(a.Skill)
		if err != nil {
			return nil, fmt.Errorf("%s: skill of agent %s: %w", path, a.Name, err)
		}
		if strings.TrimSpace(string(skill)) == "" {
			return nil, fmt.Errorf("%s: the skill of agent %s is empty", path, a.Name)
		}
		agents = append(agents, Agent{Name: a.Name, Skill: string(skill)})
	}
	return agents, nil
}

// decodeFile decodes one JSON value from a file, refusing unknown fields and
// anything after the value.
func decodeFile(path string, v any) error {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := dec.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%s: data after the JSON value", path)
	}
	return nil
}
