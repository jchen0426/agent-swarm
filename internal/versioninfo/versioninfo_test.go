package versioninfo_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jchen0426/agent-swarm/internal/versioninfo"
)

func TestGetWithoutLinkerFlagsReportsDev(t *testing.T) {
	info := versioninfo.Get()

	assert.Equal(t, "dev", info.Version)
}

func TestBuildNamesTheRevisionAndWhetherTheTreeWasClean(t *testing.T) {
	assert.Equal(t, "unknown", versioninfo.Info{}.Build(), "a test binary records no revision")
	assert.Equal(t, "0123abc", versioninfo.Info{Commit: "0123abc"}.Build())
	assert.Equal(t, "0123abc+modified", versioninfo.Info{Commit: "0123abc", Modified: true}.Build())
}
