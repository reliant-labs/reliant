package ciguard

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	yaml "gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// recordingCommands are the ci.yml commands that record a test lane through
// `forge test capture`, directly or through a script that does.
var recordingCommands = []string{
	"scripts/ci-test-shard.sh",
	"scripts/ci-test-full-shard.sh",
	"scripts/ci-changed-tests.sh",
	"scripts/ci-web-runtime-test.sh",
	"test capture",
}

// recordJob is the part of a workflow job this file reads.
type recordJob struct {
	If    string `yaml:"if"`
	Steps []struct {
		Name string            `yaml:"name"`
		Run  string            `yaml:"run"`
		Uses string            `yaml:"uses"`
		With map[string]string `yaml:"with"`
	} `yaml:"steps"`
}

// testUploadWorkflow sends main's recorded runs to the control plane.
var testUploadWorkflow = filepath.Join("..", "..", ".github", "workflows", "test-upload.yml")

// TestMainRecordingJobsKeepTheirRunsForUpload proves every ci.yml job that
// records a test lane on main keeps .forge/tests/runs as a test-runs-*
// artifact, and that test-upload.yml downloads exactly those from the CI run
// of a push to main: a lane whose runs no artifact carries never reaches the
// control plane. Merge-tier-only jobs (test-short) record for the runner
// alone.
func TestMainRecordingJobsKeepTheirRunsForUpload(t *testing.T) {
	raw, err := os.ReadFile(ciWorkflow)
	if err != nil {
		t.Fatalf("read %s: %v", ciWorkflow, err)
	}
	var wf struct {
		Jobs map[string]recordJob `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatalf("parse %s: %v", ciWorkflow, err)
	}
	var recorders []string
	for name, j := range wf.Jobs {
		cond := strings.TrimSpace(j.If)
		if cond != mainTier && cond != notReviewFirst {
			continue // not on main
		}
		records, keeps := false, false
		for _, s := range j.Steps {
			for _, c := range recordingCommands {
				if strings.Contains(s.Run, c) {
					records = true
				}
			}
			if strings.HasPrefix(s.Uses, "actions/upload-artifact@") &&
				strings.HasPrefix(s.With["name"], "test-runs-") && s.With["path"] == ".forge/tests/runs" &&
				s.With["include-hidden-files"] == "true" {
				keeps = true
			}
		}
		if !records {
			continue
		}
		recorders = append(recorders, name)
		if !keeps {
			t.Errorf("job %s records test lanes on main but keeps no test-runs-* artifact of .forge/tests/runs (include-hidden-files: true): test-upload.yml never sees them", name)
		}
	}
	if len(recorders) == 0 {
		t.Fatal("no job records a test lane on main — this guard would inspect nothing")
	}

	raw, err = os.ReadFile(testUploadWorkflow)
	if err != nil {
		t.Fatalf("read %s: %v", testUploadWorkflow, err)
	}
	var up struct {
		On struct {
			WorkflowRun struct {
				Workflows []string `yaml:"workflows"`
				Types     []string `yaml:"types"`
				Branches  []string `yaml:"branches"`
			} `yaml:"workflow_run"`
		} `yaml:"on"`
		Jobs map[string]recordJob `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &up); err != nil {
		t.Fatalf("parse %s: %v", testUploadWorkflow, err)
	}
	if wr := up.On.WorkflowRun; !slices.Equal(wr.Workflows, []string{"CI"}) || !slices.Equal(wr.Types, []string{"completed"}) || !slices.Equal(wr.Branches, []string{"main"}) {
		t.Errorf("test-upload.yml must run when a CI run on main completes, got %+v", wr)
	}
	downloads, uploads := false, false
	for _, j := range up.Jobs {
		if !strings.Contains(j.If, "github.event.workflow_run.event == 'push'") {
			t.Errorf("test-upload.yml's job has if: %q; it must run only for a push to main, whose code is merged", j.If)
		}
		for _, s := range j.Steps {
			if strings.HasPrefix(s.Uses, "actions/download-artifact@") && s.With["pattern"] == "test-runs-*" &&
				s.With["path"] == ".forge/tests/runs" && s.With["run-id"] == "${{ github.event.workflow_run.id }}" {
				downloads = true
			}
			if strings.Contains(s.Run, "scripts/ci-test-upload.sh") {
				uploads = true
			}
		}
	}
	if !downloads || !uploads {
		t.Errorf("test-upload.yml must download the CI run's test-runs-* artifacts into .forge/tests/runs (downloads=%v) and run scripts/ci-test-upload.sh (uploads=%v)", downloads, uploads)
	}
}

// TestFullShardRecordsThroughCapture pins how scripts/ci-test-full-shard.sh
// records main's -race lanes: through `forge test capture --lane
// <lane>-race --render raw` into the run `forge test begin` names, the
// lane's coverage as an atomic -coverprofile in the lane directory, and
// through scripts/ci-go-test.sh. The cli lane lists its tests with the same
// cover flags, so the run reuses the listed binary instead of a second race
// compile of internal/cli.
func TestFullShardRecordsThroughCapture(t *testing.T) {
	testkit.Slow(t, "execs bash")
	for _, lane := range []string{"unit", "cli"} {
		t.Run(lane, func(t *testing.T) {
			root := t.TempDir()
			run := filepath.Join(root, "run")
			if err := os.Mkdir(filepath.Join(root, "scripts"), 0o755); err != nil {
				t.Fatal(err)
			}
			for name, content := range map[string]string{
				"scripts/ci-test-full-shard.sh": readText(t, filepath.Join("..", "..", "scripts", "ci-test-full-shard.sh")),
				"scripts/ci-go-test.sh":         readText(t, filepath.Join("..", "..", "scripts", "ci-go-test.sh")),
				"scripts/ci-test-packages.sh":   "#!/usr/bin/env bash\necho fixture/package\n",
				// The stub go records each call's arguments; a -list call
				// lists one test, any other passes one.
				"go": `#!/usr/bin/env bash
echo "$*" >>"$(dirname "$0")/go-args"
case " $* " in
  *" -list "*) echo TestCheap; echo ok; exit 0 ;;
esac
printf '%s\n' '{"Action":"pass","Package":"fixture/package","Test":"TestCheap","Elapsed":0.01}' '{"Action":"pass","Package":"fixture/package","Elapsed":0.01}'
`,
			} {
				if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			log := filepath.Join(root, "forge-log")
			cmd := exec.Command("bash", "scripts/ci-test-full-shard.sh", lane, "0", "1")
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "PATH="+root+string(os.PathListSeparator)+os.Getenv("PATH"),
				"CI_TEST_JSON="+filepath.Join(root, "events.json"), "GITHUB_STEP_SUMMARY="+filepath.Join(root, "summary"),
				installStubForge(t, root), "FORGE_LOG="+log, "STUB_RUN="+run)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("shard: %v\n%s", err, out)
			}
			rec := lane + "-race"
			calls := readText(t, log)
			if !strings.Contains(calls, "test begin\n") || !strings.Contains(calls, "test capture --run "+run+" --lane "+rec+" --render raw -- "+filepath.Join(root, "scripts", "ci-go-test.sh")+" ") {
				t.Errorf("the shard must begin a run and record lane %s through capture, got:\n%s", rec, calls)
			}
			args := readText(t, filepath.Join(root, "go-args"))
			if want := "test -race -count=1 -json -covermode=atomic -coverprofile=" + filepath.Join(run, rec, "cover.out") + " "; !strings.Contains(args, want) {
				t.Errorf("go test lacks %q:\n%s", want, args)
			}
			if lane == "cli" && !strings.Contains(args, "test -race -cover -covermode=atomic -list ") {
				t.Errorf("the cli lane must list its tests with the run's cover flags, or the run compiles internal/cli's race binary twice:\n%s", args)
			}
			if fi, err := os.Stat(filepath.Join(run, rec)); err != nil || !fi.IsDir() {
				t.Errorf("the lane directory -coverprofile writes into must exist before go test runs: %v", err)
			}
			if events := readText(t, filepath.Join(root, "events.json")); !strings.Contains(events, `"Test":"TestCheap"`) {
				t.Errorf("$CI_TEST_JSON must hold the raw stream: %s", events)
			}
		})
	}
}

// TestUploadNeverDecides pins scripts/ci-test-upload.sh: it reports the
// commit's runs with --upload when the token is set and --upload=false when
// it is not, says why, and exits 0 whatever forge does, including when there
// is no run to report.
func TestUploadNeverDecides(t *testing.T) {
	testkit.Slow(t, "execs bash and git")
	for _, tc := range []struct {
		name, token, want string
		runs              bool
	}{
		{name: "token", token: "tok", want: "test report --commit abc123 --upload\n", runs: true},
		{name: "no token", want: "test report --commit abc123 --upload=false\n", runs: true},
		{name: "no runs", token: "tok", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
				t.Fatalf("git init: %v\n%s", err, out)
			}
			if err := os.MkdirAll(filepath.Join(root, "scripts"), 0o755); err != nil {
				t.Fatal(err)
			}
			script := readText(t, filepath.Join("..", "..", "scripts", "ci-test-upload.sh"))
			if err := os.WriteFile(filepath.Join(root, "scripts", "ci-test-upload.sh"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			if tc.runs {
				dir := filepath.Join(root, ".forge", "tests", "runs", "01RUN")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "run.json"), []byte("{}"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			log := filepath.Join(root, "forge-log")
			cmd := exec.Command("bash", "scripts/ci-test-upload.sh", "abc123")
			cmd.Dir = root
			cmd.Env = append(os.Environ(), installStubForge(t, root), "FORGE_LOG="+log, "STUB_EXIT=2",
				"FORGE_CONTROL_PLANE_TOKEN="+tc.token, "GITHUB_ACTIONS=")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("the upload must never fail the job: %v\n%s", err, out)
			}
			calls, _ := os.ReadFile(log)
			if string(calls) != tc.want {
				t.Errorf("forge calls = %q, want %q\n%s", calls, tc.want, out)
			}
			if tc.token == "" && !strings.Contains(string(out), "FORGE_CONTROL_PLANE_TOKEN is not set") {
				t.Errorf("a missing token must be said, got:\n%s", out)
			}
		})
	}
}

// TestShardsSkipQuarantinedTests proves both shard scripts pass `go test`
// the -skip pattern `forge test quarantine skip-regex go` prints, so a
// ci/quarantine/ entry (forge.yaml's tests.flakes.quarantine: pr writes them)
// takes effect, and pass no -skip at all when it prints nothing, so the
// command, and the test cache key, is unchanged with an empty list.
func TestShardsSkipQuarantinedTests(t *testing.T) {
	testkit.Slow(t, "execs bash")
	for _, tc := range []struct {
		script string
		args   []string
	}{
		{script: "ci-test-shard.sh", args: []string{"0", "1"}},
		{script: "ci-test-full-shard.sh", args: []string{"unit", "0", "1"}},
	} {
		for _, quarantined := range []string{"^(TestFlaky)$", ""} {
			t.Run(tc.script+"/"+map[bool]string{true: "quarantined", false: "empty"}[quarantined != ""], func(t *testing.T) {
				root := t.TempDir()
				if err := os.Mkdir(filepath.Join(root, "scripts"), 0o755); err != nil {
					t.Fatal(err)
				}
				for name, content := range map[string]string{
					"scripts/" + tc.script:        readText(t, filepath.Join("..", "..", "scripts", tc.script)),
					"scripts/ci-go-test.sh":       readText(t, filepath.Join("..", "..", "scripts", "ci-go-test.sh")),
					"scripts/ci-test-packages.sh": "#!/usr/bin/env bash\necho fixture/package\n",
					"go": `#!/usr/bin/env bash
echo "$*" >>"$(dirname "$0")/go-args"
printf '%s\n' '{"Action":"pass","Package":"fixture/package","Test":"TestCheap","Elapsed":0.01}' '{"Action":"pass","Package":"fixture/package","Elapsed":0.01}'
`,
				} {
					if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o755); err != nil {
						t.Fatal(err)
					}
				}
				cmd := exec.Command("bash", append([]string{"scripts/" + tc.script}, tc.args...)...)
				cmd.Dir = root
				cmd.Env = append(os.Environ(), "PATH="+root+string(os.PathListSeparator)+os.Getenv("PATH"),
					"CI_TEST_JSON="+filepath.Join(root, "events.json"), "GITHUB_STEP_SUMMARY="+filepath.Join(root, "summary"),
					installStubForge(t, root), "FORGE_LOG="+filepath.Join(root, "forge-log"), "STUB_RUN="+filepath.Join(root, "run"),
					"STUB_STDOUT="+quarantined)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("%s: %v\n%s", tc.script, err, out)
				}
				if calls := readText(t, filepath.Join(root, "forge-log")); !strings.Contains(calls, "test quarantine skip-regex go\n") {
					t.Errorf("%s must ask forge for the quarantine's -skip pattern, got:\n%s", tc.script, calls)
				}
				args := readText(t, filepath.Join(root, "go-args"))
				switch {
				case quarantined != "" && !strings.Contains(args, " -skip "+quarantined+" fixture/package"):
					t.Errorf("go test must skip the quarantined tests (-skip %s):\n%s", quarantined, args)
				case quarantined == "" && strings.Contains(args, "-skip"):
					t.Errorf("an empty quarantine must add no -skip:\n%s", args)
				}
			})
		}
	}
}

// TestVulnSBOMStepPullsTheDatabaseFirst pins the build job's SBOM scan:
// while vuln-gate does not gate (forge.yaml's lint.rules sets it to warn),
// `forge ci vuln-sbom` never downloads the
// vulnerability database, and with none on disk it exits 78 and scans
// nothing (a SKIP every push). So the step runs `forge vuln db pull` before
// it, into $FORGE_VULN_DB_DIR under the runner's temp directory, fresh each
// run, and deletes it afterwards.
func TestVulnSBOMStepPullsTheDatabaseFirst(t *testing.T) {
	raw, err := os.ReadFile(ciWorkflow)
	if err != nil {
		t.Fatalf("read %s: %v", ciWorkflow, err)
	}
	var wf struct {
		Jobs map[string]struct {
			Steps []struct {
				Run string            `yaml:"run"`
				Env map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatalf("parse %s: %v", ciWorkflow, err)
	}
	found := 0
	for name, j := range wf.Jobs {
		for _, s := range j.Steps {
			scan := strings.Index(s.Run, "forge ci vuln-sbom")
			if scan < 0 {
				continue
			}
			found++
			if pull := strings.Index(s.Run, "forge vuln db pull"); pull < 0 || pull > scan {
				t.Errorf("job %s runs forge ci vuln-sbom without `forge vuln db pull` before it: with no database it exits 78 and scans nothing", name)
			}
			if !strings.HasPrefix(s.Env["FORGE_VULN_DB_DIR"], "${{ runner.temp }}/") {
				t.Errorf("job %s: FORGE_VULN_DB_DIR = %q, want a directory under ${{ runner.temp }}", name, s.Env["FORGE_VULN_DB_DIR"])
			}
			if !strings.Contains(s.Run, `rm -rf "$FORGE_VULN_DB_DIR"`) {
				t.Errorf("job %s must delete the ~3.4 GB database after the scan", name)
			}
		}
	}
	if found == 0 {
		t.Fatalf("no %s step runs forge ci vuln-sbom — this guard would inspect nothing", ciWorkflow)
	}
}
