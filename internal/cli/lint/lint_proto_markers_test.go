package lint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
)

func writeProtoMarkersFixture(t *testing.T, name, proto string) string {
	t.Helper()
	dir := t.TempDir()
	protoDir := filepath.Join(dir, "proto", "services", "demo", "v1")
	if err := os.MkdirAll(protoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(protoDir, name), []byte(proto), 0o644); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "proto")
}

// The motivating case. forge:server-set was the spelling before the marker
// was renamed to forge:read-only; an author who writes it today gets a field
// that stays client-writable on Create/Update, exit zero, and no warning
// anywhere. This check is the one place that surfaces it — and it names the
// rename rather than a generic "unrecognized".
func TestCollectProtoMarkerFindings_FlagsRemovedServerSet(t *testing.T) {
	dir := writeProtoMarkersFixture(t, "order.proto", `syntax = "proto3";

// forge:entity
message Order {
  string id = 1;
  int64 amount_cents = 2; // forge:server-set
}
`)
	findings, err := collectProtoMarkerFindings(dir)
	if err != nil {
		t.Fatalf("collectProtoMarkerFindings: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %+v", len(findings), findings)
	}
	f := findings[0]
	if f.Marker != "forge:server-set" {
		t.Errorf("marker = %q, want forge:server-set", f.Marker)
	}
	if f.Renamed != codegen.ProtoMarkerReadOnly {
		t.Errorf("renamed = %q, want %q", f.Renamed, codegen.ProtoMarkerReadOnly)
	}
	if f.Line != 6 {
		t.Errorf("line = %d, want 6", f.Line)
	}
	hint := protoMarkerFixHint(f)
	for _, want := range []string{"forge:server-set", "renamed", "forge:read-only", "NOTHING"} {
		if !strings.Contains(hint, want) {
			t.Errorf("fix hint missing %q:\n%s", want, hint)
		}
	}
}

// A check that flags valid markers is worse than no check. Every marker in
// the registry, in both documented positions (leading full-line and trailing
// inline), must produce ZERO findings.
func TestCollectProtoMarkerFindings_KnownMarkersAreClean(t *testing.T) {
	dir := writeProtoMarkersFixture(t, "clean.proto", `syntax = "proto3";

// forge:entity
message Order {
  string id = 1;
  // forge:read-only
  string status = 2;
  string api_token = 3; // forge:secret
  int64 total_cents = 4; // forge:read-only
}

// forge:soft-delete
message Note { string id = 1; }

// forge:append-only
message AuditEvent { string id = 1; }

service DemoService {
  // forge:mutation
  rpc FindAndHoldSeat(Req) returns (Resp);
  rpc IssueRefund(Req) returns (Resp); // forge:mutation
}
`)
	findings, err := collectProtoMarkerFindings(dir)
	if err != nil {
		t.Fatalf("collectProtoMarkerFindings: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("valid markers must produce no findings, got %+v", findings)
	}
}

// Every marker the registry declares must survive the scan on its own, so a
// marker added to KnownProtoMarkers later cannot be flagged by the check
// that is supposed to enforce it.
func TestCollectProtoMarkerFindings_EveryRegisteredMarkerIsClean(t *testing.T) {
	for _, marker := range codegen.KnownProtoMarkers {
		t.Run(marker, func(t *testing.T) {
			dir := writeProtoMarkersFixture(t, "m.proto", "// "+marker+"\nmessage M { string id = 1; }\n")
			findings, err := collectProtoMarkerFindings(dir)
			if err != nil {
				t.Fatalf("collectProtoMarkerFindings: %v", err)
			}
			if len(findings) != 0 {
				t.Errorf("registered marker %q was flagged: %+v", marker, findings)
			}
		})
	}
}

func TestCollectProtoMarkerFindings_TypoGetsDidYouMean(t *testing.T) {
	dir := writeProtoMarkersFixture(t, "typo.proto", `syntax = "proto3";

// forge:entty
message Order { string id = 1; }
`)
	findings, err := collectProtoMarkerFindings(dir)
	if err != nil {
		t.Fatalf("collectProtoMarkerFindings: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %+v", len(findings), findings)
	}
	if findings[0].Suggestion != codegen.ProtoMarkerEntity {
		t.Errorf("suggestion = %q, want %q", findings[0].Suggestion, codegen.ProtoMarkerEntity)
	}
	if hint := protoMarkerFixHint(findings[0]); !strings.Contains(hint, "did you mean") {
		t.Errorf("fix hint missing did-you-mean:\n%s", hint)
	}
}

// Prose that merely mentions a marker is not a typo of one, and must not get
// an arbitrary marker pinned on it by edit distance.
func TestCollectProtoMarkerFindings_UnrelatedProseGetsNoSuggestion(t *testing.T) {
	dir := writeProtoMarkersFixture(t, "prose.proto", `syntax = "proto3";

// see https://example.test/forge:documentation-for-everything
message Order { string id = 1; }
`)
	findings, err := collectProtoMarkerFindings(dir)
	if err != nil {
		t.Fatalf("collectProtoMarkerFindings: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %+v", len(findings), findings)
	}
	if findings[0].Suggestion != "" {
		t.Errorf("unrelated prose got suggestion %q — edit distance should not reach", findings[0].Suggestion)
	}
}

// A comment with no forge: token at all is never a finding.
func TestCollectProtoMarkerFindings_IgnoresPlainComments(t *testing.T) {
	dir := writeProtoMarkersFixture(t, "plain.proto", `syntax = "proto3";

// An order placed by a customer.
message Order {
  string id = 1; // the primary key
}
`)
	findings, err := collectProtoMarkerFindings(dir)
	if err != nil {
		t.Fatalf("collectProtoMarkerFindings: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("expected no findings for plain comments, got %+v", findings)
	}
}

// Exact-match discipline: a longer marker that merely STARTS with a known
// one is still unknown. Without this, forge:entityset would silently pass.
func TestCollectProtoMarkerFindings_RejectsPrefixCollision(t *testing.T) {
	dir := writeProtoMarkersFixture(t, "prefix.proto", `syntax = "proto3";

// forge:entityset
message Order { string id = 1; }
`)
	findings, err := collectProtoMarkerFindings(dir)
	if err != nil {
		t.Fatalf("collectProtoMarkerFindings: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding for forge:entityset, got %d: %+v", len(findings), findings)
	}
	if findings[0].Marker != "forge:entityset" {
		t.Errorf("marker = %q, want forge:entityset", findings[0].Marker)
	}
}

// Trailing punctuation is prose, not part of the marker: a marker written in
// a sentence must not be reported as a typo of itself.
func TestCollectProtoMarkerFindings_TrailingPunctuationIsNotATypo(t *testing.T) {
	dir := writeProtoMarkersFixture(t, "punct.proto", `syntax = "proto3";

// forge:entity, because orders need a table.
message Order {
  // forge:read-only.
  string status = 1;
}
`)
	findings, err := collectProtoMarkerFindings(dir)
	if err != nil {
		t.Fatalf("collectProtoMarkerFindings: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("markers followed by punctuation must be clean, got %+v", findings)
	}
}

// A forge: inside a proto STRING literal is not a comment and is out of
// scope — the scan only reads text after a `//`.
func TestCollectProtoMarkerFindings_IgnoresStringLiterals(t *testing.T) {
	dir := writeProtoMarkersFixture(t, "literal.proto", `syntax = "proto3";

message Order {
  string kind = 1 [(buf.validate.field).string.const = "forge:not-a-marker"];
}
`)
	findings, err := collectProtoMarkerFindings(dir)
	if err != nil {
		t.Fatalf("collectProtoMarkerFindings: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("string literals are not comments, got %+v", findings)
	}
}

func TestCollectProtoMarkerFindings_MissingDirIsNotAnError(t *testing.T) {
	findings, err := collectProtoMarkerFindings(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("collectProtoMarkerFindings: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("expected no findings, got %+v", findings)
	}
}

func TestCollectProtoMarkersJSON_Shape(t *testing.T) {
	dir := writeProtoMarkersFixture(t, "order.proto", `syntax = "proto3";

message Order {
  int64 amount_cents = 2; // forge:server-set
}
`)
	fs, err := collectProtoMarkersJSON(dir)
	if err != nil {
		t.Fatalf("collectProtoMarkersJSON: %v", err)
	}
	if len(fs) != 1 {
		t.Fatalf("expected 1 JSON finding, got %d: %+v", len(fs), fs)
	}
	f := fs[0]
	if f.Rule != "forge-proto-markers" {
		t.Errorf("rule = %q, want forge-proto-markers", f.Rule)
	}
	if f.Severity != lintSevWarning {
		t.Errorf("severity = %q, want %q (advisory linter)", f.Severity, lintSevWarning)
	}
	if f.FixHint == "" {
		t.Error("missing fix_hint")
	}
}

// The removed spelling must stay INERT. This check improves the message
// about forge:server-set; it must never resurrect it as a working alias, so
// the removed map and the known vocabulary must not overlap.
func TestRemovedProtoMarkersAreNotRecognized(t *testing.T) {
	for removed, replacement := range codegen.RemovedProtoMarkers {
		if codegen.IsKnownProtoMarker(removed) {
			t.Errorf("%q is in RemovedProtoMarkers but still recognized — the old spelling must stay dead", removed)
		}
		if !codegen.IsKnownProtoMarker(replacement) {
			t.Errorf("%q maps to %q, which is not a known marker", removed, replacement)
		}
	}
}
