package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/exitcode"
	"github.com/reliant-labs/forge/internal/forgedelegate"
)

// The hand-off mode of every kind of command: about this forge or machine
// (runHere), a project command (handOffRequired), and the stop that must work
// offline (handOffPreferred), whose machine-wide form is no project's.
func TestHandOffModeFor(t *testing.T) {
	for _, tc := range []struct {
		rel         string
		machineWide bool
		want        handOffMode
	}{
		{"env up", false, handOffRequired},
		{"env down", false, handOffPreferred},
		{"env down", true, runHere},
		{"env ps", false, runHere},
		{"env devstack port", false, runHere},
		{"env render", false, handOffRequired},
		{"env deploy", false, handOffRequired},
		{"build", false, handOffRequired},
		{"generate", false, handOffRequired},
		{"lint", false, handOffRequired},
		{"kcl eval", false, handOffRequired},
		{"db status", false, handOffRequired},
		{"obs status", false, handOffRequired},
		{"skill load", false, handOffRequired},
		{"a-command-this-forge-lacks", false, handOffRequired},
		// machineWide means something only to a command that declares it.
		{"env up", true, handOffRequired},
		{"", false, runHere},
		{"version", false, runHere},
		{"help", false, runHere},
		{"report-bug", false, runHere},
		{"project upgrade", false, runHere},
		{"storage gc", false, runHere},
		{"mcp", false, runHere},
	} {
		if got := handOffModeFor(tc.rel, tc.machineWide); got != tc.want {
			t.Errorf("handOffModeFor(%q, machineWide=%v) = %v, want %v", tc.rel, tc.machineWide, got, tc.want)
		}
	}
}

// Every entry of machineWideFlags names a real boolean flag of its command:
// a renamed flag would silently hand `env down --all` to one project's forge.
func TestMachineWideFlagsExist(t *testing.T) {
	root := newTestRootCmd(t)
	for rel, flag := range machineWideFlags {
		cmd, _, err := root.Find(strings.Fields(rel))
		if err != nil || cmd == nil {
			t.Fatalf("find %q: %v", rel, err)
		}
		f := cmd.Flags().Lookup(flag)
		if f == nil || f.Value.Type() != "bool" {
			t.Errorf("forge %s has no boolean --%s", rel, flag)
		}
	}
}

// pinAvailability is how the forge a project pins can be had.
type pinAvailability string

const (
	pinCached       pinAvailability = "cached"       // already built; no network needed
	pinBuildable    pinAvailability = "buildable"    // not built yet; the build succeeds
	pinUnobtainable pinAvailability = "unobtainable" // not built; the build fails (offline)
)

// outcome is what the pin decision did with a command.
type outcome string

const (
	handedOver outcome = "handed to the pinned forge"
	ranHere    outcome = "ran this forge"
	refused    outcome = "failed, nothing ran"
)

// The decision table: command × skew × whether the pinned forge can be had ×
// opt-out. `env down <env>` is decided like `env up`: both hand over to the
// pin whenever it can be had (2026-11-08: a stale PATH forge stopped a stack
// `env up` had handed to its pin, failed to render the project's KCL, and left
// it half-stopped). Only when the pin cannot be had do they differ: `up` fails,
// and `down` stops the stack with this forge rather than not at all.
func TestPinDecisionTable(t *testing.T) {
	type row struct {
		args  []string
		avail pinAvailability
		want  outcome
	}
	var rows []row
	for _, avail := range []pinAvailability{pinCached, pinBuildable, pinUnobtainable} {
		project := handedOver
		if avail == pinUnobtainable {
			project = refused
		}
		stop := handedOver
		if avail == pinUnobtainable {
			stop = ranHere
		}
		rows = append(rows,
			row{[]string{"env", "up", "dev"}, avail, project},
			row{[]string{"env", "render", "dev"}, avail, project},
			row{[]string{"kcl", "eval", "x.k"}, avail, project},
			row{[]string{"build"}, avail, project},
			row{[]string{"lint"}, avail, project},
			row{[]string{"db", "status"}, avail, project},
			row{[]string{"obs", "status", "dev"}, avail, project},
			row{[]string{"env", "down", "dev"}, avail, stop},
			row{[]string{"env", "down", "--all"}, avail, ranHere},
			row{[]string{"env", "ps"}, avail, ranHere},
			row{[]string{"version"}, avail, ranHere},
			row{[]string{"report-bug"}, avail, ranHere},
		)
	}

	for _, r := range rows {
		name := strings.Join(r.args, " ") + "/" + string(r.avail)
		t.Run("skewed/"+name, func(t *testing.T) {
			skewedProject(t)
			t.Setenv(forgedelegate.MarkerEnv, "")
			h, installs := armPin(t, r.avail)
			out, err := runCheck(t, r.args...)
			if got := verdict(h, err); got != r.want {
				t.Fatalf("%s, want %s (err %v)\n%s", got, r.want, err, out)
			}
			if r.avail == pinCached && len(*installs) != 0 {
				t.Errorf("a cached pin was built again: %v", *installs)
			}
			if r.want == ranHere && r.args[0] == "env" && r.args[1] == "down" && r.args[2] == "dev" {
				// The stop that ran here says why, and what it ran.
				for _, want := range []string{"could not be obtained", "no such host", "a different forge than the project's"} {
					if !strings.Contains(out, want) {
						t.Errorf("the notice does not say %q:\n%s", want, out)
					}
				}
			}
		})

		// Opt-outs and no skew: this forge runs every command, whatever the
		// pinned forge's availability, and builds nothing.
		for _, optOut := range []string{"flag", "env", "no skew", "delegate"} {
			t.Run(optOut+"/"+name, func(t *testing.T) {
				skewedProject(t)
				t.Setenv(forgedelegate.MarkerEnv, "")
				args := r.args
				switch optOut {
				case "flag":
					// Only the commands the pin decides for take the flag.
					if handOffWanted(strings.Join(commandWords(r.args), " ")) {
						args = append(append([]string{}, r.args...), "--"+allowVersionSkewFlag)
					}
				case "env":
					t.Setenv(allowVersionSkewEnv, "1")
				case "no skew":
					buildinfo.Set(skewTestPin, "", "377b61532c0e")
				case "delegate":
					actAsDelegate(t)
				}
				h, installs := armPin(t, r.avail)
				out, err := runCheck(t, args...)
				if err != nil || h.path != "" {
					t.Fatalf("err %v, handed to %q, want this forge to run\n%s", err, h.path, out)
				}
				if len(*installs) != 0 {
					t.Errorf("built a pinned forge nobody runs: %v", *installs)
				}
			})
		}
	}
}

// A NEWER forge on PATH than the pin hands over just the same: the pin names
// one forge, and a newer one's templates and schemas are as wrong for the
// project as an older one's. `env up` and `env down` agree.
func TestPinDecision_NewerForgeHandsOverToAnOlderPin(t *testing.T) {
	for _, args := range [][]string{{"env", "up", "dev"}, {"env", "down", "dev"}} {
		skewedProject(t)
		buildinfo.Set("v0.1.46-0.20261101000000-0123456789ab", "", "0123456789ab")
		t.Setenv(forgedelegate.MarkerEnv, "")
		h, _ := armPin(t, pinBuildable)
		if _, err := runCheck(t, args...); err != nil || h.path == "" {
			t.Errorf("forge %v under a newer forge: err %v, handed to %q", args, err, h.path)
		}
	}
}

// The entrypoint makes the same decision before the command tree runs, and
// reads --all from the raw command line: the per-project stop is handed over,
// with its arguments untouched and the one-process marker; the machine-wide
// stop is not.
func TestHandOffToPinned_EnvDown(t *testing.T) {
	skewedProject(t)
	t.Setenv(forgedelegate.MarkerEnv, "")

	h, _ := armPin(t, pinBuildable)
	handed, out, err := handOffForTest(t, "env", "down", "dev")
	if err != nil || !handed {
		t.Fatalf("env down dev: handed %v, err %v\n%s", handed, err, out)
	}
	if got := strings.Join(h.args, " "); got != "env down dev" {
		t.Errorf("args %q, want the original invocation", got)
	}
	marker := forgedelegate.MarkerEntry(os.Getpid(), "forge_version "+skewTestPin)
	found := false
	for _, kv := range h.env {
		found = found || kv == marker
	}
	if !found {
		t.Errorf("the pinned forge is not told it was handed the command, so it could hand it on: %v", h.env)
	}

	for _, args := range [][]string{{"env", "down", "--all"}, {"env", "down", "--all=true"}} {
		h, installs := armPin(t, pinBuildable)
		if handed, _, err := handOffForTest(t, args...); handed || err != nil || h.path != "" || len(*installs) != 0 {
			t.Errorf("%v: handed %v to %q, built %v, err %v; want this forge", args, handed, h.path, *installs, err)
		}
	}

	// Unobtainable: the entrypoint lets this forge run the stop.
	h, _ = armPin(t, pinUnobtainable)
	handed, out, err = handOffForTest(t, "env", "down", "dev")
	if err != nil || handed || h.path != "" {
		t.Fatalf("unobtainable pin: handed %v, err %v", handed, err)
	}
	if !strings.Contains(out, "running this forge instead") {
		t.Errorf("no notice that this forge runs the stop:\n%s", out)
	}
}

// The loop guard: the forge a hand-off exec'd never hands off again, not even
// the commands it would otherwise hand to its pin — whatever its own version
// string says about the pin.
func TestPinDecision_ADelegateNeverHandsOffAgain(t *testing.T) {
	skewedProject(t)
	actAsDelegate(t)
	for _, args := range [][]string{{"env", "up", "dev"}, {"env", "down", "dev"}, {"kcl", "eval", "x.k"}, {"generate"}} {
		h, installs := armPin(t, pinBuildable)
		if handed, _, err := handOffForTest(t, args...); handed || err != nil {
			t.Errorf("entrypoint, %v: handed %v, err %v", args, handed, err)
		}
		if out, err := runCheck(t, args...); err != nil || out != "" {
			t.Errorf("pre-run check, %v: err %v, output %q", args, err, out)
		}
		if h.path != "" || len(*installs) != 0 {
			t.Errorf("%v: a delegate handed off to %q (built %v)", args, h.path, *installs)
		}
	}
}

// Review B1 on #1975: `env down` falls back to this forge only when the pinned
// forge cannot be OBTAINED. Here it cannot: nothing is exec'd, and the stop
// runs here with a notice saying why.
func TestEnvDown_FallsBackOnlyWhenThePinCannotBeObtained(t *testing.T) {
	skewedProject(t)
	t.Setenv(forgedelegate.MarkerEnv, "")
	for _, entry := range []string{"entrypoint", "pre-run"} {
		h, _ := armPin(t, pinUnobtainable)
		var handed bool
		var out string
		var err error
		if entry == "entrypoint" {
			handed, out, err = handOffForTest(t, "env", "down", "dev")
		} else {
			out, err = runCheck(t, "env", "down", "dev")
		}
		if err != nil || handed || h.path != "" {
			t.Fatalf("%s: handed %v to %q, err %v; want this forge to stop the stack", entry, handed, h.path, err)
		}
		if !strings.Contains(out, "could not be obtained") || !strings.Contains(out, "running this forge instead") {
			t.Errorf("%s: no notice that the pin could not be had:\n%s", entry, out)
		}
	}
}

// Review B1 on #1975: once the pinned forge has been obtained and run, its
// result is the command's result. A pinned `env down` that fails part way
// fails the command with the delegate's own error and exit code; it is never
// re-run under this forge, which would hide the failure behind a second,
// incompatible stop.
func TestEnvDown_PropagatesTheDelegatesFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in delegate is a shell script")
	}
	skewedProject(t)
	t.Setenv(forgedelegate.MarkerEnv, "")
	ran := filepath.Join(t.TempDir(), "delegate-ran")

	for _, entry := range []string{"entrypoint", "pre-run"} {
		_ = os.Remove(ran)
		armDelegation(t)
		// The pinned forge is obtainable (built by the stand-in install) and
		// is a program that runs and exits 3, as a failing `env down` would.
		pinDelegateResolver = func(w io.Writer) *forgedelegate.Resolver {
			return &forgedelegate.Resolver{
				Log: w,
				Install: func(_ context.Context, _, gobin string) error {
					script := "#!/bin/sh\ntouch '" + ran + "'\necho 'pinned env down: stop failed' >&2\nexit 3\n"
					return atomicfile.WriteFile(filepath.Join(gobin, forgeExeName()), []byte(script), 0o755)
				},
			}
		}
		pinDelegateExec = func(path string, args, env []string) error {
			// What the Windows relay does; Unix replaces the process, so
			// the delegate's status is the process's by construction.
			cmd := exec.Command(path, args...)
			cmd.Env = env
			return cmd.Run()
		}

		var handed bool
		var out string
		var err error
		if entry == "entrypoint" {
			handed, out, err = handOffForTest(t, "env", "down", "dev")
		} else {
			out, err = runCheck(t, "env", "down", "dev")
			handed = true // checkForgePinSkew reports only the error
		}
		if _, statErr := os.Stat(ran); statErr != nil {
			t.Fatalf("%s: the pinned forge never ran", entry)
		}
		var exitErr *exec.ExitError
		if !handed || !errors.As(err, &exitErr) || exitcode.Of(err) != 3 {
			t.Errorf("%s: handed %v, err %v (exit %d); want the delegate's exit 3, unchanged", entry, handed, err, exitcode.Of(err))
		}
		if strings.Contains(out, "running this forge instead") || strings.Contains(out, "a different forge than the project's") {
			t.Errorf("%s: fell back to this forge after the pinned one ran:\n%s", entry, out)
		}
	}

	// exec(2) itself failing is not "the pin could not be had" either: the
	// pin was obtained, and the error is returned as it is.
	armPin(t, pinBuildable)
	execFailed := errors.New("exec /cache/forge: exec format error")
	pinDelegateExec = func(string, []string, []string) error { return execFailed }
	if _, out, err := handOffForTest(t, "env", "down", "dev"); !errors.Is(err, execFailed) || strings.Contains(out, "running this forge instead") {
		t.Errorf("exec failure: err %v, want it unchanged with no fallback\n%s", err, out)
	}
}

// A KCL render's internal child (evalKCLCommand) runs this binary on purpose
// and says nothing about the pin: the parent decided, and warned once. A user's
// own `kcl eval --allow-version-skew` still warns.
func TestPinDecision_InternalKCLChildIsSilent(t *testing.T) {
	skewedProject(t)
	t.Setenv(forgedelegate.MarkerEnv, "")
	h, _ := armPin(t, pinBuildable)
	out, err := runCheck(t, "kcl", "eval", "x.k", "--allow-version-skew", "--process-rooted")
	if err != nil || out != "" || h.path != "" {
		t.Errorf("internal child: err %v, handed to %q, output %q", err, h.path, out)
	}
	out, err = runCheck(t, "kcl", "eval", "x.k", "--allow-version-skew")
	if err != nil || !strings.Contains(out, "a different forge than the project's") {
		t.Errorf("a user's kcl eval under skew: err %v, output %q; want the warning", err, out)
	}
}

// armPin arms the hand-off with the pinned forge available as avail. For
// pinCached the build is done before the test's command runs, and every later
// build fails as it does offline.
func armPin(t *testing.T, avail pinAvailability) (*handoff, *[]string) {
	t.Helper()
	h, installs := armDelegation(t)
	switch avail {
	case pinCached:
		root, _ := os.Getwd()
		if _, err := pinDelegateResolver(io.Discard).Pinned(context.Background(), root, skewTestPin); err != nil {
			t.Fatalf("prime the cache: %v", err)
		}
		unobtainablePin(t)
		*installs = nil
	case pinUnobtainable:
		unobtainablePin(t)
	}
	return h, installs
}

// commandWords is the command path args name in TestPinDecisionTable's rows:
// up to the first flag, without the env/file positionals.
func commandWords(args []string) []string {
	var out []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") || a == "dev" || strings.HasSuffix(a, ".k") {
			break
		}
		out = append(out, a)
	}
	return out
}

func verdict(h *handoff, err error) outcome {
	switch {
	case err != nil:
		return refused
	case h.path != "":
		return handedOver
	default:
		return ranHere
	}
}
