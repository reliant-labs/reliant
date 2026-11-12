package lint

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/assets"
	"github.com/reliant-labs/forge/internal/checksums"
)

// writeVendoredFixture lays out a project tree with the vendored protos
// written from the EMBEDDED copies, exactly as scaffold does — including
// the go_package rewrite WriteForgeV1Proto performs. This is the
// "freshly scaffolded" baseline every false-positive test measures from.
func writeVendoredFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := assets.WriteForgeV1Proto(filepath.Join(dir, "proto", "forge", "v1")); err != nil {
		t.Fatalf("WriteForgeV1Proto: %v", err)
	}
	if err := assets.WriteValidateProto(filepath.Join(dir, "proto", "buf", "validate")); err != nil {
		t.Fatalf("WriteValidateProto: %v", err)
	}
	return dir
}

// A scaffolded project's vendored protos are forge's own bytes and must
// report nothing. This is the check's licence to be an ERROR: if the
// clean case were noisy, every project would fail on day one.
//
// It specifically pins the go_package normalization. Scaffold REWRITES
// forge.proto's `option go_package` to point at forge/pkg/forgepb
// (assets.WriteForgeV1Proto), so a byte-for-byte comparison against the
// embedded copy would flag EVERY project on that one line forever.
func TestCollectVendoredProtoFindings_CleanScaffold(t *testing.T) {
	dir := writeVendoredFixture(t)

	findings, err := collectVendoredProtoFindings(dir)
	if err != nil {
		t.Fatalf("collectVendoredProtoFindings: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("expected 0 findings on a freshly scaffolded project, got %d: %+v", len(findings), findings)
	}
}

// A project with no proto tree at all (CLI / library projects) is not an
// error and not a finding — the same "missing dir yields nothing" grade
// the sibling marker checks take.
func TestCollectVendoredProtoFindings_NoProtoTree(t *testing.T) {
	findings, err := collectVendoredProtoFindings(t.TempDir())
	if err != nil {
		t.Fatalf("collectVendoredProtoFindings: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("expected 0 findings with no proto tree, got %+v", findings)
	}
}

// THE MOTIVATING CASE, reproduced from the real control-plane divergence:
// a vendored forge.proto that is MISSING an option field the running
// forge binary defines. Upstream had since RESERVED those field numbers,
// so the project's annotations compiled fine under buf and were read by
// nothing. Nothing in forge noticed for months.
func TestCollectVendoredProtoFindings_FlagsMissingOptionField(t *testing.T) {
	dir := writeVendoredFixture(t)
	path := filepath.Join(dir, "proto", "forge", "v1", "forge.proto")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Delete the frontend_config extension line — precisely the shape of
	// drift control-plane carried (its copy predated FrontendConfigOptions).
	drifted := strings.Replace(string(data),
		"  FrontendConfigOptions frontend_config = 50202;\n", "", 1)
	if drifted == string(data) {
		t.Fatal("fixture setup: frontend_config line not found in embedded forge.proto")
	}
	if err := os.WriteFile(path, []byte(drifted), 0o644); err != nil {
		t.Fatal(err)
	}

	findings, err := collectVendoredProtoFindings(dir)
	if err != nil {
		t.Fatalf("collectVendoredProtoFindings: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %+v", len(findings), findings)
	}
	f := findings[0]
	if f.File != filepath.Join("proto", "forge", "v1", "forge.proto") {
		t.Errorf("file = %q, want the project-relative vendored path", f.File)
	}
	if f.Severity != vendoredSeverityError {
		t.Errorf("severity = %q, want %q — a field-number collision with an upstream reserved is a correctness bug",
			f.Severity, vendoredSeverityError)
	}
	// The finding must name the SPECIFIC difference, not just "differs".
	if !strings.Contains(f.Diff, "frontend_config") {
		t.Errorf("diff does not name the missing line:\n%s", f.Diff)
	}
	// Errors are runbooks: the literal fix command, and the escape hatch.
	hint := vendoredProtoFixHint(f)
	// The hint names the file as f.File does: the native path, which the
	// user's shell takes as-is (and `forge project disown` normalizes).
	for _, want := range []string{"forge project upgrade", "forge project disown", filepath.FromSlash("proto/forge/v1/forge.proto")} {
		if !strings.Contains(hint, want) {
			t.Errorf("fix hint missing %q:\n%s", want, hint)
		}
	}
}

// Drift in the OTHER vendored file is the same class and must also be
// caught — validate.proto is auto-vendored on the first run that imports
// buf.validate and is equally invisible to the upgrade path. Severity is
// warning: forge does not read this file's contents (it is a buf compile
// input only), so a stale copy is a compile-surface question, not a
// silent misread of the author's intent.
func TestCollectVendoredProtoFindings_FlagsValidateProtoDrift(t *testing.T) {
	dir := writeVendoredFixture(t)
	path := filepath.Join(dir, "proto", "buf", "validate", "validate.proto")

	if err := os.WriteFile(path, []byte("syntax = \"proto3\";\n// hand-trimmed\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	findings, err := collectVendoredProtoFindings(dir)
	if err != nil {
		t.Fatalf("collectVendoredProtoFindings: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %+v", len(findings), findings)
	}
	if got := findings[0].Severity; got != vendoredSeverityWarning {
		t.Errorf("severity = %q, want %q for validate.proto", got, vendoredSeverityWarning)
	}
}

// The escape hatch, consistent with how forge already transfers
// ownership: a project that has DELIBERATELY customized its vendored
// copy runs `forge project disown` on the path, and the check goes quiet.
// Without this, a legitimate customization is an unfixable permanent
// error — the "spurious failure across every project" outcome this
// check must not produce.
func TestCollectVendoredProtoFindings_DisownedIsSilent(t *testing.T) {
	dir := writeVendoredFixture(t)
	path := filepath.Join(dir, "proto", "forge", "v1", "forge.proto")

	if err := os.WriteFile(path, []byte("syntax = \"proto3\";\n// ours now\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Sanity: it fires before the disown.
	before, err := collectVendoredProtoFindings(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 {
		t.Fatalf("fixture setup: expected drift to fire before disown, got %+v", before)
	}

	cs, err := checksums.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	rel := filepath.ToSlash(filepath.Join("proto", "forge", "v1", "forge.proto"))
	if err := cs.DisownPaths(dir, []string{rel}, "vendored forge.proto customized on purpose"); err != nil {
		t.Fatalf("DisownPaths: %v", err)
	}
	// DisownPaths mutates in memory; the check reads .forge/disowned.json
	// back off disk, exactly as it will in a real project.
	if err := checksums.Save(dir, cs); err != nil {
		t.Fatalf("Save: %v", err)
	}

	after, err := collectVendoredProtoFindings(dir)
	if err != nil {
		t.Fatalf("collectVendoredProtoFindings: %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("expected disowned vendored proto to be silent, got %+v", after)
	}
}

// A vendored file that is ABSENT is not drift. A project may legitimately
// not import buf.validate (control-plane does not), and forge vendors
// validate.proto lazily on the first run that needs it.
func TestCollectVendoredProtoFindings_AbsentValidateProtoIsClean(t *testing.T) {
	dir := t.TempDir()
	if err := assets.WriteForgeV1Proto(filepath.Join(dir, "proto", "forge", "v1")); err != nil {
		t.Fatal(err)
	}

	findings, err := collectVendoredProtoFindings(dir)
	if err != nil {
		t.Fatalf("collectVendoredProtoFindings: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("expected 0 findings when validate.proto is simply not vendored, got %+v", findings)
	}
}

// Whitespace-only differences are still drift, but the report must stay
// readable: the diff is BOUNDED so a wholesale reformat cannot dump the
// entire file into the terminal.
func TestVendoredProtoDiff_IsBounded(t *testing.T) {
	var want, got bytes.Buffer
	for i := range 500 {
		want.WriteString("line ")
		want.WriteString(strings.Repeat("x", 10))
		want.WriteByte('\n')
		got.WriteString("different ")
		got.WriteString(strings.Repeat("y", 10))
		got.WriteByte('\n')
		_ = i
	}
	diff := vendoredProtoDiff(want.String(), got.String())
	if lines := strings.Count(diff, "\n"); lines > vendoredDiffMaxLines+2 {
		t.Errorf("diff is %d lines, want at most %d+2", lines, vendoredDiffMaxLines)
	}
	if !strings.Contains(diff, "more") {
		t.Errorf("truncated diff should say how much was elided:\n%s", diff)
	}
}

// The check can only cover files it KNOWS forge vendors, and the whole
// failure class is "a copied file nobody tracks". So the spec list is
// pinned against the embedded FS itself: adding a third go:embed'd proto
// to internal/assets without listing it here fails right here, rather
// than silently shipping a fourth blind spot.
func TestVendoredProtoSpecs_CoversEveryEmbeddedProto(t *testing.T) {
	listed := map[string]bool{}
	for _, spec := range vendoredProtoSpecs() {
		listed[spec.RelPath] = true
	}

	var embedded []string
	err := fs.WalkDir(assets.EmbeddedFiles, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".proto") {
			embedded = append(embedded, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk embedded assets: %v", err)
	}
	if len(embedded) == 0 {
		t.Fatal("no embedded protos found — the walk is wrong, not the assets")
	}

	for _, path := range embedded {
		// Embedded paths are "proto/forge/v1/forge.proto"; specs use the
		// same project-relative spelling, since that is where scaffold
		// writes them.
		if !listed[path] {
			t.Errorf("internal/assets embeds %q but vendoredProtoSpecs() does not list it — "+
				"a vendored file forge does not track is exactly the blind spot this check exists to close", path)
		}
	}

	// The same list drives `forge project disown`'s marker exemption, so
	// this check's escape hatch only opens for paths BOTH agree on. If
	// they diverge, the lint reports a file the user cannot disown.
	for _, path := range assets.VendoredProtoRelPaths {
		if !listed[path] {
			t.Errorf("assets.VendoredProtoRelPaths has %q but vendoredProtoSpecs() does not — "+
				"`forge project disown` would accept a path this check never reports", path)
		}
	}
	for _, spec := range vendoredProtoSpecs() {
		if !assets.IsVendoredProtoRelPath(spec.RelPath) {
			t.Errorf("vendoredProtoSpecs() reports %q but assets.VendoredProtoRelPaths omits it — "+
				"this check's `forge project disown` remediation would be refused by that command", spec.RelPath)
		}
	}
}
