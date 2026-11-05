package devstack

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The block registry serves two purposes that look identical on disk:
//
//  1. DEV-STACK IDENTITY — one key per active git worktree, the roster a
//     per-stack config generator enumerates.
//  2. PLAIN PORT-BLOCK ALLOCATION — any KCL expression that wants a
//     non-colliding host port, e.g. prod's reliant-web keying on "prod".
//
// Incident (control-plane). The dev NATS generator enumerated the WHOLE
// registry and assumed every key was a worktree, so prod's web-port key
// rendered an account named CP_prod with `user: "control-plane-prod"` — a dev
// NATS account for a prod web port — into a TRACKED deploy/nats/nats.conf.
// That is the same tracked file the "prod-" fragment corrupted (blocks_key_test)
// and the same one the generate-path purity guard reverts
// (internal/cli/kcl_render_purity.go): three symptoms, one root cause — forge
// offered no way to ask which keys were actually stacks.
//
// These tests lock the distinction at the layer that can still tell the
// difference: allocation time, where forge knows whether the key it was handed
// is this command's worktree.

// setActiveWorktree points the process-global active options at wt for the
// duration of one test. The options are a process global (see options.go), so
// restoring is mandatory — a leaked value would silently change what later
// tests consider a stack key.
func setActiveWorktree(t *testing.T, wt string) {
	t.Helper()
	prev := Active()
	SetActive(Options{Worktree: wt})
	t.Cleanup(func() { SetActive(prev) })
}

// TestPortBlockKeyIsNotAStack is the regression lock for the incident: a
// composed port-block key must never appear in the dev-stack roster, even
// though it legitimately holds a block in the same registry.
func TestPortBlockKeyIsNotAStack(t *testing.T) {
	dir := t.TempDir()
	setActiveWorktree(t, "") // primary checkout, exactly as when prod is rendered

	// prod/main.k: fp.allocate_port(3000, "prod") — a real, wanted allocation.
	port, err := AllocatePort(dir, 3000, "prod")
	if err != nil {
		t.Fatalf("AllocatePort(prod): %v", err)
	}
	if port != 3100 {
		t.Errorf("prod web port = %d, want 3100 (block 1)", port)
	}

	// It holds a block...
	if blocks, _ := List(dir); len(blocks) != 1 {
		t.Fatalf("expected the port-block key to be registered, got %v", blocks)
	}
	// ...but it is NOT a stack, so a per-stack config generator never sees it.
	stacks, err := ListStacks(dir)
	if err != nil {
		t.Fatalf("ListStacks: %v", err)
	}
	if len(stacks) != 0 {
		t.Errorf("port-block key %q leaked into the dev-stack roster: %v\n"+
			"a generator reading this roster would render a dev config block for a prod port — "+
			"the CP_prod NATS account incident", "prod", stacks)
	}
}

// TestWorktreeKeyIsAStack is the other half: the roster must still contain
// real stacks, or scoping it would just break per-stack config generation.
func TestWorktreeKeyIsAStack(t *testing.T) {
	dir := t.TempDir()
	setActiveWorktree(t, "wt-feat")

	// dev/main.k keys on option("worktree") directly, so the key forge is
	// handed is byte-equal to the active fact.
	if _, err := AllocatePort(dir, 28080, "wt-feat"); err != nil {
		t.Fatalf("AllocatePort(wt-feat): %v", err)
	}
	stacks, err := ListStacks(dir)
	if err != nil {
		t.Fatalf("ListStacks: %v", err)
	}
	if len(stacks) != 1 || stacks[0] != "wt-feat" {
		t.Fatalf("dev-stack roster = %v, want [wt-feat]", stacks)
	}
}

// TestProdKeyInWorktreeIsNotAStack covers the composed form. In a linked
// worktree prod keys on "prod-<worktree>", which is a DIFFERENT string from the
// worktree name — so it must not be mistaken for the stack that shares its
// suffix.
func TestProdKeyInWorktreeIsNotAStack(t *testing.T) {
	dir := t.TempDir()
	setActiveWorktree(t, "wt-feat")

	if _, err := AllocatePort(dir, 28080, "wt-feat"); err != nil { // the stack
		t.Fatalf("AllocatePort(wt-feat): %v", err)
	}
	if _, err := AllocatePort(dir, 3000, "prod-wt-feat"); err != nil { // prod's web port
		t.Fatalf("AllocatePort(prod-wt-feat): %v", err)
	}

	stacks, err := ListStacks(dir)
	if err != nil {
		t.Fatalf("ListStacks: %v", err)
	}
	if len(stacks) != 1 || stacks[0] != "wt-feat" {
		t.Errorf("dev-stack roster = %v, want exactly [wt-feat] — "+
			"\"prod-wt-feat\" is a port-block key, not a second stack", stacks)
	}
}

// TestLegacyRegistryKeepsBlocks is the migration lock, and it is the one that
// protects real machines. A block is not a cache: it is multiplied by 100 into
// host ports that k3d pre-maps at cluster creation and that an issuer bakes
// into its `iss` claim and redirect URIs. Reading an old registry must
// therefore preserve every index EXACTLY — a "helpful" re-allocation would
// break sign-in and cluster ingress on an existing checkout.
func TestLegacyRegistryKeepsBlocks(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".forge"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The pre-stack-flag on-disk form, exactly as shipped.
	legacy := []byte(`{"": 0, "prod": 1, "wt-feat": 2}`)
	if err := os.WriteFile(filepath.Join(dir, registryRel), legacy, 0o644); err != nil {
		t.Fatal(err)
	}

	setActiveWorktree(t, "")
	for key, want := range map[string]int{"prod": 3100, "wt-feat": 3200} {
		got, err := AllocatePort(dir, 3000, key)
		if err != nil {
			t.Fatalf("AllocatePort(%s): %v", key, err)
		}
		if got != want {
			t.Errorf("legacy key %q moved: port = %d, want %d — "+
				"a moved block invalidates a k3d host mapping or a baked-in issuer claim", key, got, want)
		}
	}

	// A legacy entry carries no stack flag, so it starts out unlabelled
	// rather than being guessed at.
	if stacks, _ := ListStacks(dir); len(stacks) != 0 {
		t.Errorf("legacy entries were guessed into the stack roster: %v", stacks)
	}

	// Bringing that worktree's stack up re-marks it, without moving the block.
	setActiveWorktree(t, "wt-feat")
	got, err := AllocatePort(dir, 3000, "wt-feat")
	if err != nil {
		t.Fatalf("AllocatePort(wt-feat): %v", err)
	}
	if got != 3200 {
		t.Errorf("promoting a legacy entry to a stack moved its block: %d, want 3200", got)
	}
	stacks, _ := ListStacks(dir)
	if len(stacks) != 1 || stacks[0] != "wt-feat" {
		t.Errorf("roster after promotion = %v, want [wt-feat]", stacks)
	}
}

// TestRegistryRoundTripsNewFormat pins the on-disk shape, since it is read by
// a future forge and must stay decodable.
func TestRegistryRoundTripsNewFormat(t *testing.T) {
	dir := t.TempDir()
	setActiveWorktree(t, "wt-a")
	if _, err := AllocatePort(dir, 3000, "wt-a"); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, registryRel))
	if err != nil {
		t.Fatal(err)
	}
	var decoded registry
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("the registry forge just wrote does not decode: %v\n%s", err, raw)
	}
	if e := decoded["wt-a"]; !e.Stack {
		t.Errorf("stack flag did not survive the round trip: %+v", e)
	}
}

// TestUnarmedRenderMarksNothing is the purity half, at this layer. `forge
// generate` and `forge ci` never call SetActive, so no key can be labelled a
// stack during a read-only render — which is what lets fp.dev_stacks() return
// empty there and keeps a generated tracked file identical across machines.
func TestUnarmedRenderMarksNothing(t *testing.T) {
	dir := t.TempDir()
	setActiveWorktree(t, "") // unset, as on the generate path

	if _, err := AllocatePort(dir, 3000, "wt-feat"); err != nil {
		t.Fatal(err)
	}
	if stacks, _ := ListStacks(dir); len(stacks) != 0 {
		t.Errorf("a read-only render recorded stack identity it was never given: %v", stacks)
	}
}
