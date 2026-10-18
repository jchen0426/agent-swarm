package endpoint_test

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	// sdkModule is the SDK the platform's runtime calls the model vendor with.
	sdkModule = "github.com/anthropics/anthropic-sdk-go"
	// liveCheckFile is the record of the last live check that passed.
	liveCheckFile = "testdata/live-check.json"
)

// liveCheck is the record of a live check that passed (live_test.go): the SDK
// and the model it passed with, and the day, in UTC.
type liveCheck struct {
	SDK     string `json:"sdk"`
	Model   string `json:"model"`
	Checked string `json:"checked"`
}

// sdkRequire finds the SDK's version in the module's go.mod.
var sdkRequire = regexp.MustCompile(`(?m)^\s*(?:require\s+)?` + regexp.QuoteMeta(sdkModule) + `\s+(v\S+)`)

// sdkVersion returns the version of the SDK this test binary was built with,
// as the module's go.mod requires it: `go mod tidy` keeps that the version the
// build uses. It does not ask the build information (debug.ReadBuildInfo),
// which lists no dependencies under go test (checked on Go 1.26), so that
// branch would never run.
func sdkVersion(t *testing.T) string {
	t.Helper()
	mod, err := os.ReadFile(filepath.Join("..", "..", "..", "go.mod"))
	require.NoError(t, err)
	found := sdkRequire.FindSubmatch(mod)
	require.NotNil(t, found, "go.mod requires no %s", sdkModule)
	return string(found[1])
}

// The allowlist admits only what the platform's runtime sends today, so a new
// SDK may send a block or a field that it refuses, and every agent's turn
// would end at its second call. The live check plays a turn of the runtime
// against the model vendor to find that before agents do; it needs the
// vendor's key and costs money, so it never runs in make test. This test fails
// when the SDK is not the one the check last passed with.
func TestTheLiveCheckCoversTheRuntimesSDK(t *testing.T) {
	version := sdkVersion(t)
	raw, err := os.ReadFile(liveCheckFile)
	if errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the runtime's SDK is %s, but the live check has never passed (there is no %s): run make test-live (README) and commit %s",
			version, liveCheckFile, liveCheckFile)
	}
	require.NoError(t, err)
	var recorded liveCheck
	require.NoError(t, json.Unmarshal(raw, &recorded), "%s is not a live check's record", liveCheckFile)
	require.NotEmpty(t, recorded.SDK, "%s names no SDK", liveCheckFile)

	if recorded.SDK != version {
		t.Fatalf("the runtime's SDK is %s, but the live check last passed with %s: run make test-live (README) and commit %s",
			version, recorded.SDK, liveCheckFile)
	}
}
