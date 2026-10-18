package config_test

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/registry/config"
)

const rm = "servers/release-manager.yaml"

func TestARegistrationPinsItsImageAndNamesItsEndpoint(t *testing.T) {
	repo := edit(t, exampleRepo(), rm, "endpoint: https://release-mcp.internal", "endpoint: ftp://release-mcp.internal")
	repo = edit(t, repo, rm, `image: "sha256:`, `image: "latest`)
	repo = edit(t, repo, "servers/catalog.yaml", "credentialRef: secret://mcp/catalog", "credentialRef: vault:mcp/catalog")

	ps := refused(t, repo)

	assertProblem(t, ps, rm, "spec.endpoint", "must be an https URL")
	assertProblem(t, ps, rm, "spec.image", "pin the image by digest")
	assertProblem(t, ps, "servers/catalog.yaml", "spec.credentialRef", "secret://<path>")
}

func TestEveryListedToolDeclaresItsSchemaHashAndEffect(t *testing.T) {
	repo := edit(t, exampleRepo(), rm, `list_recent_changes: {schemaHash: "sha256:`, `list_recent_changes: {schemaHash: "sha1:`)
	repo = edit(t, repo, "servers/metrics.yaml", "query_instant: {schemaHash: \"sha256:8888888888888888888888888888888888888888888888888888888888888888\", effect: read}",
		"query_instant: {schemaHash: \"sha256:8888888888888888888888888888888888888888888888888888888888888888\", effect: readonly, audience: humans}")

	ps := refused(t, repo)

	assertProblem(t, ps, rm, "spec.tools.list_recent_changes.schemaHash", "registry hash-tools")
	assertProblem(t, ps, "servers/metrics.yaml", "spec.tools.query_instant.effect", `got "readonly"`)
	assertProblem(t, ps, "servers/metrics.yaml", "spec.tools.query_instant.audience", `got "humans"`)
}

func TestAWriteToolNeedsScopeAndSummary(t *testing.T) {
	repo := edit(t, exampleRepo(), rm, "      scope: {service: hypothesis.service}\n", "")
	repo = edit(t, repo, rm, "      summary: Roll back {args.service} by reverting change {args.change_id}\n", "")

	ps := refused(t, repo)

	assertProblem(t, ps, rm, "spec.tools.rollback_release.scope", "required for a write tool")
	assertProblem(t, ps, rm, "spec.tools.rollback_release.summary", "required for a write tool")
}

func TestATargetArgumentNeedsCandidates(t *testing.T) {
	repo := edit(t, exampleRepo(), rm, "target: [service]", "target: [service, environment]")

	ps := refused(t, repo)

	assertProblem(t, ps, rm, "spec.tools.rollback_release.target[1]", `"environment", which has no candidates`)
}

func TestReadArgumentsTakeOnlyArgumentsThatHaveCandidates(t *testing.T) {
	repo := edit(t, exampleRepo(), rm, `args: {service: "{args.service}"}, field: "$[*].id"`, `args: {service: "{args.reason}"}, field: "$[*].id"`)
	repo = edit(t, repo, rm, `current_release: {tool: get_release, args: {service: "{args.service}"}`, `current_release: {tool: get_release, args: {service: "{hypothesis.service}"}`)

	ps := refused(t, repo)

	assertProblem(t, ps, rm, "spec.tools.rollback_release.candidates.change_id.args.service", "uses {args.reason}")
	assertProblem(t, ps, rm, "spec.tools.rollback_release.facts.current_release.args.service", "uses {hypothesis.service}")
}

func TestCandidateReadsCannotDependOnEachOtherInACycle(t *testing.T) {
	repo := edit(t, exampleRepo(), rm, `service: {tool: catalog.list_services, field: "$[*].name"}`,
		`service: {tool: catalog.list_services, args: {change: "{args.change_id}"}, field: "$[*].name"}`)

	ps := refused(t, repo)

	assertProblem(t, ps, rm, "spec.tools.rollback_release.candidates", "change_id -> service -> change_id")
}

func TestASummaryShowsOnlyValidatedArgumentsAndFacts(t *testing.T) {
	repo := edit(t, exampleRepo(), rm, "summary: Roll back {args.service} by reverting change {args.change_id}",
		"summary: Roll back {args.service} because {args.reason}, now at {facts.current_release}, was {facts.previous}")

	ps := refused(t, repo)

	assertProblem(t, ps, rm, "spec.tools.rollback_release.summary", "uses {args.reason}")
	assertProblem(t, ps, rm, "spec.tools.rollback_release.summary", "uses {facts.previous}")
}

func TestReadAndWriteFieldsStayOnTheirOwnKindOfTool(t *testing.T) {
	repo := edit(t, exampleRepo(), rm, "      target: [service]\n", "      target: [service]\n      partialWhen: result.partial\n      bounds: {window: {max: 1h}}\n      audience: platform\n")
	repo = edit(t, repo, rm, "      bounds: {window: {max: 6h}}\n", "      bounds: {window: {max: 6h}}\n      summary: Read {args.service}\n      scope: {service: hypothesis.service}\n")

	ps := refused(t, repo)

	assertProblem(t, ps, rm, "spec.tools.rollback_release.partialWhen", "read tools only")
	assertProblem(t, ps, rm, "spec.tools.rollback_release.bounds", "read tools only")
	assertProblem(t, ps, rm, "spec.tools.rollback_release.audience", "only read tools serve the platform")
	assertProblem(t, ps, rm, "spec.tools.get_release.summary", "write tools only")
	assertProblem(t, ps, rm, "spec.tools.get_release.scope", "write tools only")
}

func TestPlatformReadsNameListedReadTools(t *testing.T) {
	repo := edit(t, exampleRepo(), rm, `service: {tool: catalog.list_services`, `service: {tool: catalog.list_teams`)
	repo = edit(t, repo, rm, `change_id: {tool: list_recent_changes`, `change_id: {tool: rollback_release`)
	repo = edit(t, repo, rm, `current_release: {tool: get_release`, `current_release: {tool: inventory.get_release`)
	repo = edit(t, repo, rm, `field: $.current}`, `field: current}`)

	ps := refused(t, repo)

	assertProblem(t, ps, rm, "spec.tools.rollback_release.candidates.service.tool", `tool "list_teams", which server "catalog" does not list`)
	assertProblem(t, ps, rm, "spec.tools.rollback_release.candidates.change_id.tool", "release-manager.rollback_release, which is not a read tool")
	assertProblem(t, ps, rm, "spec.tools.rollback_release.facts.current_release.tool", `no registered server "inventory"`)
	assertProblem(t, ps, rm, "spec.tools.rollback_release.facts.current_release.field", "invalid JSONPath")
}

func TestConditionsAreCheckedInTheirEnvironment(t *testing.T) {
	cc := &conditions{}
	_, err := config.Build(exampleRepo(), "c0ffee0", cc)
	require.NoError(t, err)

	assert.Contains(t, cc.asked, "partialWhen: result.truncated == true")
	assert.Contains(t, cc.asked, "scope: hypothesis.service")

	repo := edit(t, exampleRepo(), rm, "partialWhen: result.truncated == true", "partialWhen: BAD")
	ps := refused(t, repo)
	assertProblem(t, ps, rm, "spec.tools.get_release.partialWhen", "does not compile")
}

func TestBoundsCapArgumentsWithPositiveValues(t *testing.T) {
	repo := edit(t, exampleRepo(), "servers/metrics.yaml", "bounds: {window: {max: 6h}, limit: {max: 1000}}",
		"bounds: {window: {max: -1h}, limit: {max: lots}, step: {max: 0}, 'bad name': {max: 1}}")

	ps := refused(t, repo)

	assertProblem(t, ps, "servers/metrics.yaml", "spec.tools.query_range.bounds.window.max", "must be positive")
	assertProblem(t, ps, "servers/metrics.yaml", "spec.tools.query_range.bounds.limit.max", "positive number or duration")
	assertProblem(t, ps, "servers/metrics.yaml", "spec.tools.query_range.bounds.step.max", "must be positive")
	assertProblem(t, ps, "servers/metrics.yaml", "spec.tools.query_range.bounds.bad name", "not a valid argument name")
}

// A NaN cap never refuses a call, since no comparison with it holds, and an
// infinite one caps nothing.
func TestABoundIsAFiniteNumber(t *testing.T) {
	repo := edit(t, exampleRepo(), "servers/metrics.yaml", "bounds: {window: {max: 6h}, limit: {max: 1000}}",
		"bounds: {window: {max: NaN}, limit: {max: Inf}, step: {max: +infinity}}")

	ps := refused(t, repo)

	for _, arg := range []string{"window", "limit", "step"} {
		assertProblem(t, ps, "servers/metrics.yaml", "spec.tools.query_range.bounds."+arg+".max", "positive number or duration")
	}
}

func TestRedactNamesExistingRulesOnce(t *testing.T) {
	repo := edit(t, exampleRepo(), rm, "redact: [default-secrets]", "redact: [default-secrets, customer-data, default-secrets]")

	ps := refused(t, repo)

	assertProblem(t, ps, rm, "spec.tools.get_release.redact[1]", `no redaction rule "customer-data"`)
	assertProblem(t, ps, rm, "spec.tools.get_release.redact[2]", `names "default-secrets" twice`)
}

func TestRedactionRulesCompile(t *testing.T) {
	repo := with(exampleRepo(), map[string]*fstest.MapFile{
		"redaction-rules/broken.yaml": file("kind: RedactionRule\nmetadata: {name: broken, owner: security@example.com}\n" +
			"spec: {patterns: ['(unclosed', 'x*', '\\d+'], paths: ['$[?length(@) > 0]']}\n"),
		"redaction-rules/nothing.yaml": file("kind: RedactionRule\nmetadata: {name: nothing, owner: security@example.com}\nspec: {}\n"),
	})

	ps := refused(t, repo)

	assertProblem(t, ps, "redaction-rules/broken.yaml", "spec.patterns[0]", "not a valid regular expression")
	assertProblem(t, ps, "redaction-rules/broken.yaml", "spec.patterns[1]", "matches the empty string")
	assertProblem(t, ps, "redaction-rules/broken.yaml", "spec.paths[0]", "unknown function length()")
	assertProblem(t, ps, "redaction-rules/nothing.yaml", "spec", "at least one pattern or path")
	for _, p := range ps {
		assert.NotEqual(t, "spec.patterns[2]", p.Field, "a pattern that matches only non-empty text is fine")
	}
}

func TestARedactionRuleIsReadyToApply(t *testing.T) {
	rule := build(t, exampleRepo()).RedactionRule("default-secrets")
	require.NotNil(t, rule)

	assert.Len(t, rule.Patterns(), 2)
	assert.True(t, rule.Patterns()[1].MatchString("key AKIAABCDEFGHIJKLMNOP"))
	assert.Len(t, rule.Paths(), 1)
	assert.Equal(t, "[redacted:default-secrets]", rule.Replacement())
}

func TestIdentitySourcesNameListedReadToolsOnce(t *testing.T) {
	repo := with(exampleRepo(), map[string]*fstest.MapFile{
		"identity-sources/again.yaml": file("kind: IdentitySource\nmetadata: {name: again, owner: identity-team@example.com}\nspec: {tool: catalog.responsible_for}\n"),
		"identity-sources/writes.yaml": file("kind: IdentitySource\nmetadata: {name: writes, owner: identity-team@example.com}\n" +
			"spec: {tool: release-manager.rollback_release}\n"),
		"identity-sources/bare.yaml": file("kind: IdentitySource\nmetadata: {name: bare, owner: identity-team@example.com}\nspec: {tool: responsible_for}\n"),
	})

	ps := refused(t, repo)

	assertProblem(t, ps, "identity-sources/catalog-responsible.yaml", "spec.tool", `already the identity source "again"`)
	assertProblem(t, ps, "identity-sources/writes.yaml", "spec.tool", "not a read tool")
	assertProblem(t, ps, "identity-sources/bare.yaml", "spec.tool", "must be server.tool")
}

func TestToolsAreLookedUpByReference(t *testing.T) {
	rev := build(t, exampleRepo())

	tool := rev.Tool(config.ToolRef{Server: "release-manager", Tool: "rollback_release"})
	require.NotNil(t, tool)
	assert.Equal(t, config.EffectWrite, tool.Effect)
	assert.Nil(t, rev.Tool(config.ToolRef{Server: "release-manager", Tool: "delete_release"}))
	assert.Nil(t, rev.Tool(config.ToolRef{Server: "nowhere", Tool: "x"}))

	ref, err := config.ParseToolRef("catalog.responders_for")
	require.NoError(t, err)
	assert.Equal(t, "catalog-responders", rev.IdentitySource(ref).Metadata.Name)
}

func TestASecretPastedIntoAReferenceIsNeverRepeated(t *testing.T) {
	token := "ghp_8xQz1RmT9vLpK2wN4sJ7aB6cD3eF0gH5iY"
	repo := edit(t, exampleRepo(), "servers/catalog.yaml", "credentialRef: secret://mcp/catalog", "credentialRef: "+token)
	repo = edit(t, repo, rm, "endpoint: https://release-mcp.internal", "endpoint: https://deploy:"+token+"@release-mcp.internal")
	repo = edit(t, repo, alerts, "authentication: {hmacSecretRef: secret://inbound/alerting, maxAge: 5m}",
		"authentication: {bearerSecretRef: '"+token+"'}")
	repo = edit(t, repo, skillYAML, "repository: https://git.example.com/runbooks, commit: 9b8e7d6",
		"repository: 'https://x-access-token:"+token+"@git.example.com/runbooks', commit: 9b8e7d6")

	ps := refused(t, repo)

	assertProblem(t, ps, "servers/catalog.yaml", "spec.credentialRef", "secret://<path>")
	assertProblem(t, ps, rm, "spec.endpoint", "must not hold credentials")
	assertProblem(t, ps, alerts, "spec.authentication.bearerSecretRef", "secret://<path>")
	assertProblem(t, ps, skillYAML, "spec.versions[1].source.repository", "must not hold credentials")
	assert.NotContains(t, ps.Error(), token)
	for _, p := range ps {
		assert.NotContains(t, p.Message, token, p.String())
	}
}

func TestAnEndpointIsHTTPSWithoutAQueryOrFragment(t *testing.T) {
	for endpoint, msg := range map[string]string{
		"http://release-mcp.internal":           "must be an https URL",
		"https://release-mcp.internal/mcp?x=1":  "query or a fragment",
		"https://release-mcp.internal/mcp#part": "query or a fragment",
		"https://release-mcp.internal/mcp?":     "query or a fragment",
	} {
		repo := edit(t, exampleRepo(), rm, "endpoint: https://release-mcp.internal", "endpoint: "+endpoint)

		assertProblem(t, refused(t, repo), rm, "spec.endpoint", msg)
	}
}

// Everything the gateway stores or shows passes the deployment's redaction
// rules (spec Section 9.4), also the output of a tool that names none.
func TestRegisteredServersNeedARuleThatAppliesAlways(t *testing.T) {
	repo := edit(t, exampleRepo(), "redaction-rules/private-keys.yaml", "  always: true\n", "")

	assertProblem(t, refused(t, repo), "servers/catalog.yaml", "spec", "no redaction rule applies always")
}

func TestARuleThatAppliesAlwaysHoldsPatternsOnly(t *testing.T) {
	repo := edit(t, exampleRepo(), "redaction-rules/private-keys.yaml", "  always: true\n", "  always: true\n  paths: ['$..env[*].value']\n")

	assertProblem(t, refused(t, repo), "redaction-rules/private-keys.yaml", "spec.paths", "patterns only")
}

func TestTheRulesThatApplyAlwaysAreListed(t *testing.T) {
	rules := build(t, exampleRepo()).AlwaysRedactionRules()

	require.Len(t, rules, 1)
	assert.Equal(t, "private-keys", rules[0].Metadata.Name)
}

func TestABareNameWithADotThatTheServerListsIsAmbiguous(t *testing.T) {
	tool := ": {schemaHash: \"" + pinned.Replace("@H5@") + "\", effect: read}\n"
	repo := edit(t, exampleRepo(), rm, "    list_recent_changes:", "    catalog.list_services"+tool+"    list_recent_changes:")
	repo = edit(t, repo, rm, "      facts:\n        current_release: {tool: get_release,", "      facts:\n        current_release: {tool: catalog.list_services,")

	assertProblem(t, refused(t, repo), rm, "spec.tools.rollback_release.facts.current_release.tool",
		`"catalog.list_services" is ambiguous: it names tool "list_services" of server "catalog", and a tool of release-manager`)
}

// A rule that applies always redacts again what was redacted once, so its
// patterns must leave every replacement as it is: each rule's, and the LLM
// gateway's for a session's token. A pattern that turns a replacement into
// itself leaves it as it is, and a rule that does not apply always redacts
// a tool's output once.
func TestAPatternMayNotMatchAReplacement(t *testing.T) {
	repo := with(exampleRepo(), map[string]*fstest.MapFile{
		"redaction-rules/markers.yaml": file("kind: RedactionRule\nmetadata: {name: markers, owner: security@example.com}\n" +
			"spec: {always: true, patterns: ['zzz', 'redacted:[a-z-]+', 'session-token'], replacement: '<hidden>'}\n"),
		"redaction-rules/emails.yaml": file("kind: RedactionRule\nmetadata: {name: emails, owner: security@example.com}\n" +
			"spec: {always: true, patterns: ['[a-z]+@example[.]com'], replacement: 'user@example.com'}\n"),
		"redaction-rules/named.yaml": file("kind: RedactionRule\nmetadata: {name: named, owner: security@example.com}\n" +
			"spec: {patterns: ['hidden'], replacement: '[gone]'}\n"),
	})

	ps := refused(t, repo)

	assertProblem(t, ps, "redaction-rules/markers.yaml", "spec.patterns[1]", "changes the replacement")
	assertProblem(t, ps, "redaction-rules/markers.yaml", "spec.patterns[2]", config.SessionTokenReplacement)
	for _, p := range ps {
		assert.False(t, p.File == "redaction-rules/markers.yaml" && p.Field == "spec.patterns[0]", "a pattern that matches no replacement is fine")
		assert.NotEqual(t, "redaction-rules/emails.yaml", p.File, "a replacement a pattern turns into itself is fine")
		assert.NotEqual(t, "redaction-rules/named.yaml", p.File, "a rule that does not apply always redacts once")
	}
}

// A replacement holds no quote and no backslash: redacted again, it would be
// read as a member or an escape, and grow each time.
func TestAReplacementHoldsNoQuoteOrBackslash(t *testing.T) {
	repo := with(exampleRepo(), map[string]*fstest.MapFile{
		"redaction-rules/passwords.yaml": file("kind: RedactionRule\nmetadata: {name: passwords, owner: security@example.com}\n" +
			"spec: {patterns: ['password=\\S+'], replacement: '\"password\": \"***\"'}\n"),
		"redaction-rules/escapes.yaml": file("kind: RedactionRule\nmetadata: {name: escapes, owner: security@example.com}\n" +
			"spec: {patterns: ['zzz'], replacement: 'a\\b'}\n"),
	})

	ps := refused(t, repo)

	assertProblem(t, ps, "redaction-rules/passwords.yaml", "spec.replacement", "quote")
	assertProblem(t, ps, "redaction-rules/escapes.yaml", "spec.replacement", "backslash")
}

// A pattern holds no control character. A double-quoted YAML string writes \b
// as a backspace, so "\b[0-9]{16}\b" would load holding backspaces, match
// nothing, and silently disable its rule; a tab escape does the same. In
// single quotes, the pattern loads as written.
func TestAPatternHoldsNoControlCharacter(t *testing.T) {
	repo := with(exampleRepo(), map[string]*fstest.MapFile{
		"redaction-rules/double.yaml": file(`kind: RedactionRule
metadata: {name: double, owner: security@example.com}
spec:
  always: true
  patterns: ["\b[0-9]{16}\b"]
`),
		"redaction-rules/named.yaml": file(`kind: RedactionRule
metadata: {name: named, owner: security@example.com}
spec:
  patterns: ["\b[0-9]{16}\b"]
`),
		"redaction-rules/tab.yaml": file(`kind: RedactionRule
metadata: {name: tab, owner: security@example.com}
spec:
  always: true
  patterns: ["\t[0-9]{16}"]
`),
		"redaction-rules/single.yaml": file(`kind: RedactionRule
metadata: {name: single, owner: security@example.com}
spec:
  always: true
  patterns: ['\b[0-9]{16}\b']
`),
	})

	ps := refused(t, repo)

	assertProblem(t, ps, "redaction-rules/double.yaml", "spec.patterns[0]", "holds a control character, as YAML writes \\b and other escapes in a double-quoted string: write the pattern in single quotes")
	assertProblem(t, ps, "redaction-rules/named.yaml", "spec.patterns[0]", "holds a control character")
	assertProblem(t, ps, "redaction-rules/tab.yaml", "spec.patterns[0]", "holds a control character")
	for _, p := range ps {
		assert.NotEqual(t, "redaction-rules/single.yaml", p.File, "a pattern in single quotes loads as written")
	}
}

// A rule that applies always may not match what the LLM gateway forwards as
// written: a server's tool as agents name it, or a tool call's id in the
// vendor's form. The gateway refuses a request whose id or name holds what
// a rule hides, so such a rule would refuse every turn that calls a tool.
// A sample of random ids can miss a run of one character class, so the
// registry also checks one id of each class. An id is one word: a pattern
// anchored on word boundaries matches it only when it matches the whole id.
func TestARuleThatAppliesAlwaysSparesToolNamesAndIDs(t *testing.T) {
	rule := func(name, pattern string) *fstest.MapFile {
		return file("kind: RedactionRule\nmetadata: {name: " + name + ", owner: security@example.com}\n" +
			"spec: {always: true, patterns: ['" + pattern + "']}\n")
	}
	repo := with(exampleRepo(), map[string]*fstest.MapFile{
		"redaction-rules/long-tokens.yaml":  rule("long-tokens", "[A-Za-z0-9]{24,}"),
		"redaction-rules/metric-names.yaml": rule("metric-names", "metrics__[a-z]+"),
		"redaction-rules/board-reads.yaml":  rule("board-reads", "read_board"),
		"redaction-rules/codes.yaml":        rule("codes", "[0-9]{6}"),
		"redaction-rules/whole-codes.yaml":  rule("whole-codes", "\\b[0-9]{6}\\b"),
		"redaction-rules/digit-runs.yaml":   rule("digit-runs", "\\d{8,}"),
		"redaction-rules/nine-digits.yaml":  rule("nine-digits", "[0-9]{9}"),
		"redaction-rules/hex-runs.yaml":     rule("hex-runs", "[0-9a-f]{10}"),
		"redaction-rules/upper-hex.yaml":    rule("upper-hex", "[A-F0-9]{10}"),
		"redaction-rules/whole-digits.yaml": rule("whole-digits", "\\b\\d{16}\\b"),
		"redaction-rules/whole-hex.yaml":    rule("whole-hex", "\\b[0-9a-f]{32}\\b"),
	})

	ps := refused(t, repo)

	assertProblem(t, ps, "redaction-rules/long-tokens.yaml", "spec.patterns[0]", "toolu_")
	assertProblem(t, ps, "redaction-rules/metric-names.yaml", "spec.patterns[0]", "metrics__")
	assertProblem(t, ps, "redaction-rules/board-reads.yaml", "spec.patterns[0]", "read_board")
	assertProblem(t, ps, "redaction-rules/codes.yaml", "spec.patterns[0]", "toolu_")
	assertProblem(t, ps, "redaction-rules/codes.yaml", "spec.patterns[0]", "anchor the pattern, for example on word boundaries (\\b, in single quotes), and keep _ out of what it matches")
	assertProblem(t, ps, "redaction-rules/digit-runs.yaml", "spec.patterns[0]", "toolu_")
	assertProblem(t, ps, "redaction-rules/nine-digits.yaml", "spec.patterns[0]", "toolu_")
	assertProblem(t, ps, "redaction-rules/hex-runs.yaml", "spec.patterns[0]", "toolu_")
	assertProblem(t, ps, "redaction-rules/upper-hex.yaml", "spec.patterns[0]", "toolu_")
	spared := []string{"redaction-rules/whole-codes.yaml", "redaction-rules/whole-digits.yaml", "redaction-rules/whole-hex.yaml"}
	for _, p := range ps {
		assert.NotContains(t, spared, p.File, "a pattern anchored on word boundaries spares ids")
	}
}
