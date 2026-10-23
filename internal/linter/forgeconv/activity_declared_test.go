package forgeconv

import (
	"encoding/json"
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
	desc := codegen.ForgeDescriptor{Services: []codegen.ServiceDef{{
		Name: "ReleaseService", Package: "releases.v1",
		ProtoFile: "services/releases/v1/releases.proto", Methods: methods,
	}}}
	b, err := json.Marshal(desc)
	must(t, err)
	must(t, writeFile(filepath.Join(dir, "gen", "forge_descriptor.json"), string(b)))
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
	if len(got) != 1 || !strings.Contains(got[0].Message, "gen/forge_descriptor.json") || !strings.Contains(got[0].Remediation, "forge generate") {
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
