package current_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	llmapp "github.com/jchen0426/agent-swarm/internal/llm/app"
	registryapp "github.com/jchen0426/agent-swarm/internal/registry/app"
	"github.com/jchen0426/agent-swarm/internal/registry/celeval"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/registry/current"
	"github.com/jchen0426/agent-swarm/internal/registry/memfs"
)

const (
	branch = "refs/heads/main"
	// priced is the one model the deployment has a price card for.
	priced = "claude-haiku-5-5"
)

// images are the deployment's runtime images.
var images = map[string]string{"thinloop": "sha256:" + strings.Repeat("a", 64)}

// commit is a full commit id of one repeated hexadecimal digit.
func commit(digit string) string { return strings.Repeat(digit, 40) }

// Skills' texts. A skill holds no plain match of a redaction rule, which the
// registry's own scan refuses; an escape that hides one passes that scan,
// and the LLM gateway's check of a system prompt refuses it.
const (
	triage  = "# Triage\n\nRead the logs first, then the deploys.\n"
	logs    = "# Logs\n\nSearch the logs by service.\n"
	escaped = "# Triage\n\nNever print \\u0041KIA0123456789ABCDEF.\n"
)

// revision is a revision's files: a rule that applies always, a work item
// type, two skills, alpha, of model, who pins triage, of text skill, and
// beta, of the priced model, who pins logs, then triage. The logs skill
// holds a reference file beside its SKILL.md.
func revision(model, skill string) map[string][]byte {
	return map[string][]byte{
		"redaction-rules/keys.yaml": []byte("kind: RedactionRule\nmetadata: {name: keys, owner: security@example.com}\n" +
			"spec:\n  always: true\n  patterns: ['AKIA[0-9A-Z]{16}']\n"),
		"work-item-types/incident.yaml": []byte("kind: WorkItemType\nmetadata: {name: incident, owner: sre-leads}\n" +
			"spec:\n  hypothesisKey:\n    - {name: service, values: [checkout]}\n"),
		"skills/triage/skill.yaml":               []byte(skillFile("triage")),
		"skills/triage/1.0.0/SKILL.md":           []byte(skill),
		"skills/logs/skill.yaml":                 []byte(skillFile("logs")),
		"skills/logs/1.0.0/SKILL.md":             []byte(logs),
		"skills/logs/1.0.0/reference/queries.md": []byte("A query names its service.\n"),
		"agents/alpha.yaml":                      []byte(agentFile("alpha", model, "triage@1.0.0")),
		"agents/beta.yaml":                       []byte(agentFile("beta", priced, "logs@1.0.0", "triage@1.0.0")),
	}
}

func skillFile(name string) string {
	return "kind: Skill\nmetadata: {name: " + name + ", owner: alice@example.com}\nspec:\n  versions:\n" +
		"    - version: 1.0.0\n      source: {repository: https://git.example.com/runbooks, commit: 0123456789ab, path: " + name + "}\n"
}

func agentFile(name, model string, skills ...string) string {
	return "kind: Agent\nmetadata: {name: " + name + ", owner: alice@example.com}\nspec:\n  runtime: thinloop\n  model: " + model +
		"\n  skills: [" + strings.Join(skills, ", ") + "]\n  subscribe: {workItemTypes: [incident]}\n"
}

// broken is a revision that breaks one of the registry's own rules: an agent
// granted the tools of a server the revision does not register.
func broken() map[string][]byte {
	files := revision(priced, triage)
	files["agents/alpha.yaml"] = []byte(agentFile("alpha", priced, "triage@1.0.0") + "  tools: [nowhere.read]\n")
	return files
}

// repository is a configuration repository: its branch points at one of its
// commits. hold, when set, holds every read of a commit until it is closed,
// whatever the read's context, as a read of a slow disk does.
type repository struct {
	mu      sync.Mutex
	head    string
	commits map[string]map[string][]byte
	// reads counts the reads of the branch.
	reads int
	hold  chan struct{}
}

func (r *repository) Read(_ context.Context, ref string) (registryapp.Snapshot, error) {
	r.mu.Lock()
	name, hold := ref, r.hold
	if ref == branch {
		name, hold = r.head, nil
		r.reads++
	}
	files, ok := r.commits[name]
	r.mu.Unlock()
	if hold != nil {
		<-hold
	}
	if !ok {
		return registryapp.Snapshot{}, fmt.Errorf("no commit %s in the repository", ref)
	}
	fsys, err := memfs.New(files)
	if err != nil {
		return registryapp.Snapshot{}, err
	}
	return registryapp.Snapshot{Commit: name, Files: fsys}, nil
}

// commitAt adds a commit of files and points the branch at it.
func (r *repository) commitAt(name string, files map[string][]byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.commits[name] = files
	r.head = name
}

func (r *repository) branchReads() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reads
}

// recorded is the record of revisions, which keeps nothing.
type recorded struct{}

func (recorded) Record(context.Context, *config.Revision) error { return nil }

// logLines holds what a JSON log writes, from any goroutine.
type logLines struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logLines) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

// of returns the lines logged with msg.
func (l *logLines) of(t *testing.T, msg string) []map[string]any {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []map[string]any
	sc := bufio.NewScanner(bytes.NewReader(l.buf.Bytes()))
	for sc.Scan() {
		var line map[string]any
		require.NoError(t, json.Unmarshal(sc.Bytes(), &line))
		if line["msg"] == msg {
			out = append(out, line)
		}
	}
	return out
}

// setup returns a repository whose branch points at commit a, of files, and
// the registry at runtime over it, which knows the price of the priced model
// alone, checks skills as the LLM gateway does, and refreshes every minute.
func setup(t *testing.T, files map[string][]byte) (*repository, *current.Revisions, *logLines) {
	t.Helper()
	cc, err := celeval.New()
	require.NoError(t, err)
	repo := &repository{head: commit("a"), commits: map[string]map[string][]byte{commit("a"): files}}
	lines := &logLines{}
	r := current.New(registryapp.New(repo, cc, recorded{}, branch, images), current.Options{
		Refresh: time.Minute, Images: images, Priced: func(model string) bool { return model == priced }, CheckSkill: llmapp.CheckSkill,
		Log: slog.New(slog.NewJSONHandler(lines, nil)),
	})
	return repo, r, lines
}

// running runs r until the test ends, and waits for its first refresh. It
// returns a function that stops r and waits for Run to return.
func running(t *testing.T, r *current.Revisions) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		r.Run(ctx)
		close(done)
	}()
	stop = sync.OnceFunc(func() {
		cancel()
		<-done
	})
	t.Cleanup(stop)
	synctest.Wait()
	return stop
}

// A revision becomes current only once it passes the checks the registry
// cannot make (Decision 1): every agent's model has a price card in the
// deployment, and every skill an agent pins passes the LLM gateway's checks
// as a system prompt, under that revision's rules. A revision that fails one
// is refused, and logged with its commit and the reason, which carries the
// check's own words; until a revision passes, none is current. One that
// passes is current, and logged so. A commit refused is logged once, however
// many refreshes find the branch at it.
func TestARevisionBecomesCurrentOnlyIfEveryModelIsPricedAndEverySkillPasses(t *testing.T) {
	for name, tc := range map[string]struct {
		files  map[string][]byte
		reason []string
	}{
		"a model without a price card": {
			files: revision("claude-opus-5-5", triage), reason: []string{"agent alpha", "claude-opus-5-5", "price card"},
		},
		"a skill the LLM gateway refuses as a system prompt": {
			files: revision(priced, escaped),
			reason: []string{
				"triage@1.0.0", "agent alpha", "app: the system prompt holds escapes that may hide what a redaction rule matches, and cannot be redacted",
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				repo, r, lines := setup(t, tc.files)
				running(t, r)

				assert.Nil(t, r.Current(), "no revision is current")
				refused := lines.of(t, "revision refused")
				require.Len(t, refused, 1)
				assert.Equal(t, commit("a"), refused[0]["commit"])
				for _, words := range tc.reason {
					assert.Contains(t, refused[0]["reason"], words)
				}
				assert.Empty(t, lines.of(t, "revision current"))

				time.Sleep(time.Minute)
				synctest.Wait()
				assert.Nil(t, r.Current())
				assert.Len(t, lines.of(t, "revision refused"), 1, "a commit refused is logged once while the branch stays at it")

				repo.commitAt(commit("b"), revision(priced, triage))
				time.Sleep(time.Minute)
				synctest.Wait()

				require.NotNil(t, r.Current())
				assert.Equal(t, commit("b"), r.Current().Commit())
				made := lines.of(t, "revision current")
				require.Len(t, made, 1)
				assert.Equal(t, commit("b"), made[0]["commit"])
				assert.Equal(t, "INFO", made[0]["level"])
			})
		})
	}
}

// A revision refused, by the checks or by the registry's own rules, leaves
// the last current revision current: items that start go on starting under
// it, and those that locked it read it still. Either refusal is logged
// "revision refused", with its commit and the reason, once however many
// refreshes find the branch at it. A branch that leaves a refused commit and
// comes back to it has it refused, and logged, again.
func TestARefusedRevisionKeepsTheLastCurrentOne(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		repo, r, lines := setup(t, revision(priced, triage))
		running(t, r)
		first := r.Current()
		require.NotNil(t, first)
		require.Equal(t, commit("a"), first.Commit())

		repo.commitAt(commit("b"), revision("claude-opus-5-5", triage))
		time.Sleep(time.Minute)
		synctest.Wait()

		assert.Same(t, first, r.Current(), "a revision the checks refuse")
		refused := lines.of(t, "revision refused")
		require.Len(t, refused, 1)
		assert.Equal(t, commit("b"), refused[0]["commit"])

		repo.commitAt(commit("a"), revision(priced, triage))
		time.Sleep(time.Minute)
		synctest.Wait()
		repo.commitAt(commit("b"), revision("claude-opus-5-5", triage))
		time.Sleep(time.Minute)
		synctest.Wait()

		assert.Same(t, first, r.Current())
		refused = lines.of(t, "revision refused")
		require.Len(t, refused, 2, "a commit the branch comes back to is refused, and logged, again")
		assert.Equal(t, commit("b"), refused[1]["commit"])

		repo.commitAt(commit("c"), broken())
		time.Sleep(time.Minute)
		synctest.Wait()
		time.Sleep(time.Minute)
		synctest.Wait()

		assert.Same(t, first, r.Current(), "a revision the registry refuses")
		refused = lines.of(t, "revision refused")
		require.Len(t, refused, 3, "a commit the registry's rules refuse is logged once, as one the checks refuse is")
		assert.Equal(t, commit("c"), refused[2]["commit"])
		assert.Contains(t, refused[2]["reason"], "nowhere")
		assert.NotContains(t, refused[2]["reason"], commit("c"), "the commit is its own attribute")
		assert.Empty(t, lines.of(t, "the registry could not be refreshed; the next refresh tries again"))
		at, err := r.At(t.Context(), commit("a"))
		require.NoError(t, err)
		require.NotNil(t, at)
		assert.Equal(t, commit("a"), at.Commit())
		assert.Len(t, lines.of(t, "revision current"), 1, "only the first revision became current")
	})
}

// A refresh that fails, and refuses no commit, as when the branch names a
// commit the repository lacks, is logged at each refresh, and tried again: it
// says nothing about a commit.
func TestARefreshThatFailsIsLoggedEachTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		repo, r, lines := setup(t, revision(priced, triage))
		running(t, r)
		require.NotNil(t, r.Current())
		repo.mu.Lock()
		repo.head = commit("d")
		repo.mu.Unlock()

		time.Sleep(time.Minute)
		synctest.Wait()
		time.Sleep(time.Minute)
		synctest.Wait()

		failed := lines.of(t, "the registry could not be refreshed; the next refresh tries again")
		require.Len(t, failed, 2)
		assert.Contains(t, failed[0]["err"], "no commit")
		assert.Empty(t, lines.of(t, "revision refused"))
		assert.Equal(t, commit("a"), r.Current().Commit())
	})
}

// The registry refreshes at once, then every period, until Run's context
// ends; Run then returns, and no refresh follows.
func TestRefreshesRunEveryPeriodUntilTheContextEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		repo, r, _ := setup(t, revision(priced, triage))
		stop := running(t, r)
		assert.Equal(t, 1, repo.branchReads(), "a refresh at once")

		time.Sleep(time.Minute - time.Second)
		synctest.Wait()
		assert.Equal(t, 1, repo.branchReads(), "none before the period")

		time.Sleep(time.Second)
		synctest.Wait()
		assert.Equal(t, 2, repo.branchReads())

		repo.commitAt(commit("b"), revision(priced, logs))
		time.Sleep(time.Minute)
		synctest.Wait()
		assert.Equal(t, 3, repo.branchReads())
		require.NotNil(t, r.Current())
		assert.Equal(t, commit("b"), r.Current().Commit(), "a refresh makes the branch's new commit current")

		stop()
		time.Sleep(10 * time.Minute)
		synctest.Wait()
		assert.Equal(t, 3, repo.branchReads(), "none once the context ended")
	})
}

// A commit the repository lacks cannot be read: At fails, so that the step
// that needed it is tried again, and a gateway's call answers unavailable.
// A commit it has is read, current or not.
func TestAMissingCommitFailsAt(t *testing.T) {
	repo, r, _ := setup(t, revision(priced, triage))
	repo.commitAt(commit("b"), revision(priced, logs))

	_, err := r.At(t.Context(), commit("f"))
	require.ErrorContains(t, err, commit("f"))

	rev, err := r.At(t.Context(), commit("a"))
	require.NoError(t, err)
	require.NotNil(t, rev)
	assert.Equal(t, commit("a"), rev.Commit())
}

// An agent's skills are the texts of the skill versions it pins, in the
// order it pins them, at the commit its item locked: each version's
// SKILL.md, the file a runtime reads first. An agent the commit does not
// have has none to read.
func TestSkillsAreTheTextsAnAgentPinsAtACommit(t *testing.T) {
	repo, r, _ := setup(t, revision(priced, triage))
	repo.commitAt(commit("b"), revision(priced, "# Triage\n\nRead the deploys first.\n"))

	skills, err := r.Skills(t.Context(), commit("a"), "beta")
	require.NoError(t, err)
	assert.Equal(t, []string{logs, triage}, skills)

	skills, err = r.Skills(t.Context(), commit("b"), "alpha")
	require.NoError(t, err)
	assert.Equal(t, []string{"# Triage\n\nRead the deploys first.\n"}, skills, "the commit's own text")

	_, err = r.Skills(t.Context(), commit("a"), "gamma")
	require.ErrorContains(t, err, "gamma")

	_, err = r.Skills(t.Context(), commit("f"), "alpha")
	require.Error(t, err)
}

// A read of a revision returns once its context ends, though the repository
// has not answered: the runner waits for an agent's skills with no bound of
// its own, and a coordinator that is fenced waits for nothing.
func TestReadsReturnOnceTheirContextEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		repo, r, _ := setup(t, revision(priced, triage))
		repo.commitAt(commit("b"), revision(priced, logs))
		hold := make(chan struct{})
		repo.mu.Lock()
		repo.hold = hold
		repo.mu.Unlock()
		defer close(hold)

		ctx, cancel := context.WithCancel(t.Context())
		var skillsErr, atErr error
		var wg sync.WaitGroup
		wg.Go(func() { _, skillsErr = r.Skills(ctx, commit("b"), "alpha") })
		wg.Go(func() { _, atErr = r.At(ctx, commit("b")) })
		synctest.Wait()
		cancel()
		wg.Wait()

		require.ErrorIs(t, skillsErr, context.Canceled)
		require.ErrorIs(t, atErr, context.Canceled)
	})
}

// A revision is locked with the deployment's runtime images.
func TestALockPinsTheDeploymentsImages(t *testing.T) {
	_, r, _ := setup(t, revision(priced, triage))
	rev, err := r.At(t.Context(), commit("a"))
	require.NoError(t, err)
	require.NotNil(t, rev)

	lock, err := r.Lock(rev)

	require.NoError(t, err)
	assert.Equal(t, config.Lock{Commit: commit("a"), Digest: rev.Digest(), RuntimeImages: images}, lock)
}
