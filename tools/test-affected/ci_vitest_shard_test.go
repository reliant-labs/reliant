package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The merge queue's vitest files run in shards (owner, 2026-11-21): a hub web
// module reached ~1100 files (#2935, #3009) and one runner ran out of the
// gate's 30 minutes. The gate passes --skip-vitest, and the `vitest` job's
// legs run scripts/vitest-shard.sh, vitest --shard over the SAME plan. These
// tests pin each link: a leg dropped from the matrix, a gate that stops
// handing vitest off (or hands it off with no job to take it), or a leg that
// plans differently from the gate would each drop coverage silently.

const vitestShards = 4

// TestMergeQueueVitestShards: one matrix job, legs 1..4, fail-fast off, each
// running its shard of 4 from the group's base, inside the check timeout; the
// gate hands vitest to it.
func TestMergeQueueVitestShards(t *testing.T) {
	wf := loadMergeQueue(t)
	v := wf.Jobs["vitest"]
	if v == nil {
		t.Fatalf("merge-queue.yml has no vitest job: %v", sortedKeys(wf.Jobs))
	}
	if v.If != "" {
		t.Errorf("vitest if=%q: a skipped job is not a success; a group with no vitest file passes each leg's no-op instead", v.If)
	}
	// CI result starts after the slowest leg: 30 + its own 5 stays under the
	// ruleset's 45-minute check timeout.
	if v.TimeoutMinutes <= 0 || v.TimeoutMinutes > 30 {
		t.Errorf("vitest timeout-minutes = %d, want 1..30", v.TimeoutMinutes)
	}
	if v.Strategy.FailFast == nil || *v.Strategy.FailFast {
		t.Error("vitest strategy.fail-fast must be false: a failing leg must not cancel the others' findings")
	}
	var legs []string
	if l, ok := v.Strategy.Matrix["shard"].([]any); ok {
		for _, x := range l {
			legs = append(legs, fmt.Sprint(x))
		}
	}
	var want []string
	for i := 1; i <= vitestShards; i++ {
		want = append(want, fmt.Sprint(i))
	}
	if !slices.Equal(legs, want) || len(v.Strategy.Matrix) != 1 {
		t.Errorf("vitest matrix %v, want shard: %v alone (every shard of %d, once)", v.Strategy.Matrix, want, vitestShards)
	}
	if v.Env["BASE"] != "${{ github.event.merge_group.base_sha }}" {
		t.Errorf("vitest BASE = %q, want the group's base sha (the gate's)", v.Env["BASE"])
	}
	var runs, node bool
	for _, s := range v.Steps {
		if s.Run == fmt.Sprintf(`scripts/vitest-shard.sh --base "${BASE}" --shard "${{ matrix.shard }}/%d"`, vitestShards) {
			runs = true
		}
		if strings.HasSuffix(s.Uses, "/actions/setup") && s.With["node"] == "true" && s.With["go"] != "false" {
			node = true
		}
	}
	if !runs || !node {
		t.Errorf("vitest legs: runs scripts/vitest-shard.sh --shard <leg>/%d = %v, sets up Go and Node = %v", vitestShards, runs, node)
	}
	if strings.Contains(v.Name, "CI result") {
		t.Errorf("vitest is named %q: the required check would have more producers", v.Name)
	}
	var handsOff bool
	for _, s := range wf.Jobs["merge-gate"].Steps {
		if strings.Contains(s.Run, "task ci:merge") && strings.Contains(s.Run, "--skip-vitest") {
			handsOff = true
		}
	}
	if !handsOff {
		t.Error("the merge gate does not pass --skip-vitest: every selected vitest file would run twice, once on the gate's runner")
	}
}

// TestSkipVitestOnlyBesideShards: a workflow that runs the merge gate with
// --skip-vitest also runs scripts/vitest-shard.sh in a job its `CI result`
// needs. Otherwise the selected vitest files run nowhere before the merge.
func TestSkipVitestOnlyBesideShards(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(repoRoot, ".github", "workflows", "*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		var wf ciWorkflow
		if err := yaml.Unmarshal(readRepo(t, filepath.Join(".github", "workflows", filepath.Base(f))), &wf); err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		skips, shardJobs := false, []string{}
		for id, j := range wf.Jobs {
			for _, s := range j.Steps {
				if (strings.Contains(s.Run, "task ci:merge") || strings.Contains(s.Run, "merge-gate.sh")) && strings.Contains(s.Run, "--skip-vitest") {
					skips = true
				}
				if strings.Contains(s.Run, "scripts/vitest-shard.sh") {
					shardJobs = append(shardJobs, id)
				}
			}
		}
		if !skips {
			continue
		}
		res := wf.Jobs["ci-result"]
		if len(shardJobs) == 0 || res == nil {
			t.Errorf("%s: the merge gate skips vitest, but no job runs scripts/vitest-shard.sh for a CI result to require", filepath.Base(f))
			continue
		}
		for _, id := range shardJobs {
			if !slices.Contains(res.needs(), id) {
				t.Errorf("%s: CI result does not need %s, so a failing vitest shard would not fail the required check", filepath.Base(f), id)
			}
		}
	}
}

// TestVitestShardPlansLikeTheGate: the leg's plan is the gate's (same reach
// default, same FULL fallback), and the gate drops only the web lane when it
// hands vitest off, re-passing the flag when it re-execs in a heavy slot.
func TestVitestShardPlansLikeTheGate(t *testing.T) {
	gate, leg := string(readRepo(t, "scripts/merge-gate.sh")), string(readRepo(t, "scripts/vitest-shard.sh"))
	for _, s := range []string{`reach=${MG_REACH:-direct}`, `plan -json -reach "$reach" -base "$base"`, `-web-full reached`} {
		if !strings.Contains(gate, s) || !strings.Contains(leg, s) {
			t.Errorf("merge-gate.sh (%v) and vitest-shard.sh (%v) must both say %s", strings.Contains(gate, s), strings.Contains(leg, s), s)
		}
	}
	for _, s := range []string{`--skip-vitest) skip_vitest=1`, `$([ "$skip_vitest" = 1 ] && echo --skip-vitest)`, `test_lanes=go,electron`} {
		if !strings.Contains(gate, s) {
			t.Errorf("merge-gate.sh lacks %s", s)
		}
	}
	if !strings.Contains(leg, `-lanes web -web-full reached -web-shard "$shard"`) {
		t.Error("vitest-shard.sh does not run the web lane alone, sharded")
	}
}

// TestVitestShardScript runs scripts/vitest-shard.sh against stub go, git and
// pnpm: no selected file is a pass that installs and runs nothing; selected
// files install and run the web lane alone with the leg's shard; a plan that
// fails, is unreadable or has no web lane, a red vitest, and a bad --shard
// fail the leg.
func TestVitestShardScript(t *testing.T) {
	requireShellTools(t)
	script := readRepo(t, "scripts/vitest-shard.sh")
	type res struct {
		ok  bool
		out string
		log string
	}
	run := func(t *testing.T, plan string, env map[string]string, args ...string) res {
		t.Helper()
		root, bin := t.TempDir(), t.TempDir()
		putFixture(t, root, "scripts/vitest-shard.sh", string(script), 0o755)
		putFixture(t, root, "plan.fixture", plan, 0o644)
		log := filepath.Join(root, "calls.log")
		writeExec(t, filepath.Join(bin, "git"), "#!/usr/bin/env bash\n[ \"$*\" = \"rev-parse --show-toplevel\" ] && echo \""+root+"\"\n")
		writeExec(t, filepath.Join(bin, "pnpm"), "#!/usr/bin/env bash\necho \"pnpm $*\" >>\""+log+"\"\n")
		writeExec(t, filepath.Join(bin, "go"), `#!/usr/bin/env bash
echo "go $*" >>"`+log+`"
[ "$1 $2" = "run ./tools/test-affected" ] || exit 9
case $3 in
  plan) [ -z "${PLAN_FAIL:-}" ] || exit 1; cat "`+root+`/plan.fixture" ;;
  run) exit "${RUN_RC:-0}" ;;
  *) exit 9 ;;
esac
`)
		e := map[string]string{"PATH": bin + ":" + os.Getenv("PATH")}
		for k, v := range env {
			e[k] = v
		}
		cmd := "bash " + filepath.Join(root, "scripts", "vitest-shard.sh")
		for _, a := range args {
			cmd += " '" + a + "'"
		}
		ok, out := ciShell(t, cmd, e)
		b, _ := os.ReadFile(log)
		return res{ok, out, string(b)}
	}
	none := `{"web":{"full":false,"tests":[]}}`
	some := `{"web":{"full":true,"tests":["src/a.test.ts","src/b.test.ts","src/c.test.ts"]}}`
	std := []string{"--base", "abc123", "--shard", "2/4"}

	r := run(t, none, nil, std...)
	if !r.ok || !strings.Contains(r.out, "nothing to run") || strings.Contains(r.log, "pnpm") || strings.Contains(r.log, "test-affected run") {
		t.Errorf("no vitest file selected: want a pass with no install and no run:\n%s\n%s", r.out, r.log)
	}
	if !strings.Contains(r.log, "go run ./tools/test-affected plan -json -reach direct -base abc123\n") {
		t.Errorf("the leg does not plan as the gate does:\n%s", r.log)
	}

	r = run(t, some, nil, std...)
	if !r.ok || !strings.Contains(r.log, "pnpm install --dir frontends/web --frozen-lockfile") {
		t.Errorf("selected files: want an install and a pass:\n%s\n%s", r.out, r.log)
	}
	if !strings.Contains(r.log, " -base abc123 -lanes web -web-full reached -web-shard 2/4 -capture\n") {
		t.Errorf("selected files: the web lane does not run alone, as shard 2/4, recorded as the gate records it:\n%s", r.log)
	}
	if r := run(t, some, map[string]string{"MG_REACH": "all"}, std...); !strings.Contains(r.log, "plan -json -reach all ") {
		t.Errorf("MG_REACH is not honoured as in the gate:\n%s", r.log)
	}

	for name, c := range map[string]struct {
		plan string
		env  map[string]string
		args []string
	}{
		"red vitest":       {some, map[string]string{"RUN_RC": "1"}, std},
		"plan fails":       {some, map[string]string{"PLAN_FAIL": "1"}, std},
		"plan unreadable":  {"not json", nil, std},
		"plan has no web":  {`{"go":{}}`, nil, std},
		"shard above n":    {none, nil, []string{"--base", "abc123", "--shard", "5/4"}},
		"shard zero":       {none, nil, []string{"--base", "abc123", "--shard", "0/4"}},
		"no base":          {none, nil, []string{"--shard", "1/4"}},
		"shard not i of n": {none, nil, []string{"--base", "abc123", "--shard", "2"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if r := run(t, c.plan, c.env, c.args...); r.ok {
				t.Errorf("the leg passes:\n%s\n%s", r.out, r.log)
			}
		})
	}
}

// TestRunnerWebShard: -web-shard i/n adds vitest's --shard (and a pass for a
// leg handed no file) to every web step, and nothing without it.
func TestRunnerWebShard(t *testing.T) {
	pl := Classify([]Change{{Path: "frontends/web/package.json"}, {Path: "frontends/web/src/lib/leaf.ts"}}, nil, testGraph(), ReachDirect)
	webStep := func(r *runner) []string {
		for _, s := range r.steps(pl, laneSet("web")) {
			if strings.HasPrefix(s.name, "web") {
				return s.argv
			}
		}
		return nil
	}
	got := webStep(&runner{root: "/repo", webFull: webFullReached, webShard: "3/4"})
	want := append([]string{"npx", "vitest", "run", "--shard=3/4", "--passWithNoTests"}, pl.Web.Tests...)
	if !slices.Equal(got, want) {
		t.Errorf("sharded web step = %v, want %v", got, want)
	}
	if got := webStep(&runner{root: "/repo", webFull: webFullReached}); slices.ContainsFunc(got, func(a string) bool { return strings.HasPrefix(a, "--shard") }) {
		t.Errorf("unsharded web step shards: %v", got)
	}
	for s, ok := range map[string]bool{"1/4": true, "4/4": true, "1/1": true, "0/4": false, "5/4": false, "4": false, "1/0": false, "01/4": false, "a/b": false, "": false} {
		if validShard(s) != ok {
			t.Errorf("validShard(%q) = %v, want %v", s, !ok, ok)
		}
	}
}
