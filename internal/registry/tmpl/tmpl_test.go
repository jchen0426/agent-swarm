package tmpl_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/registry/tmpl"
)

func values(m map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		v, ok := m[name]
		return v, ok
	}
}

func TestRenderFillsEachHoleWithItsValue(t *testing.T) {
	tpl, err := tmpl.Parse("Roll back {args.service} by reverting change {args.change_id}")
	require.NoError(t, err)

	got, err := tpl.Render(values(map[string]string{"args.service": "cart", "args.change_id": "c-17"}))

	require.NoError(t, err)
	assert.Equal(t, "Roll back cart by reverting change c-17", got)
	assert.Equal(t, []string{"args.service", "args.change_id"}, tpl.Names())
}

func TestNamesListsEachHoleOnce(t *testing.T) {
	tpl, err := tmpl.Parse("{labels.service}:{labels.rule_id}:{labels.service}")
	require.NoError(t, err)

	assert.Equal(t, []string{"labels.service", "labels.rule_id"}, tpl.Names())
}

func TestDoubledBracesAreLiteral(t *testing.T) {
	tpl, err := tmpl.Parse("{{not a hole}} {x}")
	require.NoError(t, err)

	got, err := tpl.Render(values(map[string]string{"x": "1"}))

	require.NoError(t, err)
	assert.Equal(t, "{not a hole} 1", got)
	assert.Equal(t, []string{"x"}, tpl.Names())
}

func TestAValueIsNeverParsedAgain(t *testing.T) {
	tpl, err := tmpl.Parse("service {s}")
	require.NoError(t, err)

	got, err := tpl.Render(values(map[string]string{"s": "{args.secret}", "args.secret": "leaked"}))

	require.NoError(t, err)
	assert.Equal(t, "service {args.secret}", got)
}

func TestRenderRefusesAHoleWithoutAValue(t *testing.T) {
	tpl, err := tmpl.Parse("{a} and {b}")
	require.NoError(t, err)

	_, err = tpl.Render(values(map[string]string{"a": "1"}))

	require.ErrorIs(t, err, tmpl.ErrMissing)
	assert.Contains(t, err.Error(), `"b"`)
}

func TestParseRefusesBadTemplates(t *testing.T) {
	for _, text := range []string{"{", "x}", "{}", "{a b}", "{a..b}", "{.a}", "{a.}", "{a{b}", "{$.x}"} {
		_, err := tmpl.Parse(text)
		require.ErrorIs(t, err, tmpl.ErrSyntax, text)
	}
}

func TestATemplateWithoutHolesIsLiteral(t *testing.T) {
	tpl, err := tmpl.Parse("on-call-primary")
	require.NoError(t, err)

	got, err := tpl.Render(values(nil))

	require.NoError(t, err)
	assert.Equal(t, "on-call-primary", got)
	assert.Empty(t, tpl.Names())
	assert.Empty(t, tmpl.Template{}.Names())
}

func TestRootIsTheFirstSegment(t *testing.T) {
	assert.Equal(t, "args", tmpl.Root("args.service"))
	assert.Equal(t, "status", tmpl.Root("status"))
}
