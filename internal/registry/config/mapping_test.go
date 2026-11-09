package config_test

import (
	"fmt"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/registry/config"
)

const alerts = "trigger-mappings/alerts.yaml"

func TestRoutingOrderingAndIdentityUseTrustedFieldsOnly(t *testing.T) {
	repo := edit(t, exampleRepo(), alerts, `dedupKey: "{labels.service}:{labels.rule_id}"`, `dedupKey: "{labels.service}:{annotations.rule}"`)
	repo = edit(t, repo, alerts, `instanceId: "{fingerprint}"`, `instanceId: "host-1"`)
	repo = edit(t, repo, alerts, `status: {value: "{status}"}`, `status: {value: "{state}"}`)
	repo = edit(t, repo, alerts, `principal: {tool: catalog.responsible_for, args: {service: "{labels.service}"}`,
		`principal: {tool: catalog.responsible_for, args: {service: "{labels.owner}"}`)
	repo = edit(t, repo, alerts, `correlate: {key: "{labels.service}", within: 30m}`, `correlate: {key: "{labels.team}", within: 30m}`)

	ps := refused(t, repo)

	assertProblem(t, ps, alerts, "spec.dedupKey", "uses {annotations.rule}, which is not a trusted field")
	assertProblem(t, ps, alerts, "spec.instanceId", "uses no field")
	assertProblem(t, ps, alerts, "spec.status.value", "uses {state}")
	assertProblem(t, ps, alerts, "spec.principal.args.service", "uses {labels.owner}, which is not a trusted field")
	assertProblem(t, ps, alerts, "spec.correlate.key", "uses {labels.team}")
}

func TestTheTitleIsContentAndMayUseAnyField(t *testing.T) {
	rev := build(t, exampleRepo())

	assert.Equal(t, "{annotations.summary}", rev.TriggerMapping("alerting").Spec.Title)
}

func TestAMappingNeedsItsRoutingFields(t *testing.T) {
	repo := with(exampleRepo(), map[string]*fstest.MapFile{
		"trigger-mappings/bare.yaml": file("kind: TriggerMapping\nmetadata: {name: bare, owner: sre-leads@example.com}\n" +
			"spec: {source: pager, authentication: {bearerSecretRef: secret://inbound/pager}, workItemType: page}\n"),
	})

	ps := refused(t, repo)

	const bare = "trigger-mappings/bare.yaml"
	assertProblem(t, ps, bare, "spec.trustedFields", "routing needs trusted fields")
	for _, field := range []string{"spec.status.value", "spec.occurredAt.firing", "spec.occurredAt.resolved", "spec.dedupKey", "spec.instanceId", "spec.principal", "spec.responders"} {
		assertProblem(t, ps, bare, field, "is required")
	}
	assertProblem(t, ps, bare, "spec.workItemType", `no work item type "page"`)
}

func TestPrincipalsAndRespondersComeFromIdentitySources(t *testing.T) {
	repo := with(exampleRepo(), map[string]*fstest.MapFile{"identity-sources/catalog-responders.yaml": nil})

	ps := refused(t, repo)

	assertProblem(t, ps, alerts, "spec.responders.tool", "catalog.responders_for, which is not registered as an identity source")
}

func TestARequestIsAuthenticatedByExactlyOneScheme(t *testing.T) {
	both := edit(t, exampleRepo(), alerts, "authentication: {hmacSecretRef: secret://inbound/alerting, maxAge: 5m}",
		"authentication: {hmacSecretRef: secret://inbound/alerting, bearerSecretRef: secret://inbound/token}")
	none := edit(t, exampleRepo(), alerts, "authentication: {hmacSecretRef: secret://inbound/alerting, maxAge: 5m}", "authentication: {}")
	bearerAge := edit(t, exampleRepo(), alerts, "authentication: {hmacSecretRef: secret://inbound/alerting, maxAge: 5m}",
		"authentication: {bearerSecretRef: 'token', maxAge: 5m}")

	assertProblem(t, refused(t, both), alerts, "spec.authentication", "exactly one of hmacSecretRef and bearerSecretRef")
	assertProblem(t, refused(t, none), alerts, "spec.authentication", "exactly one of hmacSecretRef and bearerSecretRef")
	ps := refused(t, bearerAge)
	assertProblem(t, ps, alerts, "spec.authentication.bearerSecretRef", "secret://<path>")
	assertProblem(t, ps, alerts, "spec.authentication.maxAge", "signed requests only")
}

func TestASourceHasOneMapping(t *testing.T) {
	repo := with(exampleRepo(), map[string]*fstest.MapFile{"trigger-mappings/alerts-copy.yaml": nil})
	copied := edit(t, exampleRepo(), alerts, "name: alerts,", "name: alerts-copy,")
	repo["trigger-mappings/alerts-copy.yaml"] = copied[alerts]

	ps := refused(t, repo)

	assertProblem(t, ps, "trigger-mappings/alerts-copy.yaml", "spec.source", `already mapped by "alerts"`)
}

func TestStatusValuesMapToFiringAndResolved(t *testing.T) {
	m := build(t, exampleRepo()).TriggerMapping("alerting")
	assert.Equal(t, []string{"firing"}, m.Spec.Status.FiringValues())
	assert.Equal(t, []string{"resolved"}, m.Spec.Status.ResolvedValues())

	repo := edit(t, exampleRepo(), alerts, `status: {value: "{status}"}`, `status: {value: "{status}", firing: [triggered, resolved], resolved: [resolved, '']}`)
	ps := refused(t, repo)
	assertProblem(t, ps, alerts, "spec.status.firing[1]", "does not also mean resolved")
	assertProblem(t, ps, alerts, "spec.status.resolved[1]", "is empty")
}

func TestMappingDefaults(t *testing.T) {
	repo := edit(t, exampleRepo(), alerts, "authentication: {hmacSecretRef: secret://inbound/alerting, maxAge: 5m}", "authentication: {hmacSecretRef: secret://inbound/alerting}")
	repo = edit(t, repo, alerts, `correlate: {key: "{labels.service}", within: 30m}`, `correlate: {key: "{labels.service}"}`)

	m := build(t, repo).TriggerMapping("alerting")

	require.NotNil(t, m)
	assert.Equal(t, 5*time.Minute, m.Spec.Authentication.MaxAgeOrDefault())
	assert.Equal(t, 30*time.Minute, m.Spec.Correlate.WithinOrDefault())
}

func TestConsistencyChecksAreCheckedAsConditions(t *testing.T) {
	cc := &conditions{}
	_, err := config.Build(exampleRepo(), "c0ffee0", cc)
	require.NoError(t, err)

	assert.Contains(t, cc.asked, `check: trusted["labels.service"] == event.labels.service`)
}

func TestASplitAndChecksAreChecked(t *testing.T) {
	repo := edit(t, exampleRepo(), alerts, "split: $.alerts[*]", "split: alerts")
	repo = edit(t, repo, alerts, `checks: ['trusted["labels.service"] == event.labels.service']`, `checks: ['BAD']`)
	repo = edit(t, repo, alerts, "fallbackPrincipal: {group: on-call-primary}", "fallbackPrincipal: {group: 'On Call'}")
	repo = edit(t, repo, alerts, "redact: [default-secrets]", "redact: [pii]")

	ps := refused(t, repo)

	assertProblem(t, ps, alerts, "spec.split", "invalid JSONPath")
	assertProblem(t, ps, alerts, "spec.checks[0]", "does not compile")
	assertProblem(t, ps, alerts, "spec.fallbackPrincipal.group", "group name")
	assertProblem(t, ps, alerts, "spec.redact[0]", `no redaction rule "pii"`)
}

func TestAMappingsRoutingNamesTrustedFields(t *testing.T) {
	const old = "routing: {service: labels.service}"
	entries := func(n int) string {
		var b strings.Builder
		b.WriteString("routing: {")
		for i := range n {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "k%d: labels.service", i)
		}
		b.WriteString("}")
		return b.String()
	}
	cases := []struct {
		name, routing, field, text string
	}{
		{"untrusted value", "routing: {service: labels.team}", "spec.routing.service", "names labels.team, which is not a trusted field"},
		{"empty key", `routing: {"": labels.service}`, `spec.routing.`, "key"},
		{"long key", "routing: {" + strings.Repeat("k", 65) + ": labels.service}", "spec.routing." + strings.Repeat("k", 65), "key"},
		{"key with a tab", `routing: {"a\tb": labels.service}`, "spec.routing.a\tb", "key"},
		{"17 entries", entries(17), "spec.routing", "holds at most 16 keys"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ps := refused(t, edit(t, exampleRepo(), alerts, old, c.routing))

			assertProblem(t, ps, alerts, c.field, c.text)
		})
	}

	t.Run("valid", func(t *testing.T) {
		build(t, edit(t, exampleRepo(), alerts, old, entries(16)))
	})
	t.Run("a copy", func(t *testing.T) {
		rev := build(t, exampleRepo())
		rev.TriggerMapping("alerting").Spec.Routing["service"] = "changed"
		assert.Equal(t, "labels.service", rev.TriggerMapping("alerting").Spec.Routing["service"])
	})
}

// A mapping's source takes Open's form, a lowercase letter first (R132): a
// source such as 9am is a DNS label, but every request to it would be
// answered 404.
func TestAMappingSourceTakesOpensForm(t *testing.T) {
	ps := refused(t, edit(t, exampleRepo(), alerts, "source: alerting", "source: 9am"))
	assertProblem(t, ps, alerts, "spec.source", "a lowercase letter first")

	for _, source := range []string{"alerting", "a-1"} {
		rev := build(t, edit(t, exampleRepo(), alerts, "source: alerting", "source: "+source))
		m := rev.TriggerMapping(source)
		require.NotNil(t, m, source)
		assert.Equal(t, source, m.Spec.Source, source)
	}
}
