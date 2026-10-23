package devstack

import (
	"os"
	"path/filepath"
	"testing"
)

// THE PORT-BLOCK LEAK: A KEY DERIVED FROM A WORKTREE OUTLIVED IT FOREVER.
//
// Prune reclaimed only entries with Stack == true, on the stated assumption
// that every other key was "not tied to any worktree". That is true of "prod"
// and false of "prod-cp-obs": prod's web port keys on
//
//	_wt  = option("worktree") or ""
//	_key = "prod-" + _wt if _wt else "prod"
//	fp.allocate_port(3000, _key)
//
// so in a linked worktree the key IS derived from the worktree, while being a
// different string from the stack key and therefore recorded with Stack ==
// false. Delete the worktree and nothing could ever reclaim its block.
//
// Observed in control-plane, from forge's own ceiling message plus a check of
// which worktrees still existed: blocks 3, 4 and 5 were held by
// "prod-cp204-fix-st", "prod-cp-litellm" and "prod-cp-obs", whose worktrees
// were all gone. The ceiling counts BLOCKS, so those three stranded blocks are
// what made `forge env render prod` fail in every worktree on the machine.
//
// The fix records the origin worktree at ALLOCATION time (entry.Origin) rather
// than recovering it later by parsing the key. These tests pin both directions,
// and the SAFETY direction is the one that matters more: reclaiming moves ports,
// which invalidates a k3d host-port mapping and the dev IdP's baked-in `iss`
// claim, so a false positive here is strictly worse than the leak it fixes.

// registerDerivedKey allocates key while worktree is the active git fact —
// exactly what a render from that worktree does when the KCL composes the key
// from option("worktree").
func registerDerivedKey(t *testing.T, projectDir, worktree, key string, base int) {
	t.Helper()
	setActiveWorktree(t, worktree)
	if _, err := AllocatePort(projectDir, base, key); err != nil {
		t.Fatalf("AllocatePort(%q) from worktree %q: %v", key, worktree, err)
	}
}

// addWorktree creates a linked worktree named name and returns its path.
func addWorktree(t *testing.T, primary, name, branch string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	pruneGit(t, primary, "worktree", "add", "-q", "-b", branch, dir)
	return dir
}

func blockFor(t *testing.T, projectDir, key string) (Block, bool) {
	t.Helper()
	list, err := List(projectDir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, b := range list {
		if b.Key == key {
			return b, true
		}
	}
	return Block{}, false
}

// TestPruneReclaimsDerivedKeyWhenWorktreeGone is the regression lock for the
// leak itself: "prod-<worktree>" allocated from that worktree is reclaimed once
// the worktree is deleted.
//
// BEFORE the fix this FAILS — Prune reports nothing, because the entry's only
// label was Stack == false and there was no record that a worktree ever
// produced it.
func TestPruneReclaimsDerivedKeyWhenWorktreeGone(t *testing.T) {
	gitAvailable(t)
	primary := t.TempDir()
	pruneInitRepo(t, primary)

	deadDir := addWorktree(t, primary, "cp-obs", "obs")
	registerDerivedKey(t, primary, "cp-obs", "prod-cp-obs", 3000)

	if err := os.RemoveAll(deadDir); err != nil {
		t.Fatalf("remove worktree dir: %v", err)
	}
	setActiveWorktree(t, "") // prune decides from disk, not from process state

	pruned, err := Prune(primary, false)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(pruned) != 1 || pruned[0].Key != "prod-cp-obs" {
		t.Fatalf("Prune reclaimed = %+v, want [{prod-cp-obs ...}] — "+
			"a key derived from a deleted worktree leaked its block", pruned)
	}
	if pruned[0].Worktree != "cp-obs" {
		t.Errorf("PrunedBlock.Worktree = %q, want %q (the gone worktree that justified reclaiming)",
			pruned[0].Worktree, "cp-obs")
	}
	if _, ok := blockFor(t, primary, "prod-cp-obs"); ok {
		t.Error("prod-cp-obs still holds a block after prune")
	}
}

// TestPruneNeverReclaimsDerivedKeyWhileWorktreeLives is the safety direction,
// and it is the one worth being strict about: reclaiming moves ports, so a key
// whose worktree still exists must survive every prune, forever.
func TestPruneNeverReclaimsDerivedKeyWhileWorktreeLives(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	gitAvailable(t)
	primary := t.TempDir()
	pruneInitRepo(t, primary)

	addWorktree(t, primary, "cp-live", "live")
	registerDerivedKey(t, primary, "cp-live", "prod-cp-live", 3000)
	before, _ := blockFor(t, primary, "prod-cp-live")

	setActiveWorktree(t, "")

	// Run it twice: a prune that is safe once but not twice is not safe.
	for attempt := 1; attempt <= 2; attempt++ {
		pruned, err := Prune(primary, false)
		if err != nil {
			t.Fatalf("Prune (attempt %d): %v", attempt, err)
		}
		if len(pruned) != 0 {
			t.Fatalf("attempt %d reclaimed a LIVE worktree's derived key: %+v — "+
				"this moves a running stack's ports, the k3d host mapping and the IdP's iss claim",
				attempt, pruned)
		}
	}
	after, ok := blockFor(t, primary, "prod-cp-live")
	if !ok {
		t.Fatal("prod-cp-live vanished from the registry despite its worktree being live")
	}
	if after.Index != before.Index {
		t.Errorf("prod-cp-live moved block %d -> %d; an issued block must never move",
			before.Index, after.Index)
	}
}

// TestPruneNeverReclaimsStandaloneKey pins the migration decision: reclaiming
// is driven by the RECORDED origin, never by the key's shape.
//
// "prod" is allocated from the primary checkout, where option("worktree") is ""
// — there is no worktree to attribute it to, and no worktree's deletion can
// ever make it dead. It must survive a prune run from a state where a
// same-prefixed worktree has just been deleted, which is precisely the
// situation a name-parsing implementation would get wrong.
func TestPruneNeverReclaimsStandaloneKey(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	gitAvailable(t)
	primary := t.TempDir()
	pruneInitRepo(t, primary)

	registerPlainKey(t, primary, "prod", 3000)

	// A worktree exists and is then deleted, so the live set is genuinely
	// empty — "prod" must still be untouchable.
	deadDir := addWorktree(t, primary, "cp-obs", "obs")
	registerDerivedKey(t, primary, "cp-obs", "prod-cp-obs", 3000)
	if err := os.RemoveAll(deadDir); err != nil {
		t.Fatalf("remove worktree dir: %v", err)
	}
	setActiveWorktree(t, "")

	pruned, err := Prune(primary, false)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	for _, p := range pruned {
		if p.Key == "prod" {
			t.Fatalf("Prune reclaimed the standalone key %q — this moves prod's live port", p.Key)
		}
	}
	b, ok := blockFor(t, primary, "prod")
	if !ok {
		t.Fatal("standalone key \"prod\" was removed from the registry")
	}
	if b.Origin != "" {
		t.Errorf("standalone key \"prod\" acquired origin %q; it was allocated from the primary "+
			"checkout and is tied to no worktree", b.Origin)
	}
}

// TestPruneNeverReclaimsUnrelatedKeyThatMerelyLooksDerived is the false-positive
// lock for the alternative design I rejected: inferring the origin by parsing
// the key.
//
// "prod-canary" is allocated from a worktree named "cp-obs". It is a fixed
// literal — nothing about it came from the worktree — and `prod-<something>`
// name-parsing would happily attribute it to a worktree called "canary". No
// such worktree exists, so a parsing implementation reclaims it the moment
// anyone prunes, moving a port that is genuinely in use.
func TestPruneNeverReclaimsUnrelatedKeyThatMerelyLooksDerived(t *testing.T) {
	gitAvailable(t)
	primary := t.TempDir()
	pruneInitRepo(t, primary)

	addWorktree(t, primary, "cp-obs", "obs")
	registerDerivedKey(t, primary, "cp-obs", "prod-canary", 3000)
	setActiveWorktree(t, "")

	pruned, err := Prune(primary, false)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	for _, p := range pruned {
		if p.Key == "prod-canary" {
			t.Fatalf("Prune reclaimed %q by inferring a worktree from its NAME; "+
				"no worktree produced that key, so this moves a live port", p.Key)
		}
	}
}

// TestLegacyEntryWithoutOriginIsNeverReclaimed is the other half of the
// migration decision. An entry written before entry.Origin existed decodes with
// Origin == "" and is therefore treated as standalone: unreclaimable until
// something OBSERVES its origin.
//
// That is the deliberately conservative choice. The alternative — inferring an
// origin for legacy entries from their names — would reclaim a standalone key
// that merely looks composed, and the cost of the two mistakes is not
// symmetric: leaving a block held wastes a slot, while moving a live stack's
// port breaks its cluster mapping and its IdP issuer.
func TestLegacyEntryWithoutOriginIsNeverReclaimed(t *testing.T) {
	gitAvailable(t)
	primary := t.TempDir()
	pruneInitRepo(t, primary)

	// The exact bytes a pre-origin forge wrote for a derived key.
	writeRawRegistry(t, primary, `{"prod-cp-obs": {"block": 4}}`)
	setActiveWorktree(t, "")

	pruned, err := Prune(primary, false)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(pruned) != 0 {
		t.Fatalf("Prune reclaimed a legacy entry with no recorded origin: %+v — "+
			"its origin was guessed from the key name, which is what must not happen", pruned)
	}
}

// TestLegacyEntryLearnsOriginOnNextRenderThenPrunes completes the migration
// story: a legacy entry is not stranded forever, it is reclaimable one render
// later. The next render from its own worktree records the origin forge would
// have recorded at allocation, and the block is then reclaimable normally —
// with its index unchanged throughout, since a block that moved would defeat
// the point of migrating in place.
func TestLegacyEntryLearnsOriginOnNextRenderThenPrunes(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	gitAvailable(t)
	primary := t.TempDir()
	pruneInitRepo(t, primary)

	writeRawRegistry(t, primary, `{"prod-cp-obs": {"block": 4}}`)

	deadDir := addWorktree(t, primary, "cp-obs", "obs")
	// The next `forge env render prod` from that worktree.
	registerDerivedKey(t, primary, "cp-obs", "prod-cp-obs", 3000)

	b, ok := blockFor(t, primary, "prod-cp-obs")
	if !ok {
		t.Fatal("prod-cp-obs missing after re-render")
	}
	if b.Index != 4 {
		t.Fatalf("legacy block moved 4 -> %d during migration; the whole point of migrating "+
			"in place is that an issued block never moves", b.Index)
	}
	if b.Origin != "cp-obs" {
		t.Fatalf("origin = %q, want %q — the re-render observed the worktree and the key together",
			b.Origin, "cp-obs")
	}

	if err := os.RemoveAll(deadDir); err != nil {
		t.Fatalf("remove worktree dir: %v", err)
	}
	setActiveWorktree(t, "")
	pruned, err := Prune(primary, false)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(pruned) != 1 || pruned[0].Key != "prod-cp-obs" {
		t.Fatalf("Prune reclaimed = %+v, want [{prod-cp-obs ...}] after the origin was learned", pruned)
	}
}

// TestSharedKeyLosesOriginWhenAnotherWorktreeAllocatesIt pins the UNLEARN
// direction. A key that two different worktrees both ask for cannot have been
// derived from either — worktree W composes "prod-W" and nothing else — so the
// first attribution was wrong and must be dropped rather than left to strand a
// port when that worktree goes away.
func TestSharedKeyLosesOriginWhenAnotherWorktreeAllocatesIt(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	gitAvailable(t)
	primary := t.TempDir()
	pruneInitRepo(t, primary)

	addWorktree(t, primary, "wt-one", "one")
	addWorktree(t, primary, "wt-two", "two")

	// A shared literal key both worktrees render. It contains "wt-one" as a
	// segment, so the first allocation attributes it — wrongly, as the second
	// allocation then proves.
	registerDerivedKey(t, primary, "wt-one", "shared-wt-one-port", 3000)
	if b, _ := blockFor(t, primary, "shared-wt-one-port"); b.Origin != "wt-one" {
		t.Fatalf("first allocation recorded origin %q, want wt-one", b.Origin)
	}
	registerDerivedKey(t, primary, "wt-two", "shared-wt-one-port", 3000)

	b, ok := blockFor(t, primary, "shared-wt-one-port")
	if !ok {
		t.Fatal("shared key vanished")
	}
	if b.Origin != "" {
		t.Fatalf("origin = %q; a key a SECOND worktree also allocates cannot have been derived "+
			"from the first, so the attribution must be dropped", b.Origin)
	}

	// And it must then survive the deletion of the worktree it was first
	// attributed to.
	setActiveWorktree(t, "")
	pruned, err := Prune(primary, false)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	for _, p := range pruned {
		if p.Key == "shared-wt-one-port" {
			t.Fatalf("Prune reclaimed a shared key: %+v", p)
		}
	}
}

// TestUnarmedRenderNeverStripsOrigin: a render that never armed the git facts
// (forge generate, forge ci, a test) has an EMPTY active worktree, which is
// ambiguous — it means either the primary checkout or an unarmed render. It
// must not be read as proof the key is shared, or every derived key would lose
// its origin the first time `forge generate` touched it, silently re-creating
// the leak.
func TestUnarmedRenderNeverStripsOrigin(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	gitAvailable(t)
	primary := t.TempDir()
	pruneInitRepo(t, primary)

	deadDir := addWorktree(t, primary, "cp-obs", "obs")
	registerDerivedKey(t, primary, "cp-obs", "prod-cp-obs", 3000)

	// An unarmed render of the same key.
	setActiveWorktree(t, "")
	if _, err := AllocatePort(primary, 3000, "prod-cp-obs"); err != nil {
		t.Fatalf("unarmed AllocatePort: %v", err)
	}
	if b, _ := blockFor(t, primary, "prod-cp-obs"); b.Origin != "cp-obs" {
		t.Fatalf("an unarmed render stripped the origin (now %q); it is ambiguous, not contrary, evidence", b.Origin)
	}

	if err := os.RemoveAll(deadDir); err != nil {
		t.Fatalf("remove worktree dir: %v", err)
	}
	pruned, err := Prune(primary, false)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(pruned) != 1 || pruned[0].Key != "prod-cp-obs" {
		t.Fatalf("Prune reclaimed = %+v, want [{prod-cp-obs ...}]", pruned)
	}
}

// TestPruneControlPlaneRegistryShape is the end-to-end reproduction: the exact
// eight-block registry from control-plane, with the same three dead worktrees,
// pruned in one run. Before the fix this reclaims ONE block (the dev stack) and
// leaves the three leaked ones; after it, four — enough to get back under the
// 8-block ceiling that was blocking `forge env render prod`.
func TestPruneControlPlaneRegistryShape(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	gitAvailable(t)
	primary := t.TempDir()
	pruneInitRepo(t, primary)

	live := map[string]string{}
	for _, name := range []string{"newtool-5709b18d", "my-new-feature-50f77334"} {
		live[name] = addWorktree(t, primary, name, "b-"+name)
	}
	dead := map[string]string{}
	for _, name := range []string{"cp204-fix-st", "cp-litellm", "cp-obs"} {
		dead[name] = addWorktree(t, primary, name, "b-"+name)
	}

	registerPlainKey(t, primary, "prod", 3000)
	registerDerivedKey(t, primary, "newtool-5709b18d", "newtool-5709b18d", 28080)
	registerDerivedKey(t, primary, "newtool-5709b18d", "prod-newtool-5709b18d", 3000)
	registerStackKey(t, primary, "my-new-feature-50f77334", 28080)
	for name := range dead {
		registerDerivedKey(t, primary, name, "prod-"+name, 3000)
	}

	for _, dir := range dead {
		if err := os.RemoveAll(dir); err != nil {
			t.Fatalf("remove worktree dir %s: %v", dir, err)
		}
	}
	setActiveWorktree(t, "")

	pruned, err := Prune(primary, false)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	got := map[string]bool{}
	for _, p := range pruned {
		got[p.Key] = true
	}
	for _, want := range []string{"prod-cp204-fix-st", "prod-cp-litellm", "prod-cp-obs"} {
		if !got[want] {
			t.Errorf("leaked block %q was NOT reclaimed; reclaimed = %v", want, sortedKeys(pruned))
		}
	}
	// Everything backed by a live worktree, plus the standalone key, survives.
	for _, want := range []string{"prod", "newtool-5709b18d", "prod-newtool-5709b18d", "my-new-feature-50f77334"} {
		if got[want] {
			t.Errorf("Prune reclaimed %q, which is live or standalone; reclaimed = %v", want, sortedKeys(pruned))
		}
		if _, ok := blockFor(t, primary, want); !ok {
			t.Errorf("%q missing from the registry after prune", want)
		}
	}
}

// writeRawRegistry writes literal JSON into the registry, for exercising bytes
// an older forge would have produced.
func writeRawRegistry(t *testing.T, projectDir, jsonText string) {
	t.Helper()
	p := registryPath(projectDir)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir .forge: %v", err)
	}
	if err := os.WriteFile(p, []byte(jsonText), 0o644); err != nil {
		t.Fatalf("write registry: %v", err)
	}
}
