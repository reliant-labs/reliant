package forgeconv

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
)

const releasesProto = `syntax = "proto3";

package releases.v1;

service ReleaseService {
  rpc GetRelease(GetReleaseRequest) returns (GetReleaseResponse);
  rpc CreateRelease(CreateReleaseRequest) returns (CreateReleaseResponse);
  rpc PromoteRelease(PromoteReleaseRequest) returns (PromoteReleaseResponse);
  rpc PreviewPromotion(PromoteReleaseRequest) returns (PromoteReleaseResponse);
  rpc RotateKey(PromoteReleaseRequest) returns (PromoteReleaseResponse);
  // forge:lint-disable-next-line forgeconv-activity-declared: migrated next sprint
  rpc Heartbeat(PromoteReleaseRequest) returns (PromoteReleaseResponse);
  rpc RestartWorkload(PromoteReleaseRequest) returns (PromoteReleaseResponse);
}
`

// lintActivity writes proto/services/releases/v1/releases.proto and a
// descriptor whose methods carry activity, then runs the proto pass.
func lintActivity(t *testing.T, methods []codegen.Method) []Finding {
	t.Helper()
	dir := t.TempDir()
	writeReleasesProto(t, dir)
	return lintActivityIn(t, dir, methods)
}

// lintActivityIn is lintActivity over a project dir the caller prepared.
func lintActivityIn(t *testing.T, dir string, methods []codegen.Method) []Finding {
	t.Helper()
	desc := codegen.ForgeDescriptor{Services: []codegen.ServiceDef{{
		Name: "ReleaseService", Package: "releases.v1",
		ProtoFile: "services/releases/v1/releases.proto", Methods: methods,
	}}}
	b, err := json.Marshal(desc)
	must(t, err)
	must(t, os.MkdirAll(filepath.Join(dir, "gen", "forge_descriptor"), 0o755))
	must(t, writeFile(filepath.Join(dir, "gen", "forge_descriptor", "fixture.json"), string(b)))
	res, err := LintProtoTree(filepath.Join(dir, "proto"))
	if err != nil {
		t.Fatalf("LintProtoTree: %v", err)
	}
	return findingsForRule(res.Findings, ruleActivityDeclared)
}

func writeReleasesProto(t *testing.T, dir string) {
	t.Helper()
	pdir := filepath.Join(dir, "proto", "services", "releases", "v1")
	must(t, mkdirAllImpl(pdir))
	must(t, mkdirAllImpl(filepath.Join(dir, "gen")))
	must(t, writeFile(filepath.Join(pdir, "releases.proto"), releasesProto))
}

func activityMethods() []codegen.Method {
	return []codegen.Method{
		{Name: "GetRelease", Activity: &codegen.MethodActivity{}},
		{Name: "CreateRelease", Activity: &codegen.MethodActivity{Mutating: true, Declared: true, Implied: true, Verb: "create", TargetKind: "release"}},
		{Name: "PromoteRelease", Activity: &codegen.MethodActivity{Mutating: true, Declared: true, Verb: "promote", TargetKind: "release"}},
		{Name: "PreviewPromotion", Activity: &codegen.MethodActivity{Mutating: true, Declared: true, None: "dry run"}},
		{Name: "RotateKey", Activity: &codegen.MethodActivity{Mutating: true}},
		{Name: "Heartbeat", Activity: &codegen.MethodActivity{Mutating: true}},
		{Name: "RestartWorkload", Activity: &codegen.MethodActivity{Mutating: true, Error: `invalid activity declaration: target.id: path "workload_idd": releases.v1.PromoteReleaseRequest has no field "workload_idd"`}},
	}
}

func TestActivityDeclared(t *testing.T) {
	got := lintActivity(t, activityMethods())
	if len(got) != 2 {
		t.Fatalf("findings = %d, want 2 (RotateKey undeclared, RestartWorkload malformed; Heartbeat suppressed):\n%v", len(got), got)
	}
	byLine := map[int]Finding{}
	for _, f := range got {
		if f.Severity != SeverityError {
			t.Errorf("severity = %v, want error", f.Severity)
		}
		if f.File != filepath.Join("services", "releases", "v1", "releases.proto") {
			t.Errorf("file = %q", f.File)
		}
		byLine[f.Line] = f
	}
	if f, ok := byLine[10]; !ok || !strings.Contains(f.Message, "ReleaseService.RotateKey mutates") || !strings.Contains(f.Remediation, "none") {
		t.Errorf("RotateKey (line 10) finding = %+v", f)
	}
	if f, ok := byLine[13]; !ok || !strings.Contains(f.Message, `no field "workload_idd"`) {
		t.Errorf("RestartWorkload (line 13) finding = %+v", f)
	}
}

func TestActivityDeclared_StaleDescriptor(t *testing.T) {
	methods := activityMethods()
	for i := range methods {
		methods[i].Activity = nil
	}
	got := lintActivity(t, methods)
	if len(got) != 1 || !strings.Contains(got[0].Message, "gen/forge_descriptor/") || !strings.Contains(got[0].Remediation, "forge generate") {
		t.Fatalf("findings = %+v, want one asking for forge generate", got)
	}
}

func TestActivityDeclared_NoDescriptorNoFindings(t *testing.T) {
	dir := t.TempDir()
	writeReleasesProto(t, dir)
	res, err := LintProtoTree(filepath.Join(dir, "proto"))
	if err != nil {
		t.Fatal(err)
	}
	if got := findingsForRule(res.Findings, ruleActivityDeclared); len(got) != 0 {
		t.Fatalf("findings without a descriptor = %+v, want none", got)
	}
}

// TestActivityDeclared_UnwiredStubIsPending pins the one exception: an
// undeclared mutation whose own handler is still forge's `forge:gen
// unwired-stub` placeholder is a warning naming it, because that rpc
// returns Unimplemented and records nothing. The grain is the rpc: a
// sibling rpc with a real handler stays an error, and a malformed
// declaration stays an error even when its handler is a stub.
func TestActivityDeclared_UnwiredStubIsPending(t *testing.T) {
	dir := t.TempDir()
	writeReleasesProto(t, dir)
	hdir := filepath.Join(dir, "internal", "handlers", "release")
	must(t, mkdirAllImpl(hdir))
	must(t, writeFile(filepath.Join(hdir, "rpc_rotate_key.go"), `package release

// RotateKey is forge's placeholder.
`+codegen.UnwiredStubMarkerComment("release", "RotateKey")+`
func (s *Service) RotateKey() {}

// RestartWorkload is forge's placeholder.
`+codegen.UnwiredStubMarkerComment("release", "RestartWorkload")+`
func (s *Service) RestartWorkload() {}
`))
	methods := activityMethods()
	// PromoteRelease loses its declaration: a second undeclared mutation,
	// with a real handler, which must still gate.
	methods[2] = codegen.Method{Name: "PromoteRelease", Activity: &codegen.MethodActivity{Mutating: true}}
	got := lintActivityIn(t, dir, methods)

	byLine := map[int]Finding{}
	for _, f := range got {
		byLine[f.Line] = f
	}
	if len(got) != 3 {
		t.Fatalf("findings = %d, want 3 (PromoteRelease, RotateKey pending, RestartWorkload malformed):\n%v", len(got), got)
	}
	if f := byLine[10]; f.Severity != SeverityWarning ||
		!strings.HasPrefix(f.Message, "pending: implement RotateKey") ||
		!strings.Contains(f.Message, "internal/handlers/release") ||
		!strings.Contains(f.Remediation, "activity: {") {
		t.Errorf("RotateKey (forge's stub) = %+v, want a pending warning naming the stub and the fix", f)
	}
	if f := byLine[8]; f.Severity != SeverityError || !strings.Contains(f.Message, "PromoteRelease mutates") {
		t.Errorf("PromoteRelease (implemented, undeclared) = %+v, want an error", f)
	}
	if f := byLine[13]; f.Severity != SeverityError || !strings.Contains(f.Message, `no field "workload_idd"`) {
		t.Errorf("RestartWorkload (stub, malformed declaration) = %+v, want an error", f)
	}
}
