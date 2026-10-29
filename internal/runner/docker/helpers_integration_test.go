//go:build integration && linux

package docker

// The helpers every integration test of the package shares (Plan 7d-2,
// Contract C15). They run in the Linux test container that make test-docker
// starts, on the engine whose socket it mounts:
//   - newEngine, the engine through the runner's own port;
//   - newClient, a moby client, for what the tests do that the port does not;
//   - newTenant, a tenant of each test's own, whose containers go when it ends;
//   - itLabels, the labels every container a test makes carries;
//   - importImage, a throwaway image of the fields under test (R14);
//   - probeID, the probe image, the runnable image of the tests;
//   - socketsDir and itOptions, what a sandbox of the runner's settings needs.

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"slices"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/require"
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
			id := listed.Items[i].ID
			_, err := c.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
			if err != nil && !cerrdefs.IsNotFound(err) {
				t.Errorf("remove the container %s of the tenant %s: %v", id, tenant, err)
			}
		}
	})
	return tenant
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
// BuildKit (R14). It is tagged agentswarm-it:<8 hex digits> and, when run.sh
// set AGENTSWARM_IT_RUN, labelled itRunLabel with that id. It returns the
// image's id, and removes the image when the test ends.
func importImage(t *testing.T, c *client.Client, changes ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), itWait)
	defer cancel()
	var layer bytes.Buffer
	w := tar.NewWriter(&layer)
	content := []byte(hex8() + hex8())
	require.NoError(t, w.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "random", Mode: 0o644, Size: int64(len(content))}))
	_, err := w.Write(content)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	if run := os.Getenv("AGENTSWARM_IT_RUN"); run != "" {
		// Clipped, so that the label never lands in a caller's spare capacity.
		changes = append(slices.Clip(changes), "LABEL "+itRunLabel+"="+run)
	}
	ref := "agentswarm-it:" + hex8()
	out, err := c.ImageImport(ctx, client.ImageImportSource{Source: &layer, SourceName: "-"}, ref, client.ImageImportOptions{Changes: changes})
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

// socketsDir makes a sockets directory of the test's own under e2eDir, mode
// 0750, which is the same path on the engine's host, where a sandbox's bind
// mount needs it; it is removed when the test ends.
func socketsDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(e2eDir, "it-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	require.NoError(t, os.Chmod(dir, 0o750))
	return dir
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
