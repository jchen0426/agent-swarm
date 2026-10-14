package config_test

import (
	"errors"
	"maps"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/registry/config"
)

// exampleRepo is a small configuration repository that keeps every rule: the
// fictional checkout team of the example deployment profile, cut down.
func exampleRepo() fstest.MapFS {
	return fstest.MapFS{
		"README.md": file("Configuration of the checkout team.\n"),
		"redaction-rules/default-secrets.yaml": file(`kind: RedactionRule
metadata: {name: default-secrets, owner: security@example.com}
spec:
  patterns: ['(?i)bearer\s+[a-z0-9._~+/-]{8,}', 'AKIA[0-9A-Z]{16}']
  paths: ['$..env[*].value']
`),
		"servers/catalog.yaml": file(`kind: McpServer
metadata: {name: catalog, owner: platform-team}
spec:
  endpoint: https://catalog-mcp.internal
  image: "@IMG_A@"
  credentialRef: secret://mcp/catalog
  tools:
    list_services: {schemaHash: "@H1@", effect: read}
    responsible_for: {schemaHash: "@H2@", effect: read, audience: platform}
    responders_for: {schemaHash: "@H3@", effect: read, audience: platform}
`),
		"servers/release-manager.yaml": file(`kind: McpServer
metadata: {name: release-manager, owner: platform-team}
spec:
  endpoint: https://release-mcp.internal
  image: "@IMG_B@"
  tools:
    get_release:
      schemaHash: "@H4@"
      effect: read
      redact: [default-secrets]
      partialWhen: result.truncated == true
      bounds: {window: {max: 6h}}
    list_recent_changes: {schemaHash: "@H5@", effect: read}
    rollback_release:
      schemaHash: "@H6@"
      effect: write
      target: [service]
      scope: {service: hypothesis.service}
      candidates:
        service: {tool: catalog.list_services, field: "$[*].name"}
        change_id: {tool: list_recent_changes, args: {service: "{args.service}"}, field: "$[*].id"}
      summary: Roll back {args.service} by reverting change {args.change_id}
      facts:
        current_release: {tool: get_release, args: {service: "{args.service}"}, field: $.current}
`),
		"servers/metrics.yaml": file(`kind: McpServer
metadata: {name: metrics, owner: platform-team}
spec:
  endpoint: http://metrics-mcp.internal:8080/mcp
  image: "@IMG_C@"
  tools:
    query_range: {schemaHash: "@H7@", effect: read, bounds: {window: {max: 6h}, limit: {max: 1000}}}
    query_instant: {schemaHash: "@H8@", effect: read}
`),
		"identity-sources/catalog-responsible.yaml": file(`kind: IdentitySource
metadata: {name: catalog-responsible, owner: identity-team@example.com}
spec: {tool: catalog.responsible_for}
`),
		"identity-sources/catalog-responders.yaml": file(`kind: IdentitySource
metadata: {name: catalog-responders, owner: identity-team@example.com}
spec: {tool: catalog.responders_for}
`),
		"skills/release-diagnosis/skill.yaml": file(`kind: Skill
metadata: {name: release-diagnosis, owner: alice@example.com}
spec:
  versions:
    - version: 1.1.0
      source: {repository: https://git.example.com/runbooks, commit: 3f2a9c1, path: release}
    - version: 1.2.0
      source: {repository: https://git.example.com/runbooks, commit: 9b8e7d6, path: release}
`),
		"skills/release-diagnosis/1.1.0/SKILL.md":           file("# Release diagnosis\n\nRead the release history first.\n"),
		"skills/release-diagnosis/1.2.0/SKILL.md":           file("# Release diagnosis\n\nRead the release history and the flag changes first.\n"),
		"skills/release-diagnosis/1.2.0/reference/flags.md": file("A flag change is keyed to the service the flag affects.\n"),
		"work-item-types/incident.yaml": file(`kind: WorkItemType
metadata: {name: incident, owner: sre-leads@example.com}
spec:
  hypothesisKey:
    - {name: service, values: {tool: catalog.list_services, field: "$[*].name"}}
    - {name: failureMode, values: [bad-release, flag-change, config-change]}
  milestones: [acknowledged, cause_identified, mitigation_started, recovered]
  shadowRuns:
    - name: independent
      arms: [invisible]
      override: {budget: {rounds: 1}, round: {timeLimit: 15m}}
`),
		"trigger-mappings/alerts.yaml": file(`kind: TriggerMapping
metadata: {name: alerts, owner: sre-leads@example.com}
spec:
  source: alerting
  authentication: {hmacSecretRef: secret://inbound/alerting, maxAge: 5m}
  split: $.alerts[*]
  trustedFields: [labels.rule_id, labels.service, fingerprint, startsAt, endsAt, status]
  eventId: "{fingerprint}:{startsAt}"
  status: {value: "{status}"}
  occurredAt: {firing: "{startsAt}", resolved: "{endsAt}"}
  title: "{annotations.summary}"
  workItemType: incident
  dedupKey: "{labels.service}:{labels.rule_id}"
  instanceId: "{fingerprint}"
  correlate: {key: "{labels.service}", within: 30m}
  principal: {tool: catalog.responsible_for, args: {service: "{labels.service}"}, field: $.user}
  responders: {tool: catalog.responders_for, args: {service: "{labels.service}"}, field: $.group}
  fallbackPrincipal: {group: on-call-primary}
  redact: [default-secrets]
  checks: ['trusted["labels.service"] == event.labels.service']
`),
		"agents/release-investigator.yaml": file(`kind: Agent
metadata: {name: release-investigator, owner: alice@example.com}
spec:
  runtime: thinloop
  model: claude-haiku-5-5
  skills: [release-diagnosis@1.2.0]
  tools: ["release-manager.get_*", release-manager.list_recent_changes, release-manager.rollback_release, "metrics.query_*"]
  subscribe: {workItemTypes: [incident], taskTags: [release]}
`),
		"yanks.yaml": file(`kind: YankList
yanks:
  - skill: release-diagnosis@1.1.0
    by: alice@example.com
    at: 2026-10-14T09:00:00Z
    reason: Its rollback advice ignored flag changes.
`),
	}
}

// pinned are the digests the example pins. Tests use them where a registration
// needs a well-formed digest.
var pinned = strings.NewReplacer(
	"@IMG_A@", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "@IMG_B@", "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "@IMG_C@", "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
	"@H1@", "sha256:1111111111111111111111111111111111111111111111111111111111111111", "@H2@", "sha256:2222222222222222222222222222222222222222222222222222222222222222", "@H3@", "sha256:3333333333333333333333333333333333333333333333333333333333333333", "@H4@", "sha256:4444444444444444444444444444444444444444444444444444444444444444",
	"@H5@", "sha256:5555555555555555555555555555555555555555555555555555555555555555", "@H6@", "sha256:6666666666666666666666666666666666666666666666666666666666666666", "@H7@", "sha256:7777777777777777777777777777777777777777777777777777777777777777", "@H8@", "sha256:8888888888888888888888888888888888888888888888888888888888888888",
)

func file(text string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte(pinned.Replace(text)), Mode: 0o644}
}

// with returns a copy of repo with files replaced or added; a nil file removes
// the path.
func with(repo fstest.MapFS, files map[string]*fstest.MapFile) fstest.MapFS {
	out := maps.Clone(repo)
	for p, f := range files {
		if f == nil {
			delete(out, p)
			continue
		}
		out[p] = f
	}
	return out
}

// edit returns a copy of repo in which one file's text has old replaced.
func edit(t *testing.T, repo fstest.MapFS, path, old, replacement string) fstest.MapFS {
	t.Helper()
	text := string(repo[path].Data)
	require.Contains(t, text, old, "the fixture %s no longer holds the text the test edits", path)
	return with(repo, map[string]*fstest.MapFile{path: {Data: []byte(strings.Replace(text, old, replacement, 1)), Mode: 0o644}})
}

// conditions stands in for the condition language: it accepts every
// condition except those containing "BAD", and records what it was asked.
type conditions struct{ asked []string }

func (c *conditions) Check(env config.Environment, expr string) error {
	c.asked = append(c.asked, string(env)+": "+expr)
	if strings.Contains(expr, "BAD") {
		return errors.New("does not compile")
	}
	return nil
}

func build(t *testing.T, repo fstest.MapFS) *config.Revision {
	t.Helper()
	rev, err := config.Build(repo, "c0ffee0", &conditions{})
	require.NoError(t, err)
	return rev
}

// refused builds repo, expects it to be refused, and returns the problems.
func refused(t *testing.T, repo fstest.MapFS) config.Problems {
	t.Helper()
	_, err := config.Build(repo, "c0ffee0", &conditions{})
	var ps config.Problems
	require.ErrorAs(t, err, &ps)
	return ps
}

// assertProblem asserts that ps holds a problem in file, at field, whose
// message contains text.
func assertProblem(t *testing.T, ps config.Problems, file, field, text string) {
	t.Helper()
	for _, p := range ps {
		if p.File == file && p.Field == field && strings.Contains(p.Message, text) {
			return
		}
	}
	assert.Failf(t, "problem not found", "want %s: %s: ...%s...\ngot:\n%s", file, field, text, list(ps))
}

func list(ps config.Problems) string {
	var b strings.Builder
	for _, p := range ps {
		b.WriteString("  " + p.String() + "\n")
	}
	return b.String()
}
