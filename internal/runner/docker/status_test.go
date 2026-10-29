package docker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/app"
)

// TestTheMinuteCheckReloadsAnImageTheEngineLostAndNamesWhatIsMissing (R7;
// Decision 6): every minute, the runner checks each image of runner.images.
// One the engine lost comes back from the store, tagged, and its reload is
// logged; one neither on the engine nor in the store, or that breaks the
// contract, is listed, by its id, in Status's MissingImages, sorted, and
// logged an image is missing, at Error, with its id and why. Once back, it
// leaves the list. The images' names sort apart from their ids, so that a
// list in the names' order shows.
func TestTheMinuteCheckReloadsAnImageTheEngineLostAndNamesWhatIsMissing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newFixture(t)
		lost, gone, broken := goodImage(), goodImage(), goodImage()
		gone.ID, broken.ID = "sha256:"+strings.Repeat("c", 64), "sha256:"+strings.Repeat("b", 64)
		e.fe.addImage(gone)
		e.fe.addImage(broken)
		e.o.Images = map[string]string{"thinloop": lost.ID, "alpha": gone.ID, "beta": broken.ID}
		e.open(t)
		e.fe.forgetCalls()
		e.fe.lose(lost.ID)
		e.fe.lose(gone.ID)
		require.NoError(t, os.Remove(filepath.Join(e.o.Store, strings.TrimPrefix(gone.ID, "sha256:")+".tar")))
		breaks := *broken
		breaks.Volumes = []string{"/data"}
		e.fe.addImage(&breaks)

		time.Sleep(time.Minute)
		synctest.Wait()

		hex := strings.TrimPrefix(lost.ID, "sha256:")
		assert.Contains(t, e.fe.said(), "Load", "the lost image was not loaded from the store")
		assert.Equal(t, lost.ID, e.fe.tagged("agentswarm-runtime:"+hex), "the reloaded image was not tagged")
		reloaded := logged(t, e.logs, "an image was reloaded from the store")
		require.Len(t, reloaded, 1)
		assert.Equal(t, lost.ID, reloaded[0]["image"])
		assert.Equal(t, []string{broken.ID, gone.ID}, e.r.Status().MissingImages)
		missing := logged(t, e.logs, "an image is missing")
		require.Len(t, missing, 2)
		why := map[string]string{}
		for _, line := range missing {
			assert.Equal(t, "ERROR", line["level"])
			image, _ := line["image"].(string)
			why[image], _ = line["err"].(string)
		}
		assert.Contains(t, why[broken.ID], "volumes", "the image that breaks the contract")
		assert.Contains(t, why[gone.ID], "neither on the engine nor in the store")

		e.fe.addImage(gone)
		e.fe.addImage(broken)
		time.Sleep(time.Minute)
		synctest.Wait()

		assert.Empty(t, e.r.Status().MissingImages, "an image back on the engine is still listed")
		assert.Len(t, logged(t, e.logs, "an image is missing"), 2, "an image back on the engine was logged missing")
	})
}

// TestStatusSaysWhetherTheEngineAnsweredItsLastCall (Contract C14; Decision
// 7): Status's Engine is ok while the engine answered the runner's last
// call, unreachable once that call found no engine, and ok again once a
// later call was answered; a Stop that cancels a launch's held creation says
// nothing of the engine, so ok stays (plan review 2, F3). Status makes no
// engine call: with every call held, it answers at once, and the engine sees
// no call; and its MissingImages, with no image missing, is an empty list,
// not nil.
func TestStatusSaysWhetherTheEngineAnsweredItsLastCall(t *testing.T) {
	unanswered := func(t *testing.T, e *env) {
		t.Helper()
		e.fe.setHook(onFirst("Create", func(arg string) error { return down("Create", arg) }))
		turn := newTurn("unanswered")
		e.start(t, turn)
		synctest.Wait()
		require.Equal(t, app.ReasonEngine, launchOf(t, e, turn.ID).Reason)
	}
	for _, row := range []struct {
		name string
		play func(t *testing.T, e *env)
		want string
	}{
		{name: "the engine answered", play: func(*testing.T, *env) {}, want: "ok"},
		{name: "a call found no engine", play: unanswered, want: "unreachable"},
		{name: "a call answered after one that found no engine", play: func(t *testing.T, e *env) {
			unanswered(t, e)
			turn := newTurn("answered")
			e.start(t, turn)
			synctest.Wait()
			require.NoError(t, launchOf(t, e, turn.ID).Err)
		}, want: "ok"},
		{name: "a Stop that cancels a held creation", play: func(t *testing.T, e *env) {
			e.fe.block("Create")
			turn := newTurn("stopped")
			e.start(t, turn)
			synctest.Wait()
			e.r.Stop(turn.Run, turn.Agent)
			synctest.Wait()
			require.Equal(t, app.ReasonCancelled, launchOf(t, e, turn.ID).Reason)
		}, want: "ok"},
	} {
		t.Run(row.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				e := newEnv(t)
				row.play(t, e)
				e.fe.forgetCalls()
				thaw := e.fe.freeze()
				t.Cleanup(thaw)

				var got Status
				require.True(t, returnsWithin(time.Second, func() { got = e.r.Status() }), "Status waited for the engine")
				thaw()
				synctest.Wait()

				assert.Empty(t, e.fe.calls(), "Status called the engine")
				assert.Equal(t, row.want, got.Engine)
				assert.Equal(t, []string{}, got.MissingImages)
			})
		})
	}
}
