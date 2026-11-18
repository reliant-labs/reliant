// File: internal/assets/embedded_test.go
//
// The embedded forge.proto under internal/assets/proto/forge/v1/ is a
// hand-maintained copy of the source-of-truth at proto/forge/v1/. They
// MUST stay byte-identical. Any drift means the scaffolded project gets
// a stale schema — silently. E2E validation of the Tier 2 typed-errors
// work surfaced exactly this class of bug: the source proto declared
// `repeated string errors = 6` but the embedded copy didn't, so
// `forge project new` projects couldn't use the new annotation. This test pins
// the sync as a build-time invariant so future schema bumps can't ship
// half-applied.
//
// HISTORY: the embedded copy previously differed from the source in its
// go_package line, pointing at `github.com/reliant-labs/forge/gen/...`
// — a module that has never existed. WriteForgeV1Proto's per-project
// rewrite literal-matched the SOURCE's go_package line, so against the
// embedded copy it silently no-oped and scaffolds shipped a forge.proto
// whose generated code imported the nonexistent module. The fix: the
// copy is byte-identical (this test), and the rewrite matches the
// go_package option structurally (TestWriteForgeV1ProtoRewritesGoPackage).
package assets

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEmbeddedForgeProtoMatchesSource compares the source-of-truth proto
// against the embedded copy. They must be byte-identical; sync with
//
//	cp proto/forge/v1/forge.proto internal/assets/proto/forge/v1/forge.proto
//
// FRICTION (cp-forge, 2026-06-09): the embedded copy missed the
// `errors = 6` field bump, so `forge project new` projects couldn't use the
// new typed-errors proto annotation even though forge codegen
// fully supported it. The bug surfaced only during end-to-end
// validation against a freshly-scaffolded project — none of the
// codegen-side unit tests caught it because they tested against the
// source proto. This invariant test makes that whole class of skew
// loud-at-build-time.
func TestEmbeddedForgeProtoMatchesSource(t *testing.T) {
	// Walk up from internal/assets/ to the forge repo root so we can
	// reach the source-of-truth at proto/forge/v1/forge.proto. Test
	// runs under `go test ./internal/assets/` so the cwd is the
	// package dir.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// internal/assets → ../../  → repo root
	repoRoot := filepath.Join(cwd, "..", "..")

	sourcePath := filepath.Join(repoRoot, "proto", "forge", "v1", "forge.proto")
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("read source proto at %s: %v", sourcePath, err)
	}

	embedded, err := GetForgeV1Proto()
	if err != nil {
		t.Fatalf("read embedded proto: %v", err)
	}

	if bytes.Equal(source, embedded) {
		return // in sync
	}

	// Out of sync. Render a useful diff: which lines exist in one but
	// not the other. We deliberately don't pull in a diff library — a
	// hand-rolled per-line scan is plenty for a config-shape mismatch
	// and surfaces the specific schema gaps that a future drift might
	// introduce.
	srcLines := strings.Split(string(source), "\n")
	embLines := strings.Split(string(embedded), "\n")
	srcSet := setOf(srcLines)
	embSet := setOf(embLines)

	var missingFromEmbed, missingFromSource []string
	for _, line := range srcLines {
		if !embSet[line] {
			missingFromEmbed = append(missingFromEmbed, line)
		}
	}
	for _, line := range embLines {
		if !srcSet[line] {
			missingFromSource = append(missingFromSource, line)
		}
	}

	var b strings.Builder
	b.WriteString("embedded forge.proto is OUT OF SYNC with the source-of-truth.\n")
	b.WriteString("Sync with:\n  cp proto/forge/v1/forge.proto internal/assets/proto/forge/v1/forge.proto\n\n")
	if len(missingFromEmbed) > 0 {
		b.WriteString("Lines in SOURCE but missing from EMBEDDED:\n")
		for _, l := range missingFromEmbed {
			b.WriteString("  - ")
			b.WriteString(l)
			b.WriteString("\n")
		}
	}
	if len(missingFromSource) > 0 {
		b.WriteString("Lines in EMBEDDED but missing from SOURCE (likely stale or experimental):\n")
		for _, l := range missingFromSource {
			b.WriteString("  + ")
			b.WriteString(l)
			b.WriteString("\n")
		}
	}
	t.Fatal(b.String())
}

// TestWriteForgeV1ProtoRewritesGoPackage pins the load-bearing rewrite under
// "Path A" proto unification: the forge.proto vendored into a scaffolded
// project MUST declare a FIXED go_package pointing at forge's shared forgepb
// package. The project does NOT generate a local gen/forge/v1 copy (buf.gen.yaml
// excludes that path from Go output) and instead links forge/pkg/forgepb, so
// every other generated *.pb.go blank-imports the shared package — a single
// descriptor registration for "forge/v1/forge.proto", so binaries don't panic
// with "already registered".
func TestWriteForgeV1ProtoRewritesGoPackage(t *testing.T) {
	dir := t.TempDir()
	if err := WriteForgeV1Proto(dir); err != nil {
		t.Fatalf("WriteForgeV1Proto: %v", err)
	}
	out, err := os.ReadFile(filepath.Join(dir, "forge.proto"))
	if err != nil {
		t.Fatalf("read written forge.proto: %v", err)
	}
	got := string(out)

	want := `option go_package = "github.com/reliant-labs/forge/pkg/forgepb;forgepb";`
	if !strings.Contains(got, want) {
		t.Errorf("written forge.proto missing fixed forgepb go_package %q", want)
	}
	if strings.Contains(got, "/gen/forge/v1") {
		t.Errorf("written forge.proto still points go_package at a project-local gen/forge/v1 copy")
	}
	if strings.Contains(got, "reliant-labs/forge/gen/") {
		t.Errorf("written forge.proto still references the nonexistent forge/gen module")
	}
	if strings.Contains(got, "reliant-labs/forge/internal/gen/") {
		t.Errorf("written forge.proto still references forge's internal/gen path (unimportable from a project)")
	}
	if n := strings.Count(got, "option go_package"); n != 1 {
		t.Errorf("expected exactly one go_package option, got %d", n)
	}
}

func setOf(lines []string) map[string]bool {
	out := make(map[string]bool, len(lines))
	for _, l := range lines {
		out[l] = true
	}
	return out
}
