package lint

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeDotenvTestFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("K=v\n"), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// A project with no .env files passes.
func TestNoDotenvLint_CleanProjectPasses(t *testing.T) {
	root := t.TempDir()
	writeDotenvTestFile(t, filepath.Join(root, "deploy", "kcl", "dev", "main.k"))
	writeDotenvTestFile(t, filepath.Join(root, "secrets", "dev", "STRIPE_SECRET_KEY"))

	if fs := dotenvFindings(t, root); len(fs) != 0 {
		t.Fatalf("clean project should pass, got: %+v", fs)
	}
}

// Every .env* spelling is caught — including the ones a developer is most
// likely to copy in from an older project.
func TestNoDotenvLint_CatchesEveryDotenvSpelling(t *testing.T) {
	for _, name := range []string{
		".env",
		".env.dev",
		".env.dev.secrets",
		".env.local",
		".env.production",
		".env.example",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeDotenvTestFile(t, filepath.Join(root, name))

			fs := dotenvFindings(t, root)
			if len(fs) != 1 || fs[0].Severity != lintSevError {
				t.Fatalf("%s should fail the no-dotenv lint, got %+v", name, fs)
			}
			if fs[0].File != name {
				t.Fatalf("finding should name %s, got: %+v", name, fs[0])
			}
		})
	}
}

// The message has to say what to do instead, or it just blocks someone
// with no path forward.
func TestNoDotenvLint_MessageGivesTheFix(t *testing.T) {
	root := t.TempDir()
	writeDotenvTestFile(t, filepath.Join(root, ".env.dev.secrets"))

	fs := dotenvFindings(t, root)
	if len(fs) != 1 {
		t.Fatalf("expected one finding, got %+v", fs)
	}
	msg := fs[0].Message + " " + fs[0].FixHint
	if !strings.Contains(msg, "forge secret migrate") {
		t.Fatalf("a *.secrets file should point at `forge secret migrate`, got: %v", msg)
	}
	if !strings.Contains(msg, "forge secret set") {
		t.Fatalf("message should name the command that stores a value, got: %v", msg)
	}
}

// A .env inside node_modules is a dependency's file, not the project's.
func TestNoDotenvLint_SkipsVendoredTrees(t *testing.T) {
	root := t.TempDir()
	writeDotenvTestFile(t, filepath.Join(root, "node_modules", "pkg", ".env"))
	writeDotenvTestFile(t, filepath.Join(root, "vendor", "x", ".env.dev"))
	writeDotenvTestFile(t, filepath.Join(root, ".git", ".env"))

	if fs := dotenvFindings(t, root); len(fs) != 0 {
		t.Fatalf("vendored .env files should be ignored, got: %+v", fs)
	}
}

// Nested project files are still the project's own.
func TestNoDotenvLint_CatchesNestedProjectFiles(t *testing.T) {
	root := t.TempDir()
	writeDotenvTestFile(t, filepath.Join(root, "frontends", "web", ".env.local"))

	fs := dotenvFindings(t, root)
	if len(fs) != 1 {
		t.Fatalf("a .env under a project subdirectory should fail, got %+v", fs)
	}
	if fs[0].File != filepath.ToSlash(filepath.Join("frontends", "web", ".env.local")) {
		t.Fatalf("finding should give the relative path, got: %+v", fs[0])
	}
}

// JSON mode reports one finding per file with a stable rule name.
func TestNoDotenvLint_JSONFindings(t *testing.T) {
	root := t.TempDir()
	writeDotenvTestFile(t, filepath.Join(root, ".env"))
	writeDotenvTestFile(t, filepath.Join(root, ".env.dev"))

	findings, err := collectNoDotenvJSON(root)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("got %d findings, want 2: %+v", len(findings), findings)
	}
	for _, f := range findings {
		if f.Rule != "no-dotenv" {
			t.Errorf("rule = %q, want no-dotenv", f.Rule)
		}
		if f.Severity != "error" {
			t.Errorf("severity = %q, want error", f.Severity)
		}
	}
}

// A dotenv inside a gitignored DIRECTORY (build output, a tool cache) is not
// the project's source and must not fail the lint. A dotenv ignored only by
// its own name is the untracked dotenv the lint exists for, and a tracked one
// is in version control — both are still reported.
func TestNoDotenvLint_RespectsGitignoredDirectories(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("tmp/\n.cache\n.env\nfixtures/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeDotenvTestFile(t, filepath.Join(root, "tmp", "run", ".env"))           // ignored dir → skipped
	writeDotenvTestFile(t, filepath.Join(root, ".cache", "tool", ".env.local")) // ignored dir → skipped
	writeDotenvTestFile(t, filepath.Join(root, ".env"))                         // ignored by NAME → reported
	writeDotenvTestFile(t, filepath.Join(root, "fixtures", ".env.test"))        // tracked despite ignore → reported
	writeDotenvTestFile(t, filepath.Join(root, "web", ".env.local"))            // not ignored → reported
	run("add", "-f", "fixtures/.env.test")

	found, err := findDotenvFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range found {
		got = append(got, filepath.ToSlash(f.Path))
	}
	want := []string{".env", "fixtures/.env.test", "web/.env.local"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("findings = %v, want %v", got, want)
	}
}

// dotenvFindings runs the no-dotenv lane over root.
func dotenvFindings(t *testing.T, root string) []lintJSONFinding {
	t.Helper()
	fs, err := collectNoDotenvJSON(root)
	if err != nil {
		t.Fatalf("collectNoDotenvJSON: %v", err)
	}
	return fs
}
