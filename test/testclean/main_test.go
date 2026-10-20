package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	oldID   = "0123456789ab"
	otherID = "fedcba987654"
	youngID = "ba9876543210"
	dataDir = "/var/lib/postgresql/data"
)

// testNow is the moment the program thinks it is.
var testNow = time.Date(2026, 10, 20, 15, 0, 0, 0, time.FixedZone("AEDT", 11*60*60))

// psCall is the listing the program asks for: the containers of this project.
var psCall = []string{"ps", "--filter", "label=agentswarm.project=agent-swarm", "--format", `{{.ID}}|{{.CreatedAt}}|{{.Label "agentswarm.data"}}`}

// row is a line of the listing as docker ps prints it, for a container
// created age ago.
func row(id string, age time.Duration, data string) string {
	return id + "|" + testNow.Add(-age).Format("2006-01-02 15:04:05 -0700 MST") + "|" + data + "\n"
}

// fakeDocker stands in for the docker CLI: it answers the commands the
// program runs, and records them.
type fakeDocker struct {
	listing string
	// failing makes a command fail, by name. A copy fails after it wrote a part.
	failing map[string]error
	calls   [][]string
}

func (d *fakeDocker) run(_ context.Context, out io.Writer, args ...string) error {
	d.calls = append(d.calls, slices.Clone(args))
	var err error
	switch args[0] {
	case "ps":
		_, err = io.WriteString(out, d.listing)
	case "inspect":
		_, err = io.WriteString(out, `[{"Id":"`+args[1]+`"}]`)
	case "cp":
		_, err = io.WriteString(out, "tar of "+args[1])
	}
	if err != nil {
		return err
	}
	return d.failing[args[0]]
}

// clean runs the program against d.
func clean(t *testing.T, d *fakeDocker, flags ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run(t.Context(), flags, &out, &errOut, d.run, func() time.Time { return testNow })
	return out.String(), errOut.String(), code
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}

func TestTestcleanRemovesOnlyItsOldContainersAfterABackup(t *testing.T) {
	backups := filepath.Join(t.TempDir(), "backups")
	d := &fakeDocker{listing: row(oldID, 2*time.Hour, dataDir) + row(youngID, 10*time.Minute, dataDir)}

	stdout, errOut, code := clean(t, d, "-backups", backups)

	require.Equal(t, 0, code, errOut)
	assert.Equal(t, [][]string{
		psCall,
		{"inspect", oldID},
		{"cp", oldID + ":" + dataDir, "-"},
		{"rm", "-f", "-v", oldID},
	}, d.calls, "the old container's inspect output and data are saved, in that order, and then it goes; the young one stays")
	assert.JSONEq(t, `[{"Id":"`+oldID+`"}]`, read(t, filepath.Join(backups, oldID+".inspect.json")))
	assert.Equal(t, "tar of "+oldID+":"+dataDir, read(t, filepath.Join(backups, oldID+"-data.tar")))
	assert.Contains(t, stdout, oldID)
	assert.NotContains(t, stdout, youngID)
}

func TestTestcleanRemovesNothingWhoseBackupFailed(t *testing.T) {
	backups := filepath.Join(t.TempDir(), "backups")
	d := &fakeDocker{
		// The second container has no data to copy, so the failing copy is the first one's alone.
		listing: row(oldID, 2*time.Hour, dataDir) + row(otherID, 3*time.Hour, ""),
		failing: map[string]error{"cp": errors.New("no such file " + dataDir + " in container " + oldID)},
	}

	stdout, errOut, code := clean(t, d, "-backups", backups)

	assert.Equal(t, 1, code)
	assert.Equal(t, [][]string{
		psCall,
		{"inspect", oldID},
		{"cp", oldID + ":" + dataDir, "-"},
		{"inspect", otherID},
		{"rm", "-f", "-v", otherID},
	}, d.calls, "the container whose data could not be copied stays, and the others are still cleaned")
	assert.Contains(t, errOut, oldID)
	assert.Contains(t, errOut, "no such file")
	assert.NoFileExists(t, filepath.Join(backups, oldID+"-data.tar"), "a copy that failed is never left looking like a backup")
	assert.NotContains(t, stdout, oldID)
	assert.Contains(t, stdout, otherID)
}

func TestTestcleanDryRunRemovesNothing(t *testing.T) {
	backups := filepath.Join(t.TempDir(), "backups")
	d := &fakeDocker{listing: row(oldID, 2*time.Hour, dataDir) + row(youngID, 10*time.Minute, dataDir)}

	stdout, errOut, code := clean(t, d, "-backups", backups, "-dry-run")

	require.Equal(t, 0, code, errOut)
	assert.Equal(t, [][]string{psCall}, d.calls, "no inspect, no copy and no removal")
	assert.Contains(t, stdout, oldID)
	assert.NotContains(t, stdout, youngID)
	assert.NoDirExists(t, backups)
}

func TestTestcleanCopiesNoDataFromAContainerWithoutADataLabel(t *testing.T) {
	d := &fakeDocker{listing: row(oldID, 2*time.Hour, "")}

	_, errOut, code := clean(t, d, "-backups", t.TempDir())

	require.Equal(t, 0, code, errOut)
	assert.Equal(t, [][]string{psCall, {"inspect", oldID}, {"rm", "-f", "-v", oldID}}, d.calls)
}

func TestTestcleanTakesTheAgeBeyondWhichItRemoves(t *testing.T) {
	d := &fakeDocker{listing: row(oldID, 40*time.Minute, "")}

	_, errOut, code := clean(t, d, "-backups", t.TempDir())
	require.Equal(t, 0, code, errOut)
	assert.Equal(t, [][]string{psCall}, d.calls, "forty minutes is not over an hour")

	d.calls = nil
	_, errOut, code = clean(t, d, "-backups", t.TempDir(), "-age", "30m")
	require.Equal(t, 0, code, errOut)
	assert.Contains(t, d.calls, []string{"rm", "-f", "-v", oldID})
}

// A tool that removes things stops when it cannot tell what it is looking at.
func TestTestcleanRemovesNothingFromAListingItCannotRead(t *testing.T) {
	for name, listing := range map[string]string{
		"a row without fields":    row(oldID, 2*time.Hour, dataDir) + "no such container\n",
		"a creation time unknown": oldID + "|yesterday|" + dataDir + "\n",
		"an id that is no id":     row("--all", 2*time.Hour, dataDir),
	} {
		t.Run(name, func(t *testing.T) {
			d := &fakeDocker{listing: listing}

			_, errOut, code := clean(t, d, "-backups", t.TempDir())

			assert.Equal(t, 1, code)
			assert.Equal(t, [][]string{psCall}, d.calls)
			assert.Contains(t, errOut, "listing")
		})
	}
}

func TestTestcleanReportsADockerItCannotReach(t *testing.T) {
	d := &fakeDocker{failing: map[string]error{"ps": errors.New("cannot connect to the docker daemon")}}

	_, errOut, code := clean(t, d, "-backups", t.TempDir())

	assert.Equal(t, 1, code)
	assert.Contains(t, errOut, "cannot connect to the docker daemon")
}

func TestTestcleanRefusesAFlagItDoesNotKnow(t *testing.T) {
	d := &fakeDocker{}

	_, errOut, code := clean(t, d, "-all")

	assert.Equal(t, 2, code)
	assert.Contains(t, errOut, "-all")
	assert.Empty(t, d.calls)
}
