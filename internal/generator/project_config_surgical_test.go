// project_config_surgical_test.go — forge.yaml is a USER-AUTHORED file, and
// a one-field write must behave like one.
//
// Born red on `forge project upgrade`, whose bumpForgeVersion changes exactly
// one scalar — forge_version — and then called WriteProjectConfigFile, a
// whole-struct yaml.Marshal. Marshalling a Go struct back over a hand-written
// document cannot preserve what the struct does not model: every comment is
// dropped, key order becomes struct-field order, and any section that
// NormalizeForWrite considers derivable (ci:, features:) disappears entirely.
// The result still LOADS to the same semantics, which is why nobody noticed —
// on forge's own forge.yaml it silently deleted ~70 lines of documentation.
//
// The guard below is deliberately byte-level and deliberately fixtured on
// forge's own manifest: it is the richest real example in the tree (comments
// at top level, comments inside blocks, trailing end-of-line comments on
// sequence entries, quoted empty scalars, and both blocks the old path
// dropped).
package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// forgeRepoRoot is two levels above this package's source directory, which is
// the working directory `go test` runs it in.
//
// Not runtime.Caller: under -trimpath (the workspace image's GOFLAGS default)
// that reports "github.com/reliant-labs/forge/internal/...", a module-relative
// path that is not a file on disk. internal/trimpathguard holds the tree to it.
func forgeRepoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve the forge checkout root: %v", err)
	}
	return root
}

// TestSetProjectConfigScalar_PreservesForgeOwnManifest is the acceptance
// gate: bumping forge_version on forge's own forge.yaml must change THAT LINE
// AND NOTHING ELSE.
func TestSetProjectConfigScalar_PreservesForgeOwnManifest(t *testing.T) {
	t.Parallel()

	src := filepath.Join(forgeRepoRoot(t), "forge.yaml")
	before, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read forge's own forge.yaml: %v", err)
	}

	// Anti-vacuity: the fixture has to actually CARRY the things the old
	// path destroyed, or "nothing else changed" proves nothing.
	for _, marker := range []string{
		"# Forge project manifest", // leading comment block
		"\nci:\n",                  // block NormalizeForWrite drops
		"# Features, the database and the frontends", // comment NormalizeForWrite would drop
		"\ncontracts:\n",          // block NormalizeForWrite drops
		"# Pure stateless string", // trailing end-of-line comment
		"forge_version: v",        // the very scalar being rewritten
	} {
		if !strings.Contains(string(before), marker) {
			t.Fatalf("fixture forge.yaml no longer contains %q — this guard has gone blind", marker)
		}
	}

	// Work on a COPY. The real manifest is never touched.
	dst := filepath.Join(t.TempDir(), "forge.yaml")
	if err := os.WriteFile(dst, before, 0o644); err != nil {
		t.Fatalf("stage fixture copy: %v", err)
	}

	const want = "v9.9.9-test"
	if err := SetProjectConfigScalar(dst, "forge_version", want); err != nil {
		t.Fatalf("SetProjectConfigScalar: %v", err)
	}

	afterRaw, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	after := string(afterRaw)

	// ── The one line that must change. ──
	if !strings.Contains(after, "forge_version: "+want+"\n") {
		t.Errorf("forge_version was not set to %q", want)
	}

	// ── Every other line must be byte-identical, in the same order. ──
	beforeLines := strings.Split(string(before), "\n")
	afterLines := strings.Split(after, "\n")
	if len(beforeLines) != len(afterLines) {
		t.Fatalf("line count changed: %d → %d (a one-field write must not add or remove lines)\n"+
			"--- got ---\n%s", len(beforeLines), len(afterLines), after)
	}
	changed := 0
	for i := range beforeLines {
		if beforeLines[i] == afterLines[i] {
			continue
		}
		changed++
		if !strings.HasPrefix(strings.TrimSpace(beforeLines[i]), "forge_version:") {
			t.Errorf("line %d changed but is not forge_version:\n  before: %q\n  after:  %q",
				i+1, beforeLines[i], afterLines[i])
		}
	}
	if changed != 1 {
		t.Errorf("%d lines changed, want exactly 1", changed)
	}
}

// TestSetProjectConfigScalar_NoOpAndMissingKey covers the two edges the
// upgrade lane can hit: the value is already correct (leave the file alone,
// byte for byte) and the key is absent from the document (add it rather than
// silently doing nothing, or the project stays pinned to the old version).
func TestSetProjectConfigScalar_NoOpAndMissingKey(t *testing.T) {
	t.Parallel()

	t.Run("value already correct", func(t *testing.T) {
		t.Parallel()
		const doc = "# keep me\nname: app\nforge_version: v1.2.3\n"
		p := filepath.Join(t.TempDir(), "forge.yaml")
		if err := os.WriteFile(p, []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := SetProjectConfigScalar(p, "forge_version", "v1.2.3"); err != nil {
			t.Fatalf("SetProjectConfigScalar: %v", err)
		}
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != doc {
			t.Errorf("no-op write altered the file:\n--- want ---\n%s\n--- got ---\n%s", doc, got)
		}
	})

	t.Run("key absent", func(t *testing.T) {
		t.Parallel()
		const doc = "# keep me\nname: app\nmodule_path: example.com/app\n"
		p := filepath.Join(t.TempDir(), "forge.yaml")
		if err := os.WriteFile(p, []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := SetProjectConfigScalar(p, "forge_version", "v1.2.3"); err != nil {
			t.Fatalf("SetProjectConfigScalar: %v", err)
		}
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(got), "forge_version: v1.2.3") {
			t.Errorf("absent key was not added:\n%s", got)
		}
		if !strings.Contains(string(got), "# keep me") {
			t.Errorf("adding an absent key destroyed existing content:\n%s", got)
		}
		if !strings.Contains(string(got), "module_path: example.com/app") {
			t.Errorf("adding an absent key dropped a sibling key:\n%s", got)
		}
	})

	t.Run("value needs quoting", func(t *testing.T) {
		t.Parallel()
		// A version-shaped string is plain, but the writer must not emit a
		// bare scalar that re-parses as something else. `yes`, `1.0` and a
		// leading `*` are the classic YAML foot-guns.
		for _, v := range []string{"yes", "1.0", "*star", "", "a: b"} {
			p := filepath.Join(t.TempDir(), "forge.yaml")
			doc := "name: app\nmodule_path: example.com/app\nforge_version: v0\n"
			if err := os.WriteFile(p, []byte(doc), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := SetProjectConfigScalar(p, "forge_version", v); err != nil {
				t.Fatalf("SetProjectConfigScalar(%q): %v", v, err)
			}
			cfg, err := ReadProjectConfig(p)
			if err != nil {
				raw, _ := os.ReadFile(p)
				t.Fatalf("round-trip of %q produced an unloadable file: %v\n%s", v, err, raw)
			}
			if cfg.ForgeVersion != v {
				t.Errorf("round-trip of %q read back as %q", v, cfg.ForgeVersion)
			}
		}
	})
}
