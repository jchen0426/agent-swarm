package deploy

import (
	"maps"
	"path/filepath"
	"slices"
	"strings"
)

// servesGroup is runner.docker.group's default, -1: serve's own group, which
// serve resolves at its start (Plan 7d-2, R9).
const servesGroup = -1

// defaultDocker is the runner.docker block of a file that gives the block and
// leaves out its optional keys (Contract C1).
var defaultDocker = Docker{
	Endpoint: "unix:///var/run/docker.sock",
	Group:    servesGroup,
	Memory:   256 << 20,
	CPUs:     1,
	Pids:     64,
	Files:    256,
	Tmp:      16 << 20,
}

// Docker is runner.docker (Contract C1), its defaults filled in: the Docker
// runner's engine, its sockets directory and store, and its sandboxes'
// group, labels and limits.
type Docker struct {
	// Endpoint is the engine's socket, unix:// and an absolute path
	// [unix:///var/run/docker.sock].
	Endpoint string `yaml:"endpoint"`
	// Sockets is the directory serve listens on mcp.sock and llm.sock in,
	// which every sandbox mounts: an absolute path.
	Sockets string `yaml:"sockets"`
	// Store is the directory serve keeps a copy of each runtime image in: an
	// absolute path.
	Store string `yaml:"store"`
	// Group is the gid the sockets and the sandboxes share; -1, the default,
	// is serve's own group.
	Group int `yaml:"group"`
	// Labels are more labels on each sandbox, none of them the runner's own.
	Labels map[string]string `yaml:"labels"`
	// The sandbox's limits (Plan 7d-2, Decision 3): its memory in bytes
	// [256 MiB]; its whole CPUs [1] (Plan 7d-2, R10); its processes [64]; its
	// open files [256]; and the size of its /tmp in bytes [16 MiB].
	Memory int64 `yaml:"memory"`
	CPUs   int64 `yaml:"cpus"`
	Pids   int64 `yaml:"pids"`
	Files  int64 `yaml:"files"`
	Tmp    int64 `yaml:"tmp"`
}

// dockerRunner refuses the Docker runner with the process runner's binary or
// unsafeNoSandbox, and what C1 refuses of its runner.docker block that needs
// no system: an endpoint that is not unix:// and an absolute path; a sockets
// directory that cannot serve (sockets); a store left out, or not an
// absolute path; a group of 0 or below -1; a label the deployment may not
// set (labels); and a limit that is not above zero. Serve's start refuses the
// rest (Plan 7d-2, Decision 8). A file that leaves out the block is checked
// as one that gives it empty: its defaults, and no sockets or store.
func (c *checker) dockerRunner(d *Deployment) {
	r := &d.Runner
	if r.Binary != "" {
		c.add("runner.binary", "is refused with kind %s: it is the process runner's, and each sandbox runs its runtime's image", KindDocker)
	}
	if r.UnsafeNoSandbox {
		c.add("runner.unsafeNoSandbox", "is refused with kind %s: it is the process runner's, and each turn runs in a sandbox", KindDocker)
	}
	b := defaultDocker
	if r.Docker != nil {
		b = *r.Docker
	}
	if path, ok := strings.CutPrefix(b.Endpoint, "unix://"); !ok || !filepath.IsAbs(path) {
		c.add("runner.docker.endpoint", "must be unix:// and an absolute path, such as unix:///var/run/docker.sock: serve reaches the engine by its Unix socket alone")
	}
	c.sockets(d, &b)
	if c.required("runner.docker.store", b.Store) && !filepath.IsAbs(b.Store) {
		c.add("runner.docker.store", "must be an absolute path")
	}
	if b.Group == 0 || b.Group < servesGroup {
		c.add("runner.docker.group", "must be a gid above 0, or %d for serve's own group: the sockets and the sandboxes share it, and 0 is root's", servesGroup)
	}
	c.labels(b.Labels)
	for _, limit := range []struct {
		key   string
		value int64
	}{{"memory", b.Memory}, {"cpus", b.CPUs}, {"pids", b.Pids}, {"files", b.Files}, {"tmp", b.Tmp}} {
		c.positive("runner.docker."+limit.key, limit.value, false)
	}
}

// socketsKey is the key of the Docker runner's sockets directory.
const socketsKey = "runner.docker.sockets"

// maxSocketPath is the most bytes a Unix socket's path may hold on Linux,
// whose sun_path holds 108 with the terminating NUL.
const maxSocketPath = 107

// sockets refuses a sockets directory left out, or not an absolute path; one
// whose mcp.sock, and so llm.sock, of the same length, would pass
// maxSocketPath bytes; and one that does not stand apart from the
// deployment's other paths, the engine's socket among them, by their cleaned
// paths (apart). A relative one of those is left to serve's start.
func (c *checker) sockets(d *Deployment, b *Docker) {
	if !c.required(socketsKey, b.Sockets) {
		return
	}
	dir := filepath.Clean(b.Sockets)
	if !filepath.IsAbs(dir) {
		c.add(socketsKey, "must be an absolute path")
		return
	}
	if n := len(filepath.Join(dir, "mcp.sock")); n > maxSocketPath {
		c.add(socketsKey, "gives mcp.sock a path of %d bytes, above the %d a Unix socket's path may hold", n, maxSocketPath)
	}
	var engine string
	if path, ok := strings.CutPrefix(b.Endpoint, "unix://"); ok {
		engine = clean(path)
	}
	c.apart(dir, &Paths{
		Secrets: clean(d.Secrets), Output: clean(d.Runner.Output), Store: clean(b.Store),
		Addresses: clean(d.Listeners.Addresses), Repository: clean(d.Configuration.Repository), Engine: engine,
	})
}

// clean is a path cleaned, and an empty one left empty, which filepath.Clean
// would make ".".
func clean(path string) string {
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

// Paths are the deployment's paths that its sockets directory stands apart
// from (Plan 7d-2, Decision 8): each the path of the key its comment names,
// or empty.
type Paths struct {
	Secrets    string // secrets
	Output     string // runner.output
	Store      string // runner.docker.store
	Addresses  string // listeners.addresses
	Repository string // configuration.repository
	// Engine is the engine's socket, the path of a unix://
	// runner.docker.endpoint. A read-only bind does not stop a connect, so a
	// sockets directory that held it would hand every sandbox the engine.
	Engine string
}

// SocketsApart refuses runner.docker.sockets, the sockets directory at
// sockets, when it lies inside, or holds, a path of from: every sandbox
// mounts the directory, which stands apart from the deployment's other paths
// (Plan 7d-2, Decision 8). It fails with Problems, one for each path the
// directory meets, naming runner.docker.sockets and, in its message, the
// path's key; and returns nil when it meets none.
//
// It compares absolute, clean paths alone, by their names: a path of from
// that is empty or relative is skipped, as it cannot place it, and a sockets
// that is empty or relative meets nothing, as runner.docker.sockets' own
// refusal names it. So a caller with relative paths makes them absolute
// first. Its two callers give it the deployment's paths, the sockets
// directory's among them: Check, as the file gives them, cleaned, leaving a
// relative one to serve's start; serve's start (listenSockets), each made
// absolute and resolved on its system, its symbolic links followed, with ""
// for one that does not exist.
func SocketsApart(sockets string, from *Paths) error {
	var c checker
	c.apart(sockets, from)
	if len(c.problems) > 0 {
		return c.problems
	}
	return nil
}

// apart is SocketsApart's rule, which Check applies too: a problem for each
// path of from that the sockets directory meets.
func (c *checker) apart(sockets string, from *Paths) {
	if !filepath.IsAbs(sockets) {
		return
	}
	for _, p := range []struct{ key, path string }{
		{"secrets", from.Secrets},
		{"runner.output", from.Output},
		{"runner.docker.store", from.Store},
		{"listeners.addresses", from.Addresses},
		{"configuration.repository", from.Repository},
		{"runner.docker.endpoint", from.Engine},
	} {
		if !filepath.IsAbs(p.path) {
			continue
		}
		switch {
		case inside(sockets, p.path):
			c.add(socketsKey, "must lie outside %s, %s: every sandbox mounts the sockets directory, which stands apart from the deployment's other paths", p.key, p.path)
		case inside(p.path, sockets):
			c.add(socketsKey, "must not hold %s, %s: every sandbox mounts the sockets directory, and would see what it holds", p.key, p.path)
		}
	}
}

// inside reports whether path is dir, or lies inside it, both clean paths.
func inside(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, strings.TrimSuffix(dir, "/")+"/")
}

// runnersLabels are the labels the Docker runner puts on every sandbox, which
// its sweep reads (Plan 7d-2, Contract C13): runner.docker.labels may set none
// of them.
var runnersLabels = []string{
	"agentswarm.tenant", "agentswarm.holder", "agentswarm.turn", "agentswarm.run",
	"agentswarm.agent", "agentswarm.round", "agentswarm.deadline",
}

// dataLabel is make test-clean's label: before it removes a container, it
// saves the files at the label's path. runner.docker.labels may not set it.
const dataLabel = "agentswarm.data"

// labels refuses an empty key of runner.docker.labels, and a key the runner
// or make test-clean reads, each named by its key.
func (c *checker) labels(labels map[string]string) {
	for _, key := range slices.Sorted(maps.Keys(labels)) {
		switch {
		case key == "":
			c.add("runner.docker.labels", "holds an empty key")
		case slices.Contains(runnersLabels, key):
			c.add("runner.docker.labels."+key, "is the runner's own label, which it puts on every sandbox, and its sweep reads")
		case key == dataLabel:
			c.add("runner.docker.labels."+key, "is make test-clean's label: it saves the files at the label's path before it removes a container")
		}
	}
}
