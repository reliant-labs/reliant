package devstack

import (
	"os"
	"testing"
)

// THE TERMINAL CASE OF THE CONSERVATIVE MIGRATION.
//
// entry.Origin is recorded at allocation, and a pre-origin entry migrates by
// being re-allocated: the next render from its own worktree observes the fact
// and the key together. That covers every entry whose worktree still exists.
//
// It does NOT cover an entry whose worktree was deleted BEFORE forge recorded
// origins at all. There is no future render to learn from, so the block is held
// not just until next run but permanently — and because the ceiling counts
// blocks, each one consumes a slot forever. control-plane was in exactly this
// state: three pre-origin entries ("prod-cp204-fix-st", "prod-cp-litellm",
// "prod-cp-obs") whose worktrees were long gone.
//
// forge still does not guess. Inferring their origin from their names is what
// the origin field exists to prevent, and the asymmetry is unchanged: a held
// block wastes a slot, a wrongly-released one moves a live stack's ports. So
// these are REPORTED (Unattributed) and released only by explicit human
// instruction naming the key (ReleaseKey).

// TestUnattributedReportsLegacyEntriesWithNoOrigin: the blocks forge cannot
// classify are surfaced rather than silently occupying the ceiling.
func TestUnattributedReportsLegacyEntriesWithNoOrigin(t *testing.T) {
	gitAvailable(t)
	primary := t.TempDir()
	pruneInitRepo(t, primary)

	// The exact control-plane bytes: pre-origin entries, no worktrees left.
	writeRawRegistry(t, primary, `{
	  "": {"block": 0},
	  "prod": {"block": 1},
	  "prod-cp-obs": {"block": 5},
	  "my-new-feature-50f77334": {"block": 6, "stack": true}
	}`)
	setActiveWorktree(t, "")

	got, err := Unattributed(primary)
	if err != nil {
		t.Fatalf("Unattributed: %v", err)
	}
	keys := map[string]bool{}
	for _, u := range got {
		keys[u.Key] = true
	}
	for _, want := range []string{"prod", "prod-cp-obs"} {
		if !keys[want] {
			t.Errorf("Unattributed did not report %q; it holds a block forge cannot classify "+
				"and nothing else will ever surface it", want)
		}
	}
	// A dev stack and the default are classified — prune handles the first,
	// the second is never reclaimable — so neither is a decision for a human.
	for _, unwanted := range []string{"my-new-feature-50f77334", ""} {
		if keys[unwanted] {
			t.Errorf("Unattributed reported %q, which forge CAN classify", unwanted)
		}
	}
}

// TestUnattributedFlagsKeysMatchingALiveWorktree: the report's whole job is to
// help a human decide, so an entry that matches a LIVE worktree must be marked
// as likely in use — that is the one they must not release.
func TestUnattributedFlagsKeysMatchingALiveWorktree(t *testing.T) {
	gitAvailable(t)
	primary := t.TempDir()
	pruneInitRepo(t, primary)

	addWorktree(t, primary, "newtool-5709b18d", "newtool")
	writeRawRegistry(t, primary, `{
	  "prod-newtool-5709b18d": {"block": 7},
	  "prod-cp-obs": {"block": 5}
	}`)
	setActiveWorktree(t, "")

	got, err := Unattributed(primary)
	if err != nil {
		t.Fatalf("Unattributed: %v", err)
	}
	for _, u := range got {
		switch u.Key {
		case "prod-newtool-5709b18d":
			if u.LikelyLiveOrigin != "newtool-5709b18d" {
				t.Errorf("%q not flagged as likely in use (LikelyLiveOrigin=%q); its worktree "+
					"is LIVE and releasing it would move that stack's ports", u.Key, u.LikelyLiveOrigin)
			}
		case "prod-cp-obs":
			if u.LikelyLiveOrigin != "" {
				t.Errorf("%q flagged against worktree %q, which does not exist", u.Key, u.LikelyLiveOrigin)
			}
		}
	}
}

// TestUnattributedIsReportOnly: reporting must never mutate. A command that
// shows you the problem and quietly moves a port while doing so is worse than
// no command.
func TestUnattributedIsReportOnly(t *testing.T) {
	gitAvailable(t)
	primary := t.TempDir()
	pruneInitRepo(t, primary)

	writeRawRegistry(t, primary, `{"prod-cp-obs": {"block": 5}, "prod": {"block": 1}}`)
	setActiveWorktree(t, "")
	before, err := os.ReadFile(registryPath(primary))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Unattributed(primary); err != nil {
		t.Fatalf("Unattributed: %v", err)
	}
	after, err := os.ReadFile(registryPath(primary))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("Unattributed rewrote the registry:\nbefore: %s\nafter:  %s", before, after)
	}
}

// TestReleaseKeyFreesExactlyTheNamedBlock: release acts on one key the caller
// named, and on nothing else — the property that makes it safe to expose at all.
func TestReleaseKeyFreesExactlyTheNamedBlock(t *testing.T) {
	gitAvailable(t)
	primary := t.TempDir()
	pruneInitRepo(t, primary)

	writeRawRegistry(t, primary, `{
	  "prod": {"block": 1},
	  "prod-cp-obs": {"block": 5},
	  "prod-cp-litellm": {"block": 4}
	}`)
	setActiveWorktree(t, "")

	index, err := ReleaseKey(primary, "prod-cp-obs")
	if err != nil {
		t.Fatalf("ReleaseKey: %v", err)
	}
	if index != 5 {
		t.Errorf("released block %d, want 5", index)
	}
	if _, ok := blockFor(t, primary, "prod-cp-obs"); ok {
		t.Error("prod-cp-obs still holds a block after release")
	}
	for _, keep := range []string{"prod", "prod-cp-litellm"} {
		if _, ok := blockFor(t, primary, keep); !ok {
			t.Errorf("release swept up %q, which the caller did not name", keep)
		}
	}
}

// TestReleaseKeyRefusesUnknownKeyAndDefault: a typo must not read as success,
// and block 0 is not a thing you can release.
func TestReleaseKeyRefusesUnknownKeyAndDefault(t *testing.T) {
	gitAvailable(t)
	primary := t.TempDir()
	pruneInitRepo(t, primary)
	writeRawRegistry(t, primary, `{"prod": {"block": 1}}`)

	if _, err := ReleaseKey(primary, "prod-typo"); err == nil {
		t.Error("ReleaseKey silently succeeded for a key with no entry; a typo must not look like success")
	}
	if _, err := ReleaseKey(primary, ""); err == nil {
		t.Error("ReleaseKey accepted the default key; block 0 is the primary checkout's implicit block")
	}
	if _, ok := blockFor(t, primary, "prod"); !ok {
		t.Error("a refused release still mutated the registry")
	}
}

// TestFreedBlockIsReusedDensely closes the loop: reclaiming is only worth doing
// if the freed index is actually handed to the next key, which is what gets a
// project back under the ceiling.
func TestFreedBlockIsReusedDensely(t *testing.T) {
	gitAvailable(t)
	primary := t.TempDir()
	pruneInitRepo(t, primary)

	writeRawRegistry(t, primary, `{"prod": {"block": 1}, "prod-cp-obs": {"block": 2}}`)
	setActiveWorktree(t, "")

	if _, err := ReleaseKey(primary, "prod-cp-obs"); err != nil {
		t.Fatalf("ReleaseKey: %v", err)
	}
	block, err := AllocateBlock(primary, "prod-brand-new")
	if err != nil {
		t.Fatalf("AllocateBlock: %v", err)
	}
	if block != 2 {
		t.Errorf("next key got block %d, want the freed 2 — blocks must refill densely or "+
			"reclaiming does not actually relieve the ceiling", block)
	}
}
