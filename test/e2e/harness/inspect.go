//go:build e2e

package harness

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// Inspect runs agentswarm inspect on the instance's deployment (Contract C5),
// as inspect --deployment FILE item ID, board RUN or calls RUN, and returns
// the JSON lines it printed, in order. The test fails when inspect does not
// exit 0, or prints a line that is not a JSON object.
func (i *Instance) Inspect(t *testing.T, args ...string) []map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary(t), append([]string{"inspect", "--deployment", i.deployment}, args...)...) //nolint:gosec // G204: the binary under test, with the test's arguments.
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		t.Fatalf("harness: agentswarm inspect %s exited %d:\n%s", strings.Join(args, " "), exit.ExitCode(), errOut.String())
	case err != nil:
		t.Fatalf("harness: run agentswarm inspect %s: %v\n%s", strings.Join(args, " "), err, errOut.String())
	}
	var lines []map[string]any
	scanner := bufio.NewScanner(&out)
	scanner.Buffer(make([]byte, 0, 64<<10), 64<<20)
	for scanner.Scan() {
		var line map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil || line == nil {
			t.Fatalf("harness: agentswarm inspect printed a line that is not a JSON object: %q", scanner.Text())
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("harness: read what agentswarm inspect printed: %v", err)
	}
	return lines
}
