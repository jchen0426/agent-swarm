package app_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/registry/app"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/registry/memfs"
)

// commits is a fake configuration source: a branch that points at one of
// several commits.
type commits struct {
	mu     sync.Mutex
	head   string
	trees  map[string]map[string][]byte
	reads  []string
	broken error
	// hold, when set, makes the next read of the branch wait for it after it
	// has resolved the commit; held is closed when that read waits.
	hold, held chan struct{}
}

func (c *commits) Read(_ context.Context, ref string) (app.Snapshot, error) {
	c.mu.Lock()
	c.reads = append(c.reads, ref)
	if c.broken != nil {
		c.mu.Unlock()
		return app.Snapshot{}, c.broken
	}
	commit := ref
	hold, held := c.hold, c.held
	if ref == "refs/heads/main" {
		commit = c.head
		c.hold, c.held = nil, nil
	} else {
		hold, held = nil, nil
	}
	files, ok := c.trees[commit]
	c.mu.Unlock()
	if hold != nil {
		close(held)
		<-hold
	}
	if !ok {
		return app.Snapshot{}, fmt.Errorf("no commit %s", ref)
	}
	f, err := memfs.New(files)
	if err != nil {
		return app.Snapshot{}, err
	}
	return app.Snapshot{Commit: commit, Files: f}, nil
}

func (c *commits) point(commit string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.head = commit
}

// records is a fake record of revisions.
type records struct {
	mu       sync.Mutex
	recorded []string
	fail     error
}

func (r *records) Record(_ context.Context, rev *config.Revision) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		return r.fail
	}
	r.recorded = append(r.recorded, rev.Commit())
	return nil
}

type anyCondition struct{}

func (anyCondition) Check(config.Environment, string) error { return nil }

func rule(name string) []byte {
	return []byte("kind: RedactionRule\nmetadata: {name: " + name + ", owner: security@example.com}\nspec: {patterns: ['secret']}\n")
}

func setup() (*commits, *records, *app.Registry) {
	src := &commits{head: "a1", trees: map[string]map[string][]byte{
		"a1": {"redaction-rules/one.yaml": rule("one")},
		"b2": {"redaction-rules/one.yaml": rule("one"), "redaction-rules/two.yaml": rule("two")},
		"c3": {"redaction-rules/one.yaml": rule("wrong-name")},
	}}
	rec := &records{}
	return src, rec, app.New(src, anyCondition{}, rec, "refs/heads/main", runtimes)
}

// runtimes are the deployment's runtime images.
var runtimes = map[string]string{"thinloop": "sha256:abababababababababababababababababababababababababababababababab"}

func agent(runtime string) []byte {
	return []byte("kind: Agent\nmetadata: {name: investigator, owner: alice@example.com}\nspec: {runtime: " + runtime + ", model: claude-haiku-5-5}\n")
}

func TestRefreshLoadsRecordsAndServesTheRevisionAtTheRef(t *testing.T) {
	src, rec, reg := setup()
	assert.Nil(t, reg.Current())

	rev, err := reg.Refresh(t.Context())

	require.NoError(t, err)
	assert.Equal(t, "a1", rev.Commit())
	assert.Same(t, rev, reg.Current())
	assert.Equal(t, []string{"a1"}, rec.recorded)

	src.point("b2")
	rev, err = reg.Refresh(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "b2", rev.Commit())
	assert.NotNil(t, rev.RedactionRule("two"))
}

func TestARefreshAtTheSameCommitRebuildsNothing(t *testing.T) {
	_, rec, reg := setup()
	first, err := reg.Refresh(t.Context())
	require.NoError(t, err)

	again, err := reg.Refresh(t.Context())

	require.NoError(t, err)
	assert.Same(t, first, again)
	assert.Equal(t, []string{"a1"}, rec.recorded)
}

func TestARevisionThatBreaksARuleIsRefusedAndTheLastOneStays(t *testing.T) {
	src, rec, reg := setup()
	good, err := reg.Refresh(t.Context())
	require.NoError(t, err)

	src.point("c3")
	rev, err := reg.Refresh(t.Context())

	var ps config.Problems
	require.ErrorAs(t, err, &ps)
	assert.Contains(t, ps[0].Message, `must match the file name "one"`)
	assert.Same(t, good, rev)
	assert.Same(t, good, reg.Current())
	assert.Equal(t, []string{"a1"}, rec.recorded, "a refused revision is not recorded")
}

// A refresh that refuses the commit it read, by the registry's own rules,
// says which commit, as a RefusedError: a revision that breaks a rule, and
// one that names a runtime without an image. A revision that cannot be
// recorded, and a source that cannot be read, are failures to try again, not
// refusals of a commit.
func TestARefusedCommitIsNamedInTheRefreshsError(t *testing.T) {
	src, rec, reg := setup()
	src.trees["d5"] = map[string][]byte{"redaction-rules/one.yaml": rule("one"), "agents/investigator.yaml": agent("thinlop")}
	for commit, says := range map[string]string{"c3": `must match the file name "one"`, "d5": `runtime "thinlop" of agent investigator has no image`} {
		src.point(commit)

		_, err := reg.Refresh(t.Context())

		var refused *app.RefusedError
		require.ErrorAs(t, err, &refused, commit)
		assert.Equal(t, commit, refused.Commit)
		require.ErrorContains(t, refused.Err, says, commit)
		assert.NotContains(t, refused.Err.Error(), commit, "%s: the reason leaves the commit to its field", commit)
	}

	src.point("a1")
	rec.fail = errors.New("database down")
	_, unrecorded := reg.Refresh(t.Context())
	rec.fail, src.broken = nil, fs.ErrPermission
	_, unread := reg.Refresh(t.Context())

	var refused *app.RefusedError
	require.Error(t, unrecorded)
	require.Error(t, unread)
	assert.NotErrorAs(t, unrecorded, &refused, "a revision that cannot be recorded")
	assert.NotErrorAs(t, unread, &refused, "a source that cannot be read")
}

func TestARevisionThatCannotBeRecordedIsNotServed(t *testing.T) {
	_, rec, reg := setup()
	rec.fail = errors.New("database down")

	rev, err := reg.Refresh(t.Context())

	require.ErrorContains(t, err, "database down")
	assert.Nil(t, rev)
	assert.Nil(t, reg.Current())
}

func TestASourceFailureKeepsTheCurrentRevision(t *testing.T) {
	src, _, reg := setup()
	good, err := reg.Refresh(t.Context())
	require.NoError(t, err)

	src.broken = fs.ErrPermission
	rev, err := reg.Refresh(t.Context())

	require.ErrorIs(t, err, fs.ErrPermission)
	assert.Same(t, good, rev)
}

func TestAtReturnsALockedCommitAfterTheRefMoved(t *testing.T) {
	src, _, reg := setup()
	locked, err := reg.Refresh(t.Context())
	require.NoError(t, err)
	src.point("b2")
	_, err = reg.Refresh(t.Context())
	require.NoError(t, err)

	rev, err := reg.At(t.Context(), "a1")

	require.NoError(t, err)
	assert.Same(t, locked, rev)
	assert.Nil(t, rev.RedactionRule("two"))
}

func TestAtReadsACommitThatIsNotInMemory(t *testing.T) {
	src, _, reg := setup()

	rev, err := reg.At(t.Context(), "b2")

	require.NoError(t, err)
	assert.Equal(t, "b2", rev.Commit())
	assert.Equal(t, []string{"b2"}, src.reads)
	_, err = reg.At(t.Context(), "zz")
	require.ErrorContains(t, err, "no commit zz")
}

func TestAtNeedsAFullCommitID(t *testing.T) {
	_, _, reg := setup()

	_, err := reg.At(t.Context(), "refs/heads/main")

	require.ErrorContains(t, err, "ask for a full commit id")
}

func TestARunningRegistryServesCommitsOnly(t *testing.T) {
	src, _, reg := setup()
	src.trees[""] = map[string][]byte{"redaction-rules/one.yaml": rule("one")}
	src.point("")

	_, err := reg.Refresh(t.Context())

	require.ErrorIs(t, err, app.ErrNoCommit)
}

func TestRevisionsInMemoryAreBounded(t *testing.T) {
	src, _, reg := setup()
	for i := range 40 {
		c := fmt.Sprintf("x%02d", i)
		src.trees[c] = map[string][]byte{"redaction-rules/one.yaml": rule("one")}
		_, err := reg.At(t.Context(), c)
		require.NoError(t, err)
	}
	reads := len(src.reads)

	_, err := reg.At(t.Context(), "x39")
	require.NoError(t, err)
	assert.Len(t, src.reads, reads, "a recent revision is served from memory")

	_, err = reg.At(t.Context(), "x00")
	require.NoError(t, err)
	assert.Len(t, src.reads, reads+1, "an old revision is read again")
}

func TestARefreshThatReadAnOlderCommitNeverReplacesANewerOne(t *testing.T) {
	src, _, reg := setup()
	hold, held := make(chan struct{}), make(chan struct{})
	src.hold, src.held = hold, held
	slow := make(chan error, 1)
	go func() {
		_, err := reg.Refresh(context.Background())
		slow <- err
	}()
	<-held // the slow refresh has read a1, and waits
	src.point("b2")
	fast := make(chan error, 1)
	go func() {
		_, err := reg.Refresh(context.Background())
		fast <- err
	}()
	select {
	case err := <-fast: // only a registry that runs refreshes at once gets here
		fast <- err
	case <-time.After(200 * time.Millisecond):
	}
	close(hold)
	require.NoError(t, <-slow)
	require.NoError(t, <-fast)

	assert.Equal(t, "b2", reg.Current().Commit(), "the newer revision stays current")
}

func TestARevisionWhoseRuntimeHasNoImageIsRefused(t *testing.T) {
	src, rec, reg := setup()
	good, err := reg.Refresh(t.Context())
	require.NoError(t, err)
	src.trees["c4"] = map[string][]byte{"redaction-rules/one.yaml": rule("one"), "agents/investigator.yaml": agent("thinloop")}
	src.trees["d5"] = map[string][]byte{"redaction-rules/one.yaml": rule("one"), "agents/investigator.yaml": agent("thinlop")}

	src.point("d5")
	rev, err := reg.Refresh(t.Context())

	require.ErrorContains(t, err, `runtime "thinlop" of agent investigator has no image`)
	assert.Same(t, good, rev, "a revision no item could lock is not served")
	assert.Equal(t, []string{"a1"}, rec.recorded)

	src.point("c4")
	rev, err = reg.Refresh(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "c4", rev.Commit())
}
