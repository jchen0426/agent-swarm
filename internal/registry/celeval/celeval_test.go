package celeval_test

import (
	"context"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/registry/celeval"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
)

func conditions(t *testing.T) *celeval.Conditions {
	t.Helper()
	c, err := celeval.New()
	require.NoError(t, err)
	return c
}

func TestEachEnvironmentAcceptsItsOwnConditions(t *testing.T) {
	c := conditions(t)

	require.NoError(t, c.Check(config.EnvPartialWhen, "result.truncated == true"))
	require.NoError(t, c.Check(config.EnvPartialWhen, `has(result.warnings) && size(result.warnings) > 0`))
	require.NoError(t, c.Check(config.EnvScope, "hypothesis.service"))
	require.NoError(t, c.Check(config.EnvScope, `item["service"]`))
	require.NoError(t, c.Check(config.EnvCheck, `trusted["labels.service"] == event.labels.service`))
}

func TestAConditionIsTypeCheckedAgainstItsEnvironment(t *testing.T) {
	c := conditions(t)
	tests := []struct {
		env  config.Environment
		expr string
		want string
	}{
		{config.EnvPartialWhen, "result.truncated = true", "Syntax error"},
		{config.EnvPartialWhen, "result.truncated", "must produce bool, but produces dyn"},
		{config.EnvPartialWhen, "args.limit > 10", "undeclared reference to 'args'"},
		{config.EnvScope, "hypothesis.service == 'cart'", "must produce string, but produces bool"},
		{config.EnvScope, "result.service", "undeclared reference to 'result'"},
		{config.EnvCheck, "trusted.service + 1 == 2", "no matching overload"},
		{"unknown", "true", `no environment "unknown"`},
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			err := c.Check(tt.env, tt.expr)
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestAConditionHasALengthLimit(t *testing.T) {
	long := "result.a == 1"
	for len(long) <= 2048 {
		long += " || result.a == 1"
	}

	require.ErrorContains(t, conditions(t).Check(config.EnvPartialWhen, long), "longer than 2048 bytes")
}

func TestProgramsRunOnBoundValues(t *testing.T) {
	c := conditions(t)
	ctx := context.Background()

	partial, err := c.Compile(config.EnvPartialWhen, "result.truncated == true")
	require.NoError(t, err)
	yes, err := partial.Bool(ctx, map[string]any{"result": map[string]any{"truncated": true, "rows": []any{}}})
	require.NoError(t, err)
	assert.True(t, yes)
	no, err := partial.Bool(ctx, map[string]any{"result": map[string]any{"truncated": false}})
	require.NoError(t, err)
	assert.False(t, no)

	scope, err := c.Compile(config.EnvScope, "hypothesis.service")
	require.NoError(t, err)
	svc, err := scope.String(ctx, map[string]any{"hypothesis": map[string]string{"service": "cart"}, "item": map[string]string{}})
	require.NoError(t, err)
	assert.Equal(t, "cart", svc)
}

func TestAValueIsNeverConditionText(t *testing.T) {
	check, err := conditions(t).Compile(config.EnvCheck, `trusted["service"] == event.service`)
	require.NoError(t, err)

	ok, err := check.Bool(context.Background(), map[string]any{
		"trusted": map[string]string{"service": `cart" || true || "`},
		"event":   map[string]any{"service": "checkout"},
	})

	require.NoError(t, err)
	assert.False(t, ok)
}

func TestAMissingFieldIsAnErrorNotFalse(t *testing.T) {
	partial, err := conditions(t).Compile(config.EnvPartialWhen, "result.truncated == true")
	require.NoError(t, err)

	_, err = partial.Bool(context.Background(), map[string]any{"result": map[string]any{"rows": []any{}}})

	require.ErrorContains(t, err, "no such key")
}

func TestEvaluationStopsAtTheCostLimit(t *testing.T) {
	items := make([]any, 1000)
	for i := range items {
		items[i] = float64(i)
	}
	heavy, err := conditions(t).Compile(config.EnvPartialWhen, "result.items.all(x, result.items.all(y, x != y || x == y))")
	require.NoError(t, err)

	_, err = heavy.Bool(context.Background(), map[string]any{"result": map[string]any{"items": items}})

	require.ErrorContains(t, err, "cost limit exceeded")
}

func TestTheLoaderChecksConditionsInCEL(t *testing.T) {
	repo := fstest.MapFS{
		"redaction-rules/secrets.yaml": {Data: []byte("kind: RedactionRule\nmetadata: {name: secrets, owner: security@example.com}\nspec: {always: true, patterns: ['AKIA[0-9A-Z]{16}']}\n")},
		"servers/flags.yaml": {Data: []byte(`kind: McpServer
metadata: {name: flags, owner: platform-team}
spec:
  endpoint: https://flags-mcp.internal
  image: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
  tools:
    get_flag:
      schemaHash: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
      effect: read
      partialWhen: result.partial
`)},
	}

	_, err := config.Build(repo, "c0ffee0", conditions(t))

	var ps config.Problems
	require.ErrorAs(t, err, &ps)
	require.Len(t, ps, 1)
	assert.Equal(t, "spec.tools.get_flag.partialWhen", ps[0].Field)
	assert.Contains(t, ps[0].Message, "must produce bool")
}

func TestPartialWhenEvaluatesAReadsOutput(t *testing.T) {
	c, err := celeval.New()
	require.NoError(t, err)

	partial, err := c.PartialWhen(t.Context(), "result.truncated == true", map[string]any{"truncated": true})
	require.NoError(t, err)
	assert.True(t, partial)
	partial, err = c.PartialWhen(t.Context(), "result.truncated == true", map[string]any{"truncated": false})
	require.NoError(t, err)
	assert.False(t, partial)
	_, err = c.PartialWhen(t.Context(), "result.truncated == true", "plain text")
	require.Error(t, err, "a condition that cannot be evaluated is an error, which blocks")
}
