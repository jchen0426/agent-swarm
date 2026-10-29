//go:build image

// Package image_test tests the runtime image's build (Plan 7d-2, Decision 6 and
// Contract C12): make image builds a commit's image without the cache, and
// one commit gives one binary and one image on the engine at hand. It runs
// make, git and docker on this host, whose engine builds through the buildx
// plugin (R13); make test-image runs it. It leaves what make image leaves,
// the image and its two names, which a rerun at the same commit only names
// again.
package image_test

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// root is the checkout's root, where make runs.
var root = filepath.Join("..", "..")

// The lines make image prints: the binary's SHA-256, and the image's id.
var (
	binaryLine = regexp.MustCompile(`(?m)^binary (sha256:[0-9a-f]{64})$`)
	imageLine  = regexp.MustCompile(`(?m)^image (sha256:[0-9a-f]{64})$`)
)

// build is what one make image printed.
type build struct {
	binary string // the binary's SHA-256, as sha256:<64 hex digits>
	image  string // the image's id, as sha256:<64 hex digits>
}

// makeImage runs make image IMAGE_COMMIT=HEAD under umask, and returns the
// binary's sum and the image's id it printed, each on one line of its own.
func makeImage(t *testing.T, umask string) build {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "sh", "-c", "umask "+umask+" && exec make image IMAGE_COMMIT=HEAD")
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	require.NoError(t, err, "make image under umask %s\nstdout:\n%s\nstderr:\n%s", umask, stdout.String(), stderr.String())
	binaries := binaryLine.FindAllStringSubmatch(stdout.String(), -1)
	images := imageLine.FindAllStringSubmatch(stdout.String(), -1)
	require.Len(t, binaries, 1, "the binary lines make image printed under umask %s:\n%s", umask, stdout.String())
	require.Len(t, images, 1, "the image lines make image printed under umask %s:\n%s", umask, stdout.String())
	return build{binary: binaries[0][1], image: images[0][1]}
}

// command runs name with args in the checkout's root, and returns what it
// prints on its standard output, trimmed.
func command(t *testing.T, name string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), name, args...)
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, "%s %s: %s", name, strings.Join(args, " "), stderr.String())
	return strings.TrimSpace(string(out))
}

// imageID returns the id of the image name names.
func imageID(t *testing.T, name string) string {
	t.Helper()
	return command(t, "docker", "image", "inspect", "--format", "{{.Id}}", name)
}

// names returns the names make image gives a commit's image id: the build's
// own, agentswarm-runtime:<commit>, and agentswarm-runtime:<the id's 64 hex
// digits>.
func names(id, commit string) []string {
	return []string{"agentswarm-runtime:" + commit, "agentswarm-runtime:" + strings.TrimPrefix(id, "sha256:")}
}

// unname removes the names a build of commit gave the image id, so that the
// next build must make them again: an earlier build's names would hide a
// build that named, tagged or printed another image. The image stays, under a
// tag of the test's own. When the test ends, the image takes back each name
// that no build made again, and that tag is removed.
func unname(t *testing.T, id, commit string) {
	t.Helper()
	parked := "agentswarm-runtime:test-image-" + strconv.FormatInt(time.Now().UnixNano(), 16)
	command(t, "docker", "tag", id, parked)
	t.Cleanup(func() {
		for _, name := range names(id, commit) {
			if exec.Command("docker", "image", "inspect", name).Run() != nil {
				_ = exec.Command("docker", "tag", id, name).Run()
			}
		}
		_ = exec.Command("docker", "image", "rm", parked).Run()
	})
	for _, name := range names(id, commit) {
		require.Equal(t, id, imageID(t, name), "the first build's %s", name)
		command(t, "docker", "image", "rm", name)
	}
}

// One commit builds one binary and one image (Decision 6): make image, run
// twice without the cache, under macOS's umask and then Debian's, prints one
// sum and one id both times. The second build names the image by the commit
// and by its id, as the first did. The image runs the runtime as 65532:65532,
// says which spec versions it reads and which toolchain built it, and holds
// the commit's binary, whose version and commit are the commit's id and whose
// date is the commit's time in UTC.
func TestOneCommitBuildsOneBinaryAndOneImage(t *testing.T) {
	commit := command(t, "git", "rev-parse", "--verify", "HEAD^{commit}")
	epoch, err := strconv.ParseInt(command(t, "git", "log", "-1", "--format=%ct", commit), 10, 64)
	require.NoError(t, err)
	committed := time.Unix(epoch, 0).UTC().Format(time.RFC3339)

	first := makeImage(t, "022")
	unname(t, first.image, commit)
	second := makeImage(t, "002")

	assert.Equal(t, first.binary, second.binary, "the binary's sum, under umask 022 and then 002")
	require.Equal(t, first.image, second.image, "the image's id, under umask 022 and then 002")
	id := second.image
	t.Logf("commit %s: binary %s, image %s", commit, second.binary, id)
	for _, name := range names(id, commit) {
		assert.Equal(t, id, imageID(t, name), "the second build's %s", name)
	}

	var inspected []struct {
		Config struct {
			User       string
			Entrypoint []string
			Labels     map[string]string
		}
	}
	require.NoError(t, json.Unmarshal([]byte(command(t, "docker", "image", "inspect", id)), &inspected))
	require.Len(t, inspected, 1)
	config := inspected[0].Config
	assert.Equal(t, "65532:65532", config.User)
	assert.Equal(t, []string{"/agentswarm", "turn"}, config.Entrypoint)
	assert.Equal(t, "1,2", config.Labels["io.agentswarm.turn-spec"])
	assert.Equal(t, "go1.26.0", config.Labels["io.agentswarm.go"])
	assert.Equal(t, commit, config.Labels["org.opencontainers.image.version"])

	var version struct {
		Version string `json:"version"`
		Commit  string `json:"commit"`
		Date    string `json:"date"`
	}
	printed := command(t, "docker", "run", "--rm", "--label", "agentswarm.project=agent-swarm", "--entrypoint", "/agentswarm", id, "version")
	require.NoError(t, json.Unmarshal([]byte(printed), &version), printed)
	assert.Equal(t, commit, version.Version)
	assert.Equal(t, commit, version.Commit)
	assert.Equal(t, committed, version.Date)
}
