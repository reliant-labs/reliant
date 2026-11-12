package forgeconv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLintProtoTree_GoodFixtureClean verifies that a canonical proto file
// produces no findings.
func TestLintProtoTree_GoodFixtureClean(t *testing.T) {
	res, err := LintProtoTree(filepath.Join("testdata", "good"))
	if err != nil {
		t.Fatalf("LintProtoTree: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("expected 0 findings on good fixture, got %d:\n%s", len(res.Findings), res.FormatText())
	}
	if res.HasErrors() {
		t.Errorf("good fixture should not have errors")
	}
}

// TestOneServicePerFile_FiresOnSecondService verifies the analyzer fires
// once per extra service (the canonical first one is the one we'd keep,
// so the violation points at SecondService).
func TestOneServicePerFile_FiresOnSecondService(t *testing.T) {
	res, err := LintProtoTree(filepath.Join("testdata", "bad_two_services"))
	if err != nil {
		t.Fatalf("LintProtoTree: %v", err)
	}
	got := findingsForRule(res.Findings, "forgeconv-one-service-per-file")
	if len(got) != 1 {
		t.Fatalf("expected 1 finding for one-service-per-file, got %d:\n%s", len(got), res.FormatText())
	}
	if !strings.Contains(got[0].Message, "SecondService") {
		t.Errorf("finding should reference SecondService; got: %s", got[0].Message)
	}
	if got[0].Severity != SeverityError {
		t.Errorf("one-service-per-file should be an error, got %s", got[0].Severity)
	}
	if !res.HasErrors() {
		t.Errorf("two-services fixture should report errors")
	}
}

// TestParseProtoFile_HandlesMultiLineFieldAnnotations pins the parser's
// ability to span a field's `[ ... ]` annotation across lines. Field
// options are the author's business, not forge's, but a parser that loses
// its place inside one would miscount the fields around it — and the
// surviving rules read that field inventory.
func TestParseProtoFile_HandlesMultiLineFieldAnnotations(t *testing.T) {
	content := `syntax = "proto3";

package x.v1;

import "forge/v1/forge.proto";

message Item {
  option (forge.v1.entity) = {
    table: "items"
    timestamps: true
  };

  string id = 1 [(forge.v1.field) = {
    pk: true
  }];

  string name = 2;
}
`
	pf := parseProtoFile("x.proto", content)
	if len(pf.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(pf.Messages))
	}
	msg := pf.Messages[0]
	if len(msg.Fields) != 2 {
		t.Errorf("expected 2 fields, got %d", len(msg.Fields))
	}
	if msg.Fields[0].Name != "id" || msg.Fields[1].Name != "name" {
		t.Errorf("multi-line annotation lost the parser's place: got %+v", msg.Fields)
	}
}

// TestParseProtoFile_IgnoresVendoredForgeProto verifies that proto files
// inside `proto/forge/` are skipped — those are the vendored annotation
// schemas, not user code, and they declare a (different) FieldOptions
// extension that our regex shouldn't touch.
func TestLintProtoTree_SkipsVendoredForgeAnnotations(t *testing.T) {
	tmp := t.TempDir()
	// Write a proto under proto/forge/v1/ that, if scanned, would
	// definitely trigger findings (multiple "service" tokens, etc.).
	// LintProtoTree must skip it.
	dir := filepath.Join(tmp, "proto", "forge", "v1")
	if err := mkdirAll(dir); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	must(t, writeFile(filepath.Join(dir, "forge.proto"), `syntax = "proto3";
package forge.v1;
service A {}
service B {}
message Thing { string id = 1; }
`))
	res, err := LintProtoTree(tmp)
	if err != nil {
		t.Fatalf("LintProtoTree: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("expected 0 findings (vendored skipped), got %d:\n%s",
			len(res.Findings), res.FormatText())
	}
}

// The internal-package contract-shape, deps-are-interfaces and
// outbound-io-no-rpc tests live in internal/contractcheck/, where those
// rules were consolidated under a unified Inspect engine.
// See internal/contractcheck/*_test.go.

// findingsForRule filters a finding slice to a single rule. Keeps tests
// focused — the analyzer pipeline runs every rule in one pass, so a fixture
// could surface unrelated findings.
func findingsForRule(findings []Finding, rule string) []Finding {
	var out []Finding
	for _, f := range findings {
		if f.Rule == rule {
			out = append(out, f)
		}
	}
	return out
}

// ─── tiny test helpers (keep test file self-contained) ─────────────

func mkdirAll(dir string) error {
	return mkdirAllImpl(dir)
}

func writeFile(path, content string) error {
	return writeFileImpl(path, content)
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
}

// The proto convention rules honour the shared suppression engine. Red
// before: forgeconv never consulted it, so `forgeconv-one-service-per-file`
// had no escape at all (reliant's workflow.proto, H-RELIANT-CI).
func TestLintProtoTree_HonorsLineSuppression(t *testing.T) {
	dir := t.TempDir()
	src := `syntax = "proto3";

package bad.v1;

service FirstService {
  rpc Foo(FooRequest) returns (FooResponse) {}
}

// forge:lint-disable-next-line forgeconv-one-service-per-file: scenario RPCs share the workflow messages; split tracked in #1
service SecondService {
  rpc Bar(FooRequest) returns (FooResponse) {}
}

message FooRequest {}
message FooResponse {}
`
	if err := os.WriteFile(filepath.Join(dir, "api.proto"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := LintProtoTree(dir)
	if err != nil {
		t.Fatalf("LintProtoTree: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("a reasoned suppression must silence the finding:\n%s", res.FormatText())
	}

	// Without the reason it is a gating error of its own.
	bare := strings.Replace(src, ": scenario RPCs share the workflow messages; split tracked in #1", "", 1)
	if err := os.WriteFile(filepath.Join(dir, "api.proto"), []byte(bare), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err = LintProtoTree(dir)
	if err != nil {
		t.Fatalf("LintProtoTree: %v", err)
	}
	if len(res.Findings) != 1 || res.Findings[0].Rule != "forge-suppression-missing-reason" || !res.HasErrors() {
		t.Fatalf("a reasonless suppression must gate:\n%s", res.FormatText())
	}
}
