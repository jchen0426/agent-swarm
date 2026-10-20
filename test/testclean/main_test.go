package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

// stamp is the start of the run at testNow in UTC, as the names of the files it
// saves carry it.
const stamp = "20261020T040000Z"

// backup is the path in dir of a file that the run at testNow saved for a
// container: suffix is ".inspect.json" or "-data.tar".
func backup(dir, id, suffix string) string {
	return filepath.Join(dir, id+"-"+stamp+suffix)
}

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
	// failing makes a command fail, by name. A command that writes fails after
	// it wrote a part.
	failing map[string]error
	calls   [][]string
	// inspects counts the inspect commands, so that the output of each differs.
	inspects int
}

func (d *fakeDocker) run(_ context.Context, out io.Writer, args ...string) error {
	d.calls = append(d.calls, slices.Clone(args))
	var err error
	switch args[0] {
	case "ps":
		_, err = io.WriteString(out, d.listing)
	case "inspect":
		d.inspects++
		_, err = fmt.Fprintf(out, `[{"Id":%q,"Inspect":%d}]`, args[1], d.inspects)
	case "cp":
		_, err = io.WriteString(out, "tar of "+args[1])
	}
	if err != nil {
		return err
	}
	return d.failing[args[0]]
}

// clean runs the program against d, at testNow.
func clean(t *testing.T, d *fakeDocker, flags ...string) (stdout, stderr string, code int) {
	t.Helper()
	return cleanAt(t, d, testNow, flags...)
}

// cleanAt runs the program against d, at the time at.
func cleanAt(t *testing.T, d *fakeDocker, at time.Time, flags ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run(t.Context(), flags, &out, &errOut, d.run, func() time.Time { return at })
	return out.String(), errOut.String(), code
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}

// savedInspects returns the inspect output of each backup of the container in
// dir, the earliest run's first.
func savedInspects(t *testing.T, dir, id string) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, id+"*.inspect.json"))
	require.NoError(t, err)
	var saved []string
	for _, p := range paths {
		saved = append(saved, read(t, p))
	}
	return saved
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
	assert.JSONEq(t, `[{"Id":"`+oldID+`","Inspect":1}]`, read(t, backup(backups, oldID, ".inspect.json")))
	assert.Equal(t, "tar of "+oldID+":"+dataDir, read(t, backup(backups, oldID, "-data.tar")))
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
	assert.NoFileExists(t, backup(backups, oldID, "-data.tar"), "a copy that failed is never left looking like a backup")
	assert.NotContains(t, stdout, oldID)
	assert.Contains(t, stdout, otherID)
}

// A removal that fails leaves the container, and what was saved of it.
func TestTestcleanReportsARemovalThatFailedAfterItsBackup(t *testing.T) {
	backups := filepath.Join(t.TempDir(), "backups")
	d := &fakeDocker{
		listing: row(oldID, 2*time.Hour, dataDir),
		failing: map[string]error{"rm": errors.New("device or resource busy")},
	}

	stdout, errOut, code := clean(t, d, "-backups", backups)

	assert.Equal(t, 1, code)
	assert.Equal(t, [][]string{
		psCall,
		{"inspect", oldID},
		{"cp", oldID + ":" + dataDir, "-"},
		{"rm", "-f", "-v", oldID},
	}, d.calls)
	assert.Contains(t, errOut, oldID)
	assert.Contains(t, errOut, "was not removed")
	assert.Contains(t, errOut, "device or resource busy")
	assert.Empty(t, stdout, "nothing was removed")
	assert.FileExists(t, backup(backups, oldID, ".inspect.json"))
	assert.FileExists(t, backup(backups, oldID, "-data.tar"))
}

// Without the inspect output there is no backup, so the data is not copied
// and the container is not removed.
func TestTestcleanRemovesNothingWhoseInspectFailed(t *testing.T) {
	backups := filepath.Join(t.TempDir(), "backups")
	d := &fakeDocker{
		listing: row(oldID, 2*time.Hour, dataDir),
		failing: map[string]error{"inspect": errors.New("no such object: " + oldID)},
	}

	stdout, errOut, code := clean(t, d, "-backups", backups)

	assert.Equal(t, 1, code)
	assert.Equal(t, [][]string{psCall, {"inspect", oldID}}, d.calls)
	assert.Contains(t, errOut, oldID)
	assert.Contains(t, errOut, "no such object")
	assert.Empty(t, stdout)
	assert.NoFileExists(t, backup(backups, oldID, ".inspect.json"), "an inspect that failed is never left looking like a backup")
}

// A removal can fail after the backup was made, and the container can change
// before the next run: that run saves what the container holds then, and
// leaves the earlier backup as it was.
func TestTestcleanNeverReplacesAnEarlierBackup(t *testing.T) {
	backups := filepath.Join(t.TempDir(), "backups")
	d := &fakeDocker{
		listing: row(oldID, 2*time.Hour, ""),
		failing: map[string]error{"rm": errors.New("device or resource busy")},
	}
	_, _, code := clean(t, d, "-backups", backups)
	require.Equal(t, 1, code)
	earlier := savedInspects(t, backups, oldID)
	require.Len(t, earlier, 1)

	d.failing = nil
	_, errOut, code := cleanAt(t, d, testNow.Add(time.Minute), "-backups", backups)

	require.Equal(t, 0, code, errOut)
	later := savedInspects(t, backups, oldID)
	require.Len(t, later, 2, "the second run saved its own file")
	assert.Equal(t, earlier[0], later[0], "the first run's file is as it was")
	assert.NotEqual(t, later[0], later[1])
}

// Two runs that start in the same second would name their files alike. The
// second leaves the container where it is rather than replace the first's.
func TestTestcleanRefusesToReplaceABackupOfItsOwnSecond(t *testing.T) {
	backups := filepath.Join(t.TempDir(), "backups")
	d := &fakeDocker{
		listing: row(oldID, 2*time.Hour, ""),
		failing: map[string]error{"rm": errors.New("device or resource busy")},
	}
	_, _, code := clean(t, d, "-backups", backups)
	require.Equal(t, 1, code)
	earlier := read(t, backup(backups, oldID, ".inspect.json"))

	d.calls, d.failing = nil, nil
	stdout, errOut, code := clean(t, d, "-backups", backups)

	assert.Equal(t, 1, code)
	assert.Equal(t, [][]string{psCall}, d.calls, "the container is neither inspected again nor removed")
	assert.Contains(t, errOut, oldID)
	assert.Contains(t, errOut, "already exists")
	assert.Empty(t, stdout)
	assert.Equal(t, earlier, read(t, backup(backups, oldID, ".inspect.json")))
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
