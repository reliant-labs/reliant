package devstack

import (
	"strings"
	"testing"
)

// An empty KCL interpolation must not reach the block registry.
//
// Incident (control-plane, cutting v0.1.9): prod's KCL composes its port key
// as `"prod-" + (option("worktree") or "")`, meaning "prod-<worktree>". On the
// primary checkout the interpolation is empty, so the key forge was handed was
// the literal "prod-" — a trailing-dash fragment, not a name. forge accepted
// it and memoized it, and `.forge/blocks.json` gained a "prod-" entry that
// looked exactly like a registered stack.
//
// Nothing surfaced there. It surfaced two steps later, in a file under version
// control: a per-stack config generator enumerating the registry rendered an
// account named CP_prod_ with `user: "control-plane-prod-"` and
// `password: "control-plane-dev-nats-prod-"` into a TRACKED nats.conf, so
// `forge generate` produced different bytes on a machine that had once run
// `forge env up prod` than on one that had not.
//
// The empty-interpolation signature is visible right here, at the API
// boundary, and nowhere downstream: by the time a generator reads the registry
// the fragment is indistinguishable from a deliberate name. Every key forge
// itself supplies (Worktree, Branch) is already a canonical DNS-safe label, so
// a non-canonical key can only have come from a KCL expression that composed
// one — and rejecting it names the file and the fix while a silent repair
// would move an already-issued port.

// TestAllocateBlockRejectsEmptyInterpolationKey is the regression lock: the
// malformed key is refused, and — the part that actually caused the incident —
// it never lands in the registry that generators enumerate.
func TestAllocateBlockRejectsEmptyInterpolationKey(t *testing.T) {
	dir := t.TempDir()

	_, err := AllocatePort(dir, 3000, "prod-")
	if err == nil {
		t.Fatal("AllocatePort accepted the key \"prod-\"; an empty interpolation must fail loudly, " +
			"not be memoized as if it were a stack name")
	}
	// The error is a runbook: it has to name the key that arrived and the
	// canonical form the author meant, or the KCL expression that produced it
	// is not findable from the message.
	if !strings.Contains(err.Error(), `"prod-"`) || !strings.Contains(err.Error(), `"prod"`) {
		t.Errorf("error does not name both the malformed key and its canonical form: %v", err)
	}

	// The registry is what a per-stack config generator reads. A rejected key
	// that still got persisted would render the junk account anyway.
	if list, _ := List(dir); len(list) != 0 {
		t.Errorf("a rejected key was persisted anyway: %v", list)
	}
}

// TestAllocatePortAvoidingForeignRejectsEmptyInterpolationKey covers the
// second entry point. It has its own registry read/write path, so a guard on
// AllocateBlock alone would leave this one open.
func TestAllocatePortAvoidingForeignRejectsEmptyInterpolationKey(t *testing.T) {
	dir := t.TempDir()

	if _, err := AllocatePortAvoidingForeign(dir, 8080, "prod-", nil); err == nil {
		t.Fatal("AllocatePortAvoidingForeign accepted the key \"prod-\"")
	}
	if list, _ := List(dir); len(list) != 0 {
		t.Errorf("a rejected key was persisted anyway: %v", list)
	}
}

// TestAllocateBlockAcceptsCanonicalKeys pins that the guard rejects only what
// is actually malformed. The default stack and every key forge derives from
// git must keep working untouched — this guard has no business moving a port.
func TestAllocateBlockAcceptsCanonicalKeys(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates against a real registry, resolving the worktree via git each time; runs in task test")
	}
	for _, key := range []string{"", "wt-a", "prod", "prod-wt-a", "feature-123"} {
		dir := t.TempDir()
		if _, err := AllocatePort(dir, 3000, key); err != nil {
			t.Errorf("AllocatePort rejected the canonical key %q: %v", key, err)
		}
		if _, err := AllocatePortAvoidingForeign(dir, 3000, key, nil); err != nil {
			t.Errorf("AllocatePortAvoidingForeign rejected the canonical key %q: %v", key, err)
		}
	}
}

// TestAllocateBlockRejectsOtherMalformedKeys spells out the shapes the rule
// covers. Each becomes a NATS account name, a k8s namespace suffix or a DB
// name in the generators that read this registry, so a key that is not a
// DNS-safe label is a latent failure wherever it lands.
func TestAllocateBlockRejectsOtherMalformedKeys(t *testing.T) {
	for _, key := range []string{"-prod", "prod--web", "Prod", "prod_web", "prod web", "-", "  "} {
		dir := t.TempDir()
		if _, err := AllocatePort(dir, 3000, key); err == nil {
			t.Errorf("AllocatePort accepted the malformed key %q", key)
		}
	}
}
