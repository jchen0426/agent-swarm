//go:build integration && linux

package docker

// The helpers every integration test of the package shares (Plan 7d-2,
// Contract C15). They run in the Linux test container that make test-docker
// starts, on the engine whose socket it mounts:
//   - newEngine, the engine through the runner's own port;
//   - newClient, a moby client, for what the tests do that the port does not;
//   - newTenant, a tenant of each test's own, whose containers go when it ends;
//   - itLabels, the labels every container a test makes carries;
//   - importImage and importLayer, throwaway images of the fields under test
//     (R14);
//   - probeID, the probe image, the runnable image of the tests;
//   - hostDir, socketsDir and itOptions, what a sandbox of the runner's
//     settings needs;
//   - itPorts, the ports a runner of a test opens with;
//   - openRunner, liveRunner and liveTurn, a runner of a test on the engine,
//     what it reports, and its turns.

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/holding"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/tokens"
)

const (
	// engineEndpoint is the engine's socket, as the test container mounts it.
	engineEndpoint = "unix:///var/run/docker.sock"
	// probeImage is the image the tests run under the sandbox's settings, in
	// place of a runtime: curl 8.23.0 with busybox, pinned by the digest of
	// its index, which covers amd64 and arm64. It is pulled by that digest
	// alone, never by a tag, and stays on the engine for later runs.
	probeImage = "curlimages/curl@sha256:11e64e28c5f58c8d93ee95fcf22ac055f48b0bc440e0ce7d8013bbb5ad6aa8ac"
	// e2eDir holds the directories a sandbox mounts: the targets make it on
	// the engine's host, and mount it at the same path in the test container.
	e2eDir = "/srv/agentswarm-e2e"
	// itRunLabel is the label that carries the id of a build/test/run.sh
	// invocation, AGENTSWARM_IT_RUN, by which run.sh removes, at its end, the
	// containers, and then the images, its tests' cleanups never reached.
	// Its key lies outside agentswarm., which the image contract and the
	// deployment's labels keep.
	itRunLabel = "agentswarm-it.run"
	// itWait bounds what a test asks of the engine.
	itWait = 2 * time.Minute
)

// hex8 returns 8 random hexadecimal digits.
func hex8() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// newEngine returns the engine through the runner's port, Dial's, closed when
// the test ends.
func newEngine(t *testing.T) Engine {
	t.Helper()
	e, err := Dial(engineEndpoint)
	require.NoError(t, err)
	t.Cleanup(func() { _ = e.Close() })
	return e
}

// newClient returns a moby client of the engine, closed when the test ends,
// for what the tests do that the runner's port does not: pull, import and
// remove images, and list and remove a tenant's containers.
func newClient(t *testing.T) *client.Client {
	t.Helper()
	c, err := client.New(client.WithHost(engineEndpoint))
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// newTenant returns a tenant of the test's own, it-<8 hex digits>, so that no
// two tests, nor two gates on one engine, sweep each other's containers; when
// the test ends, every container labelled with the tenant is removed, running
// or not, with its volumes.
func newTenant(t *testing.T, c *client.Client) string {
	t.Helper()
	tenant := "it-" + hex8()
	t.Cleanup(func() {
		// t.Context has ended by the time a cleanup runs.
		ctx, cancel := context.WithTimeout(context.Background(), itWait)
		defer cancel()
		listed, err := c.ContainerList(ctx, client.ContainerListOptions{
			All: true, Filters: make(client.Filters).Add("label", labelTenant+"="+tenant),
		})
		if err != nil {
			t.Errorf("list the containers of the tenant %s: %v", tenant, err)
			return
		}
		for i := range listed.Items {
			removeContainer(t, c, listed.Items[i].ID)
		}
	})
	return tenant
}

// removeContainer removes the container id from the engine, running or not,
// with its volumes; a container already gone is no failure.
func removeContainer(t *testing.T, c *client.Client, id string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), itWait)
	defer cancel()
	_, err := c.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
	if err != nil && !cerrdefs.IsNotFound(err) {
		t.Errorf("remove the container %s: %v", id, err)
	}
}

// itLabels returns the labels every container a test makes carries:
// agentswarm.project=agent-swarm, by which make test-clean finds one a killed
// run left running, and, when run.sh set AGENTSWARM_IT_RUN, itRunLabel with
// that id.
func itLabels() map[string]string {
	labels := map[string]string{"agentswarm.project": "agent-swarm"}
	if run := os.Getenv("AGENTSWARM_IT_RUN"); run != "" {
		labels[itRunLabel] = run
	}
	return labels
}

// importImage imports a throwaway image of one random file, with changes,
// Dockerfile instructions such as "VOLUME /data", so that no test needs
// BuildKit (R14), as importLayer does.
func importImage(t *testing.T, c *client.Client, changes ...string) string {
	t.Helper()
	var layer bytes.Buffer
	w := tar.NewWriter(&layer)
	content := []byte(hex8() + hex8())
	require.NoError(t, w.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "random", Mode: 0o644, Size: int64(len(content))}))
	_, err := w.Write(content)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return importLayer(t, c, &layer, changes...)
}

// importLayer imports a throwaway image of the filesystem the tar archive
// layer holds, with changes. It is tagged agentswarm-it:<8 hex digits> and,
// when run.sh set AGENTSWARM_IT_RUN, labelled itRunLabel with that id. It
// returns the image's id, and removes the image when the test ends.
func importLayer(t *testing.T, c *client.Client, layer io.Reader, changes ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), itWait)
	defer cancel()
	if run := os.Getenv("AGENTSWARM_IT_RUN"); run != "" {
		// Clipped, so that the label never lands in a caller's spare capacity.
		changes = append(slices.Clip(changes), "LABEL "+itRunLabel+"="+run)
	}
	ref := "agentswarm-it:" + hex8()
	out, err := c.ImageImport(ctx, client.ImageImportSource{Source: layer, SourceName: "-"}, ref, client.ImageImportOptions{Changes: changes})
	require.NoError(t, err)
	require.NoError(t, readMessages(out), "import an image with %q", changes)
	imported, err := c.ImageInspect(ctx, ref)
	require.NoError(t, err)
	t.Cleanup(func() { removeImage(t, c, imported.ID) })
	return imported.ID
}

// removeImage removes the image id from the engine, every tag of it with it;
// an image already gone is no failure. It removes nothing else, not even an
// untagged parent.
func removeImage(t *testing.T, c *client.Client, id string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), itWait)
	defer cancel()
	if _, err := c.ImageRemove(ctx, id, client.ImageRemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
		t.Errorf("remove the image %s: %v", id, err)
	}
}

// probeID returns the probe image's id on the engine, pulling the image by
// its digest when the engine lacks it.
func probeID(t *testing.T, c *client.Client) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), itWait)
	defer cancel()
	found, err := c.ImageInspect(ctx, probeImage)
	if cerrdefs.IsNotFound(err) {
		pulled, pullErr := c.ImagePull(ctx, probeImage, client.ImagePullOptions{})
		require.NoError(t, pullErr, "pull the probe image %s", probeImage)
		require.NoError(t, pulled.Wait(ctx), "pull the probe image %s", probeImage)
		require.NoError(t, pulled.Close())
		found, err = c.ImageInspect(ctx, probeImage)
	}
	require.NoError(t, err)
	return found.ID
}

// hostDir makes a directory of the test's own under e2eDir, named by
// pattern as os.MkdirTemp names one, mode 0750, which is the same path on
// the engine's host; it is removed when the test ends.
func hostDir(t *testing.T, pattern string) string {
	t.Helper()
	dir, err := os.MkdirTemp(e2eDir, pattern)
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	require.NoError(t, os.Chmod(dir, 0o750))
	return dir
}

// socketsDir makes a sockets directory of the test's own (hostDir), where a
// sandbox's bind mount needs it.
func socketsDir(t *testing.T) string {
	t.Helper()
	return hostDir(t, "it-")
}

// itOptions returns options that run sandboxes under the tenant: the
// deployment's defaults, with the holder it, a sockets directory and a store
// of the test's own, the test's own group, and the labels every container a
// test makes carries.
func itOptions(t *testing.T, tenant string) Options {
	t.Helper()
	o := testOptions(t)
	o.Tenant, o.Holder = tenant, "it"
	o.Sockets = socketsDir(t)
	o.Group = os.Getgid()
	o.Labels = itLabels()
	require.NoError(t, o.check())
	return o
}

// itPorts returns the ports a runner of a test opens with: the engine, a
// holding that holds, the tests' skills, a board that holds no turn's end,
// an Exited and a Launched that keep nothing, which a test that reads them
// replaces, and the test's own log.
func itPorts(t *testing.T, engine Engine) Ports {
	t.Helper()
	held := &holdingFake{}
	held.valid.Store(true)
	return Ports{
		Engine:   engine,
		Holding:  held,
		Skills:   func(context.Context, string, string) ([]string, error) { return slices.Clone(testSkills), nil },
		Ended:    func(context.Context, board.RunID, int, string) (bool, error) { return false, nil },
		Exited:   func(app.Exit) {},
		Launched: func(app.Launch) {},
		Log:      slog.New(slog.NewTextHandler(t.Output(), nil)),
	}
}

// liveRunner is a runner a test opened on the live engine (openRunner), with
// what it reported: each turn's launch, and each turn's end, with the turn's
// container as the engine showed it then.
type liveRunner struct {
	r *Runner
	o Options
	c *client.Client

	mu       sync.Mutex
	started  []string // the turns started through start, by their ids
	launches []app.Launch
	ends     []*liveEnd
}

// liveEnd is a turn's end as its runner reported it, with the turn's
// container as the engine showed it while the report ran: the runner removes
// a container that has ended only once the report has returned.
type liveEnd struct {
	app.Exit
	container *container.InspectResponse
	// inspectErr is why the engine did not show the container, if it did
	// not.
	inspectErr error
}

// shownContainer returns the turn's container as the engine showed it when
// the turn's end was reported, and fails the test if the engine did not.
func (end *liveEnd) shownContainer(t *testing.T) *container.InspectResponse {
	t.Helper()
	require.NotNil(t, end.container, "the engine did not show the turn's container: %v", end.inspectErr)
	return end.container
}

// openRunner opens a runner on the live engine, Dial's, with the options o
// and itPorts's ports, whose Launched and Exited it records (liveRunner).
// When the test ends, as serve's exit does, it kills every turn the runner
// has (KillAll) and closes the runner. Between the two, it waits for the
// report of each turn started through it, and for the removal of its
// container, which follows the report of its end: so no goroutine of the
// runner's writes to the test's log after the test, and the tenant's own
// removals (newTenant) never meet the runner's. Close's failure fails the
// test. Close runs once, and a second Close returns the first's error, so
// a test that closes the runner itself gets the same assertion from the
// cleanup: nil, if its own Close succeeded.
//
//nolint:gocritic // hugeParam: Open takes the options by value.
func openRunner(t *testing.T, c *client.Client, o Options) *liveRunner {
	t.Helper()
	lr, err := tryOpen(t, c, o)
	require.NoError(t, err)
	return lr
}

// tryOpen is openRunner, but returns Open's refusal, having closed the
// engine, which a refusal leaves to its caller.
//
//nolint:gocritic // hugeParam: Open takes the options by value.
func tryOpen(t *testing.T, c *client.Client, o Options) (*liveRunner, error) {
	t.Helper()
	engine, err := Dial(engineEndpoint)
	require.NoError(t, err)
	lr := &liveRunner{o: o, c: c}
	p := itPorts(t, engine)
	p.Launched, p.Exited = lr.launched, lr.exited
	r, err := Open(t.Context(), p, o)
	if err != nil {
		_ = engine.Close()
		return nil, err
	}
	lr.r = r
	t.Cleanup(func() {
		// t.Context has ended by the time a cleanup runs.
		r.KillAll(context.Background())
		assert.Eventually(t, lr.allReported, itWait, 50*time.Millisecond, "a turn's launch or end was never reported")
		lr.mu.Lock()
		started := slices.Clone(lr.started)
		lr.mu.Unlock()
		for _, id := range started {
			lr.awaitGone(t, id)
		}
		assert.NoError(t, r.Close(), "the runner did not close")
	})
	return lr, nil
}

// launched records the report of a turn's launch.
//
//nolint:gocritic // hugeParam: Ports.Launched takes the launch by value.
func (lr *liveRunner) launched(l app.Launch) {
	lr.mu.Lock()
	defer lr.mu.Unlock()
	lr.launches = append(lr.launches, l)
}

// exited records a turn's end, with the turn's container as the engine
// shows it while the runner reports the end.
func (lr *liveRunner) exited(x app.Exit) {
	ctx, cancel := context.WithTimeout(context.Background(), itWait)
	defer cancel()
	end := &liveEnd{Exit: x}
	if shown, err := lr.c.ContainerInspect(ctx, containerName(x.Turn), client.ContainerInspectOptions{}); err != nil {
		end.inspectErr = err
	} else {
		end.container = &shown.Container
	}
	lr.mu.Lock()
	defer lr.mu.Unlock()
	lr.ends = append(lr.ends, end)
}

// allReported reports whether each turn started through the runner has been
// reported: its end, or its launch's failure.
func (lr *liveRunner) allReported() bool {
	lr.mu.Lock()
	defer lr.mu.Unlock()
	for _, id := range lr.started {
		if !slices.ContainsFunc(lr.ends, func(e *liveEnd) bool { return e.Turn == id }) &&
			!slices.ContainsFunc(lr.launches, func(l app.Launch) bool { return l.Turn == id && l.Err != nil }) {
			return false
		}
	}
	return true
}

// liveTurn returns a turn of the image id for a runner on the live engine:
// an id of its own, it-<8 hex digits>, so that the engine has no container
// of its name, and a session's token for each gateway.
func liveTurn(image string) *app.Turn {
	turn := testTurn(image)
	turn.ID = "it-" + hex8()
	turn.MCP, turn.LLM = tokens.New(tokens.MCP), tokens.New(tokens.LLM)
	return turn
}

// start starts the turn on a context of the coordinator's, which carries the
// holding's epoch, and fails the test unless its launch is left pending.
func (lr *liveRunner) start(t *testing.T, turn *app.Turn) {
	t.Helper()
	pending, err := lr.r.Start(holding.WithEpoch(t.Context(), epoch), turn)
	require.NoError(t, err)
	require.True(t, pending, "the turn's launch was not left pending")
	lr.mu.Lock()
	defer lr.mu.Unlock()
	lr.started = append(lr.started, turn.ID)
}

// launch waits for the report of the turn's launch, and returns it.
func (lr *liveRunner) launch(t *testing.T, turn *app.Turn) app.Launch {
	t.Helper()
	var found app.Launch
	require.Eventually(t, func() bool {
		lr.mu.Lock()
		defer lr.mu.Unlock()
		i := slices.IndexFunc(lr.launches, func(l app.Launch) bool { return l.Turn == turn.ID })
		if i >= 0 {
			found = lr.launches[i]
		}
		return i >= 0
	}, itWait, 50*time.Millisecond, "the launch of the turn %s was not reported", turn.ID)
	return found
}

// end waits for the report of the turn's end, and returns it.
func (lr *liveRunner) end(t *testing.T, turn *app.Turn) *liveEnd {
	t.Helper()
	var found *liveEnd
	require.Eventually(t, func() bool {
		lr.mu.Lock()
		defer lr.mu.Unlock()
		i := slices.IndexFunc(lr.ends, func(e *liveEnd) bool { return e.Turn == turn.ID })
		if i >= 0 {
			found = lr.ends[i]
		}
		return i >= 0
	}, itWait, 50*time.Millisecond, "the end of the turn %s was not reported", turn.ID)
	return found
}

// reported returns the launches and the ends the runner has reported, in
// order.
func (lr *liveRunner) reported() ([]app.Launch, []app.Exit) {
	lr.mu.Lock()
	defer lr.mu.Unlock()
	exits := make([]app.Exit, 0, len(lr.ends))
	for _, e := range lr.ends {
		exits = append(exits, e.Exit)
	}
	return slices.Clone(lr.launches), exits
}

// awaitGone waits until the engine has no container of the turn id,
// labelled agentswarm.turn with it, in any state.
func (lr *liveRunner) awaitGone(t *testing.T, id string) {
	t.Helper()
	assert.Eventually(t, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		listed, err := lr.c.ContainerList(ctx, client.ContainerListOptions{
			All: true, Filters: make(client.Filters).Add("label", labelTurn+"="+id),
		})
		return err == nil && len(listed.Items) == 0
	}, itWait, 50*time.Millisecond, "a container of the turn %s is still on the engine", id)
}

// runs reports whether the engine shows the turn's container running: a
// container gone runs no more; the engine's failure to show it is an error.
func (lr *liveRunner) runs(turn *app.Turn) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	shown, err := lr.c.ContainerInspect(ctx, containerName(turn.ID), client.ContainerInspectOptions{})
	switch {
	case cerrdefs.IsNotFound(err):
		return false, nil
	case err != nil:
		return false, err
	}
	return shown.Container.State != nil && shown.Container.State.Running, nil
}

// attachment returns the attachment the runner holds of the turn, which
// runs.
func (lr *liveRunner) attachment(t *testing.T, turn *app.Turn) Attachment {
	t.Helper()
	lr.r.mu.Lock()
	defer lr.r.mu.Unlock()
	tr, ok := lr.r.turns[turn.ID]
	require.True(t, ok, "the runner does not have the turn")
	require.NotNil(t, tr.att, "the turn has no attachment")
	return tr.att
}
