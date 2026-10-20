// Command testclean removes this project's test containers that have run for
// over an hour. A test run that is killed leaves its containers behind, and
// every container a test starts carries the label agentswarm.project=agent-swarm.
//
// Before it removes a container, testclean saves what the container held under
// the backups directory: its docker inspect output, and the files at the path
// of its agentswarm.data label, when it has one. A run names the files for the
// container and for its own start in UTC, <id>-<time>.inspect.json and
// <id>-<time>-data.tar, and no run replaces a file an earlier one saved. A
// container whose backup fails stays, and testclean exits with 1.
//
// A test run still going after the age is not told apart from one that was
// killed, so run testclean between test runs.
//
// Usage: go run ./test/testclean [-age 1h] [-backups .test-clean] [-dry-run]
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Exit codes.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

const (
	// listFilter selects this project's containers.
	listFilter = "label=agentswarm.project=agent-swarm"
	// listFormat prints a container's id, its creation time and the path of
	// its data label, which is empty when it has none.
	listFormat = `{{.ID}}|{{.CreatedAt}}|{{.Label "agentswarm.data"}}`
	// createdLayout is how docker ps prints the creation time.
	createdLayout = "2006-01-02 15:04:05 -0700 MST"
	// stampLayout is how the names of a run's files carry its start, in UTC.
	stampLayout = "20060102T150405Z"
)

// idPattern is a container id as docker prints it. Nothing else reaches a
// command that removes a container.
var idPattern = regexp.MustCompile(`^[0-9a-f]{12,64}$`)

// runner runs the docker command line with args and writes what it prints to
// out.
type runner func(ctx context.Context, out io.Writer, args ...string) error

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr, dockerCLI, time.Now)
	stop()
	os.Exit(code)
}

// run lists this project's containers, backs up each one created longer ago
// than the age flag and removes it, and returns the exit code.
func run(ctx context.Context, args []string, stdout, stderr io.Writer, docker runner, now func() time.Time) int {
	flags := flag.NewFlagSet("testclean", flag.ContinueOnError)
	flags.SetOutput(stderr)
	age := flags.Duration("age", time.Hour, "remove the containers created longer ago than this")
	backups := flags.String("backups", ".test-clean", "the directory that receives the backups")
	dryRun := flags.Bool("dry-run", false, "list the containers that would be removed, and change nothing")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if flags.NArg() > 0 || *age < 0 {
		_, _ = fmt.Fprintln(stderr, "testclean: it takes flags only, and an age of zero or more")
		flags.Usage()
		return exitUsage
	}

	listed, err := list(ctx, docker)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "testclean: %v\n", err)
		return exitError
	}
	start := now()
	cutoff := start.Add(-*age)
	stamp := start.UTC().Format(stampLayout)
	var old []container
	for _, c := range listed {
		if c.created.Before(cutoff) {
			old = append(old, c)
		}
	}
	if len(old) == 0 {
		_, _ = fmt.Fprintf(stdout, "no test container was created more than %s ago\n", *age)
		return exitOK
	}

	code := exitOK
	for _, c := range old {
		if err := ctx.Err(); err != nil {
			_, _ = fmt.Fprintf(stderr, "testclean: stopped: %v\n", err)
			return exitError
		}
		created := c.created.Format(time.RFC3339)
		if *dryRun {
			_, _ = fmt.Fprintf(stdout, "would remove %s, created %s\n", c.id, created)
			continue
		}
		stem := filepath.Join(*backups, c.id+"-"+stamp)
		if err := backUp(ctx, docker, stem, c); err != nil {
			_, _ = fmt.Fprintf(stderr, "testclean: %s stays, as its backup failed: %v\n", c.id, err)
			code = exitError
			continue
		}
		if err := docker(ctx, io.Discard, "rm", "-f", "-v", c.id); err != nil {
			_, _ = fmt.Fprintf(stderr, "testclean: %s is backed up as %s* but was not removed: %v\n", c.id, stem, err)
			code = exitError
			continue
		}
		_, _ = fmt.Fprintf(stdout, "removed %s, created %s; backed up as %s*\n", c.id, created, stem)
	}
	return code
}

// container is one of this project's containers.
type container struct {
	id      string
	created time.Time
	// data is the path of the container's agentswarm.data label, or empty.
	data string
}

// list returns the project's running containers. It fails on a row it cannot
// read, so that nothing is removed on a guess.
func list(ctx context.Context, docker runner) ([]container, error) {
	var out bytes.Buffer
	if err := docker(ctx, &out, "ps", "--filter", listFilter, "--format", listFormat); err != nil {
		return nil, err
	}
	var listed []container
	for row := range strings.Lines(out.String()) {
		row = strings.TrimRight(row, "\r\n")
		if row == "" {
			continue
		}
		c, err := parseRow(row)
		if err != nil {
			return nil, fmt.Errorf("cannot read the listing of containers: %w", err)
		}
		listed = append(listed, c)
	}
	return listed, nil
}

// parseRow reads a row of the listing: the container's id, its creation time
// and the path of its data label.
func parseRow(row string) (container, error) {
	id, rest, hasID := strings.Cut(row, "|")
	created, data, hasCreated := strings.Cut(rest, "|")
	if !hasID || !hasCreated || !idPattern.MatchString(id) {
		return container{}, fmt.Errorf("the row %q is not a container id, a creation time and a data path", row)
	}
	t, err := time.Parse(createdLayout, created)
	if err != nil {
		return container{}, fmt.Errorf("the row %q: %w", row, err)
	}
	return container{id: id, created: t, data: data}, nil
}

// backUp saves what the container held, in files whose names begin with stem:
// its docker inspect output, then the files at its data path.
func backUp(ctx context.Context, docker runner, stem string, c container) error {
	if err := os.MkdirAll(filepath.Dir(stem), 0o750); err != nil {
		return err
	}
	err := save(stem+".inspect.json", func(w io.Writer) error {
		return docker(ctx, w, "inspect", c.id)
	})
	if err != nil {
		return fmt.Errorf("save its inspect output: %w", err)
	}
	if c.data == "" {
		return nil
	}
	err = save(stem+"-data.tar", func(w io.Writer) error {
		return docker(ctx, w, "cp", c.id+":"+c.data, "-")
	})
	if err != nil {
		return fmt.Errorf("save its data at %s: %w", c.data, err)
	}
	return nil
}

// save writes what write produces to path, and never replaces a file already
// there, as a backup is never replaced. The file takes its name only once it
// is whole, so that a copy that failed never looks like a backup; the part it
// wrote stays beside it, as path.partial.
func save(path string, write func(io.Writer) error) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%s already exists, and a backup is never replaced", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	part := path + ".partial"
	f, err := os.OpenFile(filepath.Clean(part), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	err = write(f)
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(part, path)
}

// dockerCLI runs the docker command line.
func dockerCLI(ctx context.Context, out io.Writer, args ...string) error {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker", args...) //nolint:gosec // G204: the arguments are this program's own, and the ids are ones docker listed.
	cmd.Stdout, cmd.Stderr = out, &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("docker %s: %w: %s", args[0], err, msg)
		}
		return fmt.Errorf("docker %s: %w", args[0], err)
	}
	return nil
}
