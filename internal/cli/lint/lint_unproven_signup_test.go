package lint

import (
	"testing"
)

func TestUnprovenSignup_FlagsSignUpWithNoMailAndLegacySelfSignupOutsideDev(t *testing.T) {
	root := t.TempDir()
	writeOpenSignupFile(t, root, "deploy/kcl/prod/main.k", `import forge

# delivery = "none" in a comment declares nothing
IDENTITY = forge.ManagedIdentity {
    signup = "open"
    email = forge.IdentityEmail {delivery = "none"}
}
`)
	writeOpenSignupFile(t, root, "deploy/kcl/staging/main.k", `API = forge.Service {
    env_vars = {"AUTH_SELF_SIGNUP" = "true"}
}
`)
	// Development turns legacy sign-up on anyway; mail-delivering envs prove.
	writeOpenSignupFile(t, root, "deploy/kcl/dev/main.k", `API = forge.Service {env_vars = {"AUTH_SELF_SIGNUP" = "true"}}
`)
	writeOpenSignupFile(t, root, "deploy/kcl/preview/main.k", `IDENTITY = forge.ManagedIdentity {signup = "open"}
`)
	found, err := findUnprovenSignups(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 || found[0].Where != "deploy/kcl/prod/main.k:5" || found[1].Where != "deploy/kcl/staging/main.k:2" {
		t.Fatalf("findings = %+v, want prod's no-mail sign-up and staging's legacy self-sign-up", found)
	}
	js, err := collectUnprovenSignupJSON(root)
	if err != nil || len(js) != 2 || js[0].Rule != ruleUnprovenSignup || js[0].Line != 5 || js[0].Severity != "error" {
		t.Fatalf("json = %+v, %v", js, err)
	}
}

func TestUnprovenSignup_NothingWithoutADeclaration(t *testing.T) {
	if found, err := findUnprovenSignups(t.TempDir()); err != nil || len(found) != 0 {
		t.Fatalf("= %v, %v", found, err)
	}
}

// The lane is wired into the sweep a bare `forge lint` runs, and graded by
// the rollout table there: run through collectLintOutcomes (every other lane
// switched off, so no toolchain runs), a production sign-up with no mail
// warns while the pin is in the grace period and gates from GatesFrom.
// forge#2179 review: a merge once dropped the lane from lintPipeline while
// the collector's own tests stayed green.
func TestUnprovenSignup_TheBareSweepRunsAndGradesIt(t *testing.T) {
	root := t.TempDir()
	writeOpenSignupFile(t, root, "deploy/kcl/prod/main.k", `import forge

IDENTITY = forge.ManagedIdentity {
    signup = "open"
    email = forge.IdentityEmail {delivery = "none"}
}
`)
	off := map[string]bool{}
	for _, step := range lintPipeline() {
		if step.name != unprovenSignupLane {
			off[step.name] = true
		}
	}
	ro, ok := lintRollouts[ruleUnprovenSignup]
	if !ok {
		t.Fatalf("%s has no rollout row", ruleUnprovenSignup)
	}
	sweep := func(pin string) ([]lintJSONFinding, bool) {
		t.Helper()
		outcomes := collectLintOutcomes(&lintRunCtx{ctx: t.Context(), cwd: root, cfg: pinnedCfg(pin, nil), lanesOff: off})
		fs, gated := flattenLintOutcomes(outcomes)
		var mine []lintJSONFinding
		for _, f := range fs {
			if f.Rule == ruleUnprovenSignup {
				mine = append(mine, f)
			}
		}
		if len(mine) != 1 || mine[0].File != "deploy/kcl/prod/main.k" {
			t.Fatalf("pin %s: the bare sweep's %s findings = %+v, want prod's no-mail sign-up (all findings %+v)", pin, ruleUnprovenSignup, mine, fs)
		}
		return mine, gated
	}
	if fs, gated := sweep(ro.IntroducedIn); gated || fs[0].Severity == lintSevError {
		t.Errorf("pin %s (before GatesFrom %s): want a warning that does not gate, got gated=%v %+v", ro.IntroducedIn, ro.GatesFrom, gated, fs)
	}
	if fs, gated := sweep(ro.GatesFrom); !gated || fs[0].Severity != lintSevError {
		t.Errorf("pin %s: want an error that gates, got gated=%v %+v", ro.GatesFrom, gated, fs)
	}
}
