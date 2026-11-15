package lint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeProtoOptionsFixture(t *testing.T, name, proto string) string {
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

// THE MOTIVATING CASE. authz_public / required_roles were (forge.v1.method)
// option fields a project's stale vendored forge.proto still declared, on
// field numbers upstream had since RESERVED. buf compiled them (the local
// forge.proto had the fields), and forge — reading its OWN compiled
// descriptor — found nothing. 104 such annotations were inert across 14
// service protos, with no diagnostic anywhere.
//
// This is the check that would have said so on the first run.
func TestCollectProtoOptionFindings_FlagsUnknownMethodOptionField(t *testing.T) {
	dir := writeProtoOptionsFixture(t, "svc.proto", `syntax = "proto3";

package demo.v1;

import "forge/v1/forge.proto";

service DemoService {
  rpc GetThing(GetThingRequest) returns (GetThingResponse) {
    option (forge.v1.method) = { authz_public: true };
  }
}
`)
	findings, err := collectProtoOptionFindings(dir)
	if err != nil {
		t.Fatalf("collectProtoOptionFindings: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %+v", len(findings), findings)
	}
	f := findings[0]
	if f.Field != "authz_public" {
		t.Errorf("field = %q, want authz_public", f.Field)
	}
	if f.Extension != "forge.v1.method" {
		t.Errorf("extension = %q, want forge.v1.method", f.Extension)
	}
	if f.Line != 9 {
		t.Errorf("line = %d, want 9", f.Line)
	}
	// A runbook: name the annotation, say it is read by NOTHING, and list
	// the fields that do exist.
	hint := protoOptionFixHint(f)
	for _, want := range []string{"authz_public", "NOTHING", "auth_required", "forge project annotations"} {
		if !strings.Contains(hint, want) {
			t.Errorf("fix hint missing %q:\n%s", want, hint)
		}
	}
}

// A near-miss earns a did-you-mean, reusing the same Levenshtein
// machinery the comment-marker check uses rather than a parallel one.
func TestCollectProtoOptionFindings_SuggestsClosestField(t *testing.T) {
	dir := writeProtoOptionsFixture(t, "svc.proto", `syntax = "proto3";

service DemoService {
  rpc GetThing(GetThingRequest) returns (GetThingResponse) {
    option (forge.v1.method) = { auth_requird: true };
  }
}
`)
	findings, err := collectProtoOptionFindings(dir)
	if err != nil {
		t.Fatalf("collectProtoOptionFindings: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %+v", len(findings), findings)
	}
	if got := findings[0].Suggestion; got != "auth_required" {
		t.Errorf("suggestion = %q, want auth_required", got)
	}
	if !strings.Contains(protoOptionFixHint(findings[0]), "Did you mean") {
		t.Errorf("near-miss hint should offer a did-you-mean:\n%s", protoOptionFixHint(findings[0]))
	}
}

// Every option field forge actually defines must pass, across every
// extension and every syntactic form real protos use: inline braces,
// multi-line blocks, field-level `[...]` options, message-level options,
// nested submessages (AuthConfig inside ServiceOptions), repeated list
// values, and enum values.
//
// This is the false-positive backbone — the check reads the SAME compiled
// descriptors forge reads, so anything forge honors must be silent here.
func TestCollectProtoOptionFindings_KnownOptionsAreClean(t *testing.T) {
	dir := writeProtoOptionsFixture(t, "svc.proto", `syntax = "proto3";

package demo.v1;

import "forge/v1/forge.proto";
import "google/protobuf/duration.proto";

service DemoService {
  option (forge.v1.service) = {
    name: "DemoService",
    version: "1.0.0",
    description: "demo",
    visibility: SERVICE_VISIBILITY_API,
    dependencies: ["other"],
    auth: { auth_required: true, auth_provider: "jwt" }
  };

  rpc GetThing(GetThingRequest) returns (GetThingResponse) {
    option (forge.v1.method) = {
      auth_required: true,
      idempotent: true,
      timeout: { seconds: 30 },
      idempotency_key: true,
      errors: ["NotFound", "InvalidArgument"]
    };
  }
}

message AppConfig {
  option (forge.v1.binary_config) = {binary: "server"};

  int32 port = 1 [(forge.v1.config) = {
    env_var: "PORT",
    flag: "port",
    default_value: "8080",
    description: "HTTP server port",
    required: true,
    sensitive: false
  }];
}

message WebConfig {
  option (forge.v1.frontend_config) = {frontend: "web"};
}
`)
	findings, err := collectProtoOptionFindings(dir)
	if err != nil {
		t.Fatalf("collectProtoOptionFindings: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("expected 0 findings on valid annotations, got %d: %+v", len(findings), findings)
	}
}

// A forge annotation appearing in a COMMENT is documentation, not a
// declaration — forge.proto's own header and every scaffolded
// config.proto are full of them. Flagging those would fire on a freshly
// scaffolded project, which is the one outcome this check must not have.
func TestCollectProtoOptionFindings_IgnoresCommentedAnnotations(t *testing.T) {
	dir := writeProtoOptionsFixture(t, "svc.proto", `syntax = "proto3";

// Example usage:
//
//   string database_url = 1 [(forge.v1.config) = {
//     env_var: "DATABASE_URL",
//     made_up_field: true,
//     description: "PostgreSQL connection string"
//   }];
//
// Or annotate a method: option (forge.v1.method) = { totally_bogus: true };
message Thing {
  string id = 1;
}
`)
	findings, err := collectProtoOptionFindings(dir)
	if err != nil {
		t.Fatalf("collectProtoOptionFindings: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("commented annotations are documentation, not declarations; got %+v", findings)
	}
}

// A non-forge extension (buf.validate is the one every forge project
// uses) is somebody else's vocabulary and must never be validated
// against forge's descriptors.
func TestCollectProtoOptionFindings_IgnoresForeignExtensions(t *testing.T) {
	dir := writeProtoOptionsFixture(t, "svc.proto", `syntax = "proto3";

import "buf/validate/validate.proto";

message Thing {
  string email = 1 [(buf.validate.field).string.email = true];
  int32 age = 2 [(buf.validate.field) = { int32: { gte: 0, lte: 150 } }];
}
`)
	findings, err := collectProtoOptionFindings(dir)
	if err != nil {
		t.Fatalf("collectProtoOptionFindings: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("foreign extensions are not forge's to validate; got %+v", findings)
	}
}

// The vendored forge.proto DEFINES these option messages; its own field
// declarations (`bool auth_required = 1;`) are definitions, not uses, and
// must not be read as annotation fields. A project's proto tree always
// contains this file, so getting it wrong fires on every project.
func TestCollectProtoOptionFindings_IgnoresVendoredForgeProto(t *testing.T) {
	dir := t.TempDir()
	protoDir := filepath.Join(dir, "proto", "forge", "v1")
	if err := os.MkdirAll(protoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join("..", "..", "assets", "proto", "forge", "v1", "forge.proto"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(protoDir, "forge.proto"), src, 0o644); err != nil {
		t.Fatal(err)
	}

	findings, err := collectProtoOptionFindings(filepath.Join(dir, "proto"))
	if err != nil {
		t.Fatalf("collectProtoOptionFindings: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("the vendored forge.proto defines the options; it does not use them. got %+v", findings)
	}
}

// String VALUES that happen to contain `field: value` shapes must not be
// mined for option field names.
func TestCollectProtoOptionFindings_IgnoresStringValueContents(t *testing.T) {
	dir := writeProtoOptionsFixture(t, "svc.proto", `syntax = "proto3";

service DemoService {
  rpc GetThing(GetThingRequest) returns (GetThingResponse) {
    option (forge.v1.method) = { errors: ["bogus_field: true"] };
  }
}
`)
	findings, err := collectProtoOptionFindings(dir)
	if err != nil {
		t.Fatalf("collectProtoOptionFindings: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("string contents are values, not field names; got %+v", findings)
	}
}

// An empty / absent proto tree is not an error — CLI and library projects
// have none.
func TestCollectProtoOptionFindings_NoProtoTree(t *testing.T) {
	findings, err := collectProtoOptionFindings(filepath.Join(t.TempDir(), "proto"))
	if err != nil {
		t.Fatalf("collectProtoOptionFindings: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("expected no findings, got %+v", findings)
	}
}

// Nested submessage fields are validated against the NESTED message's
// descriptor, not the outer one — `auth: { bogus: true }` is wrong even
// though `auth` itself is a real ServiceOptions field.
func TestCollectProtoOptionFindings_ValidatesNestedSubmessageFields(t *testing.T) {
	dir := writeProtoOptionsFixture(t, "svc.proto", `syntax = "proto3";

service DemoService {
  option (forge.v1.service) = {
    name: "DemoService",
    auth: { auth_requird: true }
  };
}
`)
	findings, err := collectProtoOptionFindings(dir)
	if err != nil {
		t.Fatalf("collectProtoOptionFindings: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %+v", len(findings), findings)
	}
	if findings[0].Field != "auth_requird" {
		t.Errorf("field = %q, want auth_requird", findings[0].Field)
	}
	if findings[0].Message != "forge.v1.AuthConfig" {
		t.Errorf("message = %q, want the NESTED message forge.v1.AuthConfig", findings[0].Message)
	}
}
