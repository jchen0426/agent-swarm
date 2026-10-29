//go:build integration && linux

package docker

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTheMobyEngineRunsAContainerOfTheSandboxsSettings: the engine takes the
// sandbox's settings (Contract C13) around the probe image, which runs cat in
// place of a runtime. What the test writes on the attachment comes back on
// its output; closing the attachment closes the container's input, so cat
// ends, with 0; a kill of the ended container succeeds (C14, amended), as a
// kill that races a container's end must; and the container, once removed,
// is gone.
func TestTheMobyEngineRunsAContainerOfTheSandboxsSettings(t *testing.T) {
	c := newClient(t)
	e := newEngine(t)
	o := itOptions(t, newTenant(t, c))
	o.probe = []string{"cat"}
	turn := testTurn(probeID(t, c))
	turn.ID = "it-" + hex8()
	ctx, cancel := context.WithTimeout(t.Context(), itWait)
	defer cancel()

	name, cfg, host := o.sandbox(turn)
	id, err := e.Create(ctx, name, cfg, host)
	require.NoError(t, err)
	attachment, err := e.Attach(ctx, id)
	require.NoError(t, err)
	// Close is idempotent: this closes the attachment should the test end
	// before its own Close below.
	t.Cleanup(func() { _ = attachment.Close() })
	require.NoError(t, e.Start(ctx, id))
	_, err = io.WriteString(attachment, "a line\n")
	require.NoError(t, err)
	echo := make([]byte, len("a line\n"))
	_, err = io.ReadFull(attachment.Output(), echo)
	require.NoError(t, err)
	assert.Equal(t, "a line\n", string(echo))
	require.NoError(t, attachment.Close())

	waitCtx, waitCancel := context.WithTimeout(ctx, 10*time.Second)
	defer waitCancel()
	require.NoError(t, e.Wait(waitCtx, id), "the container ends once its attachment closes")
	ended, err := e.Inspect(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, id, ended.ID)
	assert.Equal(t, name, ended.Name)
	assert.Equal(t, turn.ID, ended.Labels[labelTurn])
	assert.False(t, ended.Running)
	assert.Equal(t, 0, ended.ExitCode)
	assert.False(t, ended.OOMKilled)
	assert.False(t, ended.Created.IsZero())
	assert.False(t, ended.Finished.IsZero(), "Inspect shows when the container finished")
	require.NoError(t, e.Kill(ctx, id), "a kill of a container that has ended succeeds")

	require.NoError(t, e.Remove(ctx, id))
	_, err = e.Inspect(ctx, id)
	require.ErrorIs(t, err, ErrNoContainer)
}

// TestTheMobyEngineReadsAnImagesContractFields: Image gives the fields the
// contract reads (Contract C12) as the engine holds them.
func TestTheMobyEngineReadsAnImagesContractFields(t *testing.T) {
	c := newClient(t)
	e := newEngine(t)
	id := importImage(t, c, "VOLUME /data", "HEALTHCHECK CMD true", "ENV A=b", "LABEL agentswarm.x=y", "ONBUILD RUN true")
	ctx, cancel := context.WithTimeout(t.Context(), itWait)
	defer cancel()

	info, err := e.Version(ctx)
	require.NoError(t, err)
	assert.Equal(t, "linux", info.OS)
	img, err := e.Image(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, id, img.ID)
	assert.Equal(t, "linux", img.OS)
	assert.Equal(t, info.Arch, img.Arch)
	assert.Equal(t, []string{"/data"}, img.Volumes)
	assert.Equal(t, []string{"CMD-SHELL", "true"}, img.Healthcheck)
	assert.Equal(t, []string{"A=b"}, img.Env)
	assert.Equal(t, "y", img.Labels["agentswarm.x"])
	assert.Equal(t, []string{"RUN true"}, img.OnBuild)
}

// TestAnImageTheEngineLostIsReloadedFromTheStoreAndTagged (Review Focus 3): an
// image saved to the store, then pruned from the engine, comes back from the
// store with its id, tagged agentswarm-runtime:<hex>, so that a plain prune
// does not take it again. Ids differ between the engine's image stores, so
// the test compares them on this engine alone.
func TestAnImageTheEngineLostIsReloadedFromTheStoreAndTagged(t *testing.T) {
	c := newClient(t)
	e := newEngine(t)
	id := importImage(t, c)
	s := store{dir: t.TempDir()}
	ctx, cancel := context.WithTimeout(t.Context(), itWait)
	defer cancel()

	require.NoError(t, s.save(ctx, e, id))
	require.True(t, s.has(id))
	removeImage(t, c, id)
	_, err := e.Image(ctx, id)
	require.ErrorIs(t, err, ErrNoImage, "the engine lost the image")

	loaded, err := s.reload(ctx, e, id)
	require.NoError(t, err)
	assert.Equal(t, id, loaded.ID)
	reloaded, err := e.Image(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, id, reloaded.ID)
	tagged, err := e.Image(ctx, "agentswarm-runtime:"+strings.TrimPrefix(id, "sha256:"))
	require.NoError(t, err)
	assert.Equal(t, id, tagged.ID)
}

// TestAnEngineThatIsNotThereIsUnreachable: no engine at the endpoint reads
// ErrUnreachable; on a live engine, an unknown image reads ErrNoImage, and
// an unknown container ErrNoContainer, which a launch's first step, the
// removal of a container left by an earlier attempt, meets for most turns.
func TestAnEngineThatIsNotThereIsUnreachable(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), itWait)
	defer cancel()
	absent, err := Dial("unix:///nonexistent.sock")
	require.NoError(t, err)
	t.Cleanup(func() { _ = absent.Close() })
	_, err = absent.Version(ctx)
	require.ErrorIs(t, err, ErrUnreachable)

	e := newEngine(t)
	_, err = e.Image(ctx, "sha256:"+strings.Repeat(hex8(), 8))
	require.ErrorIs(t, err, ErrNoImage)
	name := containerName("it-" + hex8())
	_, err = e.Inspect(ctx, name)
	require.ErrorIs(t, err, ErrNoContainer)
	require.ErrorIs(t, e.Kill(ctx, name), ErrNoContainer)
	require.ErrorIs(t, e.Remove(ctx, name), ErrNoContainer)
}
