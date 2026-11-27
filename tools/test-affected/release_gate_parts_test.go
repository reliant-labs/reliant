// Copyright (c) 2025 Reliant Labs
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// CI runs the release gate as finer parts than the RC train does (static
// split into generate, webbuild and compile, the go suite in shards), and the
// shards balance on the go package seconds an earlier record carries. assemble
// must accept either partition, refuse one that leaves a step out, and refuse
// shards that split the packages on different timings, since some package
// would then have run in no shard. Its coverage step unions the go parts'
// cover profiles against the commit's coverage-floor.txt: a go part with no
// profile, or a union below the floor, is a red record.
func TestReleaseGateAssemblesEitherPartition(t *testing.T) {
	testkit.Slow(t, "runs release-gate.sh assemble in bash against a scratch git repository",
		testkit.Inputs("scripts/release-gate.sh", "scripts/coverage-floor.sh"))
	script, err := os.ReadFile(filepath.Join(repoRoot, "scripts", "release-gate.sh"))
	if err != nil {
		t.Fatal(err)
	}
	coverageFloor, err := os.ReadFile(filepath.Join(repoRoot, "scripts", "coverage-floor.sh"))
	if err != nil {
		t.Fatal(err)
	}
	// Each part's steps come from the script's own PART_STEPS, so a step added
	// to the gate (demo-size, #3491) cannot leave this fixture behind.
	steps := releaseGatePartSteps(t, string(script))
	for _, p := range []string{"static", "lint", "go", "web", "generate", "webbuild", "compile"} {
		if len(steps[p]) == 0 {
			t.Fatalf("release-gate.sh's PART_STEPS has no part %q", p)
		}
	}
	type part struct{ name, shard, split, result string }
	ci := func(split2 string) []part {
		return []part{{"generate", "", "", "PASS"}, {"webbuild", "", "", "PASS"}, {"compile", "", "", "PASS"}, {"lint", "", "", "PASS"},
			{"go", "1/2", "aaaa", "PASS"}, {"go", "2/2", split2, "PASS"}, {"web", "", "", "PASS"}}
	}
	for _, tc := range []struct {
		name   string
		parts  []part
		cover  string // "": every go part leaves a 75% profile; "missing": the last go part none; "low": 0%
		wantOK bool
		want   string
	}{
		{"the RC train's four parts", []part{{"static", "", "", "PASS"}, {"lint", "", "", "PASS"}, {"go", "", "", "PASS"}, {"web", "", "", "PASS"}}, "", true, "PASSED"},
		{"CI's parts, the go suite in shards", ci("aaaa"), "", true, "PASSED"},
		{"a red part is a red record", append(ci("aaaa")[:2], part{"compile", "", "", "FAIL"}, part{"lint", "", "", "PASS"}, part{"go", "1/2", "aaaa", "PASS"}, part{"go", "2/2", "aaaa", "PASS"}, part{"web", "", "", "PASS"}), "", false, "FAILED"},
		{"a step no part ran", append(ci("aaaa")[:2], ci("aaaa")[3:]...), "", false, "steps no part ran: build, typecheck, vet"},
		{"a missing shard", append(ci("aaaa")[:5], ci("aaaa")[6]), "", false, "part go: ran [1/2]"},
		{"shards that split on different timings", ci("bbbb"), "", false, "different timings"},
		{"a go shard with no cover profile", ci("aaaa"), "missing", false, "no Go cover profile beside the part record of: go 2/2"},
		{"coverage below the floor", ci("aaaa"), "low", false, "points below the floor"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			git := func(args ...string) string {
				t.Helper()
				cmd := exec.Command("git", args...)
				cmd.Dir = dir
				cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			git("init", "-q")
			if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "scripts", "release-gate.sh"), script, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "scripts", "coverage-floor.sh"), coverageFloor, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "coverage-floor.txt"), []byte("# floor\n50\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			git("add", ".")
			git("commit", "-qm", "c")
			sha := git("rev-parse", "HEAD")
			var files []string
			lastGo := -1
			for i, p := range tc.parts {
				if p.name == "go" {
					lastGo = i
				}
			}
			for i, p := range tc.parts {
				var ss []map[string]any
				for _, s := range steps[p.name] {
					ss = append(ss, map[string]any{"name": s, "result": p.result, "seconds": 1})
				}
				rec := map[string]any{"sha": sha, "result": p.result, "started": "2026-11-20T10:00:00Z", "finished": "2026-11-20T10:05:00Z",
					"seconds": 300, "slot_wait_seconds": 0, "host": "h", "steps": ss, "part": p.name, "shard": p.shard}
				if p.name == "go" {
					rec["go_seconds"] = map[string]int{fmt.Sprintf("example.com/p%d", i): 10 + i}
					if p.split != "" {
						rec["go_split"] = p.split
					}
				}
				b, _ := json.Marshal(rec)
				f := filepath.Join(dir, fmt.Sprintf("part-%d.json", i))
				if err := os.WriteFile(f, b, 0o644); err != nil {
					t.Fatal(err)
				}
				// The part's cover profile, beside its record: 3 of each go
				// part's 4 statements covered, or none of them.
				if p.name == "go" && (tc.cover != "missing" || i != lastGo) {
					hit := 1
					if tc.cover == "low" {
						hit = 0
					}
					prof := fmt.Sprintf("mode: atomic\nexample.com/m/p%d.go:1.1,2.2 3 %d\nexample.com/m/p%d.go:3.1,4.2 1 0\n", i, hit, i)
					if err := os.WriteFile(strings.TrimSuffix(f, ".json")+".cover.out", []byte(prof), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				files = append(files, f)
			}
			run := func(args ...string) (string, error) {
				cmd := exec.Command("bash", append([]string{"scripts/release-gate.sh"}, args...)...)
				cmd.Dir = dir
				cmd.Env = append(os.Environ(), "RELEASE_GATE_PUBLISH=0", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
				out, err := cmd.CombinedOutput()
				return string(out), err
			}
			out, err := run(append([]string{"assemble", sha}, files...)...)
			if (err == nil) != tc.wantOK || !strings.Contains(out, tc.want) {
				t.Fatalf("assemble: err=%v, want ok=%v and %q in:\n%s", err, tc.wantOK, tc.want, out)
			}
			if tc.wantOK && !strings.Contains(out, "coverage: 75.00% of Go statements (floor 50%)") {
				t.Fatalf("assemble: a green record names its coverage and floor:\n%s", out)
			}
			if !strings.Contains(tc.want, "PASSED") && !strings.Contains(tc.want, "FAILED") {
				return // refused: nothing recorded
			}
			// A record (red or green) carries every shard's go seconds, and
			// is where the next run's shards balance from.
			src, err := run("go-timings-source", "HEAD")
			if err != nil || strings.TrimSpace(src) != sha {
				t.Fatalf("go-timings-source = %q (%v), want %s", src, err, sha)
			}
			tim, err := run("go-timings")
			if err != nil {
				t.Fatalf("go-timings: %v\n%s", err, tim)
			}
			for i, p := range tc.parts {
				if p.name == "go" && !strings.Contains(tim, fmt.Sprintf(`"example.com/p%d": %d`, i, 10+i)) {
					t.Errorf("go-timings lacks shard %s's package seconds:\n%s", p.shard, tim)
				}
			}
		})
	}
}

// releaseGatePartSteps parses release-gate.sh's `declare -A PART_STEPS=(...)`
// into part name -> steps.
func releaseGatePartSteps(t *testing.T, script string) map[string][]string {
	t.Helper()
	block := regexp.MustCompile(`(?s)\ndeclare -A PART_STEPS=\((.*?)\n\)`).FindStringSubmatch(script)
	if block == nil {
		t.Fatal("release-gate.sh has no `declare -A PART_STEPS=(...)`")
	}
	steps := map[string][]string{}
	for _, m := range regexp.MustCompile(`(?m)^\s*\[([a-z-]+)\]="([^"]*)"`).FindAllStringSubmatch(block[1], -1) {
		steps[m[1]] = strings.Fields(m[2])
	}
	return steps
}

// go_package_seconds reads `go test` output: ok and FAIL lines with a time,
// rounded, at least one second; cached, no-test-file and other lines ignored.
func TestReleaseGateGoPackageSeconds(t *testing.T) {
	testkit.Slow(t, "runs release-gate.sh's go_package_seconds in bash",
		testkit.Inputs("scripts/release-gate.sh"))
	b, err := os.ReadFile(filepath.Join(repoRoot, "scripts", "release-gate.sh"))
	if err != nil {
		t.Fatal(err)
	}
	fn := regexp.MustCompile(`(?s)\ngo_package_seconds\(\) \{\n.*?\n\}\n`).FindString(string(b))
	if fn == "" {
		t.Fatal("release-gate.sh has no go_package_seconds function")
	}
	in := "ok  \texample.com/a\t170.743s\n" +
		"FAIL\texample.com/b\t12.2s\n" +
		"ok  \texample.com/c\t0.031s\tcoverage: 10.0% of statements\n" +
		"ok  \texample.com/d\t(cached)\n" +
		"?   \texample.com/e\t[no test files]\n" +
		"--- FAIL: TestX (0.00s)\n" +
		"FAIL\n"
	cmd := exec.Command("bash", "-c", fn+"\ngo_package_seconds | jq -cS .")
	cmd.Stdin = strings.NewReader(in)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if got, want := strings.TrimSpace(string(out)), `{"example.com/a":171,"example.com/b":12,"example.com/c":1}`; got != want {
		t.Fatalf("go_package_seconds = %s, want %s", got, want)
	}
}
