package cli

import (
	"github.com/reliant-labs/forge/internal/codemod"
)

// `forge fix trimpath-default`: the mechanical half of the playbook
// migrations/v1.11.0/trimpath-default.
//
// From v1.11.0 a forge project builds Go with -trimpath because it SAYS so,
// where a person reads the build: every scaffolded `forge.GoBuild` carries
// `flags = ["-trimpath"]`, and forge passes GoBuild.flags to every go build
// and go run it execs from that build (`forge build`, the host go-run
// runner, air through $FORGE_GO_BUILD_FLAGS, BuildOnly variants). Nothing in
// forge adds -trimpath on its own. Without it Go keys each package's
// build-cache entry on its absolute directory, so every worktree of a module
// compiles everything and re-runs every test again.
//
// A project scaffolded earlier declares none of it, and every file involved
// is the project's own: deploy/kcl, .air.toml and Taskfile.yml are
// scaffold-once, so moving the pin changes nothing in them. This codemod
// writes the declarations, each edit in its own step:
//
//   - KCL (upgrade_trimpath_kcl.go): "-trimpath" into every GoBuild literal's
//     flags under deploy/kcl;
//   - air (upgrade_trimpath_air.go): $FORGE_GO_BUILD_FLAGS into the
//     .air.toml build commands, so air's build receives those flags;
//   - Taskfile (upgrade_trimpath_taskfile.go): the GOFLAGS entry of the
//     scaffolded Taskfile env, for everything Task runs.
//
// Each step leaves alone what already says trimpath (including
// -trimpath=false: an opt-out is a decision) and reports, as the edit to make
// by hand, any shape it cannot rewrite safely. Re-running it changes nothing.

// trimpathRelease is the release the codemod and its playbook ship in.
const trimpathRelease = "v1.11.0"

// trimpathPlaybookID is the playbook's ID, as `forge project upgrade list`
// prints it.
const trimpathPlaybookID = trimpathRelease + "/trimpath-default"

// trimpathPlaybookSkill is the playbook's skill path.
const trimpathPlaybookSkill = "migrations/" + trimpathPlaybookID

func init() {
	codemod.Register(codemod.Codemod{
		Name:    "trimpath-default",
		Version: trimpathRelease,
		Short:   "Declare -trimpath in an older project's GoBuild flags, .air.toml and Taskfile",
		Long: `Write what a project forge scaffolds today declares, so its Go builds use
-trimpath and every worktree shares Go build-cache entries:

  - deploy/kcl: "-trimpath" in each forge.GoBuild literal's flags (a new
    ` + "`flags = [\"-trimpath\"]`" + `, or first in an existing list). forge passes
    GoBuild.flags to forge build, the host go-run runner, air and BuildOnly
    variants.
  - .air.toml (and any air_config an OnHost declares): $FORGE_GO_BUILD_FLAGS
    in the [build] command, which is how those flags reach air's build; a
    [build.windows] command still in an older forge's form is replaced by the
    current one. .air-debug.toml is never touched: debug builds keep paths.
  - Taskfile.yml: the top-level env GOFLAGS entry that merges -trimpath into
    the machine's GOFLAGS, for everything Task runs.

Anything that already says trimpath, -trimpath=false included, is left alone,
and so is any shape it cannot rewrite safely (flags bound to a variable, a
spread, an edited air command, a Taskfile that sets GOFLAGS itself); the run
names each with the edit to make by hand. Re-running it changes nothing.
The rest of the story: ` + "`forge skill load " + trimpathPlaybookSkill + "`" + `.`,
		Run: runTrimpathDefault,
	})
}

// trimpathSteps are the codemod's steps, in the order their reports read.
var trimpathSteps = []func(dir string, write bool) (codemod.Report, error){
	trimpathKCLStep, trimpathAirStep, trimpathTaskfileStep,
}

func runTrimpathDefault(dir string, opts codemod.Options) (codemod.Report, error) {
	var rep codemod.Report
	for _, step := range trimpathSteps {
		r, err := step(dir, opts.Write)
		if err != nil {
			return rep, err
		}
		rep.Rewrites = append(rep.Rewrites, r.Rewrites...)
		rep.Next = append(rep.Next, r.Next...)
		for _, v := range r.Verify {
			if !containsString(rep.Verify, v) {
				rep.Verify = append(rep.Verify, v)
			}
		}
	}
	return rep, nil
}

// projectNeedsTrimpathDefault is the playbook's detection (`forge-detect
// trimpath-default`): whether the codemod would change anything, or report a
// manual step. So the playbook is offered exactly where the codemod has
// something to say, and resolved exactly when it has nothing left.
func projectNeedsTrimpathDefault(dir string) bool {
	rep, err := runTrimpathDefault(dir, codemod.Options{})
	return err == nil && (rep.Changed() || len(rep.Next) > 0)
}
