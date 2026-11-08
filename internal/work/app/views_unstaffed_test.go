package app_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// The views list the items an on-row left not staffed, as the records hold
// them (Records.UnstaffedBy), for the web's off to show as its preview
// (Plan 9a-1, Task 8; M7); another on-row names none of them.
func TestTheViewsListTheItemsAnOnLeftUnstaffed(t *testing.T) {
	h := newHarness(t, limit(1))
	killed := h.open("checkout fails")
	_, err := h.c.SetKillSwitch(t.Context(), true, killing(0))
	require.NoError(t, err)
	opened := h.open("orders fail")
	views := viewsOf(h)

	got, err := views.UnstaffedBy(t.Context(), 1)

	require.NoError(t, err)
	assert.ElementsMatch(t, []item.ID{killed, opened}, got)
	none, err := views.UnstaffedBy(t.Context(), 2)
	require.NoError(t, err)
	assert.Empty(t, none)
}
