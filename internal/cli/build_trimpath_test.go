package cli

import (
	"slices"
	"testing"
)

// A GoBuild's declared flags — a scaffolded GoBuild says -trimpath — are the
// flags of every go build forge runs from it: `forge build`, build variants,
// and the runners that compile on launch (go-run, air). The binary and delve
// runners run what forge build compiled, so they carry none.
func TestDeclaredGoBuildFlagsReachEveryBuild(t *testing.T) {
	declared := []string{"-trimpath"}
	withFlags := func(runner string) func(*WorkloadEntity) {
		return func(w *WorkloadEntity) {
			w.Build.Go.Flags = declared
			w.Runtime.Host.Runner = runner
		}
	}
	for runner, want := range map[string][]string{"go-run": declared, "air": declared, "binary": nil, "delve": nil} {
		if got := hostRunnerSpec(hostWL("api", withFlags(runner))).GoBuildFlags; !slices.Equal(got, want) {
			t.Errorf("%s: GoBuildFlags = %q, want %q", runner, got, want)
		}
	}

	tgt := goBuildTarget{cmd: "./cmd/api", outputName: "api", flags: declared}
	if args := goBuildArgs(tgt, "bin/api", false, versionInfo{}); !slices.Contains(args, "-trimpath") {
		t.Errorf("forge build argv %q lacks the declared -trimpath", args)
	}
	if args := variantBuildArgs("bin/api-linux", "./cmd/api", declared, BuildVariant{Name: "linux"}); !slices.Equal(args, []string{"build", "-o", "bin/api-linux", "-trimpath", "./cmd/api"}) {
		t.Errorf("variant argv = %q, want the declared -trimpath before the package", args)
	}
}

// A debug build keeps absolute source paths whatever the GoBuild declares:
// -trimpath=false comes after the declared flags, and the later flag wins,
// so delve's breakpoints resolve.
func TestDebugBuildOverridesADeclaredTrimpath(t *testing.T) {
	tgt := goBuildTarget{cmd: "./cmd/api", outputName: "api", flags: []string{"-trimpath"}}
	args := goBuildArgs(tgt, "bin/api", true, versionInfo{})
	on, off := slices.Index(args, "-trimpath"), slices.Index(args, "-trimpath=false")
	if on < 0 || off < on {
		t.Errorf("debug argv %q: -trimpath=false must come after the declared -trimpath", args)
	}
	if args[len(args)-1] != "./cmd/api" {
		t.Errorf("debug argv %q must end with the package", args)
	}
}

// The opt-out is in the declaration: a -trimpath=false after -trimpath wins.
func TestDeclaredTrimpathFalseIsKept(t *testing.T) {
	tgt := goBuildTarget{cmd: "./cmd/api", outputName: "api", flags: []string{"-trimpath", "-trimpath=false"}}
	args := goBuildArgs(tgt, "bin/api", false, versionInfo{})
	if slices.Index(args, "-trimpath=false") < slices.Index(args, "-trimpath") {
		t.Errorf("argv %q reordered the declared flags", args)
	}
}
