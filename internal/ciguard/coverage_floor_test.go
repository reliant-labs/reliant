package ciguard

import (
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	yaml "gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/pkg/testkit"
)

var (
	coverageFloorFile   = filepath.Join("..", "..", ".github", "coverage-floor")
	coverageFloorScript = filepath.Join("..", "..", "scripts", "ci-coverage-floor.sh")
)

// TestCoverageFloorJobJudgesMainsFullLanes holds ci.yml's coverage-floor job
// to its shape: it runs on main's push tier only (never the merge tier, which
// records no full-lane profile and must not grow a job), it waits on every
// job that records a full lane's coverage (so it judges all of them, and only
// once they finished), it downloads this run's test-runs-* artifacts, and it
// judges them with scripts/ci-coverage-floor.sh against the checked-in floor.
func TestCoverageFloorJobJudgesMainsFullLanes(t *testing.T) {
	raw, err := os.ReadFile(ciWorkflow)
	if err != nil {
		t.Fatalf("read %s: %v", ciWorkflow, err)
	}
	var wf struct {
		Jobs map[string]struct {
			If       string    `yaml:"if"`
			Needs    needsList `yaml:"needs"`
			Strategy struct {
				Matrix struct {
					Include []map[string]string `yaml:"include"`
				} `yaml:"matrix"`
			} `yaml:"strategy"`
			Steps []struct {
				Run  string            `yaml:"run"`
				Uses string            `yaml:"uses"`
				With map[string]string `yaml:"with"`
				Env  map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatalf("parse %s: %v", ciWorkflow, err)
	}
	floor, ok := wf.Jobs["coverage-floor"]
	if !ok {
		t.Fatalf("%s has no coverage-floor job: nothing holds main's coverage to its floor", ciWorkflow)
	}
	if got := strings.TrimSpace(floor.If); got != mainTier {
		t.Errorf("coverage-floor runs if %q, want main's push tier %q: the merge tier records no full-lane profile", got, mainTier)
	}

	// Every job that records a full lane (a -race shard or an integration
	// step) must be a direct need, or the floor judges a run without it.
	fullLane := regexp.MustCompile(`scripts/ci-test-full-shard\.sh|--lane integration`)
	var recorders []string
	for name, j := range wf.Jobs {
		for _, s := range j.Steps {
			if fullLane.MatchString(s.Run) {
				recorders = append(recorders, name)
				break
			}
		}
	}
	if len(recorders) == 0 {
		t.Fatal("no job records a full lane — this guard would inspect nothing")
	}
	for _, r := range recorders {
		if !slices.Contains(floor.Needs, r) {
			t.Errorf("coverage-floor does not need %q, which records a full lane: the floor would judge a run without that lane's coverage", r)
		}
	}

	// The lanes the floor must see: one -race profile per shard of each
	// full-shard matrix (scripts/ci-test-full-shard.sh records lane
	// <lane>-race), and one per integration step's --lane.
	want := map[string]int{}
	captureLane := regexp.MustCompile(`--lane (integration[A-Za-z0-9_-]*)`)
	for _, j := range wf.Jobs {
		shards := false
		for _, s := range j.Steps {
			if strings.Contains(s.Run, "scripts/ci-test-full-shard.sh") {
				shards = true
			}
			for _, m := range captureLane.FindAllStringSubmatch(s.Run, -1) {
				want[m[1]]++
			}
		}
		if shards {
			for _, e := range j.Strategy.Matrix.Include {
				if f := strings.Fields(e["args"]); len(f) == 3 {
					want[f[0]+"-race"]++
				}
			}
		}
	}

	downloads, judges := false, false
	for _, s := range floor.Steps {
		if strings.HasPrefix(s.Uses, "actions/download-artifact@") && s.With["pattern"] == "test-runs-*" &&
			s.With["path"] == ".forge/tests/runs" && s.With["merge-multiple"] == "true" && s.With["run-id"] == "" {
			downloads = true
		}
		if strings.Contains(s.Run, "scripts/ci-coverage-floor.sh .forge/tests/runs .github/coverage-floor") {
			judges = true
			got := map[string]int{}
			for _, entry := range strings.Fields(s.Env["COVERAGE_LANES"]) {
				lane, n, ok := strings.Cut(entry, ":")
				count, err := strconv.Atoi(n)
				if !ok || err != nil {
					t.Errorf("COVERAGE_LANES entry %q is not <lane>:<count>", entry)
					continue
				}
				got[lane] = count
			}
			if !maps.Equal(got, want) {
				t.Errorf("coverage-floor's COVERAGE_LANES is %v, but ci.yml records %v: a lane the list omits fails every run as unrecognised, and a stale count judges a run without a shard", got, want)
			}
		}
		if strings.Contains(s.Run, "go test") {
			t.Errorf("coverage-floor runs %q: it judges the lanes' profiles and must not run tests of its own", s.Run)
		}
	}
	if !downloads {
		t.Error("coverage-floor must download this run's test-runs-* artifacts into .forge/tests/runs (merge-multiple)")
	}
	if !judges {
		t.Error("coverage-floor must run scripts/ci-coverage-floor.sh .forge/tests/runs .github/coverage-floor")
	}
}

// TestCoverageFloorIsAPercentage reads .github/coverage-floor the way the
// script does: comments aside, exactly one number between 0 and 100.
func TestCoverageFloorIsAPercentage(t *testing.T) {
	var values []string
	for _, line := range strings.Split(readText(t, coverageFloorFile), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			values = append(values, line)
		}
	}
	if len(values) != 1 {
		t.Fatalf("%s holds %d values %q, want exactly one percentage", coverageFloorFile, len(values), values)
	}
	v, err := strconv.ParseFloat(values[0], 64)
	if err != nil || v <= 0 || v > 100 {
		t.Fatalf("%s holds %q, want a percentage in (0, 100]", coverageFloorFile, values[0])
	}
}

// TestCoverageFloorScript runs scripts/ci-coverage-floor.sh over fixture
// profiles in a throwaway git repository and pins its arithmetic: profiles
// are unioned by block (a block counts once, covered if any lane hit it),
// only the full lanes count, generated code is excluded by header or
// directory and never by a *_gen.go name, and the exit status says whether
// the total cleared the floor.
func TestCoverageFloorScript(t *testing.T) {
	testkit.Slow(t, "execs bash and git")
	script := readText(t, coverageFloorScript)

	const mod = "example.test/m"
	// a/a.go: blocks of 6 and 4 statements. b/b_gen.go is a hand-written
	// generator (no header): counted. c/c.go carries the generated header,
	// and gen/g.go sits in a gen/ directory: both excluded.
	files := map[string]string{
		"go.mod":     "module " + mod + "\n",
		"a/a.go":     "package a\n",
		"b/b_gen.go": "// Package b generates things.\npackage b\n",
		"c/c.go":     "// Code generated by fixture. DO NOT EDIT.\n\npackage c\n",
		"gen/g.go":   "package gen\n",
	}
	profile := func(lines ...string) string { return "mode: atomic\n" + strings.Join(lines, "\n") + "\n" }
	lanes := map[string]string{
		// unit-race hits a's first block; cli-race hits its second. Each
		// alone is 60% or 40% of a; unioned, all of a is covered.
		"r1/unit-race/cover.out": profile(mod+"/a/a.go:1.1,2.1 6 3", mod+"/a/a.go:3.1,4.1 4 0", mod+"/b/b_gen.go:1.1,2.1 10 0", mod+"/c/c.go:1.1,9.1 50 0"),
		"r2/cli-race/cover.out":  profile(mod+"/a/a.go:1.1,2.1 6 0", mod+"/a/a.go:3.1,4.1 4 1", mod+"/gen/g.go:1.1,9.1 70 0"),
		// integration adds b's block once more, still uncovered.
		"r3/integration/cover.out": profile(mod + "/b/b_gen.go:1.1,2.1 10 0"),
		// The -short tier is a subset of the full lanes and never counts:
		// it would cover b.
		"r4/unit/cover.out": profile(mod + "/b/b_gen.go:1.1,2.1 10 5"),
	}

	lanesEnv := "unit-race:1 cli-race:1 integration:1"
	run := func(t *testing.T, floor string) (string, int) {
		t.Helper()
		root := t.TempDir()
		for name, content := range files {
			p := filepath.Join(root, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		for _, args := range [][]string{{"init", "-q"}, {"add", "."}} {
			if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
		runs := filepath.Join(root, ".forge", "tests", "runs")
		for name, content := range lanes {
			p := filepath.Join(runs, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		floorFile := filepath.Join(root, "floor")
		if err := os.WriteFile(floorFile, []byte("# the floor\n"+floor+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("bash", "-c", script, "ci-coverage-floor.sh", runs, floorFile)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GITHUB_STEP_SUMMARY="+filepath.Join(root, "summary"), "COVERAGE_LANES="+lanesEnv)
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("run script: %v", err)
		}
		return string(out), code
	}

	// Counted: a (10, all covered) and b (10, none) = 50.00%. Concatenating
	// the profiles instead would count a and b twice (10 of 40, 25%), and
	// keeping any one lane's profile would cover at most 6 of a.
	const total = "coverage: 50.00% of 20 statements (10 covered) in 2 packages, from 3 profiles"
	for _, tc := range []struct {
		name, floor string
		code        int
		want        []string
	}{
		{"at the floor", "50", 0, []string{total + "; floor 50%"}},
		{"below the floor", "50.01", 1, []string{total, "below the 50.01% floor", "Never lower the floor"}},
		{"well over the floor", "40", 0, []string{total, "raise", "to 49.90"}},
		{"no floor", "eighty", 2, []string{"holds no coverage floor"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, code := run(t, tc.floor)
			if code != tc.code {
				t.Fatalf("exit %d, want %d\n%s", code, tc.code, out)
			}
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("output lacks %q:\n%s", w, out)
				}
			}
		})
	}

	// Incomplete runs are unmeasured (exit 2), never judged on what is left.
	// Each case would otherwise PASS a 50% floor: dropping cli-race leaves
	// a 6-of-20 total, but dropping integration leaves an unchanged one, and
	// a missing low-coverage shard raises the total.
	for _, tc := range []struct {
		name, lanes, want string
		edit              func(map[string]string)
	}{
		{"a low-coverage shard is missing", "unit-race:1 cli-race:1 integration:1", "integration: 0 profile(s), want 1",
			func(l map[string]string) { delete(l, "r3/integration/cover.out") }},
		{"a lane has fewer shards than the run declares", "unit-race:2 cli-race:1 integration:1", "unit-race: 1 profile(s), want 2", nil},
		{"a profile is empty", "unit-race:1 cli-race:1 integration:1", "holds no coverage block",
			func(l map[string]string) { l["r3/integration/cover.out"] = "mode: set\n" }},
		{"a lane nobody declared", "unit-race:1 cli-race:1", "integration: recorded, but COVERAGE_LANES does not name it", nil},
		{"only the -short tier", "unit-race:1", "unit-race: 0 profile(s), want 1",
			func(l map[string]string) {
				for k := range l {
					if k != "r4/unit/cover.out" {
						delete(l, k)
					}
				}
			}},
		{"no lane list", "", "COVERAGE_LANES is not set", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			savedLanes, savedEnv := maps.Clone(lanes), lanesEnv
			t.Cleanup(func() { lanes, lanesEnv = savedLanes, savedEnv })
			if tc.edit != nil {
				tc.edit(lanes)
			}
			lanesEnv = tc.lanes
			out, code := run(t, "10")
			if code != 2 || !strings.Contains(out, tc.want) {
				t.Fatalf("an incomplete run must exit 2 naming %q, got exit %d:\n%s", tc.want, code, out)
			}
			if strings.Contains(out, "coverage: ") {
				t.Errorf("an incomplete run must not report a total:\n%s", out)
			}
		})
	}
}

// TestCheckedInCoverageFloorRejectsWhatItRaisedPast runs the script against
// the checked-in .github/coverage-floor, not a fixture floor, so the number
// main is held to is pinned: a complete run at 82.00% (over the 81.44 the
// floor started at, under every floor since main measured 82.33%) is
// rejected, and a run exactly at the checked-in floor passes. Lowering the
// floor below 82 fails the first case.
func TestCheckedInCoverageFloorRejectsWhatItRaisedPast(t *testing.T) {
	testkit.Slow(t, "execs bash and git")
	script := readText(t, coverageFloorScript)
	floorText := readText(t, coverageFloorFile)
	var floor float64
	for _, line := range strings.Split(floorText, "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			v, err := strconv.ParseFloat(line, 64)
			if err != nil {
				t.Fatalf("%s holds %q", coverageFloorFile, line)
			}
			floor = v
		}
	}

	const mod = "example.test/m"
	// covered of 10000 statements, split over one covered and one uncovered
	// block of a complete lane set (one profile per declared lane).
	run := func(t *testing.T, covered int) (string, int) {
		t.Helper()
		root := t.TempDir()
		for name, content := range map[string]string{"go.mod": "module " + mod + "\n", "a/a.go": "package a\n"} {
			p := filepath.Join(root, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		for _, args := range [][]string{{"init", "-q"}, {"add", "."}} {
			if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
		profile := "mode: atomic\n" +
			mod + "/a/a.go:1.1,2.1 " + strconv.Itoa(covered) + " 1\n" +
			mod + "/a/a.go:3.1,4.1 " + strconv.Itoa(10000-covered) + " 0\n"
		runs := filepath.Join(root, "runs")
		for _, lane := range []string{"r1/unit-race", "r2/integration"} {
			p := filepath.Join(runs, filepath.FromSlash(lane), "cover.out")
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(profile), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		floorAbs, err := filepath.Abs(coverageFloorFile)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("bash", "-c", script, "ci-coverage-floor.sh", runs, floorAbs)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GITHUB_STEP_SUMMARY="+filepath.Join(root, "summary"), "COVERAGE_LANES=unit-race:1 integration:1")
		out, err := cmd.CombinedOutput()
		if ee, ok := err.(*exec.ExitError); ok {
			return string(out), ee.ExitCode()
		} else if err != nil {
			t.Fatalf("run script: %v", err)
		}
		return string(out), 0
	}

	t.Run("82.00% is below the floor", func(t *testing.T) {
		out, code := run(t, 8200)
		if code != 1 || !strings.Contains(out, "total Go coverage is 82.00%, below the") {
			t.Fatalf("a complete run at 82.00%% must fail the checked-in floor (%v), got exit %d:\n%s", floor, code, out)
		}
	})
	t.Run("exactly the floor passes", func(t *testing.T) {
		covered := int(floor*100 + 0.5)
		out, code := run(t, covered)
		if code != 0 {
			t.Fatalf("a complete run at the checked-in floor (%v) must pass, got exit %d:\n%s", floor, code, out)
		}
	})
}
