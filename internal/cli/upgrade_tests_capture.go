package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/reliant-labs/forge/internal/codemod"
	"github.com/reliant-labs/forge/internal/config"
)

// `forge fix tests-capture`: the mechanical half of the playbook
// migrations/v1.12.0/tests-capture.
//
// From v1.12.0 a scaffolded Taskfile records every test run: each lane runs
// its `go test` (or Vitest) through `forge test capture`, which runs the
// command unchanged and exits with its status, and records the results and
// coverage under .forge/tests/runs/<id>/. `task test:report` summarises a run
// and, with `-- --upload`, sends it to the project's control plane; the
// scaffolded CI calls it after each test step.
//
// A project scaffolded earlier has none of it, and Taskfile.yml and
// .github/workflows/ci.yml are the project's own (scaffold-once), so moving
// the pin changes nothing in them: its tests keep running unrecorded, exactly
// as before. This codemod writes what a new project gets, in two steps:
//
//   - Taskfile (upgrade_tests_capture_taskfile.go): each lane still in the
//     shape an older forge scaffolded becomes the shape the current
//     templates render, byte for byte, and `test:report` is added;
//   - CI (upgrade_tests_capture_ci.go): the `Report test results` step
//     after each `task test` / `task test:integration` step of ci.yml.
//
// The new text is taken from the rendered templates, never restated here, so
// the codemod cannot drift from what `forge project new` writes. A lane a
// person edited is not recognised and is never touched: the run names it,
// with the edit to make by hand. Re-running it changes nothing.

// testsCaptureRelease is the release the codemod and its playbook ship in.
const testsCaptureRelease = "v1.12.0"

// testsCapturePlaybookID is the playbook's ID, as `forge project upgrade
// list` prints it.
const testsCapturePlaybookID = testsCaptureRelease + "/tests-capture"

// testsCapturePlaybookSkill is the playbook's skill path.
const testsCapturePlaybookSkill = "migrations/" + testsCapturePlaybookID

func init() {
	codemod.Register(codemod.Codemod{
		Name:    "tests-capture",
		Version: testsCaptureRelease,
		Short:   "Record an older project's test runs: forge test capture in each Taskfile lane, test:report, and the CI report step",
		Long: `Rewrite the test lanes of a Taskfile an older forge scaffolded into the shape
a new project gets, so every ` + "`task test`" + ` records its results and coverage
under .forge/tests/runs/ (gitignored):

  - test, test:integration, test:e2e: the ` + "`go test`" + ` runs through
    ` + "`forge test capture`" + ` (lanes unit, integration, e2e), with -json and,
    except e2e, a -coverprofile in the run; each task gets its RUN var
    (` + "`forge test begin`" + `). The command still runs unchanged and the task
    exits with its status.
  - test:frontend: each Vitest frontend is recorded as lane frontend-<dir>
    (coverage when @vitest/coverage-v8 is installed).
  - test:report: added, to summarise runs and, with -- --upload, send them
    to the control plane.
  - .github/workflows/ci.yml: a ` + "`Report test results`" + ` step after each
    ` + "`task test`" + ` / ` + "`task test:integration`" + ` step, and ` + "`id-token: write`" + ` on its
    job: it uploads with GitHub OIDC (no secret) and never fails the job.

A lane is rewritten only when it is still exactly what forge scaffolded; one
you edited is left alone and the run names the edit to make by hand. Nothing
is written while forge.yaml pins a forge older than ` + testsCaptureRelease + `, which has no
` + "`forge test`" + `. Re-running it changes nothing.
The rest of the story: ` + "`forge skill load " + testsCapturePlaybookSkill + "`" + `.`,
		Run: runTestsCapture,
	})
}

func runTestsCapture(dir string, opts codemod.Options) (codemod.Report, error) {
	var rep codemod.Report
	tf, err := planTestsCaptureTaskfile(dir)
	if err != nil || tf == nil {
		return rep, err
	}
	rep.Next = append(rep.Next, tf.next...)
	var ci *testsCaptureCIPlan
	if tf.hasReport {
		if ci, err = planTestsCaptureCI(dir); err != nil {
			return rep, err
		}
		if ci != nil {
			rep.Next = append(rep.Next, ci.next...)
		}
	}
	changes := tf.out != nil || (ci != nil && ci.out != nil)
	if !changes {
		return rep, nil
	}
	if why := testsCapturePinTooOld(dir); why != "" {
		rep.Next = append([]string{why}, rep.Next...)
		return rep, nil
	}
	if tf.out != nil {
		if opts.Write {
			if err := writeKeepingMode(filepath.Join(dir, tf.rel), tf.out); err != nil {
				return rep, err
			}
		}
		rep.Rewrites = append(rep.Rewrites, tf.rewrites...)
	}
	if ci != nil && ci.out != nil {
		if opts.Write {
			if err := writeKeepingMode(filepath.Join(dir, filepath.FromSlash(testsCaptureCIPath)), ci.out); err != nil {
				return rep, err
			}
		}
		rep.Rewrites = append(rep.Rewrites, ci.rewrites...)
	}
	rep.Verify = []string{"task --list-all", "task test", "task test:report"}
	return rep, nil
}

// testsCapturePinTooOld names why nothing may be written when forge.yaml
// pins a released forge older than testsCaptureRelease: the Taskfile runs
// the PINNED forge, and one without `forge test` would fail every lane. ""
// when the pin has it, is a dev build or a pseudo-version forge cannot
// judge, or forge.yaml does not load. `forge project upgrade` moves the pin
// before it runs a playbook's automation, so there it never fires.
func testsCapturePinTooOld(dir string) string {
	cfg, err := config.LoadProjectDir(dir)
	if err != nil || cfg == nil {
		return ""
	}
	pin := cfg.ForgeVersion
	if !semver.IsValid(pin) || semver.Build(pin) != "" {
		return ""
	}
	if pre := semver.Prerelease(pin); pre != "" && !strings.HasPrefix(pre, "-rc") {
		return ""
	}
	if semver.Compare(pin, testsCaptureRelease+"-0") >= 0 {
		return ""
	}
	return fmt.Sprintf("forge.yaml pins forge %s, which has no `forge test` command, and the Taskfile runs the pinned forge, so nothing was written: "+
		"every rewritten lane would fail. `%s project upgrade --to %s` (or later) moves the pin and then runs this codemod",
		pin, Name(), testsCaptureRelease)
}

// projectNeedsTestsCapture is the playbook's detection (`forge-detect
// tests-capture`): a lane of the Taskfile is still in a shape an older forge
// scaffolded, or the Taskfile has forge's `test` task but no `test:report`.
// A lane someone edited does not keep it pending: forge cannot tell whether
// that edit already records it. The pin is not consulted, so `forge project
// upgrade list` offers the playbook to a project that is still pinned below
// the release.
func projectNeedsTestsCapture(dir string) bool {
	tf, err := planTestsCaptureTaskfile(dir)
	return err == nil && tf != nil && tf.pending
}
