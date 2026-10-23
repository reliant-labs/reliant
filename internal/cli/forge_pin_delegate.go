package cli

// Run the forge the project pins.
//
// forge_pin_skew.go decides that the forge in hand is not the project's. This
// file is what happens next: instead of stopping with a runbook, hand the very
// same command to the pinned forge. The project declares which forge it is
// (forge.yaml's forge_version, or a go.mod/go.work link to a checkout), the
// declaration is enough to act on, and making every agent type
// `go run …@<pin>` by hand was a wall with a door a program could open.
//
// WHICH forge, in order:
//   - a project linked to a local forge checkout (go.work use, directory
//     replace) compiles against that checkout, so the forge it runs is a build
//     of that checkout — when this binary was not built from it;
//   - otherwise the forge_version pin: the project's .tools/bin/forge when it
//     is that version, else a build of the pinned module version kept in a
//     content-addressed cache.
//
// The hand-off is an exec: same arguments, same environment (plus
// FORGE_VERSION_DELEGATED, which stops the pinned forge handing off again),
// stdio and exit status. One line on stderr says it happened.
//
// It is skipped, and the old warn/refuse behaviour applies, when
// --allow-version-skew or FORGE_ALLOW_VERSION_SKEW is set (the explicit "run
// THIS forge"), when this forge is already a delegate, and when the pinned
// forge cannot be had (offline, an unpublished version, a checkout that does
// not build): the refusal then carries the reason.

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/internal/forgecompat"
	"github.com/reliant-labs/forge/internal/forgedelegate"
)

// pinDelegateOnlyExcluded are the `env` subcommands that read no KCL.
var pinDelegateOnlyExcluded = map[string]bool{
	"env list": true, "env ps": true, "env down": true, "env devstack": true,
	"env delete": true, "env start": true, "env stop": true,
}

// pinDelegateJudges are the commands that JUDGE the project — the local CI
// gate and its checks, and the linters — whose verdict is a function of the
// forge that ran it: the analyzers, the gate's step list and tool pins, and
// the forge.yaml schema they validate first are all embedded in the binary.
// An older forge failed reliant's `forge ci gate --list` outright
// (`unknown key "lint.tracing"`) and would otherwise judge with rules the
// project did not pin.
var pinDelegateJudges = map[string]bool{
	"lint": true, "ci gate": true, "ci verify-generated": true, "ci validate-kcl": true,
	"ci migration-safety": true, "ci vuln-scan": true,
}

// pinDelegateOnlyFor reports whether rel is a command that is not pin-gated
// (it refuses nothing) but whose output is a function of the forge that ran
// it: it renders the project's KCL, and forge's KCL schemas are embedded in
// the binary (an older forge evaluates a newer project's KCL against schemas
// missing fields the project is entitled to, and KCL blames the project's
// line: `Cannot add member 'release_dependencies' to schema ...`), or it is
// one of pinDelegateJudges. These are handed to the pinned forge instead of
// failing, or judging, that way.
func pinDelegateOnlyFor(rel string) bool {
	switch {
	case rel == "kcl" || strings.HasPrefix(rel, "kcl "), rel == "doctor", rel == "project frontends", pinDelegateJudges[rel]:
		return true
	case strings.HasPrefix(rel, "env "):
		sub := strings.Join(strings.Fields(rel)[:2], " ")
		return !pinDelegateOnlyExcluded[sub]
	}
	return false
}

// Seams: the real hand-off builds forge and replaces the process, which no
// unit test may do (main_test.go installs hermetic defaults).
var (
	pinDelegateResolver = func(w io.Writer) *forgedelegate.Resolver {
		return &forgedelegate.Resolver{
			Log:     w,
			Version: forgedelegate.RunVersion,
			Same: func(want, got string) bool {
				skewed, ok := pinSkew(want, got, "")
				return ok && !skewed
			},
		}
	}
	pinDelegateExec = forgedelegate.Exec
)

// pinDelegationDisabled is set by main_test.go's TestMain: a real hand-off
// builds forge and replaces the process, neither of which a unit test may do.
// The tests of the hand-off itself clear it and swap the two seams above.
var pinDelegationDisabled bool

// delegateTarget is what the project says its forge is.
type delegateTarget struct {
	// pin is forge.yaml's forge_version (the target when checkout is "").
	pin string
	// binary is the running forge's version, for the notice.
	binary string
	// checkout, when set, is the local forge checkout the project is linked
	// to, and stamp identifies its current contents.
	checkout, stamp string
	// devTag: the running forge is a development build and the pin a
	// published tag (see pinSkewFor).
	devTag bool
}

// devAgainstTag reports whether the pairing is one the rewriting commands
// tolerate (forge's own e2e lanes and every forge developer): a binary built
// from the checkout, a project on the published floor. Only the commands
// that produce artifacts are strict about it.
func (t delegateTarget) devAgainstTag(rel string) bool {
	return t.devTag && !pinBuildCommands[rel]
}

func (t delegateTarget) String() string {
	if t.checkout != "" {
		return "the checkout " + t.checkout + " this project is linked to"
	}
	return "forge_version " + t.pin
}

// delegateTargetFor reports the forge projectRoot wants and whether this one
// is not it. ok is false when nothing is to be done: no project, no skew, or
// a comparison that cannot be made.
func delegateTargetFor(projectRoot string) (t delegateTarget, ok bool) {
	if rep := forgecompat.InspectBridge(projectRoot); rep.Bridged && rep.CheckoutOK {
		if !rep.Skewed {
			return delegateTarget{}, false
		}
		return delegateTarget{
			binary:   rep.Binary.Name,
			checkout: rep.Bridge.Dir,
			// Keyed on source edits only: an untracked report growing in the
			// checkout's root must not rebuild forge on every command.
			stamp: rep.Checkout.Head + "+" + strconv.FormatBool(rep.Checkout.SourceEdits) + "@" + strconv.FormatInt(rep.Checkout.LastSourceEditAt.UnixNano(), 10),
		}, true
	}
	pin, binary, skewed, devTag := pinSkewFor(projectRoot)
	if !skewed {
		return delegateTarget{}, false
	}
	return delegateTarget{pin: pin, binary: binary, devTag: devTag}, true
}

// delegateToPinned hands the running command to the forge projectRoot wants.
// With the real Exec it returns only on failure; handed is true when the
// command was handed over and nothing more is to be done here (a test seam
// returns instead of replacing the process). args are the forge arguments of
// this invocation.
func delegateToPinned(ctx context.Context, w io.Writer, projectRoot string, t delegateTarget, args []string) (handed bool, err error) {
	r := pinDelegateResolver(w)
	var res forgedelegate.Result
	if t.checkout != "" {
		res, err = r.Bridge(ctx, t.checkout, t.stamp)
	} else {
		res, err = r.Pinned(ctx, projectRoot, t.pin)
	}
	if err != nil {
		return false, err
	}
	_, _ = fmt.Fprintf(w, "forge: this forge is %s, but the project wants %s — running %s instead (%s, or %s=1, to run this one)\n",
		t.binary, t, res.Source, "--"+allowVersionSkewFlag, allowVersionSkewEnv)
	if err := pinDelegateExec(res.Path, args, forgedelegate.Env()); err != nil {
		return false, err
	}
	return true, nil
}

// pinnedRunLine is the command that runs args under the forge the project
// pins, for a message to hand back. The project's own .tools/bin/forge is
// named when it reports the pin (nothing to download or build); otherwise the
// `go run` form, which fetches and builds the pin into Go's cache.
func pinnedRunLine(projectRoot, pin string, args []string) string {
	if local, ok := pinDelegateResolver(io.Discard).LocalPinned(context.Background(), projectRoot, pin); ok {
		if abs, err := filepath.Abs(local); err == nil {
			local = abs
		}
		return forgecompat.RunBinaryCommand(local, args)
	}
	return forgecompat.RunPinnedCommand(pin, args)
}

// delegationWanted reports whether the command at rel should be handed over:
// every pin-gated command that writes, and the render readers. A read-only
// run of a gated command (a plan, a --check) is left to warn, as before.
func delegationWanted(rel string, readOnly bool) bool {
	if _, gated := pinGateFor(rel); gated {
		return !readOnly
	}
	return pinDelegateOnlyFor(rel)
}

// delegationFailure formats why delegation did not happen, for the refusal or
// the warning that follows it.
func delegationFailure(err error) string {
	if err == nil {
		return ""
	}
	return "  The pinned forge could not be used, so nothing was handed over: " + strings.TrimSpace(err.Error()) + "\n"
}
