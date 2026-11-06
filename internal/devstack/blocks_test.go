package devstack

import (
	"sync"
	"testing"
)

// TestAllocatePortDefaultKeyIsBase: key "" ⇒ block 0 ⇒ base unchanged, with
// no registry written (the byte-identical default-stack path).
func TestAllocatePortDefaultKeyIsBase(t *testing.T) {
	dir := t.TempDir()
	p, err := AllocatePort(dir, 28080, "")
	if err != nil {
		t.Fatal(err)
	}
	if p != 28080 {
		t.Errorf("allocate_port(28080, \"\") = %d, want 28080 (base unchanged)", p)
	}
	if list, _ := List(dir); len(list) != 0 {
		t.Errorf("default key wrote a registry entry: %v", list)
	}
}

// TestAllocatePortKeyedOffsets: distinct keys get disjoint blocks; the same
// key always returns the same block (memoized).
func TestAllocatePortKeyedOffsets(t *testing.T) {
	dir := t.TempDir()
	// First key → block 1 → base+100.
	a, err := AllocatePort(dir, 28080, "wt-a")
	if err != nil {
		t.Fatal(err)
	}
	if a != 28180 {
		t.Errorf("wt-a port = %d, want 28180 (28080 + 1*100)", a)
	}
	// A SECOND port for the SAME key shares the key's block (same +100 shift).
	a2, _ := AllocatePort(dir, 3091, "wt-a")
	if a2 != 3191 {
		t.Errorf("wt-a second port = %d, want 3191 (3091 + 1*100)", a2)
	}
	// A distinct key → a disjoint block → base+200.
	b, _ := AllocatePort(dir, 28080, "wt-b")
	if b != 28280 {
		t.Errorf("wt-b port = %d, want 28280 (28080 + 2*100)", b)
	}
	// Re-allocating wt-a after wt-b returns the SAME block (stable).
	again, _ := AllocatePort(dir, 28080, "wt-a")
	if again != a {
		t.Errorf("wt-a re-allocated to %d, want stable %d", again, a)
	}
}

// TestUpDeployIdenticalPort is the up-vs-deploy regression-lock at the engine
// level: two separate AllocatePort calls (simulating the two commands) for
// the same (base, key) resolve identically because the block is persisted.
func TestUpDeployIdenticalPort(t *testing.T) {
	dir := t.TempDir()
	up, err := AllocatePort(dir, 29190, "wt-x")
	if err != nil {
		t.Fatal(err)
	}
	// A fresh "process" (a second call reading the same persisted registry)
	// must land on the identical port.
	deploy, _ := AllocatePort(dir, 29190, "wt-x")
	if up != deploy {
		t.Fatalf("up vs deploy drift: up=%d deploy=%d", up, deploy)
	}
}

func TestNextFreeBlockFillsGaps(t *testing.T) {
	reg := registry{"a": {Block: 1}, "c": {Block: 3}}
	if got := nextFreeBlock(reg); got != 2 {
		t.Errorf("nextFreeBlock filling gap = %d, want 2", got)
	}
	reg["b"] = entry{Block: 2}
	if got := nextFreeBlock(reg); got != 4 {
		t.Errorf("nextFreeBlock dense = %d, want 4", got)
	}
}

// TestConcurrentAllocateNoDuplicateBlock is the lock regression-lock: the
// concurrent first-`up` of N distinct keys must NOT race two keys to the
// same block. The file lock serializes the read-modify-write so every key
// gets a unique slot. Run with -race.
func TestConcurrentAllocateNoDuplicateBlock(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates against a real registry, resolving the worktree via git each time; runs in task test")
	}
	dir := t.TempDir()
	const n = 16
	var wg sync.WaitGroup
	blocks := make([]int, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			blocks[i], errs[i] = AllocateBlock(dir, keyFor(i))
		}(i)
	}
	wg.Wait()

	seen := map[int]bool{}
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("AllocateBlock[%d]: %v", i, errs[i])
		}
		if blocks[i] < 1 {
			t.Errorf("block %d < 1", blocks[i])
		}
		if seen[blocks[i]] {
			t.Errorf("duplicate block %d handed to two concurrent keys", blocks[i])
		}
		seen[blocks[i]] = true
	}
	if list, _ := List(dir); len(list) != n {
		t.Errorf("registry has %d entries, want %d", len(list), n)
	}
}

// TestConcurrentSameKeyOneBlock: many goroutines allocating the SAME key all
// land on one block — no churn, no duplicates.
func TestConcurrentSameKeyOneBlock(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates against a real registry, resolving the worktree via git each time; runs in task test")
	}
	dir := t.TempDir()
	const n = 16
	var wg sync.WaitGroup
	blocks := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			blocks[i], _ = AllocateBlock(dir, "shared")
		}(i)
	}
	wg.Wait()
	for i := 1; i < n; i++ {
		if blocks[i] != blocks[0] {
			t.Errorf("same key got differing blocks: %d vs %d", blocks[i], blocks[0])
		}
	}
	if list, _ := List(dir); len(list) != 1 {
		t.Errorf("registry has %d entries for one key, want 1", len(list))
	}
}

func keyFor(i int) string {
	return "wt-" + string(rune('a'+i))
}

// TWO PROJECTS, ONE MACHINE. The block registry is per-project, so the
// default key is block 0 in every project and each one asks for the same base
// port. For the dev IdP that was fatal rather than untidy: forge refuses to
// adopt an identity provider it did not start, so the second project on a
// machine could not bring up sign-in at all.
func TestAllocatePortAvoidingForeign(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates against a real registry, resolving the worktree via git each time; runs in task test")
	}
	t.Run("takes the base port when it is free", func(t *testing.T) {
		dir := t.TempDir()
		got, err := AllocatePortAvoidingForeign(dir, 8080, "", func(int) bool { return true })
		if err != nil {
			t.Fatal(err)
		}
		if got != 8080 {
			t.Errorf("port = %d, want 8080 (a free base must not move)", got)
		}
	})

	t.Run("steps past a port another stack holds", func(t *testing.T) {
		dir := t.TempDir()
		free := func(p int) bool { return p != 8080 }
		got, err := AllocatePortAvoidingForeign(dir, 8080, "", free)
		if err != nil {
			t.Fatal(err)
		}
		if got != 8180 {
			t.Errorf("port = %d, want 8180 (the first free block)", got)
		}
	})

	t.Run("memoizes the shifted port even once the base frees up", func(t *testing.T) {
		// This is the property the issuer actually needs. `iss` and the
		// registered redirect URI are baked from this port, so it must not
		// move between runs — a second run that reverted to the now-free base
		// would invalidate every token the first run minted.
		dir := t.TempDir()
		first, err := AllocatePortAvoidingForeign(dir, 8080, "", func(p int) bool { return p != 8080 })
		if err != nil {
			t.Fatal(err)
		}
		second, err := AllocatePortAvoidingForeign(dir, 8080, "", func(int) bool { return true })
		if err != nil {
			t.Fatal(err)
		}
		if first != second {
			t.Errorf("port moved between runs: %d then %d", first, second)
		}
	})

	t.Run("a busy port does not move an already-assigned block", func(t *testing.T) {
		dir := t.TempDir()
		first, err := AllocatePortAvoidingForeign(dir, 8080, "", func(int) bool { return true })
		if err != nil {
			t.Fatal(err)
		}
		// Same project, next run, base momentarily busy — possibly with its
		// OWN still-running IdP. Stability wins.
		second, err := AllocatePortAvoidingForeign(dir, 8080, "", func(p int) bool { return p != 8080 })
		if err != nil {
			t.Fatal(err)
		}
		if first != second {
			t.Errorf("an assigned port moved when it went busy: %d then %d", first, second)
		}
	})

	t.Run("falls back to the deterministic port when nothing is free", func(t *testing.T) {
		// Better to hand back the deterministic answer and let the caller's
		// own port guard report the collision with real context.
		dir := t.TempDir()
		got, err := AllocatePortAvoidingForeign(dir, 8080, "", func(int) bool { return false })
		if err != nil {
			t.Fatal(err)
		}
		if got != 8080 {
			t.Errorf("port = %d, want the deterministic 8080 fallback", got)
		}
	})
}
