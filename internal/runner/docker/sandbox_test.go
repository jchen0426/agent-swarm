package docker

import (
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTheSandboxIsTheRunnersWhateverTheImageSays (Contract C13): every
// setting of a turn's container is the runner's, from its options and the
// turn, and none is left to the image.
func TestTheSandboxIsTheRunnersWhateverTheImageSays(t *testing.T) {
	o := testOptions(t)
	o.Tenant, o.Holder, o.Sockets, o.Group = "acme", "a", "/srv/agentswarm/a", 1234
	// Every limit differs from its default, so that none is fixed in code.
	o.Memory, o.CPUs, o.Pids, o.Files, o.Tmp = 300<<20, 2, 70, 300, 20<<20
	// The deployment's labels join the runner's, which win over any of the
	// same key.
	o.Labels = map[string]string{"agentswarm.project": "agent-swarm", "team": "sre", "agentswarm.turn": "another"}
	turn := testTurn("sha256:" + strings.Repeat("d", 64))
	turn.Deadline = time.Date(2026, 10, 29, 9, 30, 15, 123456789, time.FixedZone("AEDT", 11*3600))

	name, cfg, host := o.sandbox(turn)
	require.NotNil(t, cfg)
	require.NotNil(t, host)

	assert.Equal(t, "agentswarm-turn-turn-1", name)

	assert.Equal(t, turn.Image, cfg.Image)
	assert.Nil(t, cfg.Entrypoint, "the image's entrypoint runs")
	assert.Nil(t, cfg.Cmd)
	assert.Nil(t, cfg.Env)
	assert.Equal(t, "65532:65532", cfg.User)
	assert.Equal(t, "/tmp", cfg.WorkingDir)
	assert.True(t, cfg.OpenStdin)
	assert.True(t, cfg.StdinOnce)
	assert.True(t, cfg.AttachStdin)
	assert.True(t, cfg.AttachStdout)
	assert.True(t, cfg.AttachStderr)
	assert.False(t, cfg.Tty)
	assert.Equal(t, &container.HealthConfig{Test: []string{"NONE"}}, cfg.Healthcheck)
	assert.Equal(t, "SIGTERM", cfg.StopSignal)
	require.NotNil(t, cfg.StopTimeout)
	assert.Equal(t, 10, *cfg.StopTimeout)
	assert.Equal(t, map[string]string{
		"agentswarm.tenant":   "acme",
		"agentswarm.holder":   "a",
		"agentswarm.turn":     "turn-1",
		"agentswarm.run":      "run-1",
		"agentswarm.agent":    "alpha",
		"agentswarm.round":    "3",
		"agentswarm.deadline": "2026-10-28T22:30:15.123456789Z",
		"agentswarm.project":  "agent-swarm",
		"team":                "sre",
	}, cfg.Labels)
	assert.Nil(t, cfg.Volumes)
	assert.Empty(t, cfg.Hostname)

	assert.Equal(t, []string{"1234"}, host.GroupAdd)
	assert.Equal(t, container.NetworkMode("none"), host.NetworkMode)
	assert.Equal(t, []netip.Addr{netip.MustParseAddr("127.0.0.1")}, host.DNS)
	assert.Equal(t, []string{"."}, host.DNSSearch)
	assert.Equal(t, container.IPCModeNone, host.IpcMode)
	assert.True(t, host.ReadonlyRootfs)
	assert.Equal(t, []string{"ALL"}, host.CapDrop)
	assert.Nil(t, host.CapAdd)
	assert.Equal(t, []string{"no-new-privileges"}, host.SecurityOpt)
	require.NotNil(t, host.Init)
	assert.True(t, *host.Init)
	assert.Equal(t, int64(300<<20), host.Memory)
	assert.Equal(t, int64(300<<20), host.MemorySwap, "no swap")
	assert.Equal(t, int64(2_000_000_000), host.NanoCPUs)
	require.NotNil(t, host.PidsLimit)
	assert.Equal(t, int64(70), *host.PidsLimit)
	assert.Equal(t, []*container.Ulimit{{Name: "nofile", Soft: 300, Hard: 300}}, host.Ulimits)
	assert.Equal(t, 500, host.OomScoreAdj)
	assert.Equal(t, map[string]string{"/tmp": "rw,noexec,nosuid,nodev,size=20971520"}, host.Tmpfs)
	assert.Equal(t, []mount.Mount{{Type: mount.TypeBind, Source: "/srv/agentswarm/a", Target: "/run/agentswarm", ReadOnly: true}}, host.Mounts)
	assert.Equal(t, container.LogConfig{Type: "none"}, host.LogConfig)

	// Anything else is not set.
	assert.False(t, host.Privileged)
	assert.Nil(t, host.Binds)
	assert.Nil(t, host.PortBindings)
	assert.Nil(t, host.Devices)
	assert.Nil(t, host.VolumesFrom)
	assert.False(t, host.AutoRemove)
	assert.Zero(t, host.RestartPolicy)
	assert.Empty(t, host.PidMode)
	assert.Empty(t, host.UsernsMode)
	assert.Nil(t, host.ExtraHosts)
}
