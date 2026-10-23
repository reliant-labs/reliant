package cli

// FORGE_LEDGER — the one override of declarative ledger selection.
//
// Selection is declarative (ledgerForEntities): an env whose KCL declares
// `forge.ControlPlane` records there. That is right for a person deploying,
// and wrong for a TEST that renders or imports a real env's history: the
// declaration names a live control plane, so a hermetic check of prod's
// render reached prod's ledger. control-plane's scripts/test-kata-prepull.sh
// did exactly that — its `ledger import --apply` was refused by prod's
// hosted ledger only because the plan happened to conflict.
//
// WHY NOT FORGE_LEDGER_HOME. That script set it, believing it kept the run
// private, and two agents reported the same surprise. But FORGE_LEDGER_HOME
// is a LOCATION — where this machine's ledger lives (ledgerfile.Home) — and
// people set it for reasons unrelated to selection: ledgerfile's own refusal
// of a network filesystem tells them to. If a location also chose the
// backend, a user who followed that advice would have every hosted env read
// "never promoted" from an empty machine ledger and ship mutable tags, which
// is the failure ledgerFor exists to prevent. So location and selection are
// two variables, and each means one thing:
//
//	FORGE_LEDGER_HOME=<dir>   where this machine's ledger is
//	FORGE_LEDGER=machine      use this machine's ledger, whatever the env declares
//
// WHY AN ENV VAR AND NOT A FLAG. A ledger is reached by a dozen verbs (render,
// deploy, build, status, import, where, …), and a hermetic script runs several
// of them; forge also runs forge. A flag would have to be repeated on every
// invocation, and the one that forgot it would reach the declared control
// plane — the bug this closes. A variable is set once and inherited by every
// process the script and forge start.
//
// WHY NO URL VALUE. The endpoint is part of the env's DECLARATION
// (cloud.ResolveEndpoint) and its credential is resolved for that endpoint. A
// machine-local variable that redirected a ledger to an arbitrary URL would
// carry the declared env's token there. Pointing an env at another control
// plane is a KCL edit, reviewed like any other.

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// ledgerSelectionEnv names the variable that overrides ledger selection.
const ledgerSelectionEnv = "FORGE_LEDGER"

// ledgerSelection is FORGE_LEDGER's closed set of values.
type ledgerSelection string

const (
	// ledgerSelectDeclared is the default: the env's KCL decides.
	ledgerSelectDeclared ledgerSelection = "declared"
	// ledgerSelectMachine forces this machine's ledger (under
	// $FORGE_LEDGER_HOME) for every env, including one that declares a
	// control plane. Nothing is read from or written to that control plane.
	ledgerSelectMachine ledgerSelection = "machine"
)

// ledgerSelectionFromEnv reads FORGE_LEDGER. Unset or empty is the default.
//
// Any other value is an ERROR, never a fallback to the default. The person
// who sets this variable is asking to stay OFF a control plane, so reading a
// typo (`FORGE_LEDGER=local`) as "declared" would send exactly that run to
// the control plane it asked to avoid.
func ledgerSelectionFromEnv() (ledgerSelection, error) {
	switch raw := strings.TrimSpace(os.Getenv(ledgerSelectionEnv)); ledgerSelection(raw) {
	case "", ledgerSelectDeclared:
		return ledgerSelectDeclared, nil
	case ledgerSelectMachine:
		return ledgerSelectMachine, nil
	default:
		return "", fmt.Errorf("%s=%q is not a ledger selection.\n"+
			"  %s=%s   use this machine's ledger (under $FORGE_LEDGER_HOME) for every env, whatever it declares\n"+
			"  %s=%s  the default: an env declaring forge.ControlPlane records there\n"+
			"  An unrecognised value is refused rather than read as the default, because the default reaches a control plane",
			ledgerSelectionEnv, raw,
			ledgerSelectionEnv, ledgerSelectMachine,
			ledgerSelectionEnv, ledgerSelectDeclared)
	}
}

// ledgerOverrideNotices is where the override notice goes. A seam so a test
// can read it; os.Stderr otherwise, so it never mixes into a --json stdout.
var ledgerOverrideNotices io.Writer = os.Stderr

// ledgerOverrideNoticed remembers which envs this process already announced.
// Selection runs several times per command, and one line per env is the
// signal; a line per selection would be noise that teaches people to skip it.
var ledgerOverrideNoticed sync.Map

// noteLedgerOverride announces, once per env per process, that FORGE_LEDGER
// displaced a control plane the env declares.
//
// It is printed because the override is invisible otherwise, and a variable
// left exported in a shell outlives the test it was set for: the next
// `forge env deploy prod` in that shell would record on this machine, and
// nothing would say so.
func noteLedgerOverride(env, declaredEndpoint, location string) {
	if _, seen := ledgerOverrideNoticed.LoadOrStore(env+"\x00"+declaredEndpoint, true); seen {
		return
	}
	fmt.Fprintf(ledgerOverrideNotices,
		"%s=%s: env %s declares the control plane at %s, but this run records in this machine's ledger (%s); nothing is read from or written to that control plane.\n",
		ledgerSelectionEnv, ledgerSelectMachine, env, declaredEndpoint, location)
}
