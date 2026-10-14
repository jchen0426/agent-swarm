package config_test

import (
	"maps"
	"reflect"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/registry/config"
)

// everySetting is the example's work item type with every setting set, so
// that the copy test below reaches every field.
const everySetting = `kind: WorkItemType
metadata: {name: incident, owner: sre-leads@example.com}
spec:
  hypothesisKey:
    - {name: service, values: {tool: catalog.list_services, field: "$[*].name"}}
    - {name: failureMode, values: [bad-release, flag-change, config-change]}
  budget: {wallClock: 15m, tokens: 2000000, rounds: 6}
  round: {timeLimit: 150s}
  team: {maxAgents: 4}
  limits: {entriesPerRound: 5, openClaims: 2, leaseRounds: 2}
  convergence: {minContributors: 2, minSources: 2, refutationMargin: 2}
  milestones: [acknowledged, cause_identified, mitigation_started, recovered]
  autoClose: {after: 30m}
  measurement: {invisibleShare: 0.5}
  shadowRuns:
    - name: independent
      arms: [invisible]
      override:
        budget: {wallClock: 10m, tokens: 500000, rounds: 1}
        round: {timeLimit: 5m}
        agents: [release-investigator]
        runtime: thinloop
`

// A revision is shared by every work item that locked it, so nothing a caller
// does to what an accessor returns may reach it.
func TestChangingWhatAnAccessorReturnsLeavesTheRevisionAsBuilt(t *testing.T) {
	repo := with(exampleRepo(), map[string]*fstest.MapFile{"work-item-types/incident.yaml": file(everySetting)})
	repo = edit(t, repo, "trigger-mappings/alerts.yaml", `status: {value: "{status}"}`,
		`status: {value: "{status}", firing: [firing], resolved: [resolved]}`)
	rev, built := build(t, repo), build(t, repo)
	release := config.SkillRef{Skill: "release-diagnosis", Version: "1.2.0"}
	rollback := config.ToolRef{Server: "release-manager", Tool: "rollback_release"}
	responsible := config.ToolRef{Server: "catalog", Tool: "responsible_for"}
	accessors := map[string]func(*config.Revision) any{
		"Agents":                 func(r *config.Revision) any { return r.Agents() },
		"Agent":                  func(r *config.Revision) any { return r.Agent("release-investigator") },
		"Agent.Tools":            func(r *config.Revision) any { return r.Agent("release-investigator").Tools() },
		"Agent.Skills":           func(r *config.Revision) any { return r.Agent("release-investigator").Skills() },
		"Server":                 func(r *config.Revision) any { return r.Server("release-manager") },
		"Tool":                   func(r *config.Revision) any { return r.Tool(rollback) },
		"Skill":                  func(r *config.Revision) any { return r.Skill("release-diagnosis") },
		"SkillVersion":           func(r *config.Revision) any { return r.SkillVersion(release) },
		"SkillVersion.Files":     func(r *config.Revision) any { return r.SkillVersion(release).Files() },
		"WorkItemType":           func(r *config.Revision) any { return r.WorkItemType("incident") },
		"WorkItemType.Settings":  func(r *config.Revision) any { return r.WorkItemType("incident").Settings() },
		"TriggerMapping":         func(r *config.Revision) any { return r.TriggerMapping("alerting") },
		"RedactionRule":          func(r *config.Revision) any { return r.RedactionRule("default-secrets") },
		"RedactionRule.Patterns": func(r *config.Revision) any { return r.RedactionRule("default-secrets").Patterns() },
		"RedactionRule.Paths":    func(r *config.Revision) any { return r.RedactionRule("default-secrets").Paths() },
		"IdentitySource":         func(r *config.Revision) any { return r.IdentitySource(responsible) },
	}
	set := map[string]bool{}

	for _, name := range slices.Sorted(maps.Keys(accessors)) {
		get := accessors[name]
		got := get(rev)
		require.NotNil(t, got, name)
		scribble(reflect.ValueOf(got), set)
		assert.Equal(t, get(built), get(rev), "changing what %s returned changed the revision", name)
	}

	for _, field := range slices.Sorted(maps.Keys(set)) {
		assert.True(t, set[field], "the fixture never sets %s, so this test cannot tell whether it is copied", field)
	}
}

// scribble changes everything in v that a caller can change: it rewrites
// scalars, writes through pointers, and replaces the elements of slices and
// maps. It records in set, by type and field, whether each exported pointer,
// map or slice it meets holds anything.
func scribble(v reflect.Value, set map[string]bool) {
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			scribble(v.Elem(), set)
		}
	case reflect.Struct:
		for i := range v.NumField() {
			field := v.Type().Field(i)
			if !field.IsExported() {
				continue
			}
			f := v.Field(i)
			if k := f.Kind(); k == reflect.Pointer || k == reflect.Map || k == reflect.Slice {
				name := v.Type().Name() + "." + field.Name
				set[name] = set[name] || !f.IsNil()
			}
			scribble(f, set)
		}
	case reflect.Slice:
		for i := range v.Len() {
			scribble(v.Index(i), set)
			v.Index(i).SetZero()
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			e := reflect.New(v.Type().Elem()).Elem()
			e.Set(v.MapIndex(k))
			scribble(e, set)
			v.SetMapIndex(k, reflect.Zero(v.Type().Elem()))
		}
	default:
		scribbleScalar(v)
	}
}

// scribbleScalar changes a settable string, integer, float or bool.
func scribbleScalar(v reflect.Value) {
	if !v.CanSet() {
		return
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString(v.String() + "!")
	case reflect.Int, reflect.Int64:
		v.SetInt(v.Int() + 1)
	case reflect.Float64:
		v.SetFloat(v.Float() + 1)
	case reflect.Bool:
		v.SetBool(!v.Bool())
	default:
	}
}
