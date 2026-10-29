//go:build image

// Package image_test tests the runtime image's build (Plan 7d-2, Decision 6 and
// Contract C12): make image builds a commit's image from the commit alone,
// without the cache, so that one commit gives one binary and one image on the
// engine at hand. It runs make, git and docker on this host, whose engine
// builds through the buildx plugin (R13); make test-image runs it. It leaves
// what make image leaves, HEAD's image and its two names, which a rerun at the
// same commit only names again.
package image_test

import (
	"bytes"
	"debug/buildinfo"
	"encoding/json"
	"os"
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

// otherToolchain is a released toolchain other than the module's own, which a
// commit of TestACommitsImageHoldsTheCommitsDockerfileAndToolchain names in
// its go.mod.
const otherToolchain = "go1.26.1"

// The lines a build prints: the binary's SHA-256, and the image's id.
var (
	binaryLine = regexp.MustCompile(`(?m)^binary (sha256:[0-9a-f]{64})$`)
	imageLine  = regexp.MustCompile(`(?m)^image (sha256:[0-9a-f]{64})$`)
)

// The lines of a go.mod that name its toolchain and its Go version.
var (
	toolchainLine = regexp.MustCompile(`(?m)^toolchain\s+(\S+)`)
	goLine        = regexp.MustCompile(`(?m)^go\s+(\S+)`)
)

// build is what one build printed.
type build struct {
	binary string // the binary's SHA-256, as sha256:<64 hex digits>
	image  string // the image's id, as sha256:<64 hex digits>
}

// run runs cmd, which builds an image, and returns the binary's sum and the
// image's id it printed, each on one line of its own.
func run(t *testing.T, cmd *exec.Cmd) build {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	require.NoError(t, err, "%s\nstdout:\n%s\nstderr:\n%s", cmd, stdout.String(), stderr.String())
	binaries := binaryLine.FindAllStringSubmatch(stdout.String(), -1)
	images := imageLine.FindAllStringSubmatch(stdout.String(), -1)
	require.Len(t, binaries, 1, "the binary lines %s printed:\n%s", cmd, stdout.String())
	require.Len(t, images, 1, "the image lines %s printed:\n%s", cmd, stdout.String())
	return build{binary: binaries[0][1], image: images[0][1]}
}

// makeImage runs make image IMAGE_COMMIT=HEAD in the checkout under umask,
// with env added to the test's environment.
func makeImage(t *testing.T, umask string, env ...string) build {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "sh", "-c", "umask "+umask+" && exec make image IMAGE_COMMIT=HEAD")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), env...)
	return run(t, cmd)
}

// commandIn runs name with args in dir, and returns what it prints on its
// standard output, trimmed.
func commandIn(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), name, args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, "%s %s: %s", name, strings.Join(args, " "), stderr.String())
	return strings.TrimSpace(string(out))
}

// command runs name with args in the checkout's root, and returns what it
// prints on its standard output, trimmed.
func command(t *testing.T, name string, args ...string) string {
	t.Helper()
	return commandIn(t, root, name, args...)
}

// imageID returns the id of the image name names.
func imageID(t *testing.T, name string) string {
	t.Helper()
	return command(t, "docker", "image", "inspect", "--format", "{{.Id}}", name)
}

// imageIDIfAny returns the id of the image name names, or "" when no image
// has that name.
func imageIDIfAny(t *testing.T, name string) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "docker", "image", "inspect", "--format", "{{.Id}}", name).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
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

// imageConfig is an image's configuration, as docker image inspect shows it.
type imageConfig struct {
	User       string
	Entrypoint []string
	Labels     map[string]string
}

// inspect returns the configuration of the image id.
func inspect(t *testing.T, id string) imageConfig {
	t.Helper()
	var inspected []struct{ Config imageConfig }
	require.NoError(t, json.Unmarshal([]byte(command(t, "docker", "image", "inspect", id)), &inspected))
	require.Len(t, inspected, 1)
	return inspected[0].Config
}

// goToolchain returns the toolchain the go.mod of commit names: its toolchain
// line, or else the version of its go line, as go1.26.0.
func goToolchain(t *testing.T, commit string) string {
	t.Helper()
	mod := command(t, "git", "show", commit+":go.mod")
	if found := toolchainLine.FindStringSubmatch(mod); found != nil {
		return found[1]
	}
	found := goLine.FindStringSubmatch(mod)
	require.NotNil(t, found, "the go.mod of %s has no go line", commit)
	return "go" + found[1]
}

// binaryGoVersion returns the Go version that built the image id's binary, as
// go version reports it (debug/buildinfo, which go version reads). The binary
// is copied out of a container created from the image, and labelled, which is
// removed when the test ends.
func binaryGoVersion(t *testing.T, id string) string {
	t.Helper()
	container := command(t, "docker", "create", "--label", "agentswarm.project=agent-swarm", id)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-v", container).Run() })
	binary := filepath.Join(t.TempDir(), "agentswarm")
	command(t, "docker", "cp", container+":/agentswarm", binary)
	info, err := buildinfo.ReadFile(binary)
	require.NoError(t, err)
	return info.GoVersion
}

// developersGoEnvironment returns a Go environment that would change a binary
// it reached: build flags, an experiment, the architectures' levels and FIPS
// 140 mode, each in the environment and in a go env file of its own, and a
// workspace that does not hold the module. Its files are the test's own.
func developersGoEnvironment(t *testing.T) []string {
	t.Helper()
	dir := t.TempDir()
	settings := []string{"GOFLAGS=-tags=foo", "GOEXPERIMENT=jsonv2", "GOAMD64=v3", "GOARM64=v8.1", "GOFIPS140=latest"}
	goenv := filepath.Join(dir, "go.env")
	require.NoError(t, os.WriteFile(goenv, []byte(strings.Join(settings, "\n")+"\n"), 0o600))
	work := filepath.Join(dir, "go.work")
	require.NoError(t, os.WriteFile(work, []byte("go 1.26.0\n"), 0o600))
	return append(settings, "GOENV="+goenv, "GOWORK="+work)
}

// appendTo appends text to the file at path.
func appendTo(t *testing.T, path, text string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = file.WriteString(text)
	require.NoError(t, err)
	require.NoError(t, file.Close())
}

// One commit builds one binary and one image (Decision 6): make image, run
// without the cache under macOS's umask, then Debian's, then in a developer's
// Go environment, prints one sum and one id each time. The second build names
// the image by the commit and by its id, as the first did. The image runs the
// runtime as 65532:65532, says which spec versions it reads and which
// toolchain built it, the one the commit's go.mod names, and holds the
// commit's binary, whose version and commit are the commit's id and whose date
// is the commit's time in UTC.
func TestOneCommitBuildsOneBinaryAndOneImage(t *testing.T) {
	commit := command(t, "git", "rev-parse", "--verify", "HEAD^{commit}")
	epoch, err := strconv.ParseInt(command(t, "git", "log", "-1", "--format=%ct", commit), 10, 64)
	require.NoError(t, err)
	committed := time.Unix(epoch, 0).UTC().Format(time.RFC3339)

	first := makeImage(t, "022")
	unname(t, first.image, commit)
	second := makeImage(t, "002")
	third := makeImage(t, "022", developersGoEnvironment(t)...)

	assert.Equal(t, first.binary, second.binary, "the binary's sum, under umask 022 and then 002")
	assert.Equal(t, first.binary, third.binary, "the binary's sum, in a developer's Go environment")
	require.Equal(t, first.image, second.image, "the image's id, under umask 022 and then 002")
	require.Equal(t, first.image, third.image, "the image's id, in a developer's Go environment")
	id := second.image
	t.Logf("commit %s: binary %s, image %s", commit, second.binary, id)
	for _, name := range names(id, commit) {
		assert.Equal(t, id, imageID(t, name), "the second build's %s", name)
	}

	config := inspect(t, id)
	assert.Equal(t, "65532:65532", config.User)
	assert.Equal(t, []string{"/agentswarm", "turn"}, config.Entrypoint)
	assert.Equal(t, "1,2", config.Labels["io.agentswarm.turn-spec"])
	assert.Equal(t, commit, config.Labels["org.opencontainers.image.version"])
	toolchain := goToolchain(t, commit)
	assert.Equal(t, toolchain, config.Labels["io.agentswarm.go"], "the toolchain the commit's go.mod names")
	assert.Equal(t, toolchain, binaryGoVersion(t, id), "the toolchain that built the image's binary")

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

// A commit without build/runtime/Dockerfile is refused, and nothing is built:
// its runtime predates the image contract, so an image of it could claim only
// a label of today's Dockerfile, which its runtime may not keep (Decision 6).
// The commit is the parent of the one that added the Dockerfile.
func TestACommitWithoutTheRuntimesDockerfileIsRefused(t *testing.T) {
	added := strings.Fields(command(t, "git", "log", "--diff-filter=A", "--format=%H", "--", "build/runtime/Dockerfile"))
	require.NotEmpty(t, added, "no commit added build/runtime/Dockerfile")
	before := command(t, "git", "rev-parse", "--verify", added[len(added)-1]+"^")
	name := "agentswarm-runtime:" + before
	named := imageIDIfAny(t, name)

	cmd := exec.CommandContext(t.Context(), "make", "image", "IMAGE_COMMIT="+before)
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()

	require.Error(t, err, "make image IMAGE_COMMIT=%s\nstdout:\n%s", before, stdout.String())
	assert.Contains(t, stderr.String(), before+" has no build/runtime/Dockerfile: its runtime predates the image contract")
	assert.NotRegexp(t, binaryLine, stdout.String())
	assert.NotRegexp(t, imageLine, stdout.String())
	assert.Equal(t, named, imageIDIfAny(t, name), "the image named %s", name)
}

// A commit's image holds the commit alone (Decision 6): its build takes the
// commit's own Dockerfile, not the checkout's uncommitted edit of it, and the
// toolchain the commit's go.mod names, which the image's label then names
// truly. The commit is the test's own, made in a clone of the checkout, whose
// go.mod names another toolchain; the build is the checkout's
// build/runtime/image.sh, run in the clone, and its image is removed when the
// test ends.
func TestACommitsImageHoldsTheCommitsDockerfileAndToolchain(t *testing.T) {
	head := command(t, "git", "rev-parse", "--verify", "HEAD^{commit}")
	checkout, err := filepath.Abs(root)
	require.NoError(t, err)
	clone := t.TempDir()
	commandIn(t, clone, "git", "clone", "--quiet", "--shared", "--no-checkout", checkout, ".")
	commandIn(t, clone, "git", "checkout", "--quiet", "--detach", head)
	appendTo(t, filepath.Join(clone, "go.mod"), "\ntoolchain "+otherToolchain+"\n")
	commandIn(t, clone, "git", "-c", "user.name=image test", "-c", "user.email=image-test@example.invalid", "-c", "commit.gpgsign=false",
		"commit", "--quiet", "--message", "test: build with "+otherToolchain, "go.mod")
	commit := commandIn(t, clone, "git", "rev-parse", "--verify", "HEAD^{commit}")
	appendTo(t, filepath.Join(clone, "build", "runtime", "Dockerfile"), `LABEL io.agentswarm.turn-spec="9"`+"\n")
	var id string
	t.Cleanup(func() {
		args := []string{"image", "rm", "agentswarm-runtime:" + commit}
		if id != "" {
			args = append(args, "agentswarm-runtime:"+strings.TrimPrefix(id, "sha256:"))
		}
		_ = exec.Command("docker", args...).Run()
	})

	cmd := exec.CommandContext(t.Context(), filepath.Join(checkout, "build", "runtime", "image.sh"), "commit", "HEAD")
	cmd.Dir = clone
	id = run(t, cmd).image

	config := inspect(t, id)
	assert.Equal(t, "1,2", config.Labels["io.agentswarm.turn-spec"], "the committed Dockerfile's label, not the checkout's edit")
	assert.Equal(t, commit, config.Labels["org.opencontainers.image.version"])
	assert.Equal(t, otherToolchain, config.Labels["io.agentswarm.go"], "the toolchain the commit's go.mod names")
	assert.Equal(t, otherToolchain, binaryGoVersion(t, id), "the toolchain that built the image's binary")
}
