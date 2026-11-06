package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/forgedelegate"
)

type handoff struct {
	path string
	args []string
	env  []string
}

// armDelegation turns the hand-off on for one test and records what it would
// exec instead of exec'ing. The resolver builds nothing: its Install seam
// writes a stand-in binary.
func armDelegation(t *testing.T) (*handoff, *[]string) {
	t.Helper()
	var got handoff
	var installs []string
	prevDisabled, prevResolver, prevExec := pinDelegationDisabled, pinDelegateResolver, pinDelegateExec
	t.Cleanup(func() {
		pinDelegationDisabled, pinDelegateResolver, pinDelegateExec = prevDisabled, prevResolver, prevExec
	})
	pinDelegationDisabled = false
	t.Setenv(forgedelegate.CacheDirEnv, t.TempDir())
	pinDelegateResolver = func(w io.Writer) *forgedelegate.Resolver {
		return &forgedelegate.Resolver{
			Log: w,
			Install: func(_ context.Context, pkg, gobin string) error {
				installs = append(installs, pkg)
				return atomicfile.WriteFile(filepath.Join(gobin, forgeExeName()), []byte("#!/bin/sh\n"), 0o755)
			},
		}
	}
	pinDelegateExec = func(path string, args, env []string) error {
		got = handoff{path, args, env}
		return nil
	}
	return &got, &installs
}

// forgeExeName is the file `go install` writes for cmd/forge on this OS: the
// stand-in has to land where the resolver looks for the real build.
func forgeExeName() string {
	if runtime.GOOS == "windows" {
		return "forge.exe"
	}
	return "forge"
}

func runCheck(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := newTestRootCmd(t)
	cmd, rest, err := root.Find(args)
	if err != nil {
		t.Fatalf("find %v: %v", args, err)
	}
	if err := cmd.ParseFlags(rest); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	allow, _ := cmd.Flags().GetBool(allowVersionSkewFlag)
	var out strings.Builder
	prev := os.Args
	os.Args = append([]string{"forge"}, args...)
	defer func() { os.Args = prev }()
	err = checkForgePinSkew(cmd, root, &out, allow)
	return out.String(), err
}

// reliant, 2026-10-24: `forge lint` under a skewed forge printed "running
// <pin> instead (--allow-version-skew, or FORGE_ALLOW_VERSION_SKEW=1, to run
// this one)", and `forge lint --allow-version-skew` then failed "unknown flag".
// Every command the hand-off can take accepts the flag its notice names.
func TestDelegatedCommandsAcceptTheSkewFlag(t *testing.T) {
	root := newTestRootCmd(t)
	for _, args := range [][]string{{"lint"}, {"ci", "gate"}, {"doctor"}, {"generate"}} {
		cmd, _, err := root.Find(args)
		if err != nil {
			t.Fatalf("find %v: %v", args, err)
		}
		if err := cmd.ParseFlags([]string{"--" + allowVersionSkewFlag}); err != nil {
			t.Errorf("forge %s --%s: %v", strings.Join(args, " "), allowVersionSkewFlag, err)
		}
	}
	if cmd, _, err := root.Find([]string{"version"}); err == nil && cmd.Flags().Lookup(allowVersionSkewFlag) != nil {
		t.Errorf("forge version registers --%s, which means nothing there", allowVersionSkewFlag)
	}
}

// The wall becomes a door: a stale forge running a rewriting command hands it
// to the pinned forge, with the same arguments, and says so on one line.
func TestDelegation_RewritingCommandRunsUnderThePinnedForge(t *testing.T) {
	skewedProject(t)
	t.Setenv(forgedelegate.MarkerEnv, "")
	h, installs := armDelegation(t)

	out, err := runCheck(t, "generate", "--no-cache")
	if err != nil {
		t.Fatalf("delegated command was refused: %v", err)
	}
	if want := "github.com/reliant-labs/forge/cmd/forge@" + skewTestPin; len(*installs) != 1 || (*installs)[0] != want {
		t.Fatalf("built %v, want [%s]", *installs, want)
	}
	if h.path == "" {
		t.Fatal("the command was not handed to the pinned forge")
	}
	if strings.Join(h.args, " ") != "generate --no-cache" {
		t.Errorf("args %v, want the original invocation", h.args)
	}
	hasMarker := false
	for _, kv := range h.env {
		hasMarker = hasMarker || kv == forgedelegate.MarkerEntry(os.Getpid(), "forge_version "+skewTestPin)
	}
	if !hasMarker {
		t.Errorf("the delegated forge is not told it is one: %v", h.env)
	}
	if n := strings.Count(out, "running "); n != 1 || !strings.Contains(out, skewTestPin) || !strings.Contains(out, "--allow-version-skew") {
		t.Errorf("want exactly one notice naming the pin and the escape hatch:\n%s", out)
	}
}

// KCL-reading commands are not pin-gated, but an older forge fails them with
// schema errors that blame the project: they are handed over too.
func TestDelegation_RenderCommandsAreHandedOver(t *testing.T) {
	skewedProject(t)
	t.Setenv(forgedelegate.MarkerEnv, "")
	// lint and the gate judge the project with the binary's own analyzers,
	// step list and forge.yaml schema (pinDelegateJudges).
	for _, args := range [][]string{{"env", "render", "dev"}, {"env", "shape", "dev"}, {"kcl", "eval", "x.k"}, {"scaffold", "service", "billing"}, {"component", "install", "button"}, {"lint"}, {"ci", "gate", "--list"}} {
		h, _ := armDelegation(t)
		if _, err := runCheck(t, args...); err != nil {
			t.Errorf("forge %v: %v", args, err)
		}
		if h.path == "" {
			t.Errorf("forge %v was not handed over", args)
		}
	}
}

// Reads are handed over too: a plan, an audit and a skill are functions of the
// binary, and the wrong forge's answer looks right. Only the commands about
// this forge or this machine stay here.
func TestDelegation_ReadsAreHandedOverAndExemptCommandsStayLocal(t *testing.T) {
	skewedProject(t)
	t.Setenv(forgedelegate.MarkerEnv, "")
	for _, args := range [][]string{{"generate", "--dry-run"}, {"generate", "--check"}, {"scaffold", "--dry-run"}, {"env", "list"}, {"project", "audit"}, {"skill", "list"}} {
		h, _ := armDelegation(t)
		if _, err := runCheck(t, args...); err != nil {
			t.Errorf("forge %v: %v", args, err)
		}
		if h.path == "" {
			t.Errorf("forge %v ran under a forge the project does not pin", args)
		}
	}
	for _, args := range [][]string{{"version"}, {"env", "ps"}, {"env", "down"}, {"project", "upgrade"}, {"report-bug"}, {"slot", "status"}} {
		h, installs := armDelegation(t)
		_, _ = runCheck(t, args...)
		if h.path != "" || len(*installs) != 0 {
			t.Errorf("forge %v was handed over (built %v)", args, *installs)
		}
	}
}

// Every way of saying "run THIS forge" keeps the old behaviour, and a forge
// that was itself delegated to never hands off again.
func TestDelegation_EscapeHatchesAndNoLoop(t *testing.T) {
	skewedProject(t)
	t.Run("flag", func(t *testing.T) {
		t.Setenv(forgedelegate.MarkerEnv, "")
		h, _ := armDelegation(t)
		if _, err := runCheck(t, "generate", "--allow-version-skew"); err != nil || h.path != "" {
			t.Errorf("--allow-version-skew: err %v, handed over %q", err, h.path)
		}
	})
	t.Run("env", func(t *testing.T) {
		t.Setenv(forgedelegate.MarkerEnv, "")
		t.Setenv(allowVersionSkewEnv, "1")
		h, _ := armDelegation(t)
		if _, err := runCheck(t, "generate"); err != nil || h.path != "" {
			t.Errorf("%s=1: err %v, handed over %q", allowVersionSkewEnv, err, h.path)
		}
	})
	t.Run("already delegated", func(t *testing.T) {
		actAsDelegate(t)
		h, _ := armDelegation(t)
		if out, err := runCheck(t, "generate"); err != nil || h.path != "" || out != "" {
			t.Errorf("a delegated forge handed off again or complained: err %v, %q, %q", err, h.path, out)
		}
	})
	t.Run("matching pin", func(t *testing.T) {
		t.Setenv(forgedelegate.MarkerEnv, "")
		buildinfo.Set(skewTestPin, "", "377b61532c0e")
		h, _ := armDelegation(t)
		if _, err := runCheck(t, "generate"); err != nil || h.path != "" {
			t.Errorf("the pinned forge handed off: err %v, %q", err, h.path)
		}
	})
}

// Offline, or a pin nobody published: the build fails, and so does the
// command, with the reason — reads as well as writes. Falling back to this
// forge is the silent rewrite the hand-off exists to prevent.
func TestDelegation_UnavailablePinFailsAndRunsNothing(t *testing.T) {
	skewedProject(t)
	t.Setenv(forgedelegate.MarkerEnv, "")
	for _, args := range [][]string{{"generate"}, {"skill", "list"}, {"project", "shapes"}} {
		h, _ := armDelegation(t)
		unobtainablePin(t)
		_, err := runCheck(t, args...)
		if err == nil {
			t.Fatalf("forge %v ran although the pinned forge could not be had", args)
		}
		for _, want := range []string{"no such host", skewTestPin, "Nothing ran", "--allow-version-skew"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("forge %v: the failure does not mention %q:\n%v", args, want, err)
			}
		}
		if h.path != "" {
			t.Error("handed over a forge that was never built")
		}
	}
}

// unobtainablePin makes every build of a pinned forge fail as it does offline.
func unobtainablePin(t *testing.T) {
	t.Helper()
	pinDelegateResolver = func(w io.Writer) *forgedelegate.Resolver {
		return &forgedelegate.Resolver{
			Log: w,
			Install: func(_ context.Context, _, _ string) error {
				return errors.New("dial tcp: lookup proxy.golang.org: no such host")
			},
		}
	}
}

// A project linked to a forge checkout runs a build of that checkout, not the
// version in forge.yaml.
func TestDelegationTargetFor_PrefersLinkedCheckout(t *testing.T) {
	skewedProject(t)
	root, _ := os.Getwd()
	target, ok := delegateTargetFor(root)
	if !ok || target.checkout != "" || target.pin != skewTestPin {
		t.Fatalf("unlinked project: %+v ok=%v, want the forge_version pin", target, ok)
	}
	if got := target.String(); !strings.Contains(got, skewTestPin) {
		t.Errorf("target does not name the pin: %q", got)
	}
}

func TestHandOffWanted(t *testing.T) {
	for rel, want := range map[string]bool{
		"env render": true, "env shape": true, "env config": true, "env diff": true, "env up": true, "env list": true,
		"kcl eval": true, "doctor": true, "project frontends": true, "project audit": true, "project shapes": true,
		"lint": true, "ci gate": true, "ci summarize": true, "generate": true, "skill load": true, "mcp": false,
		"a-command-this-forge-lacks": true, "project a-subcommand-this-forge-lacks": true,
		"": false, "version": false, "help": false, "help generate": false, "completion bash": false, "__complete": false,
		"env down": false, "env ps": false, "env devstack": false,
		"project upgrade": false, "project new": false, "report-bug": false,
		"storage gc": false, "slot run": false,
	} {
		if got := handOffWanted(rel); got != want {
			t.Errorf("handOffWanted(%q) = %v, want %v", rel, got, want)
		}
	}
}

// A forge.yaml this binary would REJECT is still handed to the forge it pins:
// that forge is the one that judges the file. The case that bit: a site's
// forge.yaml has no module_path (optional only in newer forges), and an older
// `reliant forge` stopped at "'module_path' is required" without ever reading
// the pin. Simulated here with a key this binary does not know.
func TestDelegation_AForgeYAMLThisBinaryRejectsIsStillHandedOver(t *testing.T) {
	skewedProject(t)
	writeForgeYAML(t, ".", "name: demo\nforge_version: "+skewTestPin+"\nkey_from_a_newer_forge: true\n")
	t.Setenv(forgedelegate.MarkerEnv, "")
	h, _ := armDelegation(t)

	if _, err := runCheck(t, "env", "render", "prod"); err != nil {
		t.Fatalf("delegation was refused: %v", err)
	}
	if h.path == "" {
		t.Fatal("a forge.yaml this binary cannot validate was not handed to the pinned forge")
	}
}

// A pinned forge older than #1231 trims the Go build cache down to now-20m and
// removes the executable of a running `go run`; two of those (ee3bded4,
// f7e6289e) are still installed on shared machines. The storage commands are
// the current forge's own: a project that pins an old forge must never get a
// trim handed to it, whether it is `storage gocache`, `storage gc` or the
// detached `storage auto-gc`.
func TestDelegation_StorageCommandsAreNeverHandedToAPinnedForge(t *testing.T) {
	skewedProject(t)
	t.Setenv(forgedelegate.MarkerEnv, "")
	h, installs := armDelegation(t)

	root := newTestRootCmd(t)
	storageCmd, _, err := root.Find([]string{"storage"})
	if err != nil || storageCmd == nil {
		t.Fatalf("find storage: %v", err)
	}
	var paths [][]string
	var walk func(c *cobra.Command, prefix []string)
	walk = func(c *cobra.Command, prefix []string) {
		for _, sub := range c.Commands() {
			p := append(append([]string{}, prefix...), sub.Name())
			paths = append(paths, p)
			walk(sub, p)
		}
	}
	walk(storageCmd, []string{"storage"})
	for _, want := range []string{"storage gocache", "storage gc", "storage auto-gc"} {
		found := false
		for _, p := range paths {
			found = found || strings.Join(p, " ") == want
		}
		if !found {
			t.Fatalf("no %q command under storage: %v", want, paths)
		}
	}

	for _, p := range paths {
		rel := strings.Join(p, " ")
		if handOffWanted(rel) {
			t.Errorf("handOffWanted(%q): a storage command would be handed to the pinned forge", rel)
		}
		*h = handoff{}
		*installs = nil
		if _, err := runCheck(t, p...); err != nil {
			t.Errorf("%s: %v", rel, err)
		}
		if h.path != "" || len(*installs) != 0 {
			t.Errorf("%s was handed to a pinned forge (%q) or built one (%v)", rel, h.path, *installs)
		}
	}
}

// The exclusion holds even if a later edit lists storage among the pin-gated
// groups: the hand-off does not consult the gate.
func TestDelegation_StorageIsExcludedEvenWhenListedAsPinGated(t *testing.T) {
	prev := pinGatedGroups
	t.Cleanup(func() { pinGatedGroups = prev })
	pinGatedGroups = map[string][]string{"storage": nil}
	for _, rel := range []string{"storage", "storage gc", "storage gocache", "storage auto-gc"} {
		if handOffWanted(rel) {
			t.Errorf("handOffWanted(%q) = true with storage in pinGatedGroups", rel)
		}
	}
	if !handOffWanted("generate") {
		t.Error("the exclusion swallowed generate")
	}
}
