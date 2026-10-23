package devstack

import (
	"strings"
	"testing"
)

// A LONG WORKTREE NAME MUST NOT MAKE AN ENVIRONMENT UNRENDERABLE.
//
// Incident (control-plane): `forge env render prod` failed outright from a
// worktree named `forge-deploy-882e308d` —
//
//	render deploy/kcl/prod/: main.k:720:1
//	  port-block key "prod-forge-deploy-882e308d" is not a canonical name
//	  (canonical form: "prod-forge-deploy-882e30")
//
// validateKey compared the key against Sanitize, and Sanitize does two
// separate jobs: it fixes SHAPE (DNS-label rules) and it TRUNCATES at
// maxNameLen. So a well-formed key differed from its own sanitized form purely
// by being long and was refused as "not canonical". prod's key is "prod-" plus
// the worktree name, so any worktree name over 19 characters could not render
// prod at all — and reliant's own tooling generates worktree names of exactly
// that shape and length.
//
// The second half of the defect was the MESSAGE: it blamed an empty
// interpolation and printed the `_wt = option("worktree") or ""` guard, which
// was already present and correct in the failing file. The reader was sent to
// inspect KCL that had nothing wrong with it.
//
// These tests pin both halves, plus the reason the fix accepts the key rather
// than truncating it — see TestLongKeysThatTruncateAlikeStayDistinct.

// TestAllocateBlockAcceptsLongComposedKey is the regression lock for the
// reported failure, using the exact key from the incident.
func TestAllocateBlockAcceptsLongComposedKey(t *testing.T) {
	// 21 characters — an ordinary auto-generated worktree name, and 2 past
	// the 19 the old bound allowed once "prod-" was prepended.
	const worktree = "forge-deploy-882e308d"
	key := "prod-" + worktree

	// Guard the premise. If maxNameLen ever grew enough to fit this key, the
	// test would pass without exercising the bug at all.
	if len(key) <= maxNameLen {
		t.Fatalf("test no longer exercises the bug: key %q (%d chars) now fits maxNameLen (%d)",
			key, len(key), maxNameLen)
	}

	dir := t.TempDir()
	port, err := AllocatePort(dir, 3000, key)
	if err != nil {
		t.Fatalf("AllocatePort rejected the well-formed key %q: %v\n"+
			"a long worktree name must not make an environment unrenderable", key, err)
	}
	// First named key in an empty registry ⇒ block 1 ⇒ base + 100.
	if port != 3100 {
		t.Errorf("port = %d, want 3100 (block 1)", port)
	}

	// The key must be memoized VERBATIM. A key repaired on write — truncated
	// or hash-suffixed — would no longer be the string the KCL composed, so
	// the registry would not map back to the expression that produced it.
	list, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].Key != key {
		t.Fatalf("registry stored %v, want the key exactly as supplied (%q)", list, key)
	}

	// Second entry point, its own registry read/write path. A guard fixed on
	// AllocateBlock alone would leave this one still refusing.
	if _, err := AllocatePortAvoidingForeign(t.TempDir(), 8080, key, nil); err != nil {
		t.Errorf("AllocatePortAvoidingForeign rejected the well-formed key %q: %v", key, err)
	}
}

// TestLongKeysThatTruncateAlikeStayDistinct is the reason the fix accepts the
// long key rather than truncating it to the 24-character form the old error
// message computed and printed.
//
// Truncation is not injective. Two prod stacks whose worktree names share a
// long prefix collapse onto ONE key under a 24-character cut, and one key
// means one port block — two stacks silently rendering the same host ports,
// with no error anywhere. That is worse than the refusal it would replace,
// because it converts a loud failure into a silent one.
func TestLongKeysThatTruncateAlikeStayDistinct(t *testing.T) {
	a := "prod-implement-billing-webhooks-a"
	b := "prod-implement-billing-webhooks-b"

	// Precondition: these really do collide under the fact-length cut, so a
	// regression to truncate-then-allocate would actually be caught here.
	if Sanitize(a) != Sanitize(b) {
		t.Fatalf("test premise broken: %q and %q no longer truncate alike (%q vs %q)",
			a, b, Sanitize(a), Sanitize(b))
	}

	dir := t.TempDir()
	portA, err := AllocatePort(dir, 3000, a)
	if err != nil {
		t.Fatalf("AllocatePort(%q): %v", a, err)
	}
	portB, err := AllocatePort(dir, 3000, b)
	if err != nil {
		t.Fatalf("AllocatePort(%q): %v", b, err)
	}
	if portA == portB {
		t.Errorf("two distinct keys collapsed onto one port block (%d): %q and %q\n"+
			"truncating a key before allocating merges stacks instead of separating them",
			portA, a, b)
	}
}

// TestOverlongKeyReportsLengthNotShape covers the half of the defect that
// wasted the most time: the error diagnosed the WRONG BUG. A genuinely
// pathological key must still be refused — but in its own words, so the reader
// is not sent to inspect an interpolation guard that is already correct.
func TestOverlongKeyReportsLengthNotShape(t *testing.T) {
	key := "prod-" + strings.Repeat("a", maxKeyLen)

	_, err := AllocatePort(t.TempDir(), 3000, key)
	if err == nil {
		t.Fatalf("AllocatePort accepted a %d-character key", len(key))
	}
	msg := err.Error()

	// It must name the real cause...
	if !strings.Contains(msg, "exceeds") || !strings.Contains(msg, "LENGTH") {
		t.Errorf("error does not identify the cause as length: %v", msg)
	}
	// ...and must NOT recycle the empty-interpolation runbook, which points at
	// a different bug with an unrelated fix.
	if strings.Contains(msg, "EMPTY INTERPOLATION") {
		t.Errorf("a length failure was reported as an empty interpolation, "+
			"sending the reader to inspect correct KCL: %v", msg)
	}
}

// TestShapeGuardStillFiresOnLongMalformedKeys is the CONTROL for this whole
// change: it is what stops "accept the long key" from degrading into "accept
// anything".
//
// Each key below is malformed AND longer than the old 24-character bound, so
// it can only be refused by the shape rule — the length rule that used to
// catch them incidentally no longer fires at these lengths. A trailing-dash
// fragment is still a fragment at any length; that is the original "prod-"
// incident's guard, checked at a length the old code could never reach.
func TestShapeGuardStillFiresOnLongMalformedKeys(t *testing.T) {
	for _, key := range []string{
		"prod-forge-deploy-882e308d-", // trailing dash: empty interpolation
		"prod-forge_deploy_882e308d",  // underscores
		"Prod-Forge-Deploy-882e308d",  // capitals
		"prod--forge-deploy-882e308d", // collapsed dash run
		"prod-forge-deploy 882e308d",  // embedded space
	} {
		if len(key) <= maxNameLen {
			t.Fatalf("test premise broken: %q (%d chars) is within the old bound, "+
				"so it would be caught by length rather than shape", key, len(key))
		}
		if _, err := AllocatePort(t.TempDir(), 3000, key); err == nil {
			t.Errorf("AllocatePort accepted the malformed key %q", key)
		}
		// A refused key must also never be persisted — that was the actual
		// mechanism of the original incident, where a memoized fragment
		// rendered junk names into a tracked config file.
		if list, _ := List(t.TempDir()); len(list) != 0 {
			t.Errorf("a rejected key was persisted anyway: %v", list)
		}
	}
}

// TestDevStackKeyIsBoundedByTheFactBudget records WHY relaxing the key bound
// does not put dev at risk, which was an open question when this was filed.
//
// dev's KCL keys on option("worktree") directly, and that value is Sanitize's
// own output — already ≤ maxNameLen and idempotent under the shape check. So
// dev cannot trip either branch of validateKey no matter how long the
// directory name is. Only a COMPOSED key ("prod-" + fact) can exceed the fact
// budget, which is exactly the case that must not be bounded by it.
func TestDevStackKeyIsBoundedByTheFactBudget(t *testing.T) {
	// A directory name far past the budget, of the shape reliant generates.
	raw := "forge-deploy-" + strings.Repeat("x", 60) + "-882e308d"

	key := Sanitize(raw) // what Worktree() hands to KCL as option("worktree")
	if len(key) > maxNameLen {
		t.Fatalf("Sanitize did not bound the git fact: %q is %d chars", key, len(key))
	}
	if canonicalLabel(key) != key {
		t.Errorf("a sanitized fact is not stable under the shape check: %q -> %q",
			key, canonicalLabel(key))
	}
	if _, err := AllocatePort(t.TempDir(), 28080, key); err != nil {
		t.Errorf("dev-style key %q rejected: %v", key, err)
	}
}
