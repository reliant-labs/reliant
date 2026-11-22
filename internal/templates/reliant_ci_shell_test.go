package templates

// The ci.provider reliant ci.yaml runs shell commands built from templates,
// one of them holding the machine's git credentials. These tests pin how a
// value reaches those shells: only from the facts and resolved compute nodes,
// which check every value against a pattern that cannot carry shell syntax,
// and only inside single quotes. They also pin that the pr and queue tiers
// never gate without a base commit.

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/pkg/testkit"
)

type reliantCINode struct {
	ID        string            `yaml:"id"`
	Type      string            `yaml:"type"`
	Condition string            `yaml:"condition"`
	Outputs   map[string]string `yaml:"outputs"`
	Args      struct {
		Command     string `yaml:"command"`
		Credentials string `yaml:"credentials"`
		WorkDir     string `yaml:"work_dir"`
	} `yaml:"args"`
}

type reliantCITrigger struct {
	Name        string `yaml:"name"`
	Integration struct {
		Events []string `yaml:"events"`
	} `yaml:"integration"`
	OriginFilter string            `yaml:"origin_filter"`
	Filter       string            `yaml:"filter"`
	Inputs       map[string]string `yaml:"inputs"`
}

type reliantCIDoc struct {
	Triggers []reliantCITrigger `yaml:"triggers"`
	Nodes    []reliantCINode    `yaml:"nodes"`
	Edges    []struct {
		From  string `yaml:"from"`
		Cases []struct {
			Condition string   `yaml:"condition"`
			To        []string `yaml:"to"`
		} `yaml:"cases"`
		Default string `yaml:"default"`
	} `yaml:"edges"`
	Finally struct {
		Sequence []reliantCINode `yaml:"sequence"`
	} `yaml:"finally"`
}

func renderReliantCI(t *testing.T) reliantCIDoc {
	t.Helper()
	out, err := CITemplates("reliant").Render("ci.yaml.tmpl", ReliantCIWorkflowData{Check: "demo/ci"})
	if err != nil {
		t.Fatal(err)
	}
	var doc reliantCIDoc
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatalf("ci.yaml is not YAML: %v", err)
	}
	return doc
}

func (d reliantCIDoc) node(t *testing.T, id string) reliantCINode {
	t.Helper()
	for _, n := range append(append([]reliantCINode(nil), d.Nodes...), d.Finally.Sequence...) {
		if n.ID == id {
			return n
		}
	}
	t.Fatalf("ci.yaml has no node %q", id)
	return reliantCINode{}
}

var templateAction = regexp.MustCompile(`'?\{\{\s*(.*?)\s*\}\}'?`)

// checkedValues are the node outputs a shell may interpolate: each is
// checked by facts.valid or resolved.ok before any run node can run.
var checkedValues = map[string]bool{
	"nodes.facts.tier": true, "nodes.facts.sha": true, "nodes.facts.base": true,
	"nodes.facts.default_branch": true, "nodes.facts.path": true,
	"nodes.resolved.base": true, "nodes.resolved.prefix": true, "nodes.resolved.run": true,
	// finally's cleanup may run when resolved did not (a cancel before it).
	"nodes.?resolved.run.orValue('')": true,
}

// Every value a run node's command interpolates is a checked one, inside
// single quotes: never workflow.*, inputs.* or another node's raw output.
func TestReliantCI_ShellsInterpolateOnlyCheckedQuotedValues(t *testing.T) {
	doc := renderReliantCI(t)
	runs := 0
	for _, n := range append(append([]reliantCINode(nil), doc.Nodes...), doc.Finally.Sequence...) {
		if n.Type != "run" {
			continue
		}
		runs++
		for _, m := range templateAction.FindAllStringSubmatch(n.Args.Command, -1) {
			if !checkedValues[m[1]] {
				t.Errorf("run node %s interpolates %q, which no compute node checked", n.ID, m[1])
			}
			if !strings.HasPrefix(m[0], "'") || !strings.HasSuffix(m[0], "'") {
				t.Errorf("run node %s interpolates %s outside single quotes", n.ID, m[0])
			}
		}
	}
	if runs != 3 {
		t.Fatalf("checked %d run nodes, want fetch, gate and cleanup", runs)
	}
	// The gate runs PR code: it starts in the run's own directory, never the
	// project's checkout, and reads the commit from fetch's bundle, never
	// from the checkout's object store.
	gate := doc.node(t, "gate")
	if gate.Args.WorkDir != "{{ nodes.resolved.dir }}" {
		t.Errorf("gate work_dir = %q, want the run's own directory", gate.Args.WorkDir)
	}
	for _, banned := range []string{"nodes.facts.path", "--shared", "git clone", "objects/info/alternates", "--reference"} {
		if strings.Contains(gate.Args.Command, banned) {
			t.Errorf("the gate's command contains %q: it must not reach the project's checkout", banned)
		}
	}
	if !strings.Contains(gate.Args.Command, `fetch --quiet --no-tags "$bundle"`) {
		t.Error("the gate does not read the commit from the bundle")
	}
	if got := doc.node(t, "cleanup").Args.WorkDir; got != "{{ nodes.?resolved.root.orValue('') }}" {
		t.Errorf("cleanup work_dir = %q, want the run directories' parent", got)
	}
	// The run directory is mktemp's, reported by fetch: never derived from
	// the chat id, which two runs can share.
	if raw, err := CITemplates("reliant").Render("ci.yaml.tmpl", ReliantCIWorkflowData{Check: "demo/ci"}); err != nil {
		t.Fatal(err)
	} else if strings.Contains(string(raw), "chat_id") {
		t.Error("ci.yaml reads workflow.chat_id: a run directory or ref named by it is shared by runs of one chat")
	}
	fetch := doc.node(t, "fetch").Args.Command
	for _, want := range []string{`mktemp -d "$root/run.XXXXXXXX"`, `find -P "$root" -mindepth 1 -maxdepth 1 -type d -user "$uid" -name 'run.*' -mtime +2`, `-perm 700`} {
		if !strings.Contains(fetch, want) {
			t.Errorf("fetch lacks %q", want)
		}
	}
	if strings.Contains(fetch, "-exec rm") {
		t.Error("fetch deletes what find matched without checking each directory")
	}
	if doc.node(t, "gate").Args.Credentials != "none" || doc.node(t, "cleanup").Args.Credentials != "none" {
		t.Error("gate and cleanup must run with credentials: none")
	}
}

// facts.valid refuses a project path that could break out of the single
// quotes, or otherwise mean something to a shell.
func TestReliantCI_FactsRefuseShellSyntax(t *testing.T) {
	valid := renderReliantCI(t).node(t, "facts").Outputs["valid"]
	m := regexp.MustCompile(`workflow\.path\.matches\('([^']+)'\)`).FindStringSubmatch(valid)
	if m == nil {
		t.Fatalf("facts.valid does not check workflow.path against a pattern:\n%s", valid)
	}
	path := regexp.MustCompile(m[1]) // RE2, as CEL's matches is
	for _, v := range []string{"relative/dir", "/a'b", "/a\nb", "/$(id)", "/`id`", "/a;b", "/a\"b"} {
		if path.MatchString(v) {
			t.Errorf("a project path %q passes facts.valid", v)
		}
	}
	for _, v := range []string{"/home/workspace/projects/demo", "/Users/me/My Projects/demo"} {
		if !path.MatchString(v) {
			t.Errorf("project path %q is refused", v)
		}
	}
	if !strings.Contains(valid, "inputs.tier in [") {
		t.Errorf("facts.valid does not check the tier:\n%s", valid)
	}
	// resolved accepts only a run directory named as mktemp names it.
	m = regexp.MustCompile(`\^run=\(([^)]+)\)\$`).FindStringSubmatch(renderReliantCI(t).node(t, "resolved").Outputs["run"])
	if m == nil {
		t.Fatal("resolved.run does not extract a run directory by a pattern")
	}
	run := regexp.MustCompile("^" + m[1] + "$")
	for _, v := range []string{"run.", "run.abc", "run.abcdefgh/../x", "run.abcd'fgh", "../run.abcdefgh", "run.abcdefghi", "x/run.abcdefgh", "run.abcd$(id)"} {
		if run.MatchString(v) {
			t.Errorf("run directory %q passes resolved", v)
		}
	}
	if !run.MatchString("run.Ab3dE6gH") {
		t.Errorf("resolved's run pattern %s refuses mktemp's run.Ab3dE6gH", run)
	}
}

// The gate runs only when resolved.ok, which requires a base sha unless the
// tier is main; anything else goes to doc, which reports resolved.error and so
// concludes the check failure.
func TestReliantCI_GateNeedsAResolvedBase(t *testing.T) {
	doc := renderReliantCI(t)
	ok := doc.node(t, "resolved").Outputs["ok"]
	for _, want := range []string{"nodes.fetch.exit_code == 0", "(?m)^base=([0-9a-f]{40})$').hasValue()", "nodes.facts.tier == 'main'", "contains('..')"} {
		if !strings.Contains(ok, want) {
			t.Errorf("resolved.ok lacks %q:\n%s", want, ok)
		}
	}
	routed := false
	for _, e := range doc.Edges {
		switch e.From {
		case "fetch":
			if e.Default != "resolved" || len(e.Cases) != 0 {
				t.Errorf("fetch must go only to resolved, got %+v", e)
			}
		case "resolved":
			routed = len(e.Cases) == 1 && e.Cases[0].Condition == "nodes.resolved.ok == true" &&
				strings.Join(e.Cases[0].To, ",") == "gate" && e.Default == "doc"
		}
	}
	if !routed {
		t.Error("resolved must route to gate only when ok, and to doc otherwise")
	}
	if !strings.Contains(doc.node(t, "doc").Outputs["doc"], "!nodes.resolved.ok") {
		t.Error("doc does not report a run whose base did not resolve")
	}
}

// script is a run node's command as the engine would run it, with the
// checked values substituted (each lands inside the template's quotes).
func script(t *testing.T, node string, values map[string]string) string {
	t.Helper()
	cmd := renderReliantCI(t).node(t, node).Args.Command
	return templateAction.ReplaceAllStringFunc(cmd, func(s string) string {
		m := templateAction.FindStringSubmatch(s)
		v, ok := values[m[1]]
		if !ok {
			t.Fatalf("%s interpolates %s", node, m[1])
		}
		return "'" + v + "'"
	})
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.email=t@t", "-c", "user.name=t", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// ciFixture is an origin with two commits, the project's checkout cloned
// from it (in a directory with a space), and a cache home for run
// directories.
type ciFixture struct {
	tmp, origin, checkout, cache, base, head, side string
}

func newCIFixture(t *testing.T) ciFixture {
	t.Helper()
	tmp := t.TempDir()
	f := ciFixture{tmp: tmp, origin: filepath.Join(tmp, "origin"), checkout: filepath.Join(tmp, "my project"), cache: filepath.Join(tmp, "cache")}
	if err := os.MkdirAll(filepath.Join(f.origin, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, f.origin, "init", "-q")
	if err := os.WriteFile(filepath.Join(f.origin, "sub", "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, f.origin, "add", ".")
	git(t, f.origin, "commit", "-q", "-m", "base")
	f.base = git(t, f.origin, "rev-parse", "HEAD")
	git(t, f.origin, "commit", "-q", "--allow-empty", "-m", "head")
	f.head = git(t, f.origin, "rev-parse", "HEAD")
	git(t, f.origin, "checkout", "-q", "-b", "side", f.base)
	git(t, f.origin, "commit", "-q", "--allow-empty", "-m", "side")
	f.side = git(t, f.origin, "rev-parse", "HEAD")
	git(t, f.origin, "checkout", "-q", "main")
	git(t, f.origin, "config", "uploadpack.allowAnySHA1InWant", "true")
	git(t, tmp, "clone", "-q", f.origin, f.checkout)
	return f
}

// run runs a script with HOME and XDG_CACHE_HOME in the fixture.
func (f ciFixture) run(t *testing.T, dir, body string, env ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command("bash", "-c", body)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "HOME="+f.tmp, "XDG_CACHE_HOME="+f.cache), env...)
	var o, e strings.Builder
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(o.String()), e.String(), code
}

func (f ciFixture) fetch(t *testing.T, tier, base, branch string, env ...string) (map[string]string, string, int) {
	t.Helper()
	return f.fetchIn(t, filepath.Join(f.checkout, "sub"), tier, base, branch, env...)
}

func (f ciFixture) fetchIn(t *testing.T, project, tier, base, branch string, env ...string) (map[string]string, string, int) {
	t.Helper()
	out, errOut, code := f.run(t, f.tmp, script(t, "fetch", map[string]string{
		"nodes.facts.tier": tier, "nodes.facts.sha": f.head, "nodes.facts.base": base,
		"nodes.facts.default_branch": branch, "nodes.facts.path": project,
	}), env...)
	return parseLines(out), errOut, code
}

func parseLines(out string) map[string]string {
	lines := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			lines[k] = v
		}
	}
	return lines
}

// cleanup runs finally's cleanup as the engine would: in the root, for the
// run directory fetch reported.
func (f ciFixture) cleanup(t *testing.T, run string) {
	t.Helper()
	if _, errOut, code := f.run(t, f.root(), script(t, "cleanup", map[string]string{"nodes.?resolved.run.orValue('')": run})); code != 0 {
		t.Fatalf("cleanup: exit %d\n%s", code, errOut)
	}
}

func (f ciFixture) root() string { return filepath.Join(f.cache, "reliant-ci") }

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

var mktempRun = regexp.MustCompile(`^run\.[A-Za-z0-9]{8}$`)

// The fetch step, run for real against a local origin: the pr and queue
// tiers fail with a clear message when their base does not resolve, and
// main needs none.
func TestReliantCI_FetchRefusesAMissingBase(t *testing.T) {
	testkit.Slow(t, "execs git and bash")
	f := newCIFixture(t)

	out, errOut, code := f.fetch(t, "pr", f.base, "main")
	if code != 0 || out["base"] != f.base || out["root"] != f.root() || out["prefix"] != "./sub/" || !mktempRun.MatchString(out["run"]) {
		t.Fatalf("pr with a base: exit %d, out %v (want base %s, root %s, prefix ./sub/, a run.XXXXXXXX)\n%s", code, out, f.base, f.root(), errOut)
	}
	if !exists(filepath.Join(f.root(), out["run"], "head.bundle")) || !exists(filepath.Join(f.root(), out["run"], ".reliant-ci-run")) {
		t.Error("fetch wrote no bundle or no marker")
	}
	if fi, err := os.Stat(f.root()); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("the root is not mode 0700: %v %v", fi.Mode(), err)
	}
	if refs := git(t, f.checkout, "for-each-ref", "refs/reliant-ci"); refs != "" {
		t.Errorf("fetch left its refs in the checkout: %q", refs)
	}
	// No base given: the default branch's tip (here head) is the base.
	if out, errOut, code := f.fetch(t, "pr", "", "main"); code != 0 || out["base"] != f.head {
		t.Errorf("pr with no base: exit %d, out %v\n%s", code, out, errOut)
	}
	// A merge group's base must be an ancestor of the group.
	if out, errOut, code := f.fetch(t, "queue", f.base, "main"); code != 0 || out["base"] != f.base {
		t.Errorf("queue with its base: exit %d, out %v\n%s", code, out, errOut)
	}
	before, _ := os.ReadDir(f.root())
	missing := strings.Repeat("0", 39) + "1"
	for _, tc := range []struct{ name, tier, base, branch, want string }{
		{"pr, base not on origin", "pr", missing, "main", "tier needs"},
		{"queue, base not on origin", "queue", missing, "main", "tier needs"},
		{"pr, no base and no default branch", "pr", "", "no-such-branch", "tier needs"},
		{"queue, base not an ancestor of the group", "queue", f.side, "main", "is not an ancestor"},
	} {
		_, errOut, code := f.fetch(t, tc.tier, tc.base, tc.branch)
		if code == 0 || !strings.Contains(errOut, tc.want) {
			t.Errorf("%s: exit %d, want a failure that says %q:\n%s", tc.name, code, tc.want, errOut)
		}
	}
	if after, _ := os.ReadDir(f.root()); len(after) != len(before) {
		t.Errorf("failed fetches left run directories: %d before, %d after", len(before), len(after))
	}
	if out, errOut, code := f.fetch(t, "main", "", "main"); code != 0 || out["base"] != "" || out["root"] != f.root() {
		t.Errorf("main with no base: exit %d, out %v (want success and no base)\n%s", code, out, errOut)
	}
	// A project at the repository's root: the prefix is ./, never empty.
	if out, errOut, code := f.fetchIn(t, f.checkout, "main", "", "main"); code != 0 || out["prefix"] != "./" {
		t.Errorf("a project at the repository root: exit %d, out %v (want prefix ./)\n%s", code, out, errOut)
	}
}

// Two runs at once (the same chat, or any two) get distinct run directories,
// and cleaning up one leaves the other.
func TestReliantCI_ConcurrentRunsGetTheirOwnDirectories(t *testing.T) {
	testkit.Slow(t, "execs git and bash")
	f := newCIFixture(t)
	type result struct {
		out    map[string]string
		errOut string
		code   int
	}
	// Rendered here: a goroutine must not call t.Fatal.
	body := script(t, "fetch", map[string]string{
		"nodes.facts.tier": "pr", "nodes.facts.sha": f.head, "nodes.facts.base": f.base,
		"nodes.facts.default_branch": "main", "nodes.facts.path": filepath.Join(f.checkout, "sub"),
	})
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command("bash", "-c", body)
			cmd.Dir = f.tmp
			cmd.Env = append(os.Environ(), "HOME="+f.tmp, "XDG_CACHE_HOME="+f.cache)
			var o, e strings.Builder
			cmd.Stdout, cmd.Stderr = &o, &e
			code := 0
			if err := cmd.Run(); err != nil {
				code = -1
				if ee, ok := err.(*exec.ExitError); ok {
					code = ee.ExitCode()
				}
				e.WriteString(err.Error())
			}
			results <- result{parseLines(strings.TrimSpace(o.String())), e.String(), code}
		}()
	}
	wg.Wait()
	close(results)
	var runs []string
	for r := range results {
		if r.code != 0 {
			t.Fatalf("a concurrent fetch failed: exit %d\n%s", r.code, r.errOut)
		}
		runs = append(runs, r.out["run"])
	}
	if runs[0] == runs[1] {
		t.Fatalf("two runs share the run directory %s", runs[0])
	}
	f.cleanup(t, runs[0])
	if exists(filepath.Join(f.root(), runs[0])) {
		t.Error("cleanup left its own run directory")
	}
	if !exists(filepath.Join(f.root(), runs[1], "head.bundle")) {
		t.Error("cleaning up one run removed the other's directory")
	}
}

// A symlink at the root, a root another user owns, a root with a looser mode,
// and a run directory that is a symlink are refused: nothing is written
// through them and nothing they point at is deleted.
func TestReliantCI_RefusesPlantedRootsAndRunDirectories(t *testing.T) {
	testkit.Slow(t, "execs git and bash")
	f := newCIFixture(t)
	victim := filepath.Join(f.tmp, "victim")
	if err := os.MkdirAll(filepath.Join(victim, "run.Ab3dE6gH"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(victim, "keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(f.cache, 0o755); err != nil {
		t.Fatal(err)
	}

	// The root is a symlink.
	if err := os.Symlink(victim, f.root()); err != nil {
		t.Fatal(err)
	}
	if _, errOut, code := f.fetch(t, "main", "", "main"); code == 0 || !strings.Contains(errOut, "refused:") {
		t.Errorf("a symlinked root: exit %d, want refused\n%s", code, errOut)
	}
	if entries, _ := os.ReadDir(victim); len(entries) != 2 {
		t.Errorf("fetch wrote through a symlinked root: %v", entries)
	}
	if err := os.Remove(f.root()); err != nil {
		t.Fatal(err)
	}

	// The root has a looser mode than 0700.
	if err := os.Mkdir(f.root(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f.root(), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, errOut, code := f.fetch(t, "main", "", "main"); code == 0 || !strings.Contains(errOut, "mode 0700") {
		t.Errorf("a 0755 root: exit %d, want refused\n%s", code, errOut)
	}
	if err := os.Chmod(f.root(), 0o700); err != nil {
		t.Fatal(err)
	}

	// The root is another user's: simulated with an id that answers another
	// uid, since a test cannot chown.
	bin := filepath.Join(f.tmp, "fake-id")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "id"), []byte("#!/bin/sh\necho 4242\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, errOut, code := f.fetch(t, "main", "", "main", "PATH="+bin+":"+os.Getenv("PATH")); code == 0 || !strings.Contains(errOut, "owned by uid 4242") {
		t.Errorf("a root another uid owns: exit %d, want refused\n%s", code, errOut)
	}

	// A run directory that is a symlink: cleanup leaves it and its target.
	planted := filepath.Join(f.root(), "run.Ab3dE6gH")
	if err := os.Symlink(filepath.Join(victim, "run.Ab3dE6gH"), planted); err != nil {
		t.Fatal(err)
	}
	f.cleanup(t, "run.Ab3dE6gH")
	if !exists(planted) || !exists(filepath.Join(victim, "run.Ab3dE6gH")) {
		t.Error("cleanup removed a symlinked run directory or what it points at")
	}
	// Nor does cleanup take a name that is not a run directory.
	if err := os.Mkdir(filepath.Join(f.root(), "precious"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"precious", "../victim", "run.Ab3dE6g", "run.Ab3dE6gH/.."} {
		f.cleanup(t, name)
	}
	if !exists(filepath.Join(f.root(), "precious")) || !exists(filepath.Join(victim, "keep")) {
		t.Error("cleanup removed something that is not a run directory")
	}
}

// The stale sweep removes only run.XXXXXXXX directories this user owns,
// that carry the marker and are older than two days: never a fresh one, an
// unmarked one, a symlink, a differently named one, or (with an id that
// answers another uid) any of them.
func TestReliantCI_StaleSweepRemovesOnlyMarkedOldRunDirectories(t *testing.T) {
	testkit.Slow(t, "execs git and bash")
	f := newCIFixture(t)
	if err := os.MkdirAll(f.root(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f.root(), 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-72 * time.Hour)
	mk := func(name string, marked bool, mtime time.Time) string {
		d := filepath.Join(f.root(), name)
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		if marked {
			if err := os.WriteFile(filepath.Join(d, ".reliant-ci-run"), []byte(name+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Chtimes(d, mtime, mtime); err != nil {
			t.Fatal(err)
		}
		return d
	}
	stale := mk("run.OLDmark1", true, old)
	unmarked := mk("run.OLDnomk1", false, old)
	fresh := mk("run.NEWmark1", true, time.Now())
	other := mk("cache-of-something", true, old)
	target := filepath.Join(f.tmp, "target")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, ".reliant-ci-run"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(f.root(), "run.LINKmrk1")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	// As another uid, nothing is this user's: the sweep removes nothing (and
	// the root, not this user's either, is refused).
	bin := filepath.Join(f.tmp, "fake-id")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "id"), []byte("#!/bin/sh\necho 4242\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.fetch(t, "main", "", "main", "PATH="+bin+":"+os.Getenv("PATH"))
	if !exists(stale) {
		t.Error("the sweep removed a directory another uid owns")
	}

	out, errOut, code := f.fetch(t, "main", "", "main")
	if code != 0 {
		t.Fatalf("fetch: exit %d\n%s", code, errOut)
	}
	if exists(stale) {
		t.Error("the sweep left a marked run directory older than two days")
	}
	for name, d := range map[string]string{"unmarked": unmarked, "fresh": fresh, "differently named": other, "symlink": link, "symlink target": target, "this run's": filepath.Join(f.root(), out["run"])} {
		if !exists(d) {
			t.Errorf("the sweep removed the %s directory %s", name, d)
		}
	}
}

// The gate, run for real after fetch, with a stand-in `reliant` that does what
// hostile PR code could: it writes objects into the repository it runs in and
// reports whether that repository can reach the project's checkout. The gate's
// repository has no alternates and no remote, and nothing it writes reaches
// the checkout's object store.
func TestReliantCI_GateRepositoryIsIsolatedFromTheCheckout(t *testing.T) {
	testkit.Slow(t, "execs git and bash")
	f := newCIFixture(t)
	out, errOut, code := f.fetch(t, "pr", f.base, "main")
	if code != 0 {
		t.Fatalf("fetch: exit %d\n%s", code, errOut)
	}
	objects := func() string {
		return git(t, f.checkout, "count-objects", "-v")
	}
	before := objects()

	bin := filepath.Join(f.tmp, "bin")
	report := filepath.Join(f.tmp, "report")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	stub := `#!/bin/sh
gd=$(git rev-parse --absolute-git-dir)
{
  echo "pwd=$(pwd)"
  echo "head=$(git rev-parse HEAD)"
  [ -e "$gd/objects/info/alternates" ] && echo "alternates=$(cat "$gd/objects/info/alternates")" || echo "alternates=none"
  echo "remotes=$(git remote | tr '\n' ' ')"
  echo "args=$*"
} >"$REPORT"
# What hostile code would do: write objects and a commit, and repack.
echo hostile | git hash-object -w --stdin >/dev/null
git -c user.email=x@x -c user.name=x commit -q --allow-empty -m hostile
git gc -q --prune=now >/dev/null 2>&1
echo '{"schema":"forge.ci.gate/v1","ok":true,"verdict":"pass","checks":[]}'
`
	if err := os.WriteFile(filepath.Join(bin, "reliant"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	gate := func(dir string) (string, string, int) {
		return f.run(t, dir, script(t, "gate", map[string]string{
			"nodes.facts.tier": "pr", "nodes.facts.sha": f.head, "nodes.resolved.base": out["base"],
			"nodes.resolved.prefix": out["prefix"], "nodes.resolved.run": out["run"],
		}), "PATH="+bin+":"+os.Getenv("PATH"), "REPORT="+report)
	}
	// Started anywhere but its run directory, it refuses.
	if stdout, _, _ := gate(f.tmp); !strings.Contains(stdout, "not in its run directory") {
		t.Errorf("a gate outside its run directory ran: %s", stdout)
	}
	stdout, gateErr, code := gate(filepath.Join(out["root"], out["run"]))
	if code != 0 || !strings.Contains(stdout, `"ok":true`) {
		t.Fatalf("gate: exit %d, stdout %q\n%s", code, stdout, gateErr)
	}
	got, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("the gate never ran forge ci gate: %v\n%s", err, gateErr)
	}
	r := string(got)
	for _, want := range []string{"head=" + f.head, "alternates=none", "remotes=\n", "args=forge ci gate --tier pr --base " + f.base + " --json"} {
		if !strings.Contains(r, want) {
			t.Errorf("the gate's repository report lacks %q:\n%s", want, r)
		}
	}
	if !strings.Contains(r, "/sub\n") || strings.Contains(r, "pwd="+f.checkout) {
		t.Errorf("the gate ran outside its own repository's project directory:\n%s", r)
	}
	if after := objects(); after != before {
		t.Errorf("the gate changed the checkout's object store:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// Only a trusted head runs the gate, and each trigger's tier is the trigger's
// own, never inferred from a branch name. There is no Re-run trigger: GitHub's
// check_run.rerequested and check_suite.rerequested name a commit and a branch
// but nothing a workflow can read says the commit passed the pull-request
// filter, so a re-run could gate a branch or sha that filter never admitted.
func TestReliantCI_OnlyTrustedEventsRunTheGate(t *testing.T) {
	for _, workflow := range []string{"ci", "review"} {
		out, err := CITemplates("reliant").Render(workflow+".yaml.tmpl", ReliantCIWorkflowData{Check: "demo/" + workflow})
		if err != nil {
			t.Fatal(err)
		}
		var doc reliantCIDoc
		if err := yaml.Unmarshal(out, &doc); err != nil {
			t.Fatal(err)
		}
		for _, tr := range doc.Triggers {
			for _, ev := range tr.Integration.Events {
				if strings.HasSuffix(ev, ".rerequested") || strings.HasPrefix(ev, "check_run.") {
					t.Errorf("%s.yaml trigger %s listens to %s: a re-run is not re-checked against the pull-request filter", workflow, tr.Name, ev)
				}
			}
			if tr.OriginFilter != "" {
				t.Errorf("%s.yaml trigger %s overrides origin_filter", workflow, tr.Name)
			}
		}
	}

	doc := renderReliantCI(t)
	want := map[string]struct {
		events, tier string
		filter       []string
	}{
		"pull-request": {"pull_request.opened,pull_request.synchronize,pull_request.reopened,pull_request.ready_for_review", "pr", []string{
			"!trigger.payload.data.pull_request.draft",
			"trigger.payload.data.pull_request.head.repo.full_name == trigger.payload.attributes.repository",
			"trigger.payload.data.pull_request.author_association in ['OWNER', 'MEMBER', 'COLLABORATOR']",
			"in trigger.activation.params.trusted_ids",
		}},
		"merge-group": {"check_suite.requested", "queue", []string{"matches('^gh-readonly-queue/.+/pr-[0-9]+-[0-9a-f]{40}$')"}},
		"push":        {"push", "main", []string{"trigger.payload.attributes.branch == trigger.payload.data.repository.default_branch", "!trigger.payload.data.deleted"}},
	}
	if len(doc.Triggers) != len(want) {
		t.Errorf("ci.yaml declares %d triggers, want exactly pull-request, merge-group and push", len(doc.Triggers))
	}
	for _, tr := range doc.Triggers {
		w, ok := want[tr.Name]
		if !ok {
			t.Errorf("ci.yaml declares an unexpected trigger %q", tr.Name)
			continue
		}
		if got := strings.Join(tr.Integration.Events, ","); got != w.events {
			t.Errorf("trigger %s listens to %s, want %s", tr.Name, got, w.events)
		}
		if tr.Inputs["tier"] != w.tier {
			t.Errorf("trigger %s sets tier %q, want the literal %q", tr.Name, tr.Inputs["tier"], w.tier)
		}
		for _, f := range w.filter {
			if !strings.Contains(tr.Filter, f) {
				t.Errorf("trigger %s's filter lacks %q:\n%s", tr.Name, f, tr.Filter)
			}
		}
	}
}
