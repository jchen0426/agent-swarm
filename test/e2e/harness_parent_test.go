//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/test/e2e/harness"
)

// parentScript is the script of a child that waits for its standard input to
// end and then leaves a marker.
func parentScript(marker string) []string {
	return []string{"sh", "-c", `cat >/dev/null; echo eof > "$1"`, "sh", marker}
}

func markerHolds(marker, text string) func() bool {
	return func() bool {
		data, err := os.ReadFile(marker)
		return err == nil && string(data) == text
	}
}

func TestKillingTheParentClosesItsChildsStandardInput(t *testing.T) {
	env := harness.New(t, harness.Options{NoDatabase: true})
	marker := filepath.Join(t.TempDir(), "MARKER")
	parent := env.StartParent(t, parentScript(marker), nil)
	assert.Never(t, markerHolds(marker, "eof\n"), 300*time.Millisecond, 20*time.Millisecond,
		"the parent holds its child's standard input open")

	parent.Kill(t)

	assert.Eventually(t, markerHolds(marker, "eof\n"), 2*time.Second, 10*time.Millisecond,
		"the child's standard input closed with the parent")
}

func TestClosingTheParentsOwnInputEndsItAndItsChild(t *testing.T) {
	env := harness.New(t, harness.Options{NoDatabase: true})
	marker := filepath.Join(t.TempDir(), "MARKER")
	parent := env.StartParent(t, parentScript(marker), nil)
	assert.Never(t, markerHolds(marker, "eof\n"), 300*time.Millisecond, 20*time.Millisecond,
		"the parent holds its child's standard input open")

	parent.Close(t)

	assert.Equal(t, 0, parent.Wait(t, 2*time.Second), "the parent ends with its child's exit code")
	assert.Eventually(t, markerHolds(marker, "eof\n"), 2*time.Second, 10*time.Millisecond,
		"the child's standard input closed when the parent's did")
}

func TestAParentKillsAChildThatDoesNotEndWhenItsInputCloses(t *testing.T) {
	env := harness.New(t, harness.Options{NoDatabase: true})
	pidFile := filepath.Join(t.TempDir(), "pid")
	parent := env.StartParent(t, []string{"sh", "-c", `echo $$ > "$1"; exec sleep 300`, "sh", pidFile}, nil)
	var pid int
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
		return err == nil
	}, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, syscall.Kill(pid, 0), "the child runs")

	parent.Close(t)

	assert.Equal(t, 128+int(syscall.SIGKILL), parent.Wait(t, 20*time.Second), "the parent kills a child that outlasts its grace")
	assert.ErrorIs(t, syscall.Kill(pid, 0), syscall.ESRCH, "the child is gone")
}

func TestTheParentKeepsItsChildsOutputInFilesAndExitsWithItsCode(t *testing.T) {
	env := harness.New(t, harness.Options{NoDatabase: true})
	parent := env.StartParent(t, []string{"sh", "-c", `echo "out $TOKEN"; echo err >&2; exit 3`}, []string{"TOKEN=seven"})

	assert.Equal(t, 3, parent.Wait(t, 5*time.Second), "the child's exit code")

	out, err := os.ReadFile(parent.Stdout())
	require.NoError(t, err)
	assert.Equal(t, "out seven\n", string(out), "the child ran with the environment it was given")
	errOut, err := os.ReadFile(parent.Stderr())
	require.NoError(t, err)
	assert.Equal(t, "err\n", string(errOut))
}
