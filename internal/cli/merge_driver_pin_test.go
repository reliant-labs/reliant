package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The generated-file merge driver merges only as the forge the project pins.
// reliant's clone kept a driver naming a forge several pins old; a
// cherry-pick warned it was not the pinned forge, then merged with it anyway.

func pinTestRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repo := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	writeTreeFileT(t, filepath.Join(repo, "svc", "forge.yaml"), "name: svc\nmodule_path: example.com/svc\n")
	for _, env := range []string{"FORGE_NO_GIT_HOOKS", "FORGE_NO_HOOKS", "FORGE_NO_MERGE_DRIVER", delegatedDriverEnv} {
		t.Setenv(env, "")
	}
	// No test writes a durable driver copy into the real user cache.
	prev := mergeDriverCacheDir
	t.Cleanup(func() { mergeDriverCacheDir = prev })
	cache := t.TempDir()
	mergeDriverCacheDir = func() (string, error) { return cache, nil }
	return repo
}

func setPinCheck(t *testing.T, skewed bool) {
	t.Helper()
	prev := mergeDriverPinCheck
	t.Cleanup(func() { mergeDriverPinCheck = prev })
	mergeDriverPinCheck = func(string) (string, string, bool) {
		return "v0.1.46-0.20261014193438-7410e351b23b", "v0.1.45-0.20261001000000-6f9811cb0000", skewed
	}
}

func gitConfig(t *testing.T, dir, key string) string {
	t.Helper()
	out, _ := exec.Command("git", "-C", dir, "config", "--get", key).Output()
	return strings.TrimSpace(string(out))
}

// The pinned forge repairs a driver another forge left, and records itself
// so a driver from another pin can hand it the merge.
func TestEnsureGeneratedMergeDriver_PinnedForgeRepairsAStaleDriver(t *testing.T) {
	repo := pinTestRepo(t)
	stale := filepath.Join(t.TempDir(), "old-forge")
	writeTreeFileT(t, stale, "#!/bin/sh\n")
	staleCmd := "'" + stale + "' merge-generated %O %A %B %L %P"
	if out, err := exec.Command("git", "-C", repo, "config", "merge.forge-generated.driver", staleCmd).CombinedOutput(); err != nil {
		t.Fatalf("seed: %v\n%s", err, out)
	}
	prevExe, prevID := mergeDriverExecutable, mergeDriverIdentity
	t.Cleanup(func() { mergeDriverExecutable, mergeDriverIdentity = prevExe, prevID })
	mergeDriverExecutable = func() (string, error) { return "/opt/forge/bin/forge", nil }
	mergeDriverIdentity = func() string { return "7410e351b23b" }

	setPinCheck(t, false)
	ensureGeneratedMergeDriver(filepath.Join(repo, "svc"))
	got := gitConfig(t, repo, "merge.forge-generated.driver")
	if !strings.HasPrefix(got, "[ -x '/opt/forge/bin/forge' ]") || !strings.Contains(got, " merge-generated %O %A %B %L %P") {
		t.Errorf("the pinned forge did not take over a stale driver: %q", got)
	}
	if reg := gitConfig(t, repo, pinnedDriverSection+".7410e351b23b.command"); reg != got {
		t.Errorf("the pinned forge did not record itself: %q, want %q", reg, got)
	}

	// A forge that is not the pin leaves the pinned forge's driver alone.
	otherExe := filepath.Join(t.TempDir(), "other-forge")
	writeTreeFileT(t, otherExe, "#!/bin/sh\n")
	mergeDriverExecutable = func() (string, error) { return otherExe, nil }
	mergeDriverIdentity = func() string { return "6f9811cb0000" }
	setPinCheck(t, true)
	ensureGeneratedMergeDriver(filepath.Join(repo, "svc"))
	if again := gitConfig(t, repo, "merge.forge-generated.driver"); again != got {
		t.Errorf("a forge that is not the pin rewrote the driver: %q", again)
	}
	if reg := gitConfig(t, repo, pinnedDriverSection+".6f9811cb0000.command"); reg != "" {
		t.Errorf("a forge that is not the pin registered itself: %q", reg)
	}
}

func runMergeDriver(t *testing.T, repo, relPath, base, ours, theirs string) (merged string, err error) {
	t.Helper()
	dir := t.TempDir()
	paths := []string{filepath.Join(dir, "base"), filepath.Join(dir, "ours"), filepath.Join(dir, "theirs")}
	for i, body := range []string{base, ours, theirs} {
		if werr := os.WriteFile(paths[i], []byte(body), 0o644); werr != nil {
			t.Fatal(werr)
		}
	}
	t.Chdir(repo)
	cmd := newMergeGeneratedCmd()
	cmd.SetArgs([]string{paths[0], paths[1], paths[2], "7", relPath})
	cmd.SetOut(&strings.Builder{})
	cmd.SetErr(&strings.Builder{})
	err = cmd.Execute()
	out, rerr := os.ReadFile(paths[1])
	if rerr != nil {
		t.Fatal(rerr)
	}
	return string(out), err
}

// A driver that is not the pinned forge hands the merge to the pinned one.
func TestMergeGenerated_DelegatesToThePinnedForge(t *testing.T) {
	repo := pinTestRepo(t)
	setPinCheck(t, true)
	pinned := filepath.Join(t.TempDir(), "pinned-forge")
	writeTreeFileT(t, pinned, "#!/bin/sh\n[ \"$"+delegatedDriverEnv+"\" = 1 ] || exit 3\nprintf 'merged by the pinned forge\\n' > \"$3\"\n")
	if err := os.Chmod(pinned, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", repo, "config", pinnedDriverSection+".7410e351b23b.command",
		"'"+pinned+"' merge-generated %O %A %B %L %P").CombinedOutput(); err != nil {
		t.Fatalf("seed: %v\n%s", err, out)
	}
	merged, err := runMergeDriver(t, repo, "svc/x_gen.go", "a\n", "b\n", "c\n")
	if err != nil {
		t.Fatalf("delegated merge failed: %v", err)
	}
	if merged != "merged by the pinned forge\n" {
		t.Errorf("%%A = %q; the pinned forge did not merge", merged)
	}
}

// With no pinned forge known, a mismatched driver merges as git does without
// one: a normal conflict where the sides overlap, a clean merge where not —
// never its own rules (which would settle the fingerprint line below).
func TestMergeGenerated_WithoutThePinnedForgeMergesAsGitDoes(t *testing.T) {
	repo := pinTestRepo(t)
	setPinCheck(t, true)
	base := "// forge:hash=aaaa\nx := 1\n"
	merged, err := runMergeDriver(t, repo, "svc/x_gen.go", base, "// forge:hash=bbbb\nx := 1\n", "// forge:hash=cccc\nx := 1\n")
	if err == nil {
		t.Fatalf("a mismatched forge settled a conflict by its own rules:\n%s", merged)
	}
	if !strings.Contains(merged, "<<<<<<< ours") || !strings.Contains(merged, ">>>>>>> theirs") {
		t.Errorf("want git's conflict markers:\n%s", merged)
	}

	merged, err = runMergeDriver(t, repo, "svc/x_gen.go", "a\nb\nc\nd\ne\n", "A\nb\nc\nd\ne\n", "a\nb\nc\nd\nE\n")
	if err != nil || merged != "A\nb\nc\nd\nE\n" {
		t.Errorf("a non-overlapping change must merge cleanly: %q, %v", merged, err)
	}
}

// fakeGoRunForge writes an executable where `go run` links one
// (<tmp>/go-build<N>/b001/exe/forge) and points the driver wiring at it, at
// a durable-copy cache of the test's own. It returns the binary and the cache.
func fakeGoRunForge(t *testing.T, id string) (exe, cache string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "go-build4242", "b001", "exe")
	exe = filepath.Join(dir, "forge")
	writeTreeFileT(t, exe, "#!/bin/sh\n# the pinned forge\n")
	if err := os.Chmod(exe, 0o755); err != nil {
		t.Fatal(err)
	}
	cache = t.TempDir()
	prevExe, prevID, prevCache := mergeDriverExecutable, mergeDriverIdentity, mergeDriverCacheDir
	t.Cleanup(func() { mergeDriverExecutable, mergeDriverIdentity, mergeDriverCacheDir = prevExe, prevID, prevCache })
	mergeDriverExecutable = func() (string, error) { return exe, nil }
	mergeDriverIdentity = func() string { return id }
	mergeDriverCacheDir = func() (string, error) { return cache, nil }
	return exe, cache
}

// reliant pins forge with `FORGE_NO_HOOKS=1 go run .../cmd/forge@<pin>
// generate`. That run is the pinned forge, so it must repair a driver the
// previous pin left — under FORGE_NO_HOOKS (the driver is git config, not a
// hook) and from a `go run` build, by naming a durable copy of itself rather
// than the temp path that is gone when go run exits.
func TestEnsureGeneratedMergeDriver_GoRunPinRepairsTheDriver(t *testing.T) {
	for _, env := range []string{"", "FORGE_NO_HOOKS", "FORGE_NO_GIT_HOOKS"} {
		t.Run("env="+env, func(t *testing.T) {
			repo := pinTestRepo(t)
			if env != "" {
				t.Setenv(env, "1")
			}
			stale := filepath.Join(t.TempDir(), "old-forge")
			writeTreeFileT(t, stale, "#!/bin/sh\n")
			staleCmd := "'" + stale + "' merge-generated %O %A %B %L %P"
			if out, err := exec.Command("git", "-C", repo, "config", "merge.forge-generated.driver", staleCmd).CombinedOutput(); err != nil {
				t.Fatalf("seed: %v\n%s", err, out)
			}
			exe, cache := fakeGoRunForge(t, "7410e351b23b")
			setPinCheck(t, false)

			ensureGeneratedMergeDriver(filepath.Join(repo, "svc"))

			durable := filepath.Join(cache, "7410e351b23b", "forge")
			want := gitConfig(t, repo, "merge.forge-generated.driver")
			if !strings.HasPrefix(want, "[ -x '"+durable+"' ] ") || !strings.Contains(want, " merge-generated %O %A %B %L %P") {
				t.Fatalf("driver = %q, want the durable copy %s", want, durable)
			}
			if reg := gitConfig(t, repo, pinnedDriverSection+".7410e351b23b.command"); reg != want {
				t.Errorf("registered = %q, want %q", reg, want)
			}
			body, err := os.ReadFile(durable)
			if err != nil || string(body) != "#!/bin/sh\n# the pinned forge\n" {
				t.Fatalf("durable copy = %q, %v", body, err)
			}
			// go run deletes its build when it exits; the driver survives.
			if err := os.RemoveAll(filepath.Dir(filepath.Dir(filepath.Dir(exe)))); err != nil {
				t.Fatal(err)
			}
			if !driverCommandUsable(want) {
				t.Error("the driver is unusable once the go run build is gone")
			}
		})
	}
}

// A second `go run` of the same pin reuses the copy; a forge that is not the
// pin never copies itself.
func TestEnsureGeneratedMergeDriver_GoRunCopyIsOncePerPinnedIdentity(t *testing.T) {
	repo := pinTestRepo(t)
	_, cache := fakeGoRunForge(t, "7410e351b23b")
	setPinCheck(t, false)
	ensureGeneratedMergeDriver(filepath.Join(repo, "svc"))
	durable := filepath.Join(cache, "7410e351b23b", "forge")
	first, err := os.Stat(durable)
	if err != nil {
		t.Fatal(err)
	}
	ensureGeneratedMergeDriver(filepath.Join(repo, "svc"))
	second, err := os.Stat(durable)
	if err != nil || !os.SameFile(first, second) {
		t.Errorf("the second run replaced the copy (%v)", err)
	}

	other := pinTestRepo(t)
	_, otherCache := fakeGoRunForge(t, "6f9811cb0000")
	setPinCheck(t, true)
	ensureGeneratedMergeDriver(filepath.Join(other, "svc"))
	if entries, _ := os.ReadDir(otherCache); len(entries) != 0 {
		t.Errorf("a forge that is not the pin copied itself: %v", entries)
	}
	if got := gitConfig(t, other, "merge.forge-generated.driver"); got != "" {
		t.Errorf("a go run forge that is not the pin became the driver: %q", got)
	}
}

// Copies are kept for the keptDriverCopies most recently used identities.
func TestDurableDriverCopy_KeepsTheMostRecentIdentities(t *testing.T) {
	exe, cache := fakeGoRunForge(t, "newest000000")
	old := time.Now().Add(-48 * time.Hour)
	for i, id := range []string{"aaaa00000000", "bbbb00000000", "cccc00000000", "dddd00000000"} {
		dir := filepath.Join(cache, id)
		writeTreeFileT(t, filepath.Join(dir, "forge"), "old\n")
		at := old.Add(time.Duration(i) * time.Hour) // dddd most recent
		if err := os.Chtimes(dir, at, at); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := durableDriverCopy(exe); err != nil {
		t.Fatal(err)
	}
	var kept []string
	entries, _ := os.ReadDir(cache)
	for _, e := range entries {
		kept = append(kept, e.Name())
	}
	if want := []string{"cccc00000000", "dddd00000000", "newest000000"}; strings.Join(kept, ",") != strings.Join(want, ",") {
		t.Errorf("kept %v, want %v", kept, want)
	}
}

func TestGoRunExecutable(t *testing.T) {
	hash := strings.Repeat("0a", 32)
	for exe, want := range map[string]bool{
		"/tmp/go-build123/b001/exe/forge":                         true,
		"/var/cache/workspace/go-build/28/" + hash + "-d/forge":   true,
		"/home/u/.cache/custom-gocache/28/" + hash + "-d/reliant": true,  // GOCACHE moved by go env -w
		"/tmp/forge-bin-123/forge":                                false, // e2e corpus build
		"/home/u/go/bin/forge":                                    false,
		"/tmp/go-build1/b001/cli.test":                            false,
	} {
		if got := goRunExecutable(exe); got != want {
			t.Errorf("goRunExecutable(%q) = %v, want %v", exe, got, want)
		}
	}
}

// Two copies of the pinned forge at different paths — each worktree's
// tool-cache install, a `go install`ed one — must not take the driver from
// each other: each used to rewrite .git/config to name itself, so alternating
// between them rewrote it on every run. The copy already registered keeps it
// while it exists; when it is gone, the next copy repairs the driver.
func TestEnsureGeneratedMergeDriver_CopiesOfThePinDoNotFightOverTheDriver(t *testing.T) {
	repo := pinTestRepo(t)
	setPinCheck(t, false)
	prevExe, prevID := mergeDriverExecutable, mergeDriverIdentity
	t.Cleanup(func() { mergeDriverExecutable, mergeDriverIdentity = prevExe, prevID })
	mergeDriverIdentity = func() string { return "7410e351b23b" }
	realTemp := func() string {
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		return dir
	}
	first := filepath.Join(realTemp(), "worktree-a", "forge")
	second := filepath.Join(realTemp(), "worktree-b", "forge")
	for _, exe := range []string{first, second} {
		writeTreeFileT(t, exe, "#!/bin/sh\n")
	}
	// Binaries under the temp directory are ephemeral and never become the
	// driver; move the temp directory away from these two. os.TempDir reads
	// TMPDIR on Unix but TMP/TEMP on Windows, where setting TMPDIR alone left
	// both copies "ephemeral" and the driver unset.
	movedTemp := filepath.Join(t.TempDir(), "tmp")
	for _, v := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(v, movedTemp)
	}
	if os.TempDir() != movedTemp {
		t.Fatalf("os.TempDir() = %q after moving it to %q", os.TempDir(), movedTemp)
	}

	mergeDriverExecutable = func() (string, error) { return first, nil }
	ensureGeneratedMergeDriver(filepath.Join(repo, "svc"))
	want := gitConfig(t, repo, "merge.forge-generated.driver")
	if !strings.HasPrefix(want, "[ -x '"+first+"' ]") {
		t.Fatalf("driver = %q, want the first copy", want)
	}
	configBefore, err := os.ReadFile(filepath.Join(repo, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}

	mergeDriverExecutable = func() (string, error) { return second, nil }
	ensureGeneratedMergeDriver(filepath.Join(repo, "svc"))
	configAfter, err := os.ReadFile(filepath.Join(repo, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if string(configAfter) != string(configBefore) {
		t.Errorf("a second copy of the pin rewrote .git/config:\n--- before\n%s\n--- after\n%s", configBefore, configAfter)
	}

	if err := os.Remove(first); err != nil {
		t.Fatal(err)
	}
	ensureGeneratedMergeDriver(filepath.Join(repo, "svc"))
	got := gitConfig(t, repo, "merge.forge-generated.driver")
	if !strings.HasPrefix(got, "[ -x '"+second+"' ]") {
		t.Errorf("driver = %q after the registered copy was deleted, want the second copy", got)
	}
	if reg := gitConfig(t, repo, pinnedDriverSection+".7410e351b23b.command"); reg != got {
		t.Errorf("registered = %q, want %q", reg, got)
	}
}
