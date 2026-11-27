package fix

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/cli/factory"
	"github.com/reliant-labs/forge/internal/codemod"
)

// projectDir makes a forge project (just forge.yaml) and points the
// resolution root at it for the test.
func projectDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("name: app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cmdutil.SetProjectDir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmdutil.SetProjectDir("") })
	return dir
}

func runCodemod(t *testing.T, c codemod.Codemod, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := newCodemodCmd(&factory.Factory{Out: &out}, c)
	cmd.SetArgs(args)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	return out.String(), err
}

func TestPrintCodemodReport(t *testing.T) {
	scope, err := codemod.ParseScope(projectDirFor(t, "internal/a"), []string{"internal/a"})
	if err != nil {
		t.Fatal(err)
	}
	c := codemod.Codemod{Name: "demo", Version: "v1.2.3"}

	tests := []struct {
		name    string
		rep     codemod.Report
		dryRun  bool
		scope   *codemod.Scope
		want    []string
		wantNot []string
	}{
		{
			name: "blocked lists every shape and each next step, and claims nothing was written",
			rep: codemod.Report{
				Blocked:  []string{"a.go:3 has a ServeSpec knob", "b.go:9 calls Serve"},
				Next:     []string{"clear them, then re-run"},
				Rewrites: []string{"ignored.go: must not be reported when blocked"},
			},
			want: []string{
				"demo: nothing written — these shapes need your decision first:",
				"✗ a.go:3 has a ServeSpec knob", "✗ b.go:9 calls Serve",
				"Next: clear them, then re-run",
			},
			wantNot: []string{"ignored.go", "✅"},
		},
		{
			name: "nothing to do names the whole project",
			rep:  codemod.Report{},
			want: []string{"demo: nothing to rewrite — the project is already in its v1.2.3 shape"},
		},
		{
			name:  "nothing to do under a scope names the scope",
			rep:   codemod.Report{},
			scope: scope,
			want:  []string{"nothing to rewrite — internal/a is already in its v1.2.3 shape"},
		},
		{
			name: "applied rewrites are ticked, numbered next steps and the default verify are printed",
			rep: codemod.Report{
				Rewrites: []string{"x.go: A → B"},
				Next:     []string{"first", "second"},
				Verify:   []string{"go build ./...", "forge lint"},
			},
			want: []string{"✅ x.go: A → B", "Next:", "1. first", "2. second", "Verify: go build ./... && forge lint"},
		},
		{
			name:    "a dry run says would rewrite and prints no verify line",
			rep:     codemod.Report{Rewrites: []string{"x.go: A → B"}, Verify: []string{"go build ./..."}},
			dryRun:  true,
			want:    []string{"would rewrite: x.go: A → B"},
			wantNot: []string{"Verify", "✅"},
		},
		{
			name:    "next steps alone (no rewrites) are still reported rather than 'nothing to rewrite'",
			rep:     codemod.Report{Next: []string{"do this by hand"}},
			want:    []string{"1. do this by hand"},
			wantNot: []string{"nothing to rewrite"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			printCodemodReport(&out, c, tc.rep, tc.dryRun, tc.scope)
			for _, w := range tc.want {
				if !strings.Contains(out.String(), w) {
					t.Errorf("output lacks %q:\n%s", w, out.String())
				}
			}
			for _, w := range tc.wantNot {
				if strings.Contains(out.String(), w) {
					t.Errorf("output must not contain %q:\n%s", w, out.String())
				}
			}
		})
	}
}

// projectDirFor makes a temp project holding the named subdirectories, for
// building a Scope that passes ParseScope's existence check.
func projectDirFor(t *testing.T, subdirs ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, s := range subdirs {
		if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(s)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestMigrationSkillClauses(t *testing.T) {
	// v1.0.0's playbook ships in the binary; v0.0.1 never did.
	if got := migrationSkillSentence("v1.0.0"); !strings.Contains(got, "`forge skill load migrations/v1.0.0`") {
		t.Errorf("shipped version: sentence = %q, want it to name the skill", got)
	}
	if got := manualPathClause("v1.0.0"); got != "; `forge skill load migrations/v1.0.0` is the manual path" {
		t.Errorf("shipped version: clause = %q", got)
	}
	if got := migrationSkillSentence("v0.0.1"); got != "" {
		t.Errorf("a version with no shipped skill must add nothing, got %q", got)
	}
	if got := manualPathClause("v0.0.1"); got != "" {
		t.Errorf("a version with no shipped skill must add nothing, got %q", got)
	}
}

func TestCodemodCmd_Behaviour(t *testing.T) {
	base := codemod.Codemod{Name: "demo", Version: "v0.0.1", Short: "demo codemod"}

	t.Run("outside a project it fails before running the codemod", func(t *testing.T) {
		empty := t.TempDir()
		if err := cmdutil.SetProjectDir(empty); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmdutil.SetProjectDir("") })
		ran := false
		c := base
		c.Run = func(string, codemod.Options) (codemod.Report, error) { ran = true; return codemod.Report{}, nil }
		_, err := runCodemod(t, c)
		if err == nil || !strings.Contains(err.Error(), "forge.yaml not found") {
			t.Fatalf("err = %v, want a forge.yaml-not-found error", err)
		}
		if ran {
			t.Fatal("the codemod ran outside a project")
		}
	})

	t.Run("a bad --scope is a user error naming the flag", func(t *testing.T) {
		projectDir(t)
		c := base
		c.Scoped = true
		c.Run = func(string, codemod.Options) (codemod.Report, error) {
			t.Fatal("must not run")
			return codemod.Report{}, nil
		}
		_, err := runCodemod(t, c, "--scope", "no/such/dir")
		if err == nil || !strings.Contains(err.Error(), "--scope") || !strings.Contains(err.Error(), "does not exist") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("a codemod error is returned unchanged", func(t *testing.T) {
		projectDir(t)
		boom := errors.New("boom")
		c := base
		c.Run = func(string, codemod.Options) (codemod.Report, error) { return codemod.Report{}, boom }
		if _, err := runCodemod(t, c); !errors.Is(err, boom) {
			t.Fatalf("err = %v, want %v", err, boom)
		}
	})

	t.Run("--dry-run asks the codemod not to write, a plain run asks it to", func(t *testing.T) {
		projectDir(t)
		var got []codemod.Options
		c := base
		c.Run = func(_ string, o codemod.Options) (codemod.Report, error) {
			got = append(got, o)
			return codemod.Report{}, nil
		}
		if _, err := runCodemod(t, c, "--dry-run"); err != nil {
			t.Fatal(err)
		}
		if _, err := runCodemod(t, c); err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got[0].Write || !got[1].Write {
			t.Fatalf("Options.Write per run = %+v, want [false true]", got)
		}
	})

	t.Run("blocked shapes print the report, fail, and name the manual path when shipped", func(t *testing.T) {
		projectDir(t)
		c := base
		c.Version = "v1.0.0"
		c.Run = func(string, codemod.Options) (codemod.Report, error) {
			return codemod.Report{Blocked: []string{"x.go:1 needs a decision"}}, nil
		}
		out, err := runCodemod(t, c)
		if err == nil {
			t.Fatal("a blocked run must fail")
		}
		for _, want := range []string{"1 shape(s) the codemod cannot convert", "clear each item listed above, then re-run", "forge skill load migrations/v1.0.0` is the manual path"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error lacks %q:\n%v", want, err)
			}
		}
		if !strings.Contains(out, "✗ x.go:1 needs a decision") {
			t.Errorf("the blocked shape was not printed:\n%s", out)
		}
	})

	t.Run("a deprecated codemod is hidden and warns", func(t *testing.T) {
		c := base
		c.Deprecated = "use `forge project upgrade`"
		cmd := newCodemodCmd(&factory.Factory{}, c)
		if !cmd.Hidden && cmd.Deprecated == "" {
			t.Fatal("deprecated codemod is neither hidden nor marked deprecated")
		}
		if cmd.Deprecated != c.Deprecated {
			t.Fatalf("Deprecated = %q", cmd.Deprecated)
		}
	})

	t.Run("flags follow the codemod's declared capabilities", func(t *testing.T) {
		for _, tc := range []struct {
			clean, scoped        bool
			wantDirty, wantScope bool
		}{
			{false, false, false, false},
			{true, false, true, false},
			{false, true, false, true},
		} {
			c := base
			c.CleanTree, c.Scoped = tc.clean, tc.scoped
			cmd := newCodemodCmd(&factory.Factory{}, c)
			if got := cmd.Flags().Lookup("allow-dirty") != nil; got != tc.wantDirty {
				t.Errorf("CleanTree=%v: --allow-dirty present = %v", tc.clean, got)
			}
			if got := cmd.Flags().Lookup("scope") != nil; got != tc.wantScope {
				t.Errorf("Scoped=%v: --scope present = %v", tc.scoped, got)
			}
			if cmd.Flags().Lookup("dry-run") == nil {
				t.Error("--dry-run is always offered")
			}
		}
	})
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir,
		"-c", "user.email=t@example.com", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// A CleanTree codemod replaces files wholesale, so it refuses to run over
// uncommitted changes unless told to: `git diff` is the review.
func TestCodemodCmd_CleanTreeRefusesDirtyProject(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := projectDir(t)
	if err := os.MkdirAll(filepath.Join(dir, "internal", "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "internal", "a", "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "init", "-q")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "init")

	var ran []codemod.Options
	c := codemod.Codemod{Name: "demo", Version: "v0.0.1", CleanTree: true, Scoped: true,
		Run: func(_ string, o codemod.Options) (codemod.Report, error) {
			ran = append(ran, o)
			return codemod.Report{}, nil
		}}

	if _, err := runCodemod(t, c); err != nil {
		t.Fatalf("clean tree: %v", err)
	}
	if len(ran) != 1 {
		t.Fatalf("codemod ran %d times on a clean tree, want 1", len(ran))
	}

	if err := os.WriteFile(filepath.Join(dir, "internal", "a", "a.go"), []byte("package a\n\nvar X = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := runCodemod(t, c)
	if err == nil || !strings.Contains(err.Error(), "the project has uncommitted changes") ||
		!strings.Contains(err.Error(), "internal/a/a.go") || !strings.Contains(err.Error(), "--allow-dirty") {
		t.Fatalf("dirty tree: err = %v, want a refusal naming the file and --allow-dirty", err)
	}
	if len(ran) != 1 {
		t.Fatal("the codemod ran over a dirty tree")
	}

	if _, err := runCodemod(t, c, "--dry-run"); err != nil || len(ran) != 2 || ran[1].Write {
		t.Fatalf("--dry-run over a dirty tree: err=%v runs=%d opts=%+v", err, len(ran), ran)
	}
	if _, err := runCodemod(t, c, "--allow-dirty"); err != nil || len(ran) != 3 || !ran[2].Write {
		t.Fatalf("--allow-dirty: err=%v runs=%d", err, len(ran))
	}

	// Dirt outside the scope is another lane's work, not this run's.
	if err := os.MkdirAll(filepath.Join(dir, "internal", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "checkout", "--", "internal/a/a.go")
	if err := os.WriteFile(filepath.Join(dir, "internal", "b", "b.go"), []byte("package b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runCodemod(t, c, "--scope", "internal/a"); err != nil {
		t.Fatalf("dirt outside --scope blocked the run: %v", err)
	}
	if _, err := runCodemod(t, c); err == nil {
		t.Fatal("unscoped run must see internal/b's uncommitted file")
	}
}

func TestUncommitted_NotAGitRepoIsClean(t *testing.T) {
	if got := uncommitted(t.TempDir(), nil); got != "" {
		t.Fatalf("uncommitted outside git = %q, want empty", got)
	}
}
