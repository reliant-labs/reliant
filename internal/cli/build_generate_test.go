package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
)

// writeFile writes content at dir/rel, creating parent dirs.
func writeFileAt(t *testing.T, dir, rel, content string) string {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	return full
}

func TestMissingGoWorkModule(t *testing.T) {
	t.Run("no go.work returns empty", func(t *testing.T) {
		dir := t.TempDir()
		if got := missingGoWorkModule(dir); got != "" {
			t.Fatalf("want empty, got %q", got)
		}
	})

	t.Run("use'd module without go.mod is reported", func(t *testing.T) {
		dir := t.TempDir()
		writeFileAt(t, dir, "go.work", "go 1.26.2\n\nuse (\n\t.\n\tgen\n)\n")
		// main module go.mod present; gen/ has none.
		writeFileAt(t, dir, "go.mod", "module example.com/x\n\ngo 1.26.2\n")
		if got := missingGoWorkModule(dir); got != "gen" {
			t.Fatalf("want \"gen\", got %q", got)
		}
	})

	t.Run("all use'd modules present returns empty", func(t *testing.T) {
		dir := t.TempDir()
		writeFileAt(t, dir, "go.work", "go 1.26.2\n\nuse (\n\t.\n\tgen\n)\n")
		writeFileAt(t, dir, "go.mod", "module example.com/x\n\ngo 1.26.2\n")
		writeFileAt(t, dir, "gen/go.mod", "module example.com/x/gen\n\ngo 1.26.2\n")
		if got := missingGoWorkModule(dir); got != "" {
			t.Fatalf("want empty, got %q", got)
		}
	})

	t.Run("single-line use directive", func(t *testing.T) {
		dir := t.TempDir()
		writeFileAt(t, dir, "go.work", "go 1.26.2\n\nuse ./gen\n")
		if got := missingGoWorkModule(dir); got != "gen" {
			t.Fatalf("want \"gen\", got %q", got)
		}
	})
}

// TestGeneratedCodeNeedsRefresh_MissingDevRuntimeConfig: public/config.js is
// gitignored (it bakes in machine-local ports), so a fresh clone has none.
// That used to trigger a FULL `forge generate` before every build — which
// refuses outright when the forge on PATH is not the project's pin, so a
// fresh worktree could not `forge env build prod --target <a Go workload>`,
// a build that never reads a frontend's dev config. The missing document is
// still DETECTED (ensureDevRuntimeConfigs renders just it, where a build
// consumes it), but it no longer asks for a regenerate.
func TestGeneratedCodeNeedsRefresh_MissingDevRuntimeConfig(t *testing.T) {
	fresh := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		writeFileAt(t, dir, "go.mod", "module example.com/x\n\ngo 1.26.2\n")
		return dir
	}
	t.Run("a missing dev document is detected but does not regenerate", func(t *testing.T) {
		dir := fresh(t)
		writeFileAt(t, dir, "frontends/web/src/lib/config_gen.ts", "// generated\n")
		writeFileAt(t, dir, "frontends/web/public/favicon.ico", "x")
		if got := missingDevRuntimeConfig(dir); got != "frontends/web/public/config.js" {
			t.Fatalf("missingDevRuntimeConfig = %q, want frontends/web/public/config.js", got)
		}
		if reason, needs := generatedCodeNeedsRefresh(dir); needs {
			t.Fatalf("a missing machine-local config.js must not trigger a full generate, got %q", reason)
		}
		writeFileAt(t, dir, "frontends/web/public/config.js", "window.__FORGE_CONFIG__ = {};\n")
		if got := missingDevRuntimeConfig(dir); got != "" {
			t.Fatalf("document present, still reported missing: %q", got)
		}
	})
	t.Run("frontends that consume no document are ignored", func(t *testing.T) {
		dir := fresh(t)
		writeFileAt(t, dir, "frontends/plain/public/favicon.ico", "x")           // no config module
		writeFileAt(t, dir, "frontends/mobile/src/lib/config_gen.ts", "// rn\n") // no public/
		if got := missingDevRuntimeConfig(dir); got != "" {
			t.Fatalf("reported a document for a frontend that consumes none: %q", got)
		}
	})
}

func TestGeneratedCodeNeedsRefresh(t *testing.T) {
	t.Run("missing module trumps staleness", func(t *testing.T) {
		dir := t.TempDir()
		writeFileAt(t, dir, "go.work", "go 1.26.2\n\nuse (\n\t.\n\tgen\n)\n")
		writeFileAt(t, dir, "go.mod", "module example.com/x\n\ngo 1.26.2\n")
		reason, needs := generatedCodeNeedsRefresh(dir)
		if !needs {
			t.Fatal("want needs=true for missing gen module")
		}
		if reason == "" {
			t.Fatal("want a non-empty reason")
		}
	})

	t.Run("fresh tree needs nothing", func(t *testing.T) {
		dir := t.TempDir()
		writeFileAt(t, dir, "go.work", "go 1.26.2\n\nuse (\n\t.\n\tgen\n)\n")
		writeFileAt(t, dir, "go.mod", "module example.com/x\n\ngo 1.26.2\n")
		writeFileAt(t, dir, "gen/go.mod", "module example.com/x/gen\n\ngo 1.26.2\n")
		// proto older than gen: write proto, then bump gen mtime forward.
		proto := writeFileAt(t, dir, "proto/v1/a.proto", "syntax = \"proto3\";\n")
		gen := filepath.Join(dir, "gen/go.mod")
		past := time.Now().Add(-time.Hour)
		future := time.Now().Add(time.Hour)
		if err := os.Chtimes(proto, past, past); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(gen, future, future); err != nil {
			t.Fatal(err)
		}
		if _, needs := generatedCodeNeedsRefresh(dir); needs {
			t.Fatal("want needs=false for fresh tree")
		}
	})

	t.Run("proto newer than gen is stale", func(t *testing.T) {
		dir := t.TempDir()
		writeFileAt(t, dir, "go.work", "go 1.26.2\n\nuse (\n\t.\n\tgen\n)\n")
		writeFileAt(t, dir, "go.mod", "module example.com/x\n\ngo 1.26.2\n")
		gen := writeFileAt(t, dir, "gen/go.mod", "module example.com/x/gen\n\ngo 1.26.2\n")
		proto := writeFileAt(t, dir, "proto/v1/a.proto", "syntax = \"proto3\";\n")
		past := time.Now().Add(-time.Hour)
		future := time.Now().Add(time.Hour)
		if err := os.Chtimes(gen, past, past); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(proto, future, future); err != nil {
			t.Fatal(err)
		}
		reason, needs := generatedCodeNeedsRefresh(dir)
		if !needs {
			t.Fatal("want needs=true when proto newer than gen")
		}
		if reason == "" {
			t.Fatal("want a non-empty reason")
		}
	})

	// A fresh git checkout writes gen/ before proto/ (index order), so the
	// protos are always the newer files. A stamped descriptor must win over
	// that: content decides, in both directions.
	stamped := func(t *testing.T, protoBody string) string {
		t.Helper()
		dir := t.TempDir()
		writeFileAt(t, dir, "go.mod", "module example.com/x\n\ngo 1.26.2\n")
		writeFileAt(t, dir, "proto/v1/a.proto", protoBody)
		sources, err := json.Marshal(codegen.DescriptorSources(dir))
		if err != nil {
			t.Fatal(err)
		}
		gen := writeFileAt(t, dir, "gen/forge_descriptor.json", `{"services": [], "sources": `+string(sources)+`}`)
		past := time.Now().Add(-time.Hour)
		if err := os.Chtimes(gen, past, past); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	t.Run("stamped descriptor matching the protos is fresh even when proto is newer", func(t *testing.T) {
		dir := stamped(t, "syntax = \"proto3\";\n")
		if reason, needs := generatedCodeNeedsRefresh(dir); needs {
			t.Fatalf("a committed, current gen/ was judged stale by mtime: %q", reason)
		}
	})
	t.Run("stamped descriptor not matching the protos is stale even when gen is newer", func(t *testing.T) {
		dir := stamped(t, "syntax = \"proto3\";\n")
		proto := writeFileAt(t, dir, "proto/v1/a.proto", "syntax = \"proto3\";\nmessage Edited {}\n")
		gen := filepath.Join(dir, "gen", "forge_descriptor.json")
		past, future := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
		if err := os.Chtimes(proto, past, past); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(gen, future, future); err != nil {
			t.Fatal(err)
		}
		reason, needs := generatedCodeNeedsRefresh(dir)
		if !needs || reason == "" {
			t.Fatalf("an edited proto behind a newer gen/ was judged fresh (needs=%v, reason=%q)", needs, reason)
		}
	})

	t.Run("no codegen surface is a no-op", func(t *testing.T) {
		dir := t.TempDir()
		writeFileAt(t, dir, "go.mod", "module example.com/x\n\ngo 1.26.2\n")
		if _, needs := generatedCodeNeedsRefresh(dir); needs {
			t.Fatal("want needs=false when project has no go.work/proto")
		}
	})
}

// A fresh clone lacks the gitignored dev document; ensureDevRuntimeConfigs
// must put it back by rendering that one file from the env's KCL — no
// generate pipeline, so no forge-version gate can stand in the way.
func TestEnsureDevRuntimeConfigs_RendersOnlyTheMissingDocument(t *testing.T) {
	dir := frontendConfigProject(t, map[string]string{"dev": "dev"})
	writeFrontendDescriptor(t, dir)
	writeFileAt(t, dir, "frontends/web/src/lib/config_gen.ts", "// generated\n")
	writeFileAt(t, dir, "frontends/web/public/favicon.ico", "x")
	cfg := &config.ProjectConfig{
		Name:      "demo",
		Frontends: []config.FrontendConfig{config.FrontendConfig{Name: "web", Type: "vite"}.WithDir("frontends/web")},
	}
	if got := missingDevRuntimeConfig(dir); got != "frontends/web/public/config.js" {
		t.Fatalf("fixture is wrong: missingDevRuntimeConfig = %q", got)
	}

	n, err := ensureDevRuntimeConfigs(cfg, dir)
	if err != nil {
		t.Fatalf("ensureDevRuntimeConfigs: %v", err)
	}
	if n != 1 {
		t.Errorf("rendered %d document(s), want 1", n)
	}
	body, err := os.ReadFile(filepath.Join(dir, "frontends", "web", "public", "config.js"))
	if err != nil {
		t.Fatalf("config.js not written: %v", err)
	}
	if !strings.Contains(string(body), "__FORGE_CONFIG__") || !strings.Contains(string(body), `"dev"`) {
		t.Errorf("config.js is not dev's runtime config document:\n%s", body)
	}
	// Present now: a second call is a no-op.
	if n, err := ensureDevRuntimeConfigs(cfg, dir); err != nil || n != 0 {
		t.Errorf("second call: n=%d err=%v, want a no-op", n, err)
	}
}
