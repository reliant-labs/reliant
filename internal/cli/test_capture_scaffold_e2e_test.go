//go:build e2e

package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// A brand-new project must record its tests and coverage with no setup, and
// its scaffolded CI must stay green when no upload credential exists. These
// run the scaffold's own Taskfile and ci.yml, not a copy of them.

// captureEnvE2E is the environment a scaffolded project's tasks run in: the
// process environment minus every CI and credential signal (so a CI runner's
// own settings cannot change what is asserted), plus FORGE_BIN, which makes
// the Taskfile's FORGE the binary under test and not a `go run` of a linked
// forge workspace. Later entries win.
func captureEnvE2E(forgeBin string, extra ...string) []string {
	drop := map[string]bool{
		"CI": true, "GITHUB_ACTIONS": true, "FORGE_CONTROL_PLANE_TOKEN": true,
		"FORGE_TESTS": true, "FORGE_TEST_RUN": true, "FORGE_BIN": true,
	}
	var env []string
	for _, kv := range os.Environ() {
		if drop[strings.SplitN(kv, "=", 2)[0]] {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "FORGE_BIN="+forgeBin,
		"GIT_AUTHOR_NAME=forge-test", "GIT_AUTHOR_EMAIL=forge-test@example.com",
		"GIT_COMMITTER_NAME=forge-test", "GIT_COMMITTER_EMAIL=forge-test@example.com")
	return append(env, extra...)
}

func runCaptureE2E(t *testing.T, dir string, env []string, name string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if err == nil {
		return out.String(), 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return out.String(), ee.ExitCode()
	}
	t.Fatalf("%s %v: %v\n%s", name, args, err, out.String())
	return "", -1
}

// newCaptureProjectE2E scaffolds a project of the given kind and commits it,
// so the recorded run is for a clean tree.
func newCaptureProjectE2E(t *testing.T, forgeBin, name string, newArgs ...string) (projectDir string, env []string) {
	t.Helper()
	dir := t.TempDir()
	env = captureEnvE2E(forgeBin)
	args := append([]string{"project", "new", name, "--mod", "example.com/" + name}, newArgs...)
	if out, code := runCaptureE2E(t, dir, env, forgeBin, args...); code != 0 {
		t.Fatalf("forge %v exited %d:\n%s", args, code, out)
	}
	projectDir = filepath.Join(dir, name)
	// `project new` already git-inits and commits; commit only what it left.
	if out, code := runCaptureE2E(t, projectDir, env, "git", "rev-parse", "--verify", "-q", "HEAD"); code != 0 {
		t.Fatalf("scaffold left no commit to record a clean-tree run against:\n%s", out)
	}
	if status, _ := runCaptureE2E(t, projectDir, env, "git", "status", "--porcelain"); strings.TrimSpace(status) != "" {
		for _, step := range [][]string{{"add", "-A"}, {"commit", "-q", "--no-verify", "-m", "scaffold"}} {
			if out, code := runCaptureE2E(t, projectDir, env, "git", step...); code != 0 {
				t.Fatalf("git %v exited %d:\n%s", step, code, out)
			}
		}
	}
	return projectDir, env
}

func captureRunDirsE2E(t *testing.T, projectDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(projectDir, ".forge", "tests", "runs"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, filepath.Join(projectDir, ".forge", "tests", "runs", e.Name()))
		}
	}
	return dirs
}

func readJSONE2E(t *testing.T, path string, into any) {
	t.Helper()
	if err := json.Unmarshal([]byte(readFileE2E(t, path)), into); err != nil {
		t.Fatalf("%s is not the JSON expected: %v", path, err)
	}
}

// TestE2EScaffoldedTaskTestRecordsRun: `task test` in a fresh project of each
// Go kind exits 0 and leaves exactly one run with the unit lane's results and
// coverage, which `forge test report --json` then summarises. No frontend
// lane is asserted: it needs a package-manager install per fixture, and the
// capture it adds is a vitest-only wrapper that is unit-tested in the
// Taskfile template tests.
func TestE2EScaffoldedTaskTestRecordsRun(t *testing.T) {
	t.Parallel() // independent projects in their own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	requireTool(t, "task", "git")

	for _, kind := range []struct {
		name string
		args []string
	}{
		{"service", []string{"--service", "item"}},
		{"cli", []string{"--kind", "cli"}},
		{"library", []string{"--kind", "library"}},
	} {
		t.Run(kind.name, func(t *testing.T) {
			t.Parallel()
			projectDir, env := newCaptureProjectE2E(t, forgeBin, "cap"+kind.name, kind.args...)

			if out, code := runCaptureE2E(t, projectDir, env, "task", "test"); code != 0 {
				t.Fatalf("`task test` exited %d on a fresh %s project:\n%s", code, kind.name, out)
			}

			runs := captureRunDirsE2E(t, projectDir)
			if len(runs) != 1 {
				t.Fatalf("want exactly one run under .forge/tests/runs/, got %v", runs)
			}
			run := runs[0]

			var meta struct {
				Lanes []string `json:"lanes"`
			}
			readJSONE2E(t, filepath.Join(run, "run.json"), &meta)
			if len(meta.Lanes) != 1 || meta.Lanes[0] != "unit" {
				t.Errorf("run.json lanes = %v, want [unit] (a %s project has no frontend)", meta.Lanes, kind.name)
			}

			var capture struct {
				Lane     string `json:"lane"`
				ExitCode *int   `json:"exit_code"`
				Coverage []struct {
					Path   string `json:"path"`
					Format string `json:"format"`
				} `json:"coverage"`
			}
			readJSONE2E(t, filepath.Join(run, "unit", "capture.json"), &capture)
			if capture.Lane != "unit" || capture.ExitCode == nil || *capture.ExitCode != 0 {
				t.Errorf("unit/capture.json = lane %q exit_code %v, want unit / 0", capture.Lane, capture.ExitCode)
			}
			if len(capture.Coverage) == 0 {
				t.Errorf("unit/capture.json lists no coverage")
			}
			if fi, err := os.Stat(filepath.Join(run, "unit", "cover.out")); err != nil || fi.Size() == 0 {
				t.Errorf("unit/cover.out missing or empty: %v", err)
			}
			if fi, err := os.Stat(filepath.Join(run, "unit", "results.json")); err != nil || fi.Size() == 0 {
				t.Errorf("unit/results.json missing or empty: %v", err)
			}

			// stdout alone: stderr carries notices that are not the document.
			reportCmd := exec.Command(forgeBin, "test", "report", "--json")
			reportCmd.Dir, reportCmd.Env = projectDir, env
			var stderr bytes.Buffer
			reportCmd.Stderr = &stderr
			stdout, err := reportCmd.Output()
			out := string(stdout)
			if err != nil {
				t.Fatalf("`forge test report --json` failed: %v\nstdout:\n%s\nstderr:\n%s", err, out, stderr.String())
			}
			var rep struct {
				OK     bool `json:"ok"`
				Totals struct {
					Passed int `json:"passed"`
					Failed int `json:"failed"`
				} `json:"totals"`
				Runs []struct {
					Lane     string `json:"lane"`
					ExitCode int    `json:"exit_code"`
				} `json:"runs"`
				Coverage struct {
					Combined *float64           `json:"combined"`
					Lanes    map[string]float64 `json:"lanes"`
				} `json:"coverage"`
			}
			if err := json.Unmarshal([]byte(out), &rep); err != nil {
				t.Fatalf("`forge test report --json` printed no JSON document: %v\n%s", err, out)
			}
			if !rep.OK || rep.Totals.Failed != 0 {
				t.Errorf("report: ok=%v failed=%d, want ok with no failures:\n%s", rep.OK, rep.Totals.Failed, out)
			}
			// Only the service scaffold ships tests; a CLI or library starts
			// with none, so a run with zero tests and no coverage is right.
			if kind.name == "service" && rep.Totals.Passed == 0 {
				t.Errorf("report counts no passed tests for a service scaffold:\n%s", out)
			}
			if len(rep.Runs) != 1 || rep.Runs[0].Lane != "unit" {
				t.Errorf("report runs = %+v, want the one unit run", rep.Runs)
			}
			if _, ok := rep.Coverage.Lanes["unit"]; kind.name == "service" && (!ok || rep.Coverage.Combined == nil) {
				t.Errorf("report carries no unit coverage number:\n%s", out)
			}
		})
	}
}

// reportStepRunE2E returns the `run:` line of the "Report test results" step
// of every job in the scaffolded ci.yml that has one, keyed by job id.
func reportStepRunE2E(t *testing.T, ciYML string) map[string]string {
	t.Helper()
	var wf struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(ciYML), &wf); err != nil {
		t.Fatalf("ci.yml is not YAML: %v", err)
	}
	runs := map[string]string{}
	for id, job := range wf.Jobs {
		for _, s := range job.Steps {
			if s.Name == "Report test results" {
				runs[id] = s.Run
			}
		}
	}
	return runs
}

// TestE2EScaffoldedCIReportWithoutTokenStaysGreen: the scaffolded CI's report
// step, run as a runner with no FORGE_CONTROL_PLANE_TOKEN and no sign-in
// would run it, exits 0 and says once that the upload was skipped (plus once
// as a GitHub Actions warning); and
// FORGE_TESTS=off records nothing.
func TestE2EScaffoldedCIReportWithoutTokenStaysGreen(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	requireTool(t, "task", "git", "bash")

	projectDir, env := newCaptureProjectE2E(t, forgeBin, "capci", "--service", "item")
	if out, code := runCaptureE2E(t, projectDir, env, "task", "test"); code != 0 {
		t.Fatalf("`task test` exited %d:\n%s", code, out)
	}
	runsBefore := captureRunDirsE2E(t, projectDir)
	if len(runsBefore) != 1 {
		t.Fatalf("want one recorded run to report, got %v", runsBefore)
	}

	steps := reportStepRunE2E(t, readFileE2E(t, filepath.Join(projectDir, ".github", "workflows", "ci.yml")))
	for _, job := range []string{"test", "integration-test"} {
		if steps[job] == "" {
			t.Fatalf("ci.yml job %q has no \"Report test results\" step (found in: %v)", job, steps)
		}
	}

	for job, run := range steps {
		t.Run(job, func(t *testing.T) {
			// A runner's environment: CI set, no token, no sign-in (an empty
			// HOME holds no credential store). Real go/task caches are not
			// needed: the step only runs `task test:report`.
			home := t.TempDir()
			ciEnv := captureEnvE2E(forgeBin, "CI=true", "GITHUB_ACTIONS=true", "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, ".config"))
			out, code := runCaptureE2E(t, projectDir, ciEnv, "bash", "-eo", "pipefail", "-c", run)
			if code != 0 {
				t.Fatalf("report step exited %d, want 0:\n%s", code, out)
			}
			if strings.Contains(out, "forge test report exited") {
				t.Errorf("the step's `|| echo` fallback fired, so `forge test report --upload` itself failed without a token:\n%s", out)
			}
			// The note, once, plus (GITHUB_ACTIONS is set) the same note
			// once as a `::warning` workflow command for the run's summary
			// (#3007): the runner turns that line into an annotation.
			var skipped, annotations []string
			for _, line := range strings.Split(out, "\n") {
				switch {
				case strings.Contains(line, "::warning title=Test results not uploaded::"):
					annotations = append(annotations, line)
				case strings.Contains(line, "upload skipped"):
					skipped = append(skipped, line)
				}
			}
			if len(skipped) != 1 || !strings.Contains(skipped[0], "FORGE_CONTROL_PLANE_TOKEN") {
				t.Errorf("want exactly one \"upload skipped\" line naming FORGE_CONTROL_PLANE_TOKEN, got %q in:\n%s", skipped, out)
			}
			if len(annotations) != 1 || !strings.Contains(annotations[0], "upload skipped") {
				t.Errorf("want exactly one \"Test results not uploaded\" warning saying the upload was skipped, got %q in:\n%s", annotations, out)
			}
		})
	}

	out, code := runCaptureE2E(t, projectDir, captureEnvE2E(forgeBin, "FORGE_TESTS=off"), "task", "test")
	if code != 0 {
		t.Fatalf("`FORGE_TESTS=off task test` exited %d:\n%s", code, out)
	}
	if after := captureRunDirsE2E(t, projectDir); len(after) != len(runsBefore) || after[0] != runsBefore[0] {
		t.Errorf("FORGE_TESTS=off recorded a run: before %v, after %v", runsBefore, after)
	}
}
