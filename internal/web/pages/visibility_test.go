package pages_test

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jchen0426/agent-swarm/internal/web/pages"
	"github.com/jchen0426/agent-swarm/internal/work/app"
)

func TestTheBoardsVisibilityIsTheAppsOnePredicate(t *testing.T) {
	assert.Equal(t,
		reflect.ValueOf(app.Visible).Pointer(),
		reflect.ValueOf(pages.Visibility()).Pointer(),
		"R144: the board pages must ask app.Visible, the one predicate, not a second one")
}
