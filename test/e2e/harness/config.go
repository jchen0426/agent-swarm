//go:build e2e

package harness

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	// runtimeName is the runtime every agent of the harness names; every
	// deployment the harness writes maps it to an image.
	runtimeName = "thinloop"
	// configBranch is the branch of the configuration repository, and
	// configRef the full reference a deployment names it by: the registry's
	// source refuses a short name.
	configBranch = "main"
	configRef    = "refs/heads/" + configBranch

	skillVersion = "1.0.0"
	personOwner  = "harness@example.com"
	teamOwner    = "harness-team"
)

// agentName is what an agent's name may be: a registration's name, a DNS label.
var agentName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ConfigSpec describes the configuration repository Config writes: one team of
// agents, the work item type their items open as, and the scripted servers
// the agents may call. Every scripted server of the Env that is not Plain is
// registered, with its tools pinned.
type ConfigSpec struct {
	Agents   []AgentSpec
	WorkType WorkTypeSpec
}

// AgentSpec describes an agent. Its runtime is thinloop.
type AgentSpec struct {
	// Name is the agent's name, a lowercase DNS label.
	Name string
	// Model is the model the agent uses; when empty, claude-sonnet-5-5.
	Model string
	// Skill is the text of the agent's skill. The skill starts with the line
	// "You are <Name>.", by which the scripted vendor tells the agents apart.
	Skill string
	// Servers are the scripted servers the agent is granted: every tool.
	Servers []string
}

// WorkTypeSpec describes the work item type. A field left zero takes the
// harness's value, noted here, which may differ from the platform's default.
type WorkTypeSpec struct {
	// Name is the type's name; when empty, incident.
	Name string
	// RoundLimit is a round's time limit; when zero, 30 s.
	RoundLimit time.Duration
	// WallClock, Tokens and Rounds are the run's budget; when zero, 5 minutes
	// or six round limits if longer, 2,000,000 cost-weighted tokens, and 6
	// rounds.
	WallClock time.Duration
	Tokens    int64
	Rounds    int
	// MinContributors and MinSources are the convergence thresholds: how many
	// agents, and how many sources, a hypothesis needs to converge and so
	// to get its wrap-up round; when zero, the platform's 2 and 2. A work item
	// type has no setting for the wrap-up rounds themselves: a run has at most
	// two.
	MinContributors int
	MinSources      int
}

// Repo is a configuration repository the harness wrote: a Git repository with
// the branch main checked out.
type Repo struct {
	dir     string
	models  []string
	commits int
}

// Dir returns the repository's directory, which a deployment names as
// configuration.repository.
func (r *Repo) Dir() string { return r.dir }

// Ref returns the full reference of the branch a deployment follows,
// configuration.ref.
func (r *Repo) Ref() string { return configRef }

// Commit lets change edit the repository's files, in dir, then commits every
// change and returns the new commit's id. A change that edits nothing still
// makes a commit.
func (r *Repo) Commit(t *testing.T, change func(dir string)) string {
	t.Helper()
	change(r.dir)
	r.commits++
	git(t, r.dir, "add", "--all")
	git(t, r.dir, "commit", "--quiet", "--allow-empty", "--no-gpg-sign", "-m", "harness change "+strconv.Itoa(r.commits))
	return git(t, r.dir, "rev-parse", "HEAD")
}

// git runs git in dir, with no configuration of the user's, and returns its
// output without the final newline.
func git(t testing.TB, dir string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...) //nolint:gosec // G204: the harness's own arguments.
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=harness", "GIT_AUTHOR_EMAIL="+personOwner,
		"GIT_COMMITTER_NAME=harness", "GIT_COMMITTER_EMAIL="+personOwner)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("harness: git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// Config writes the configuration repository the spec describes, commits it
// to the branch main, and returns it. A later Deployment names this
// repository, and a price card for each model its agents use. Each scripted
// server that is not Plain is registered with its tools pinned by the hashes
// that agentswarm registry hash-tools prints for them.
//
//nolint:gocritic // hugeParam: the contract passes a scenario's spec by value.
func (e *Env) Config(t *testing.T, c ConfigSpec) *Repo {
	t.Helper()
	files, models := e.configFiles(t, &c)
	e.mu.Lock()
	e.configs++
	dir := filepath.Join(e.dir, "config-"+strconv.Itoa(e.configs))
	e.mu.Unlock()
	for name, text := range files {
		writeFile(t, filepath.Join(dir, filepath.FromSlash(name)), text)
	}
	git(t, dir, "init", "--quiet", "--initial-branch", configBranch)
	git(t, dir, "add", "--all")
	git(t, dir, "commit", "--quiet", "--no-gpg-sign", "-m", "harness configuration")
	repo := &Repo{dir: dir, models: models}
	e.mu.Lock()
	e.repo = repo
	e.mu.Unlock()
	return repo
}

func writeFile(t testing.TB, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("harness: %v", err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatalf("harness: %v", err)
	}
}

// configFiles returns the files of the repository by their paths, and the
// models the agents use.
func (e *Env) configFiles(t testing.TB, c *ConfigSpec) (files map[string]string, models []string) {
	t.Helper()
	wt := c.WorkType.withDefaults()
	files = map[string]string{
		"redaction-rules/default-secrets.yaml": redactionRule,
		"work-item-types/" + wt.Name + ".yaml": wt.yaml(),
	}
	for _, name := range e.order {
		s := e.servers[name]
		if s.spec.Plain {
			continue
		}
		files["servers/"+name+".yaml"] = e.serverYAML(t, s)
	}
	seen := map[string]bool{}
	for i := range c.Agents {
		a := c.Agents[i]
		if !agentName.MatchString(a.Name) || seen[a.Name] {
			t.Fatalf("harness: %q is not the name of one agent: a lowercase DNS label", a.Name)
		}
		seen[a.Name] = true
		a.Model = cmp.Or(a.Model, defaultModel)
		if !slices.Contains(models, a.Model) {
			models = append(models, a.Model)
		}
		for _, server := range a.Servers {
			if s := e.servers[server]; s == nil || s.spec.Plain {
				t.Fatalf("harness: agent %s is granted %q, which is not a scripted server that is registered", a.Name, server)
			}
		}
		files["agents/"+a.Name+".yaml"] = agentYAML(&a, wt.Name)
		files["skills/"+a.Name+"-skill/skill.yaml"] = skillYAML(a.Name)
		files["skills/"+a.Name+"-skill/"+skillVersion+"/SKILL.md"] = skillText(a.Name, a.Skill)
	}
	if !slices.Contains(models, defaultModel) {
		models = append(models, defaultModel)
	}
	return files, models
}

// redactionRule is the rule the skills' blocking scan needs: a revision with no
// pattern could scan nothing, and refuses its skills.
const redactionRule = `kind: RedactionRule
metadata: {name: default-secrets, owner: ` + teamOwner + `}
spec:
  always: true
  patterns:
    - '\bAKIA[0-9A-Z]{16}\b'
`

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// serverYAML registers a scripted server: its endpoint, a pinned image, its
// credential, and its tools pinned by hash-tools, all of them reads.
func (e *Env) serverYAML(t testing.TB, s *MCPServer) string {
	t.Helper()
	if len(s.spec.Tools) == 0 {
		t.Fatalf("harness: server %s lists no tool, and the registry refuses a registration without tools", s.name)
	}
	hashes := e.pinTools(t, s)
	var b strings.Builder
	b.WriteString("kind: McpServer\nmetadata: {name: " + s.name + ", owner: " + teamOwner + "}\nspec:\n")
	b.WriteString("  endpoint: " + s.URL() + "\n")
	sum := sha256.Sum256([]byte("harness server " + s.name))
	b.WriteString("  image: " + quote("sha256:"+hex.EncodeToString(sum[:])) + "\n")
	b.WriteString("  credentialRef: " + credentialRef(s.name) + "\n  tools:\n")
	for _, tool := range s.spec.Tools {
		b.WriteString("    " + quote(tool.Name) + ": {schemaHash: " + quote(hashes[tool.Name]) + ", effect: read}\n")
	}
	return b.String()
}

// credentialRef is the reference of a server's credential in the secrets
// directory of each instance.
func credentialRef(server string) string { return "secret://mcp/" + server }

// pinTools lists the server's tools through the binary, as a person pins them,
// at the server's pin door, which asks for no credential, and returns each
// tool's schema hash.
func (e *Env) pinTools(t testing.TB, s *MCPServer) map[string]string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary(t), "registry", "hash-tools", "--endpoint", s.PinURL(), "--ca", e.caFile) //nolint:gosec // G204: the binary under test, and the harness's own server.
	out, err := cmd.Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		t.Fatalf("harness: hash the tools of server %s: %v\n%s", s.name, err, exit.Stderr)
	}
	if err != nil {
		t.Fatalf("harness: hash the tools of server %s: %v", s.name, err)
	}
	var listed []struct {
		Name       string `json:"name"`
		SchemaHash string `json:"schemaHash"`
		Error      string `json:"error"`
	}
	if err := json.Unmarshal(out, &listed); err != nil {
		t.Fatalf("harness: read the hashes of server %s: %v\n%s", s.name, err, out)
	}
	hashes := map[string]string{}
	for _, tool := range listed {
		if tool.Error != "" || tool.SchemaHash == "" {
			t.Fatalf("harness: tool %s of server %s has no hash: %s", tool.Name, s.name, tool.Error)
		}
		hashes[tool.Name] = tool.SchemaHash
	}
	for _, tool := range s.spec.Tools {
		if hashes[tool.Name] == "" {
			t.Fatalf("harness: server %s did not list tool %s", s.name, tool.Name)
		}
	}
	return hashes
}

func agentYAML(a *AgentSpec, workType string) string {
	grants := make([]string, 0, len(a.Servers))
	for _, server := range a.Servers {
		grants = append(grants, quote(server+".*"))
	}
	return "kind: Agent\nmetadata: {name: " + a.Name + ", owner: " + personOwner + "}\nspec:\n" +
		"  runtime: " + runtimeName + "\n" +
		"  model: " + quote(a.Model) + "\n" +
		"  skills: [" + a.Name + "-skill@" + skillVersion + "]\n" +
		"  tools: [" + strings.Join(grants, ", ") + "]\n" +
		"  subscribe: {workItemTypes: [" + workType + "]}\n"
}

func skillYAML(agent string) string {
	return "kind: Skill\nmetadata: {name: " + agent + "-skill, owner: " + personOwner + "}\nspec:\n  versions:\n" +
		"    - version: " + skillVersion + "\n" +
		"      source: {repository: https://git.example.com/harness, commit: 0123456789ab, path: skills/" + agent + "}\n"
}

// skillText is a skill's SKILL.md: the line that names the agent, then the
// text the scenario gave.
func skillText(agent, text string) string {
	skill := "# " + agent + "\n\nYou are " + agent + ".\n"
	if text != "" {
		skill += "\n" + text + "\n"
	}
	return skill
}

// withDefaults fills in the fields left zero.
func (w WorkTypeSpec) withDefaults() WorkTypeSpec {
	w.Name = cmp.Or(w.Name, "incident")
	if w.RoundLimit == 0 {
		w.RoundLimit = 30 * time.Second
	}
	if w.Rounds == 0 {
		w.Rounds = 6
	}
	if w.Tokens == 0 {
		w.Tokens = 2_000_000
	}
	if w.WallClock == 0 {
		w.WallClock = max(5*time.Minute, w.RoundLimit*time.Duration(w.Rounds))
	}
	return w
}

func (w WorkTypeSpec) yaml() string {
	var b strings.Builder
	b.WriteString("kind: WorkItemType\nmetadata: {name: " + w.Name + ", owner: " + teamOwner + "}\nspec:\n")
	b.WriteString("  hypothesisKey:\n")
	b.WriteString("    - {name: service, values: [checkout, payments, cart]}\n")
	b.WriteString("    - {name: failureMode, values: [bad-release, flag-change, config-change]}\n")
	b.WriteString("  budget: {wallClock: " + w.WallClock.String() + ", tokens: " + strconv.FormatInt(w.Tokens, 10) +
		", rounds: " + strconv.Itoa(w.Rounds) + "}\n")
	b.WriteString("  round: {timeLimit: " + w.RoundLimit.String() + "}\n")
	if w.MinContributors > 0 || w.MinSources > 0 {
		var fields []string
		if w.MinContributors > 0 {
			fields = append(fields, "minContributors: "+strconv.Itoa(w.MinContributors))
		}
		if w.MinSources > 0 {
			fields = append(fields, "minSources: "+strconv.Itoa(w.MinSources))
		}
		b.WriteString("  convergence: {" + strings.Join(fields, ", ") + "}\n")
	}
	b.WriteString("  measurement: {invisibleShare: 0}\n")
	return b.String()
}
