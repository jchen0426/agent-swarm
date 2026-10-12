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
