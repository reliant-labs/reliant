package ciguard

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	yaml "gopkg.in/yaml.v3"
)

// The file under guard, relative to this package's directory — the working
// directory `go test` runs it in. Read by this process, so the test cache sees
// an edit to it.
var ciWorkflow = filepath.Join("..", "..", ".github", "workflows", "ci.yml")

type workflow struct {
	On   map[string]any `yaml:"on"`
	Jobs map[string]job `yaml:"jobs"`
}

type job struct {
	Name     string   `yaml:"name"`
	If       string   `yaml:"if"`
	Needs    []string `yaml:"needs"`
	Strategy struct {
		Matrix struct {
			Include []map[string]string `yaml:"include"`
		} `yaml:"matrix"`
	} `yaml:"strategy"`
}

func readWorkflow(t *testing.T) workflow {
	t.Helper()
	raw, err := os.ReadFile(ciWorkflow)
	if err != nil {
		t.Fatalf("read %s: %v", ciWorkflow, err)
	}
	var wf workflow
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatalf("parse %s: %v", ciWorkflow, err)
	}
	if len(wf.Jobs) == 0 {
		t.Fatalf("%s declares no jobs — this guard would inspect nothing", ciWorkflow)
	}
	return wf
}

// TestShortTierShardsCoverEveryIndex proves the test-short matrix runs every
// shard of the partition it declares. scripts/ci-test-packages.sh deals every
// package onto `short I N`, which is total only if all N indices run. A matrix
// entry deleted, mistyped, or left at an old N after the others moved would
// drop a slice of the module's packages while every remaining shard stays
// green.
func TestShortTierShardsCoverEveryIndex(t *testing.T) {
	wf := readWorkflow(t)
	short, ok := wf.Jobs["test-short"]
	if !ok {
		t.Fatalf("%s has no test-short job", ciWorkflow)
	}
	entries := short.Strategy.Matrix.Include
	if len(entries) == 0 {
		t.Fatal("test-short has no matrix.include entries — this guard would inspect nothing")
	}

	shards := -1
	var indices []int
	for _, e := range entries {
		args := strings.Fields(e["args"])
		if len(args) == 0 && e["flake"] == "true" {
			continue // changed-test repetitions run independently of both shards
		}
		if len(args) != 2 {
			t.Errorf("test-short entry %q: args %q are not `I N`", e["shard"], e["args"])
			continue
		}
		i, err1 := strconv.Atoi(args[0])
		n, err2 := strconv.Atoi(args[1])
		if err1 != nil || err2 != nil || n < 1 || i < 0 || i >= n {
			t.Errorf("test-short entry %q: %q is not a valid shard I of N (0 <= I < N)", e["shard"], e["args"])
			continue
		}
		if shards == -1 {
			shards = n
		}
		if n != shards {
			t.Errorf("test-short entries disagree on the shard count (%d and %d) — every entry must name the same N", shards, n)
		}
		indices = append(indices, i)
	}
	if shards < 1 {
		return
	}
	sort.Ints(indices)
	want := make([]int, shards)
	for i := range want {
		want[i] = i
	}
	if !slices.Equal(indices, want) {
		t.Errorf("test-short runs shards %v of %d, want exactly %v — a missing shard is a slice of the module that runs nowhere on a pull request", indices, shards, want)
	}
}

// TestFullTierShardsCoverEveryIndex is TestShortTierShardsCoverEveryIndex for
// main's full lanes: `test` deals the unit set and `test-cli` internal/cli's
// tests onto `<lane> I N` (scripts/ci-test-full-shard.sh), which is total only
// if every index runs, and scripts/check-release-tests.sh reads each shard's
// `I/N` from its job name, so the label must agree with the args.
func TestFullTierShardsCoverEveryIndex(t *testing.T) {
	wf := readWorkflow(t)
	for jobName, lane := range map[string]string{"test": "unit", "test-cli": "cli"} {
		j, ok := wf.Jobs[jobName]
		if !ok {
			t.Errorf("%s has no %s job", ciWorkflow, jobName)
			continue
		}
		entries := j.Strategy.Matrix.Include
		if len(entries) == 0 {
			t.Errorf("%s has no matrix.include entries", jobName)
			continue
		}
		shards := -1
		var indices []int
		for _, e := range entries {
			args := strings.Fields(e["args"])
			if len(args) != 3 || args[0] != lane {
				t.Errorf("%s entry %q: args %q are not `%s I N`", jobName, e["label"], e["args"], lane)
				continue
			}
			i, err1 := strconv.Atoi(args[1])
			n, err2 := strconv.Atoi(args[2])
			if err1 != nil || err2 != nil || n < 1 || i < 0 || i >= n {
				t.Errorf("%s entry %q: %q is not a valid shard I of N", jobName, e["label"], e["args"])
				continue
			}
			if shards == -1 {
				shards = n
			}
			if n != shards {
				t.Errorf("%s entries disagree on the shard count (%d and %d)", jobName, shards, n)
			}
			if want := strconv.Itoa(i+1) + "/" + strconv.Itoa(n); !strings.HasSuffix(e["label"], " "+want) {
				t.Errorf("%s entry label %q must end in %q: the release check reads the shard from the job name", jobName, e["label"], want)
			}
			indices = append(indices, i)
		}
		if shards < 1 {
			continue
		}
		sort.Ints(indices)
		want := make([]int, shards)
		for i := range want {
			want[i] = i
		}
		if !slices.Equal(indices, want) {
			t.Errorf("%s runs shards %v of %d, want exactly %v: a missing shard is a slice of the suite that runs nowhere", jobName, indices, shards, want)
		}
	}
}

// TestGateNeedsEveryJob proves `CI gate` waits on, and so reports, every other
// job in ci.yml. It is the one check the main ruleset requires; a job missing
// from its needs could fail while the gate stays green.
func TestGateNeedsEveryJob(t *testing.T) {
	wf := readWorkflow(t)
	gate, ok := wf.Jobs["gate"]
	if !ok {
		t.Fatalf("%s has no gate job", ciWorkflow)
	}
	if !strings.Contains(gate.If, "always()") {
		t.Errorf("gate's if is %q: without always() it is skipped when a job it needs fails, and a skipped gate reads as passing", gate.If)
	}
	for name := range wf.Jobs {
		if name != "gate" && !slices.Contains(gate.Needs, name) {
			t.Errorf("gate does not need %q: that job could fail while CI gate stays green", name)
		}
	}
	for _, name := range gate.Needs {
		if _, ok := wf.Jobs[name]; !ok {
			t.Errorf("gate needs %q, which is not a job in %s", name, ciWorkflow)
		}
	}
}

// requiredCheck is the status-check context the main ruleset requires and the
// merge queue waits for. It is the gate job's `name:`, so renaming the job
// would leave every pull request waiting on a check nothing reports.
const requiredCheck = "CI gate"

// When a job may run (ci.yml's header, "REVIEW FIRST"). A job with no `if:`
// runs always.
const (
	// The merge tier: the merge queue, and a pull request labelled ci:run.
	queueTier = "${{ github.event_name == 'merge_group' || (github.event_name == 'pull_request' && contains(github.event.pull_request.labels.*.name, 'ci:run')) }}"
	// The slow tier: a push to main, and a manual dispatch.
	mainTier = "${{ github.event_name == 'push' || github.event_name == 'workflow_dispatch' }}"
	// Everywhere but an unlabelled pull request: the merge tier plus main.
	notReviewFirst = "${{ github.event_name != 'pull_request' || contains(github.event.pull_request.labels.*.name, 'ci:run') }}"
	// The checks that read the pull request itself.
	prOnly = "${{ github.event_name == 'pull_request' }}"
)

// The jobs an unlabelled pull request runs before its review (REVIEW FIRST).
// Anything else there spends CI minutes on a head nobody has reviewed, and
// could hold a reviewed pull request out of the queue.
var reviewFirstJobs = []string{"pr-checks", "gate"}

// TestRequiredGateReportsOnEveryPullRequestAndQueueEntry proves `CI gate`
// reports on every event the main ruleset judges a merge by.
//
//   - pull_request carries no path filter: a filter that skips the workflow
//     never creates the required check, and the pull request waits on it
//     forever.
//   - merge_group is a trigger: without it the merge queue waits forever for
//     a check that never starts, and every merge blocks.
//   - every job selects when it runs by one of the spellings in ci.yml's
//     header. A job gated on `!= 'pull_request'` alone would also run in the
//     merge queue, holding every merge behind the slow tier; one gated on
//     `== 'pull_request'` alone would silently not run there.
//   - an unlabelled pull request runs only the review-first jobs; the merge
//     tier (test-short, checks) runs in the merge queue.
func TestRequiredGateReportsOnEveryPullRequestAndQueueEntry(t *testing.T) {
	wf := readWorkflow(t)
	if gate := wf.Jobs["gate"]; gate.Name != requiredCheck {
		t.Errorf("the gate job is named %q, want %q: that name is the main ruleset's required check", gate.Name, requiredCheck)
	}
	if _, ok := wf.On["merge_group"]; !ok {
		t.Errorf("%s has no merge_group trigger: the merge queue would wait forever for %q", ciWorkflow, requiredCheck)
	}
	pr, ok := wf.On["pull_request"]
	if !ok {
		t.Fatalf("%s has no pull_request trigger", ciWorkflow)
	}
	if m, ok := pr.(map[string]any); ok {
		for _, filter := range []string{"paths", "paths-ignore"} {
			if _, ok := m[filter]; ok {
				t.Errorf("pull_request has a %s filter: a pull request it skips never reports %q, the required check", filter, requiredCheck)
			}
		}
	}
	allowed := []string{queueTier, mainTier, notReviewFirst, prOnly}
	for name, j := range wf.Jobs {
		cond := strings.TrimSpace(j.If)
		if name == "gate" {
			continue
		}
		if !slices.Contains(allowed, cond) {
			t.Errorf("job %q has if: %q; select when it runs with exactly one of %q", name, j.If, allowed)
			continue
		}
		// Runs on an unlabelled pull request: no if:, or a spelling that
		// admits one.
		onPR := cond == "" || cond == prOnly
		if onPR && !slices.Contains(reviewFirstJobs, name) {
			t.Errorf("job %q runs on every pull request push, before review; only %v may (REVIEW FIRST): gate it with %q", name, reviewFirstJobs, queueTier)
		}
	}
	for _, name := range []string{"test-short", "checks"} {
		cond := strings.TrimSpace(wf.Jobs[name].If)
		if cond != queueTier && cond != notReviewFirst {
			t.Errorf("job %q has if: %q; the merge tier must run in the merge queue (%q)", name, cond, queueTier)
		}
	}
}

// The guard packages, as scripts/ci-test-packages.sh declares them.
var guardScript = filepath.Join("..", "..", "scripts", "ci-test-packages.sh")

// Whole-program loads and native KCL renders belong to the full tier. PRs
// still compile and run each guard package's cheap -short assertions.
func TestWholeRepoGuardsRunOnlyInFullTier(t *testing.T) {
	raw, err := os.ReadFile(guardScript)
	if err != nil {
		t.Fatalf("read %s: %v", guardScript, err)
	}
	var declared []string
	in := false
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "guards=(":
			in = true
		case in && line == ")":
			in = false
		case in && line != "":
			declared = append(declared, filepath.Base(line))
		}
	}
	if len(declared) == 0 {
		t.Fatalf("%s declares no guards=( ... ) list — this guard would inspect nothing", guardScript)
	}
	wf := readWorkflow(t)
	for _, e := range wf.Jobs["test-short"].Strategy.Matrix.Include {
		if e["guards"] != "" {
			t.Errorf("PR entry still runs full guards %q; keep whole-repo work in full mode", e["guards"])
		}
	}
	if wf.Jobs["test-short-warm"].If != mainTier {
		t.Fatal("full guards must run on main and manual full dispatch")
	}
	body, err := os.ReadFile(ciWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "run: go test -p 1 -count=1 -timeout 30m $(scripts/ci-test-packages.sh guards)") {
		t.Fatalf("all %d declared full guards must still run through their authoritative package set", len(declared))
	}
}
