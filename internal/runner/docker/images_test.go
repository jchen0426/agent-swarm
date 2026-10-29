package docker

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/runner/spec"
)

// goodImage returns an image that meets the contract on an arm64 engine: for
// Linux on arm64, reading the spec's versions 1 and 2, with PATH alone in its
// environment and no label of the runner's.
func goodImage() *Image {
	return &Image{
		ID: "sha256:" + strings.Repeat("a", 64), OS: "linux", Arch: "arm64",
		Labels: map[string]string{spec.Label: "1,2", "org.opencontainers.image.version": "abc"},
		Env:    []string{"PATH=/usr/bin:/bin"},
	}
}

// TestAnImageMeetsTheContract (Contract C12): an image that meets it gives
// the spec's version 2, the newest the image and the runner both know; each
// field that breaks it is refused, saying which.
func TestAnImageMeetsTheContract(t *testing.T) {
	for _, row := range []struct {
		name   string
		change func(img *Image)
		// says is what the refusal says; "" for an image that passes.
		says string
		// hides is what the refusal must not say.
		hides string
	}{
		{name: "a good image", change: func(*Image) {}},
		{name: "a health check NONE", change: func(img *Image) { img.Healthcheck = []string{"NONE"} }},
		{name: "windows", change: func(img *Image) { img.OS = "windows" }, says: `OS is "windows"`},
		{name: "another architecture", change: func(img *Image) { img.Arch = "amd64" }, says: `architecture is "amd64"`},
		{name: "no label", change: func(img *Image) { delete(img.Labels, spec.Label) }, says: "no label " + spec.Label},
		{name: "a label of 1 alone", change: func(img *Image) { img.Labels[spec.Label] = "1" }, says: "writes version 2"},
		{name: "a label 1,,2", change: func(img *Image) { img.Labels[spec.Label] = "1,,2" }, says: "not a list of versions"},
		{name: "a volume", change: func(img *Image) { img.Volumes = []string{"/data"} }, says: "volume"},
		{name: "a health check CMD true", change: func(img *Image) { img.Healthcheck = []string{"CMD", "true"} }, says: "health check"},
		{name: "ONBUILD", change: func(img *Image) { img.OnBuild = []string{"RUN true"} }, says: "ONBUILD"},
		{
			name:   "HTTPS_PROXY in its environment",
			change: func(img *Image) { img.Env = append(img.Env, "HTTPS_PROXY=http://user:secret@proxy:3128") },
			says:   "HTTPS_PROXY", hides: "secret",
		},
		{name: "the label agentswarm.tenant", change: func(img *Image) { img.Labels["agentswarm.tenant"] = "x" }, says: "agentswarm.tenant"},
	} {
		t.Run(row.name, func(t *testing.T) {
			img := goodImage()
			row.change(img)
			version, err := checkImage(img, "arm64")
			if row.says == "" {
				require.NoError(t, err)
				assert.Equal(t, spec.V2, version)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), row.says)
			assert.Contains(t, err.Error(), img.ID, "the refusal names the image")
			if row.hides != "" {
				assert.NotContains(t, err.Error(), row.hides)
			}
			assert.Zero(t, version)
		})
	}
}
