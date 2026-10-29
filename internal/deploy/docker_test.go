package deploy_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/jchen0426/agent-swarm/internal/deploy"
)

// minimalDocker is a deployment of the Docker runner with C1's required keys
// alone, as a document a test may change before it writes it: minimal's,
// without the agents' listeners and unsafeNoSandbox, which the Docker runner
// refuses, and with a runner.docker block of its sockets and store.
func minimalDocker() map[string]any {
	doc := minimal()
	remove(doc, "listeners.mcp")
	remove(doc, "listeners.llm")
	remove(doc, "runner.unsafeNoSandbox")
	set(doc, "runner.kind", "docker")
	set(doc, "runner.docker", map[string]any{"sockets": "/run/agentswarm/a", "store": "/var/lib/agentswarm/images"})
	return doc
}

// A Docker runner's block that gives its sockets and store alone gives every
// default of C1: the engine's socket, serve's own group, no labels of the
// deployment's, and the sandbox's limits, its CPUs whole (Plan 7d-2, R9 and
// R10).
func TestADockerRunnerLoadsWithItsDefaults(t *testing.T) {
	d, err := deploy.Load(write(t, minimalDocker()))

	require.NoError(t, err)
	assert.Equal(t, deploy.Runner{
		Kind: deploy.KindDocker, Output: "/var/lib/agentswarm/turns", OutputCap: 1 << 20, MaxTokens: 4096,
		Images: map[string]string{"thinloop": image},
		Docker: &deploy.Docker{
			Endpoint: "unix:///var/run/docker.sock", Sockets: "/run/agentswarm/a", Store: "/var/lib/agentswarm/images", Group: -1,
			Memory: 268_435_456, CPUs: 1, Pids: 64, Files: 256, Tmp: 16_777_216,
		},
	}, d.Runner)
}

// dockerExample is the Docker variant of Contract C1's example: its listeners
// and its runner, as the plan gives them, the image's digest filled in. A
// deployment's other keys are as before.
const dockerExample = `listeners:
  admin: 127.0.0.1:7403             # required, loopback only
  addresses: /var/lib/agentswarm/a/addresses.json  # required (C2)
  # mcp and llm are refused: the sandboxes reach serve through runner.docker.sockets
runner:
  kind: docker
  output: /var/lib/agentswarm/a/turns   # as before
  outputCap: 1048576                    # as before [1 MiB]
  maxTokens: 4096                       # as before [4,096]
  images:
    thinloop: sha256:0000000000000000000000000000000000000000000000000000000000000000
  docker:
    endpoint: unix:///var/run/docker.sock  # optional, this default; unix:// only
    sockets: /run/agentswarm/a             # required: serve listens on mcp.sock and llm.sock here
    store: /var/lib/agentswarm/images      # required: serve keeps a copy of each runtime image here
    group: 990                             # optional: the gid the sockets and the sandboxes share; default serve's own
    labels: {}                             # optional: more labels on each sandbox
    memory: 268435456                      # optional [256 MiB]
    cpus: 1                                # optional [1], whole CPUs
    pids: 64                               # optional [64]
    files: 256                             # optional [256]
    tmp: 16777216                          # optional [16 MiB]
`

// The Docker variant of C1's example loads key by key: its listeners without
// the agents' two, and its runner with its runner.docker block, each key in
// its own field. Its other keys are minimal's.
func TestTheDockerVariantOfTheContractsExampleLoads(t *testing.T) {
	others := minimal()
	delete(others, "listeners")
	delete(others, "runner")
	raw, err := yaml.Marshal(others)
	require.NoError(t, err)

	d, err := deploy.Load(writeText(t, string(raw)+dockerExample))

	require.NoError(t, err)
	assert.Equal(t, deploy.Listeners{Admin: "127.0.0.1:7403", Addresses: "/var/lib/agentswarm/a/addresses.json"}, d.Listeners)
	assert.Equal(t, deploy.Runner{
		Kind: "docker", Output: "/var/lib/agentswarm/a/turns", OutputCap: 1_048_576, MaxTokens: 4096,
		Images: map[string]string{"thinloop": image},
		Docker: &deploy.Docker{
			Endpoint: "unix:///var/run/docker.sock", Sockets: "/run/agentswarm/a", Store: "/var/lib/agentswarm/images", Group: 990,
			Labels: map[string]string{}, Memory: 268_435_456, CPUs: 1, Pids: 64, Files: 256, Tmp: 16_777_216,
		},
	}, d.Runner)
}

// A runner.docker block is the Docker runner's: with the process runner it is
// refused, naming runner.docker, rather than ignored, and so is an empty one,
// which the decoder reads as a block given. A guard: the decoder refused the
// block as no key of a deployment before runner.docker was one, and Check
// must go on refusing it with the process runner once it is.
func TestADockerBlockIsRefusedWithTheProcessRunner(t *testing.T) {
	for _, block := range []map[string]any{
		{"sockets": "/run/agentswarm/a", "store": "/var/lib/agentswarm/images"},
		{},
	} {
		doc := minimal()
		set(doc, "runner.docker", block)

		assert.Equal(t, []string{"runner.docker"}, keys(refused(t, doc)), "%v", block)
	}
}

// The process runner's keys are refused with the Docker runner, each naming
// its key: the agents' listeners, as the sandboxes reach serve through
// runner.docker.sockets, and the process runner's binary and
// unsafeNoSandbox, as each turn runs its runtime's image in a sandbox.
func TestKeysOfTheProcessRunnerAreRefusedWithTheDockerRunner(t *testing.T) {
	for _, tc := range []struct {
		key   string
		value any
	}{
		{key: "listeners.mcp", value: "127.0.0.1:7401"},
		{key: "listeners.llm", value: "127.0.0.1:7402"},
		{key: "runner.binary", value: "/usr/local/bin/agentswarm"},
		{key: "runner.unsafeNoSandbox", value: true},
	} {
		doc := minimalDocker()
		set(doc, tc.key, tc.value)

		ps := refused(t, doc)

		assert.Equal(t, []string{tc.key}, keys(ps), tc.key)
		if len(ps) == 1 {
			assert.Contains(t, ps[0].Message, "docker", tc.key)
		}
	}
}

// The Docker runner needs its sockets directory and its store: without them
// the deployment is refused, naming each, whether its runner.docker block
// leaves them out or the file leaves out the whole block, which reads as one
// given empty. A block without its sockets alone draws that key's own
// refusal and no other: no path is held by a directory that is not there.
// Its store is an absolute path.
func TestTheDockerRunnerNeedsItsSocketsAndStore(t *testing.T) {
	doc := minimalDocker()
	remove(doc, "runner.docker.sockets")
	remove(doc, "runner.docker.store")
	set(doc, "runner.docker.group", 990)
	assert.Equal(t, []string{"runner.docker.sockets", "runner.docker.store"}, keys(refused(t, doc)), "a block without them")

	doc = minimalDocker()
	remove(doc, "runner.docker.sockets")
	ps := refused(t, doc)
	assert.Equal(t, []string{"runner.docker.sockets"}, keys(ps), "a block without its sockets alone")
	if len(ps) == 1 {
		assert.Contains(t, ps[0].Message, "required", "a block without its sockets alone")
	}

	doc = minimalDocker()
	remove(doc, "runner.docker")
	assert.Equal(t, []string{"runner.docker.sockets", "runner.docker.store"}, keys(refused(t, doc)), "no block")

	doc = minimalDocker()
	set(doc, "runner.docker.store", "var/lib/agentswarm/images")
	ps = refused(t, doc)
	assert.Equal(t, []string{"runner.docker.store"}, keys(ps), "a store that is not an absolute path")
	if len(ps) == 1 {
		assert.Contains(t, ps[0].Message, "absolute")
	}
}

// Serve reaches the engine on its Unix socket alone: an endpoint of another
// scheme, or a unix:// endpoint whose path is not absolute, is refused,
// naming runner.docker.endpoint. One at another absolute path is taken.
func TestAnEndpointThatIsNotAUnixSocketIsRefused(t *testing.T) {
	for _, endpoint := range []string{"tcp://127.0.0.1:2375", "ssh://host", "unix://relative.sock"} {
		doc := minimalDocker()
		set(doc, "runner.docker.endpoint", endpoint)

		assert.Equal(t, []string{"runner.docker.endpoint"}, keys(refused(t, doc)), endpoint)
	}
	doc := minimalDocker()
	set(doc, "runner.docker.endpoint", "unix:///run/user/1000/docker.sock")
	d, err := deploy.Load(write(t, doc))
	require.NoError(t, err)
	assert.Equal(t, "unix:///run/user/1000/docker.sock", d.Runner.Docker.Endpoint)
}

// The runner's own labels, which it puts on every sandbox and its sweep
// reads, and agentswarm.data, which make test-clean reads, are refused in
// runner.docker.labels, each naming its key; so is an empty key, naming the
// labels. Other keys, agentswarm.project among them, are the deployment's.
func TestTheRunnersOwnLabelsAreRefused(t *testing.T) {
	for _, key := range []string{
		"agentswarm.tenant", "agentswarm.holder", "agentswarm.turn", "agentswarm.run",
		"agentswarm.agent", "agentswarm.round", "agentswarm.deadline", "agentswarm.data",
	} {
		doc := minimalDocker()
		set(doc, "runner.docker.labels", map[string]any{key: "x", "agentswarm.project": "agent-swarm"})

		assert.Equal(t, []string{"runner.docker.labels." + key}, keys(refused(t, doc)), key)
	}
	doc := minimalDocker()
	set(doc, "runner.docker.labels", map[string]any{"": "x", "agentswarm.project": "agent-swarm"})
	assert.Equal(t, []string{"runner.docker.labels"}, keys(refused(t, doc)), "an empty key")

	doc = minimalDocker()
	set(doc, "runner.docker.labels", map[string]any{"agentswarm.project": "agent-swarm"})
	d, err := deploy.Load(write(t, doc))
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"agentswarm.project": "agent-swarm"}, d.Runner.Docker.Labels)
}

// runner.docker.group is the gid the sockets and the sandboxes share, by
// default -1, serve's own: 0, root's, is refused, and so is a value below
// -1, naming the key. A gid above 0 is taken.
func TestAGroupOf0IsRefused(t *testing.T) {
	for _, group := range []int{0, -2} {
		doc := minimalDocker()
		set(doc, "runner.docker.group", group)

		assert.Equal(t, []string{"runner.docker.group"}, keys(refused(t, doc)), "%d", group)
	}
	doc := minimalDocker()
	set(doc, "runner.docker.group", 990)
	d, err := deploy.Load(write(t, doc))
	require.NoError(t, err)
	assert.Equal(t, 990, d.Runner.Docker.Group)
}

// A sockets directory that no socket could be made in, or that shares a path
// with the deployment's other files, is refused, naming runner.docker.sockets
// and what it meets (Plan 7d-2, Review Focus 5): a relative path; a path
// whose mcp.sock passes a Unix socket's 107 bytes; and a path that lies
// inside, or holds, the secrets, the output, the store, the addresses file,
// the configuration repository or the engine's socket, by their cleaned
// paths. A read-only bind does not stop a connect, so a sockets directory
// that held the engine's socket would hand every sandbox the engine. A path
// whose mcp.sock fits is taken, and so is one that stands apart: the paths
// are compared by their elements, not as strings; the engine's socket is
// held by a directory above it alone; and a relative path, which Check
// cannot place, is left to serve's start.
func TestTheSocketsDirectoryMustStandApartAndFitASocketsPath(t *testing.T) {
	// sized is a sockets path of n bytes, under /run/agentswarm.
	sized := func(n int) string {
		const dir = "/run/agentswarm/"
		return dir + strings.Repeat("s", n-len(dir))
	}
	for _, tc := range []struct {
		name string
		set  map[string]any
		// meets is what the problem's message names: the rule, or the key
		// of the path the sockets directory meets.
		meets string
	}{
		{name: "a relative path", set: map[string]any{"runner.docker.sockets": "run/agentswarm/a"}, meets: "absolute"},
		{name: "a path of 99 bytes, its mcp.sock at 108", set: map[string]any{"runner.docker.sockets": sized(99)}, meets: "107"},
		{
			// The secrets' path does not hold its key's name, which the
			// message must name.
			name: "a path inside the secrets", meets: "secrets",
			set: map[string]any{"secrets": "/etc/agentswarm/keys", "runner.docker.sockets": "/etc/agentswarm/keys/sockets"},
		},
		{
			name: "a path that holds the output, written uncleaned", meets: "runner.output",
			set: map[string]any{"runner.docker.sockets": "/var/lib/agentswarm/a", "runner.output": "/var/lib/agentswarm//a/turns"},
		},
		{name: "the store's path, written uncleaned", set: map[string]any{"runner.docker.sockets": "/var/lib/agentswarm//images"}, meets: "runner.docker.store"},
		{name: "a path that holds the addresses file", set: map[string]any{"runner.docker.sockets": "/run/agentswarm"}, meets: "listeners.addresses"},
		{name: "a path inside the repository", set: map[string]any{"runner.docker.sockets": "/srv/agentswarm/config/sockets"}, meets: "configuration.repository"},
		{name: "a path that holds the default endpoint's socket", set: map[string]any{"runner.docker.sockets": "/var/run"}, meets: "runner.docker.endpoint"},
	} {
		doc := minimalDocker()
		for key, value := range tc.set {
			set(doc, key, value)
		}

		ps := refused(t, doc)

		assert.Equal(t, []string{"runner.docker.sockets"}, keys(ps), tc.name)
		if len(ps) == 1 {
			assert.Contains(t, ps[0].Message, tc.meets, tc.name)
		}
	}
	for _, tc := range []struct {
		name string
		set  map[string]any
	}{
		// Two string prefixes that are not path elements, one each way.
		{name: "a sibling of the repository whose name begins with the repository's", set: map[string]any{"runner.docker.sockets": "/srv/agentswarm/configs"}},
		{name: "a sibling of the repository whose name begins the repository's", set: map[string]any{"runner.docker.sockets": "/srv/agentswarm/conf"}},
		{
			name: "a directory beside the engine's socket",
			set:  map[string]any{"runner.docker.sockets": "/run/agentswarm/a", "runner.docker.endpoint": "unix:///run/docker.sock"},
		},
		// Placed from the root, the secrets would hold the sockets.
		{name: "a relative secrets directory", set: map[string]any{"secrets": "run/agentswarm"}},
	} {
		doc := minimalDocker()
		for key, value := range tc.set {
			set(doc, key, value)
		}

		_, err := deploy.Load(write(t, doc))

		require.NoError(t, err, tc.name)
	}
	doc := minimalDocker()
	set(doc, "runner.docker.sockets", sized(98))
	d, err := deploy.Load(write(t, doc))
	require.NoError(t, err, "a path of 98 bytes, its mcp.sock at 107")
	assert.Len(t, d.Runner.Docker.Sockets, 98)
}

// SocketsApart, which serve's start calls too, with the paths it resolves,
// compares absolute paths alone: a sockets directory left empty, or
// relative, meets nothing, as its own key's refusal names it. Its refusal is
// a Problems naming runner.docker.sockets, and the key it meets in its
// message; a directory that meets nothing gets nil, never a Problems holding
// none.
func TestSocketsApartComparesAbsolutePathsAlone(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sockets string
		from    deploy.Paths
		// meets is the key the refusal names; empty when there is none.
		meets string
	}{
		{name: "a directory inside the secrets", sockets: "/etc/agentswarm/keys/sockets", from: deploy.Paths{Secrets: "/etc/agentswarm/keys"}, meets: "secrets"},
		{name: "a directory apart", sockets: "/run/agentswarm/a", from: deploy.Paths{Secrets: "/etc/agentswarm/keys"}},
		{name: "an empty directory", from: deploy.Paths{Secrets: "/etc/agentswarm/keys"}},
		{name: "a relative directory", sockets: "etc/agentswarm/keys/sockets", from: deploy.Paths{Secrets: "etc/agentswarm/keys"}},
	} {
		err := deploy.SocketsApart(tc.sockets, &tc.from)

		if tc.meets == "" {
			require.NoError(t, err, tc.name)
			continue
		}
		var ps deploy.Problems
		require.ErrorAs(t, err, &ps, tc.name)
		assert.Equal(t, []string{"runner.docker.sockets"}, keys(ps), tc.name)
		if len(ps) == 1 {
			assert.Contains(t, ps[0].Message, tc.meets, tc.name)
		}
	}
}

// The sandbox's limits are each above zero: a limit of 0 is refused, naming
// its key. CPUs are whole (Plan 7d-2, R10), so half a CPU is refused, as the
// decoder reads whole numbers only.
func TestTheSandboxsLimitsMustBeAboveZero(t *testing.T) {
	for _, key := range []string{"memory", "cpus", "pids", "files", "tmp"} {
		doc := minimalDocker()
		set(doc, "runner.docker."+key, 0)

		ps := refused(t, doc)

		assert.Equal(t, []string{"runner.docker." + key}, keys(ps), key)
		if len(ps) == 1 {
			assert.Contains(t, ps[0].Message, "above zero", key)
		}
	}
	doc := minimalDocker()
	set(doc, "runner.docker.cpus", 0.5)
	ps := refused(t, doc)
	assert.Equal(t, []string{"runner.docker.cpus"}, keys(ps), "half a CPU")
	if len(ps) == 1 {
		assert.Contains(t, ps[0].Message, "whole number", "half a CPU")
	}
}
