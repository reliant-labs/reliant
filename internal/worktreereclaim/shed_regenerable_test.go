// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// shedFixture is a live, dirty worktree (a unique untracked file, so it is
// never retired whole) with an install's worth of regenerable output: a
// node_modules holding a .bin symlink and a pnpm-style hard link, and a
// .next build cache.
func shedFixture(t *testing.T) (*fixture, Worktree, map[string]string) {
	t.Helper()
	f := newFixture(t)
	f.idle()
	ignore(t, f, "node_modules/\n.next/\n")
	w := f.worktree("shed", "feat/shed")
	w.State, w.Fence = StateActive, ""
	w.ShedAfterSeconds = 3600
	store := filepath.Join(t.TempDir(), "store-file.js")
	write(t, store, "shared content")
	p := map[string]string{
		"unique":   filepath.Join(w.Path, "unique.txt"),
		"module":   filepath.Join(w.Path, "node_modules", "pkg", "index.js"),
		"linked":   filepath.Join(w.Path, "node_modules", "pkg", "linked.js"),
		"bin":      filepath.Join(w.Path, "node_modules", ".bin", "tool"),
		"next":     filepath.Join(w.Path, ".next", "cache", "chunk.js"),
		"store":    store,
		"modules":  filepath.Join(w.Path, "node_modules"),
		"nextRoot": filepath.Join(w.Path, ".next"),
	}
	write(t, p["unique"], "only copy")
	write(t, p["module"], "regenerable")
	write(t, p["next"], "regenerable")
	if err := os.Link(store, p["linked"]); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p["bin"]), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(store, p["bin"]); err != nil {
		t.Fatal(err)
	}
	return f, w, p
}

// A live worktree kept for its work sheds node_modules and build output once
// nothing in it has changed for the floor: the incident machine held 77 GiB
// of node_modules in worktrees nothing ever reclaimed.
func TestShed_LiveIdleWorktreeShedsRegenerableOutputOnly(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "real git worktree fixture")
	f, w, p := shedFixture(t)
	got := f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]
	if got.Outcome != OutcomeLocked || len(got.Stripped) != 2 {
		t.Fatalf("got %+v, want locked with node_modules and .next shed", got)
	}
	if exists(p["modules"]) || exists(p["nextRoot"]) {
		t.Fatalf("regenerable output survived: %+v", got)
	}
	if !exists(p["unique"]) || !exists(w.Path) {
		t.Fatal("the work in the worktree was touched")
	}
	if b, err := os.ReadFile(p["store"]); err != nil || string(b) != "shared content" {
		t.Fatalf("a symlink or hard link target outside the tree was touched: %q %v", b, err)
	}
}

// Installed packages ship names the never-list reserves for user data
// (@protobufjs/utf8 has tests/data, tsconfig-paths lib/__tests__/data, in
// nearly every frontend's tree). Inside a package an install put there, they
// are the package's content and shed with the rest; before this, one such
// directory held every node_modules on the machine.
func TestShed_InstalledPackageContentIsNotUserData(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "real git worktree fixture")
	f, w, p := shedFixture(t)
	// npm/yarn layout, and pnpm's content-addressed layout with a scope.
	write(t, filepath.Join(p["modules"], "pkg", "package.json"), `{"name":"pkg"}`)
	write(t, filepath.Join(p["modules"], "pkg", "tests", "data", "fixture.bin"), "package content")
	scoped := filepath.Join(p["modules"], ".pnpm", "@protobufjs+utf8@1.1.2", "node_modules", "@protobufjs", "utf8")
	write(t, filepath.Join(scoped, "package.json"), `{"name":"@protobufjs/utf8"}`)
	write(t, filepath.Join(scoped, "tests", "data", "utf8.txt"), "package content")
	write(t, filepath.Join(scoped, "tests", ".env"), "package content")
	got := f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]
	if got.Outcome != OutcomeLocked || exists(p["modules"]) {
		t.Fatalf("node_modules with installed package content was not shed: %+v", got)
	}
	if !exists(p["unique"]) || !exists(w.Path) {
		t.Fatal("the work in the worktree was touched")
	}
	if b, err := os.ReadFile(p["store"]); err != nil || string(b) != "shared content" {
		t.Fatalf("a hard link target outside the tree was touched: %q %v", b, err)
	}
}

func TestShed_HoldsWhatItCannotProveRegenerableAndIdle(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "real git worktree fixtures")
	for _, kind := range []string{"protected", "protected-not-a-package", "protected-beside-packages", "fresh", "in-use", "process-use", "keep", "no-floor", "nested-repo"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			f, w, p := shedFixture(t)
			switch kind {
			case "protected":
				write(t, filepath.Join(p["modules"], "pkg", ".env"), "only secret")
			case "protected-not-a-package":
				// package.json as a directory is not a package manifest.
				write(t, filepath.Join(p["modules"], "pkg", "package.json", "x"), "not a manifest")
				write(t, filepath.Join(p["modules"], "pkg", "data", "only-copy.csv"), "only copy")
			case "protected-beside-packages":
				// node_modules' own data/, next to an installed package, is
				// no package's content.
				write(t, filepath.Join(p["modules"], "lib", "package.json"), `{"name":"lib"}`)
				write(t, filepath.Join(p["modules"], "data", "only-copy.csv"), "only copy")
			case "fresh":
				now := f.r.now()
				if err := os.Chtimes(p["module"], now, now); err != nil {
					t.Fatal(err)
				}
			case "in-use":
				f.r.Cwds = func(context.Context) ([]string, error) { return []string{w.Path}, nil }
			case "process-use":
				f.r.ProcessUse = func(context.Context, []string) error { return ErrProcessUseUnsupported }
			case "keep":
				w.KeepFiles = true
			case "no-floor":
				w.ShedAfterSeconds = 0
			case "nested-repo":
				write(t, filepath.Join(p["modules"], "dep", ".git"), "gitdir: elsewhere")
			}
			got := f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]
			if !exists(p["unique"]) {
				t.Fatal("the work in the worktree was touched")
			}
			if exists(p["modules"]) {
				return // only node_modules is held in these cases; .next may go
			}
			t.Fatalf("%s: node_modules was shed: %+v", kind, got)
		})
	}
}

// An archived worktree held for its work (dirty) sheds the same way.
func TestShed_ArchivedDirtyWorktreeSheds(t *testing.T) {
	t.Parallel()
	heavyUnderShort(t, "real git worktree fixture")
	f, w, p := shedFixture(t)
	w.State, w.Fence = StateArchived, "100"
	got := f.r.Reconcile(context.Background(), Request{Worktrees: []Worktree{w}}).Results[0]
	if got.Outcome != OutcomeHeld || exists(p["modules"]) || !exists(p["unique"]) || got.StrippedBytes == 0 {
		t.Fatalf("got %+v, want held with node_modules shed", got)
	}
}
