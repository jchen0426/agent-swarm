package docker

import (
	"maps"
	"net/netip"
	"strconv"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"

	"github.com/jchen0426/agent-swarm/internal/work/app"
)

// Where a sandbox reaches the gateways (Contract C6), fixed in code: the
// sockets directory, mounted read-only at SocketsDir, holds each gateway's
// socket, which the runtime dials whatever its URL's host, the name the
// gateways' certificate is checked against (Decision 2).
const (
	SocketsDir = "/run/agentswarm"
	MCPSocket  = "mcp.sock"
	LLMSocket  = "llm.sock"
	MCPURL     = "https://" + mcpHost + "/"
	LLMURL     = "https://" + llmHost
)

// The gateways' hosts, in their URLs and in their certificate's names
// (NewCertificate).
const (
	mcpHost = "mcp.agentswarm.internal"
	llmHost = "llm.agentswarm.internal"
)

// The labels of the runner's own that every sandbox carries (Contract C13),
// which the sweep reads; a deployment's runner.docker.labels may set none of
// them.
const (
	labelTenant = "agentswarm.tenant"
	labelHolder = "agentswarm.holder"
	labelTurn   = "agentswarm.turn"
	labelRun    = "agentswarm.run"
	labelAgent  = "agentswarm.agent"
	labelRound  = "agentswarm.round"
	// labelDeadline holds the turn's deadline, in RFC 3339 with nanoseconds,
	// in UTC.
	labelDeadline = "agentswarm.deadline"
)

// The sandbox's settings that no option changes (Decision 3).
const (
	// sandboxUser runs a sandbox's processes, a user of no one's.
	sandboxUser = "65532:65532"
	// sandboxStopTimeout is how long, in seconds, an engine's stop waits
	// after SIGTERM before its SIGKILL.
	sandboxStopTimeout = 10
	// sandboxOOMScoreAdj raises a sandbox's OOM score, so that the host's OOM
	// killer takes a sandbox before serve.
	sandboxOOMScoreAdj = 500
)

// containerName names a turn's container: agentswarm-turn-<turn id>.
func containerName(turn string) string { return "agentswarm-turn-" + turn }

// sandbox returns a turn's container configuration (Contract C13). Every
// setting is the runner's, whatever the image says, but for the image's
// entrypoint, which runs the runtime, and which the tests' probe seam
// replaces (R15). The deployment's labels join the runner's, which win over
// any of the same key.
func (o *Options) sandbox(t *app.Turn) (name string, cfg *container.Config, host *container.HostConfig) {
	labels := make(map[string]string, len(o.Labels)+7)
	maps.Copy(labels, o.Labels)
	labels[labelTenant] = o.Tenant
	labels[labelHolder] = o.Holder
	labels[labelTurn] = t.ID
	labels[labelRun] = string(t.Run)
	labels[labelAgent] = t.Agent
	labels[labelRound] = strconv.Itoa(t.Round)
	labels[labelDeadline] = t.Deadline.UTC().Format(time.RFC3339Nano)
	stopTimeout, withInit, pids := sandboxStopTimeout, true, o.Pids
	cfg = &container.Config{
		Image:      t.Image,
		Entrypoint: o.probe, // nil but in the package's tests: the image's entrypoint runs
		User:       sandboxUser,
		WorkingDir: "/tmp",
		// The runner attaches to standard input before the start, and the
		// engine closes the container's input once that attachment ends.
		OpenStdin: true, StdinOnce: true, AttachStdin: true, AttachStdout: true, AttachStderr: true,
		Healthcheck: &container.HealthConfig{Test: []string{"NONE"}},
		StopSignal:  "SIGTERM",
		StopTimeout: &stopTimeout,
		Labels:      labels,
	}
	host = &container.HostConfig{
		GroupAdd:       []string{strconv.Itoa(o.Group)},
		NetworkMode:    network.NetworkNone,
		DNS:            []netip.Addr{netip.AddrFrom4([4]byte{127, 0, 0, 1})},
		DNSSearch:      []string{"."},
		IpcMode:        container.IPCModeNone,
		ReadonlyRootfs: true,
		CapDrop:        []string{"ALL"},
		SecurityOpt:    []string{"no-new-privileges"},
		Init:           &withInit,
		Resources: container.Resources{
			Memory: o.Memory, MemorySwap: o.Memory, NanoCPUs: o.CPUs * 1e9, PidsLimit: &pids,
			Ulimits: []*container.Ulimit{{Name: "nofile", Soft: o.Files, Hard: o.Files}},
		},
		OomScoreAdj: sandboxOOMScoreAdj,
		Tmpfs:       map[string]string{"/tmp": "rw,noexec,nosuid,nodev,size=" + strconv.FormatInt(o.Tmp, 10)},
		Mounts:      []mount.Mount{{Type: mount.TypeBind, Source: o.Sockets, Target: SocketsDir, ReadOnly: true}},
		LogConfig:   container.LogConfig{Type: "none"},
	}
	return containerName(t.ID), cfg, host
}
