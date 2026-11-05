package forgecompat

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// forgeModule makes dir a module declaring forge's path, as a checkout is.
func forgeModule(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module "+ModulePath+"\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLocalBridge(t *testing.T) {
	t.Setenv("GOWORK", "")
	base := t.TempDir()
	checkout := forgeModule(t, filepath.Join(base, "forge"))
	other := filepath.Join(base, "other")
	writeFile(t, filepath.Join(other, "go.mod"), "module example.com/other\n")

	t.Run("go.work use, relative", func(t *testing.T) {
		project := filepath.Join(base, "p1")
		writeFile(t, filepath.Join(project, "go.work"), "go 1.26\n\nuse (\n\t.\n\t../other\n\t../forge\n)\n")
		b, ok := LocalBridge(project)
		if !ok || b.Dir != checkout || b.Replace {
			t.Fatalf("LocalBridge = %+v, %v; want a use of %s", b, ok, checkout)
		}
		if got, want := b.Undo(), "go work edit -dropuse=../forge"; got != want {
			t.Errorf("Undo = %q, want %q (the path as go.work spells it)", got, want)
		}
		if got := b.String(); got != "go.work bridges "+checkout {
			t.Errorf("String = %q", got)
		}
	})
	t.Run("go.work replace", func(t *testing.T) {
		project := filepath.Join(base, "p2")
		writeFile(t, filepath.Join(project, "go.work"), "go 1.26\n\nuse .\n\nreplace "+ModulePath+" => "+checkout+"\n")
		b, ok := LocalBridge(project)
		if !ok || b.Dir != checkout || !b.Replace {
			t.Fatalf("LocalBridge = %+v, %v", b, ok)
		}
		if !strings.Contains(b.Undo(), "go work edit -dropreplace="+ModulePath) {
			t.Errorf("Undo = %q", b.Undo())
		}
	})
	t.Run("go.mod replace", func(t *testing.T) {
		project := filepath.Join(base, "p3")
		writeFile(t, filepath.Join(project, "go.mod"), "module example.com/p3\n\nrequire "+ModulePath+" v0.1.29\n\nreplace "+ModulePath+" => ../forge\n")
		b, ok := LocalBridge(project)
		if !ok || b.Dir != checkout || !b.Replace || !strings.HasPrefix(b.Undo(), "go mod edit") {
			t.Fatalf("LocalBridge = %+v, %v", b, ok)
		}
	})
	t.Run("published version is not a bridge", func(t *testing.T) {
		project := filepath.Join(base, "p4")
		writeFile(t, filepath.Join(project, "go.mod"), "module example.com/p4\n\nrequire "+ModulePath+" v0.1.29\n")
		writeFile(t, filepath.Join(project, "go.work"), "go 1.26\n\nuse (\n\t.\n\t../other\n)\n")
		if b, ok := LocalBridge(project); ok {
			t.Fatalf("an unbridged project reported a bridge: %+v", b)
		}
	})
	t.Run("GOWORK=off disables the workspace", func(t *testing.T) {
		project := filepath.Join(base, "p5")
		writeFile(t, filepath.Join(project, "go.work"), "go 1.26\n\nuse ../forge\n")
		t.Setenv("GOWORK", "off")
		if b, ok := LocalBridge(project); ok {
			t.Fatalf("GOWORK=off still reported the go.work bridge: %+v", b)
		}
	})
	t.Run("GOWORK selects a file", func(t *testing.T) {
		project := filepath.Join(base, "p6")
		writeFile(t, filepath.Join(project, "go.mod"), "module example.com/p6\n")
		elsewhere := filepath.Join(base, "ws", "go.work")
		writeFile(t, elsewhere, "go 1.26\n\nuse ../forge\n")
		t.Setenv("GOWORK", elsewhere)
		if b, ok := LocalBridge(project); !ok || b.Dir != checkout {
			t.Fatalf("GOWORK=<file> was not honoured: %+v, %v", b, ok)
		}
	})
	t.Run("a go.work above the project applies", func(t *testing.T) {
		parent := filepath.Join(base, "mono")
		writeFile(t, filepath.Join(parent, "go.work"), "go 1.26\n\nuse (\n\t./svc\n\t"+checkout+"\n)\n")
		project := filepath.Join(parent, "svc")
		writeFile(t, filepath.Join(project, "go.mod"), "module example.com/svc\n")
		if b, ok := LocalBridge(project); !ok || b.Dir != checkout {
			t.Fatalf("the enclosing go.work was not found: %+v, %v", b, ok)
		}
	})
}

func TestAssessSkew(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	built := time.Date(2026, 10, 1, 1, 31, 0, 0, time.UTC)
	before := built.Add(-time.Hour)
	after := built.Add(96 * time.Hour)
	const head = "750833054b78ab216150bdaad19c389cb91cbc53"
	const otherRev = "6d2870f016fb0473efb588adc60f1b4e6f64aba4"

	tests := []struct {
		name string
		b    Binary
		c    Checkout
		want SkewKind // 0 = in sync
	}{
		{"same commit, clean", Binary{Revision: head, Root: root, BuiltAt: built}, Checkout{Dir: root, Head: head}, 0},
		{"short revision matches full", Binary{Revision: head[:12]}, Checkout{Dir: root, Head: head}, 0},
		{"different commit", Binary{Revision: otherRev, Root: root, BuiltAt: built}, Checkout{Dir: root, Head: head}, SkewRevision},
		{"checkout dirty, binary clean", Binary{Revision: head, Root: root, BuiltAt: built}, Checkout{Dir: root, Head: head, Dirty: true, SourceEdits: true, LastSourceEditAt: before}, SkewCheckoutEdits},
		{"dirty binary built from these edits", Binary{Revision: head, Modified: true, Root: root, BuiltAt: built}, Checkout{Dir: root, Head: head, Dirty: true, SourceEdits: true, LastSourceEditAt: before}, 0},
		{"edits newer than the dirty binary", Binary{Revision: head, Modified: true, Root: root, BuiltAt: built}, Checkout{Dir: root, Head: head, Dirty: true, SourceEdits: true, LastSourceEditAt: after}, SkewCheckoutEdits},
		{"dirty binary from ANOTHER tree", Binary{Revision: head, Modified: true, Root: other, BuiltAt: built}, Checkout{Dir: root, Head: head, Dirty: true, SourceEdits: true, LastSourceEditAt: before}, SkewCheckoutEdits},
		{"binary carries edits the clean checkout lacks", Binary{Revision: head, Modified: true, Root: root, BuiltAt: built}, Checkout{Dir: root, Head: head}, SkewBinaryEdits},
		// Dirt no build reads — a CI job's test report teed into the root,
		// growing as it runs — is not skew, however recent.
		{"checkout dirty with no source edit, binary clean", Binary{Revision: head, Root: root, BuiltAt: built}, Checkout{Dir: root, Head: head, Dirty: true}, 0},
		{"dirty binary, checkout dirty with no source edit", Binary{Revision: head, Modified: true, Root: root, BuiltAt: built}, Checkout{Dir: root, Head: head, Dirty: true}, 0},
		{"dirty binary from ANOTHER tree, checkout dirty with no source edit", Binary{Revision: head, Modified: true, Root: other, BuiltAt: built}, Checkout{Dir: root, Head: head, Dirty: true}, SkewBinaryEdits},
		{"no revision, untouched HEAD, newer edits no build reads", Binary{Root: root, BuiltAt: built}, Checkout{Dir: root, Head: head, HeadMovedAt: before, Dirty: true}, 0},

		// The roofers case: forge embedded in reliant through a workspace
		// records no forge commit at all.
		{"no revision, checkout moved after the build", Binary{Root: root, BuiltAt: built, Embedded: true}, Checkout{Dir: root, Head: head, HeadMovedAt: after}, SkewUnverifiable},
		{"no revision, checkout untouched since the build", Binary{Root: root, BuiltAt: built, Embedded: true}, Checkout{Dir: root, Head: head, HeadMovedAt: before}, 0},
		{"no revision, untouched HEAD but newer edits", Binary{Root: root, BuiltAt: built}, Checkout{Dir: root, Head: head, HeadMovedAt: before, Dirty: true, SourceEdits: true, LastSourceEditAt: after}, SkewUnverifiable},
		{"no revision, built from another tree", Binary{Root: other, BuiltAt: built}, Checkout{Dir: root, Head: head, HeadMovedAt: before}, SkewUnverifiable},
		{"no revision, no root, no build time", Binary{}, Checkout{Dir: root, Head: head, HeadMovedAt: before}, SkewUnverifiable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, skewed := AssessSkew(tc.b, tc.c)
			if skewed != (tc.want != 0) || got != tc.want {
				t.Errorf("AssessSkew = %v, %v; want %v", got, skewed, tc.want)
			}
		})
	}
}

func TestSkewLine(t *testing.T) {
	built := time.Date(2026, 10, 1, 1, 31, 0, 0, time.UTC)
	br := Bridge{Dir: "/src/forge", File: "/p/go.work", written: "/src/forge"}
	c := Checkout{Dir: "/src/forge", Head: "750833054b78ab216150bdaad19c389cb91cbc53"}

	tests := []struct {
		name string
		kind SkewKind
		b    Binary
		c    Checkout
		want []string
	}{
		{"revision", SkewRevision, Binary{Name: "forge", Revision: "6d2870f016fb0473"}, c,
			[]string{"forge was built from forge 6d2870f016fb, but go.work bridges /src/forge at 750833054b78"}},
		{"embedded, moved", SkewUnverifiable, Binary{Name: "reliant", Root: "/src/forge", BuiltAt: built, Embedded: true}, c,
			[]string{"reliant (built 2026-10-01 01:31) records no forge commit", "which changed after it was built (now 750833054b78)"}},
		{"embedded, other tree", SkewUnverifiable, Binary{Name: "reliant", Root: "/src/reliant-forge"}, c,
			[]string{"compiled from /src/reliant-forge"}},
		{"dirty checkout", SkewCheckoutEdits, Binary{Name: "forge"}, Checkout{Dir: "/src/forge", Head: c.Head, Dirty: true, SourceEdits: true},
			[]string{"at 750833054b78 + uncommitted changes, but forge was not built from those changes"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			line := SkewLine(tc.kind, tc.b, br, tc.c)
			if strings.Contains(line, "\n") {
				t.Errorf("the warning must be ONE line:\n%s", line)
			}
			for _, want := range append(tc.want, "⚠️  forge skew: ", "Fix: rebuild "+tc.b.Name, "go work edit -dropuse=/src/forge") {
				if !strings.Contains(line, want) {
					t.Errorf("line lacks %q:\n%s", want, line)
				}
			}
		})
	}
}

// TestReadCheckout runs real git: HEAD, cleanliness, and the edit time a dirty
// tree carries.
func TestReadCheckout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	writeFile(t, filepath.Join(dir, "a.go"), "package a\n")
	git("add", "a.go")
	git("commit", "-q", "-m", "init")
	head := git("rev-parse", "HEAD")

	c, ok := ReadCheckout(dir)
	if !ok || c.Head != head || c.Dirty {
		t.Fatalf("clean checkout read as %+v, %v; want HEAD %s, clean", c, ok, head)
	}
	if c.HeadMovedAt.IsZero() {
		t.Error("HEAD's last move time was not read")
	}

	edited := time.Now().Add(-time.Minute).Truncate(time.Second)
	writeFile(t, filepath.Join(dir, "a.go"), "package a\n\nvar X = 1\n")
	if err := os.Chtimes(filepath.Join(dir, "a.go"), edited, edited); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "new file.go"), "package a\n") // untracked, with a space
	if err := os.Chtimes(filepath.Join(dir, "new file.go"), edited.Add(-time.Hour), edited.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	c, ok = ReadCheckout(dir)
	if !ok || !c.Dirty || !c.SourceEdits {
		t.Fatalf("an edited checkout read as clean: %+v", c)
	}
	if !c.LastSourceEditAt.Equal(edited) {
		t.Errorf("LastSourceEditAt = %v, want the newest edit %v", c.LastSourceEditAt, edited)
	}

	if _, ok := ReadCheckout(t.TempDir()); ok {
		t.Error("a directory that is not a git checkout must not read as one")
	}

	// A directory INSIDE a checkout (a project's vendored `.forge-pkg`) is
	// not a checkout of its own: git would answer with the enclosing repo's
	// HEAD, and forge would try to build itself from it.
	nested := filepath.Join(dir, ".forge-pkg")
	writeFile(t, filepath.Join(nested, "go.mod"), "module github.com/reliant-labs/forge\n")
	if _, ok := ReadCheckout(nested); ok {
		t.Error("a subdirectory of another repository must not read as a checkout")
	}
}

// Reading a bridged checkout must not write it. `git status` normally
// refreshes the index's stat cache and rewrites .git/index when a tracked
// file's mtime moved — a write into another worktree's checkout from a check
// that only reads. Deterministic: the test moves an mtime, then reads.
func TestReadCheckout_DoesNotRewriteTheIndex(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(cfg, []byte("[maintenance]\n\tauto = false\n[gc]\n\tauto = 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	file := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(file, []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "a.txt")
	run("commit", "-q", "-m", "init")
	// Same content, new mtime: the index's cached stat is now stale.
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(file, later, later); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(dir, ".git", "index")
	before, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadCheckout(dir); !ok {
		t.Fatal("ReadCheckout: not a checkout")
	}
	after, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("ReadCheckout rewrote .git/index of the checkout it only reads")
	}
}

// TestReadCheckout_OnlySourceEditsAreSkew is the e2e shard's case: a CI job
// tees a growing test report into the root of the forge checkout every
// fixture bridges. That report is no input to a build of forge, so the
// checkout is Dirty but has no SourceEdits — while an untracked template
// beneath a package directory (go:embed reaches it) does count.
func TestReadCheckout_OnlySourceEditsAreSkew(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	// forge's shape: the root holds only tests; packages live below it.
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/m\n")
	writeFile(t, filepath.Join(dir, "root_test.go"), "package m\n")
	writeFile(t, filepath.Join(dir, "CHANGELOG.md"), "# log\n")
	writeFile(t, filepath.Join(dir, "internal", "templates", "templates.go"), "package templates\n")
	writeFile(t, filepath.Join(dir, "internal", "templates", "project", "a.tmpl"), "a\n")
	writeFile(t, filepath.Join(dir, "docs", "guide.md"), "guide\n")
	git("add", ".")
	git("commit", "-q", "-m", "init")

	for _, tc := range []struct {
		name   string
		edit   func(t *testing.T)
		source bool
	}{
		{"a test report teed into the root", func(t *testing.T) { writeFile(t, filepath.Join(dir, "e2e-shard-0.json"), "{}\n") }, false},
		{"a tracked doc edited", func(t *testing.T) { writeFile(t, filepath.Join(dir, "docs", "guide.md"), "edited\n") }, false},
		{"a tracked root file edited", func(t *testing.T) { writeFile(t, filepath.Join(dir, "CHANGELOG.md"), "# edited\n") }, false},
		{"an untracked template beneath a package", func(t *testing.T) {
			writeFile(t, filepath.Join(dir, "internal", "templates", "project", "b.tmpl"), "b\n")
		}, true},
		{"a tracked template edited", func(t *testing.T) {
			writeFile(t, filepath.Join(dir, "internal", "templates", "project", "a.tmpl"), "edited\n")
		}, true},
		{"an untracked Go file at the root", func(t *testing.T) { writeFile(t, filepath.Join(dir, "scratch.go"), "package m\n") }, true},
		{"go.mod edited", func(t *testing.T) { writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/m\n\ngo 1.22\n") }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			git("reset", "-q", "--hard")
			git("clean", "-q", "-fdx")
			tc.edit(t)
			c, ok := ReadCheckout(dir)
			if !ok || !c.Dirty {
				t.Fatalf("the edited checkout read as clean: %+v, %v", c, ok)
			}
			if c.SourceEdits != tc.source {
				t.Fatalf("SourceEdits = %v, want %v (%+v)", c.SourceEdits, tc.source, c)
			}
			if !tc.source && !c.LastSourceEditAt.IsZero() {
				t.Errorf("LastSourceEditAt = %v with no source edit: a growing report would still move the rebuild key", c.LastSourceEditAt)
			}
		})
	}
}
