// Copyright (c) 2025 Reliant Labs

package daemonpolicy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func confinedCtx(root string) context.Context {
	return NewContext(context.Background(), &Policy{
		GrantID:  "grant-rootfs",
		Tools:    map[string]bool{"fs.read_file": true},
		PathRoot: root,
	})
}

func TestResolveFileUnconfinedIsPassthrough(t *testing.T) {
	// The first-party path must be byte-for-byte unchanged.
	got, err := ResolveFile(context.Background(), "/etc/passwd")
	if err != nil {
		t.Fatalf("unconfined resolve failed: %v", err)
	}
	if got != "/etc/passwd" {
		t.Fatalf("unconfined resolve must not rewrite the path, got %q", got)
	}
}

func TestResolveFileConfined(t *testing.T) {
	root := t.TempDir()

	// Compare against the resolved root: ResolveFile returns paths in the
	// resolved namespace, and on macOS t.TempDir() hands back /var/... while
	// /var is a symlink to /private/var.
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	resolvedInside := filepath.Join(resolvedRoot, "a.txt")

	inside := filepath.Join(root, "a.txt")
	if err := os.WriteFile(inside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := confinedCtx(root)

	t.Run("inside root", func(t *testing.T) {
		got, err := ResolveFile(ctx, inside)
		if err != nil {
			t.Fatalf("in-root path denied: %v", err)
		}
		if got != resolvedInside {
			t.Fatalf("got %q want %q", got, resolvedInside)
		}
	})

	t.Run("relative resolves against root", func(t *testing.T) {
		got, err := ResolveFile(ctx, "a.txt")
		if err != nil {
			t.Fatalf("relative path denied: %v", err)
		}
		if got != resolvedInside {
			t.Fatalf("got %q want %q", got, resolvedInside)
		}
	})

	t.Run("outside root denied", func(t *testing.T) {
		if _, err := ResolveFile(ctx, "/etc/passwd"); !errors.Is(err, ErrDenied) {
			t.Fatal("a path outside the root must be denied")
		}
	})

	t.Run("traversal denied", func(t *testing.T) {
		if _, err := ResolveFile(ctx, "../../etc/passwd"); !errors.Is(err, ErrDenied) {
			t.Fatal("traversal must be denied")
		}
	})

	t.Run("empty path means the root", func(t *testing.T) {
		got, err := ResolveDir(ctx, "")
		if err != nil {
			t.Fatalf("empty path should resolve to the root: %v", err)
		}
		if got != resolvedRoot {
			t.Fatalf("empty path resolved to %q, want the root %q", got, resolvedRoot)
		}
	})

	t.Run("missing file under root is not a denial", func(t *testing.T) {
		// A nonexistent target is the handler's error to report, not the
		// policy's — otherwise every create would look like a permission
		// problem.
		if _, err := ResolveDir(ctx, filepath.Join(root, "new", "file.txt")); err != nil {
			t.Fatalf("a new file under the root must be allowed: %v", err)
		}
	})
}

// TestResolveFileSymlinkEscape covers the case string comparison gets wrong.
func TestResolveFileSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevation on Windows")
	}

	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("sensitive"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}

	ctx := confinedCtx(root)

	if _, err := ResolveFile(ctx, filepath.Join(root, "escape", "secret.txt")); !errors.Is(err, ErrDenied) {
		t.Fatal("reading through an escaping symlink must be denied")
	}
	if _, err := ResolveDir(ctx, filepath.Join(root, "escape", "planted.txt")); !errors.Is(err, ErrDenied) {
		t.Fatal("writing through an escaping symlink must be denied")
	}
}

// TestResolveFileClosesTOCTOU is the reason this resolution happens at the
// handler rather than only at dispatch.
//
// The sequence: a path passes the dispatch-time check while it is a real
// directory, then an attacker (the connector itself, which may hold write
// access inside its own root) replaces it with a symlink pointing out. A check
// performed earlier is now stale. Re-resolving here, immediately before use,
// catches the swap.
func TestResolveFileClosesTOCTOU(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevation on Windows")
	}

	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("sensitive"), 0o600); err != nil {
		t.Fatal(err)
	}

	victim := filepath.Join(root, "subdir")
	if err := os.Mkdir(victim, 0o755); err != nil {
		t.Fatal(err)
	}

	ctx := confinedCtx(root)
	target := filepath.Join(victim, "secret.txt")

	// Before the swap the directory is genuinely inside the root, so an
	// earlier check would have passed.
	if _, err := ResolveDir(ctx, target); err != nil {
		t.Fatalf("path should be in bounds before the swap: %v", err)
	}

	// The swap.
	if err := os.Remove(victim); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, victim); err != nil {
		t.Fatal(err)
	}

	// Re-resolving now must refuse, even though the same path was fine a
	// moment ago.
	if _, err := ResolveFile(ctx, target); !errors.Is(err, ErrDenied) {
		t.Fatal("a directory swapped for an escaping symlink must be caught at use time")
	}
}

func TestRootForUnconfinedIsNil(t *testing.T) {
	r, err := RootFor(context.Background())
	if err != nil {
		t.Fatalf("unconfined RootFor should not error: %v", err)
	}
	if r != nil {
		_ = r.Close()
		t.Fatal("an unconfined caller should get no root, and use the ordinary os package")
	}
}

func TestRootForConfined(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	r, err := RootFor(confinedCtx(root))
	if err != nil {
		t.Fatalf("RootFor failed: %v", err)
	}
	if r == nil {
		t.Fatal("a confined caller should get a root")
	}
	defer func() { _ = r.Close() }()

	if _, err := r.ReadFile("a.txt"); err != nil {
		t.Fatalf("reading inside the root failed: %v", err)
	}
	// The kernel enforces the boundary; no string comparison involved.
	if _, err := r.ReadFile("../../../etc/passwd"); err == nil {
		t.Fatal("os.Root must refuse a traversal out of the root")
	}
}

func TestResolveWithNoPathRootDenies(t *testing.T) {
	ctx := NewContext(context.Background(), &Policy{
		Tools:    map[string]bool{"fs.read_file": true},
		PathRoot: "",
	})
	if _, err := ResolveFile(ctx, "anything.txt"); !errors.Is(err, ErrDenied) {
		t.Fatal("a policy with no path root must deny filesystem access")
	}
}

// TestResolveRejectsSymlinkedTarget covers the escape that a returned path
// string cannot otherwise prevent.
//
// os.Root.Lstat does not follow the final component, so a symlink — including
// a DANGLING one, whose target does not exist yet — returns success. The
// handler's own os.WriteFile then follows it straight out of the root. The
// only answer that holds is to refuse the link itself.
func TestResolveRejectsSymlinkedTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevation on Windows")
	}

	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	outside := filepath.Join(base, "outside")
	require(t, os.MkdirAll(root, 0o755))
	require(t, os.MkdirAll(outside, 0o755))

	ctx := confinedCtx(root)

	t.Run("dangling symlink refused for write", func(t *testing.T) {
		link := filepath.Join(root, "dangling")
		require(t, os.Symlink(filepath.Join(outside, "not-yet.txt"), link))

		if _, err := ResolveDir(ctx, link); !errors.Is(err, ErrDenied) {
			t.Fatal("a dangling symlink must be refused; the handler's write would follow it out of the root")
		}
	})

	t.Run("live symlink refused for read", func(t *testing.T) {
		target := filepath.Join(outside, "secret.txt")
		require(t, os.WriteFile(target, []byte("sensitive"), 0o600))
		link := filepath.Join(root, "live")
		require(t, os.Symlink(target, link))

		if _, err := ResolveFile(ctx, link); !errors.Is(err, ErrDenied) {
			t.Fatal("a symlink pointing outside the root must be refused")
		}
	})

	t.Run("ordinary file still works", func(t *testing.T) {
		ordinary := filepath.Join(root, "real.txt")
		require(t, os.WriteFile(ordinary, []byte("x"), 0o600))
		if _, err := ResolveFile(ctx, ordinary); err != nil {
			t.Fatalf("an ordinary file must still resolve: %v", err)
		}
	})

	t.Run("new file under a real directory still works", func(t *testing.T) {
		if _, err := ResolveDir(ctx, filepath.Join(root, "sub", "new.txt")); err != nil {
			t.Fatalf("creating a new file must not be blocked: %v", err)
		}
	})
}

// require fails the test on error, keeping the cases above readable.
func require(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
