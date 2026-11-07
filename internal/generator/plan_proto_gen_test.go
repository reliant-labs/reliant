package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

func TestGeneratePlanProtoFileBasic(t *testing.T) {
	root := t.TempDir()

	rpcs := []config.PlanRPC{
		{
			Name: "SubmitIntakeForm",
			Request: []config.PlanField{
				{Name: "patient_name", Type: "string"},
				{Name: "email", Type: "string"},
				{Name: "phone", Type: "string"},
			},
			Response: []config.PlanField{
				{Name: "patient_id", Type: "string"},
			},
		},
		{
			Name: "GetPatient",
			Request: []config.PlanField{
				{Name: "patient_id", Type: "string"},
			},
			Response: []config.PlanField{
				{Name: "patient", Type: "Patient"},
			},
		},
	}

	if err := GeneratePlanProtoFile(root, "example.com/myapp", "careflow", rpcs, nil); err != nil {
		t.Fatalf("GeneratePlanProtoFile() error = %v", err)
	}

	protoPath := filepath.Join(root, "proto", "services", "careflow", "v1", "careflow.proto")
	content, err := os.ReadFile(protoPath)
	if err != nil {
		t.Fatalf("ReadFile error = %v", err)
	}
	proto := string(content)

	// Check header
	if !strings.Contains(proto, `syntax = "proto3";`) {
		t.Error("missing proto3 syntax declaration")
	}
	if !strings.Contains(proto, "package services.careflow.v1;") {
		t.Error("missing package declaration")
	}
	if !strings.Contains(proto, `option go_package = "example.com/myapp/gen/services/careflow/v1;careflowv1";`) {
		t.Error("missing go_package option")
	}

	// Check service block
	if !strings.Contains(proto, "service CareflowService {") {
		t.Error("missing service declaration")
	}
	if !strings.Contains(proto, "rpc SubmitIntakeForm(SubmitIntakeFormRequest) returns (SubmitIntakeFormResponse) {}") {
		t.Error("missing SubmitIntakeForm RPC")
	}
	if !strings.Contains(proto, "rpc GetPatient(GetPatientRequest) returns (GetPatientResponse) {}") {
		t.Error("missing GetPatient RPC")
	}

	// Check messages
	if !strings.Contains(proto, "message SubmitIntakeFormRequest {") {
		t.Error("missing SubmitIntakeFormRequest message")
	}
	if !strings.Contains(proto, "string patient_name = 1;") {
		t.Error("missing patient_name field")
	}
	if !strings.Contains(proto, "string email = 2;") {
		t.Error("missing email field")
	}
	if !strings.Contains(proto, "string phone = 3;") {
		t.Error("missing phone field")
	}
	if !strings.Contains(proto, "message SubmitIntakeFormResponse {") {
		t.Error("missing SubmitIntakeFormResponse message")
	}
	if !strings.Contains(proto, "string patient_id = 1;") {
		t.Error("missing patient_id field in response")
	}
	if !strings.Contains(proto, "Patient patient = 1;") {
		t.Error("missing patient field in GetPatientResponse")
	}

	// Should NOT have timestamp import
	if strings.Contains(proto, "timestamp.proto") {
		t.Error("timestamp import should not be present when no timestamp fields exist")
	}
}

func TestGeneratePlanProtoFileTimestampImport(t *testing.T) {
	root := t.TempDir()

	rpcs := []config.PlanRPC{
		{
			Name: "CreateEvent",
			Request: []config.PlanField{
				{Name: "name", Type: "string"},
				{Name: "start_time", Type: "timestamp"},
			},
			Response: []config.PlanField{
				{Name: "id", Type: "string"},
			},
		},
	}

	if err := GeneratePlanProtoFile(root, "example.com/myapp", "events", rpcs, nil); err != nil {
		t.Fatalf("GeneratePlanProtoFile() error = %v", err)
	}

	content, err := os.ReadFile(filepath.Join(root, "proto", "services", "events", "v1", "events.proto"))
	if err != nil {
		t.Fatalf("ReadFile error = %v", err)
	}
	proto := string(content)

	if !strings.Contains(proto, `import "google/protobuf/timestamp.proto";`) {
		t.Error("missing timestamp import")
	}
	if !strings.Contains(proto, "google.protobuf.Timestamp start_time = 2;") {
		t.Error("missing timestamp field mapping")
	}
}

func TestGeneratePlanProtoFileRepeatedField(t *testing.T) {
	root := t.TempDir()

	rpcs := []config.PlanRPC{
		{
			Name: "ListTags",
			Request: []config.PlanField{
				{Name: "resource_id", Type: "string"},
			},
			Response: []config.PlanField{
				{Name: "tags", Type: "repeated string"},
			},
		},
	}

	if err := GeneratePlanProtoFile(root, "example.com/myapp", "tags", rpcs, nil); err != nil {
		t.Fatalf("GeneratePlanProtoFile() error = %v", err)
	}

	content, err := os.ReadFile(filepath.Join(root, "proto", "services", "tags", "v1", "tags.proto"))
	if err != nil {
		t.Fatalf("ReadFile error = %v", err)
	}
	proto := string(content)

	if !strings.Contains(proto, "repeated string tags = 1;") {
		t.Error("missing repeated string field")
	}
}

func TestGeneratePlanProtoFileRPCDescription(t *testing.T) {
	root := t.TempDir()

	rpcs := []config.PlanRPC{
		{
			Name:        "Ping",
			Description: "Health check endpoint",
			Request:     []config.PlanField{},
			Response:    []config.PlanField{},
		},
	}

	if err := GeneratePlanProtoFile(root, "example.com/myapp", "health", rpcs, nil); err != nil {
		t.Fatalf("GeneratePlanProtoFile() error = %v", err)
	}

	content, err := os.ReadFile(filepath.Join(root, "proto", "services", "health", "v1", "health.proto"))
	if err != nil {
		t.Fatalf("ReadFile error = %v", err)
	}
	proto := string(content)

	if !strings.Contains(proto, "// Health check endpoint") {
		t.Error("missing RPC description comment")
	}
}

func TestGeneratePlanProtoFileOverwritesExisting(t *testing.T) {
	root := t.TempDir()

	// Pre-create a proto stub
	protoDir := filepath.Join(root, "proto", "services", "orders", "v1")
	if err := os.MkdirAll(protoDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(protoDir, "orders.proto"), []byte("// old stub"), 0644); err != nil {
		t.Fatal(err)
	}

	rpcs := []config.PlanRPC{
		{
			Name:    "CreateOrder",
			Request: []config.PlanField{{Name: "item", Type: "string"}},
		},
	}

	if err := GeneratePlanProtoFile(root, "example.com/myapp", "orders", rpcs, nil); err != nil {
		t.Fatalf("GeneratePlanProtoFile() error = %v", err)
	}

	content, err := os.ReadFile(filepath.Join(protoDir, "orders.proto"))
	if err != nil {
		t.Fatal(err)
	}
	proto := string(content)

	if strings.Contains(proto, "old stub") {
		t.Error("old stub should have been overwritten")
	}
	if !strings.Contains(proto, "rpc CreateOrder") {
		t.Error("new proto should contain CreateOrder RPC")
	}
}

func TestGeneratePlanProtoFileEntityMessages(t *testing.T) {
	root := t.TempDir()

	rpcs := []config.PlanRPC{
		{
			Name: "CreatePatient",
			Request: []config.PlanField{
				{Name: "name", Type: "string"},
				{Name: "email", Type: "string"},
			},
			Response: []config.PlanField{
				{Name: "patient", Type: "Patient"},
			},
		},
		{
			Name: "GetPatient",
			Request: []config.PlanField{
				{Name: "id", Type: "string"},
			},
			Response: []config.PlanField{
				{Name: "patient", Type: "Patient"},
			},
		},
	}

	entities := []config.PlanEntity{
		{
			Name:       "Patient",
			Timestamps: true,
			SoftDelete: true,
			Fields: []config.PlanEntityField{
				{Name: "id", Type: "string", PrimaryKey: true},
				{Name: "name", Type: "string"},
				{Name: "email", Type: "string"},
				{Name: "org_id", Type: "string"},
			},
		},
	}

	if err := GeneratePlanProtoFile(root, "example.com/myapp", "careflow", rpcs, entities); err != nil {
		t.Fatalf("GeneratePlanProtoFile() error = %v", err)
	}

	content, err := os.ReadFile(filepath.Join(root, "proto", "services", "careflow", "v1", "careflow.proto"))
	if err != nil {
		t.Fatalf("ReadFile error = %v", err)
	}
	proto := string(content)

	// Entity message should be present
	if !strings.Contains(proto, "message Patient {") {
		t.Error("missing Patient entity message")
	}

	// Entity fields
	if !strings.Contains(proto, "string id = 1;") {
		t.Error("missing id field in Patient entity")
	}
	if !strings.Contains(proto, "string name = 2;") {
		t.Error("missing name field in Patient entity")
	}
	if !strings.Contains(proto, "string org_id = 4;") {
		t.Error("missing org_id field in Patient entity")
	}

	// Timestamp fields from Timestamps: true
	if !strings.Contains(proto, "google.protobuf.Timestamp created_at = 5;") {
		t.Error("missing created_at timestamp field")
	}
	if !strings.Contains(proto, "google.protobuf.Timestamp updated_at = 6;") {
		t.Error("missing updated_at timestamp field")
	}

	// SoftDelete field
	if !strings.Contains(proto, "google.protobuf.Timestamp deleted_at = 7;") {
		t.Error("missing deleted_at soft-delete field")
	}

	// Timestamp import should be present (from entity timestamps)
	if !strings.Contains(proto, `import "google/protobuf/timestamp.proto";`) {
		t.Error("missing timestamp import for entity timestamps")
	}

	// CRUD RPCs should reference Patient by bare name
	if !strings.Contains(proto, "Patient patient = 1;") {
		t.Error("CRUD response should reference Patient by bare name")
	}

	// Should NOT have db/v1 import
	if strings.Contains(proto, "db/v1") {
		t.Error("should not have db/v1 import — entities are inline")
	}
}

func TestGeneratePlanProtoFileEntityAutoID(t *testing.T) {
	root := t.TempDir()

	// Entity without explicit id field — should get auto-injected.
	entities := []config.PlanEntity{
		{
			Name: "Task",
			Fields: []config.PlanEntityField{
				{Name: "title", Type: "string"},
				{Name: "done", Type: "bool"},
			},
		},
	}

	if err := GeneratePlanProtoFile(root, "example.com/myapp", "tasks", nil, entities); err != nil {
		t.Fatalf("GeneratePlanProtoFile() error = %v", err)
	}

	content, err := os.ReadFile(filepath.Join(root, "proto", "services", "tasks", "v1", "tasks.proto"))
	if err != nil {
		t.Fatalf("ReadFile error = %v", err)
	}
	proto := string(content)

	// id should be auto-injected as field 1
	if !strings.Contains(proto, "string id = 1;") {
		t.Error("missing auto-injected id field")
	}
	// title should be field 2 (bumped by 1)
	if !strings.Contains(proto, "string title = 2;") {
		t.Error("title should be field 2 when id is auto-injected")
	}
	// done should be field 3
	if !strings.Contains(proto, "bool done = 3;") {
		t.Error("done should be field 3 when id is auto-injected")
	}
}

// TestGeneratePlanProtoFile_RESTDisabled_NoAnnotations verifies that
// without `api.rest: true` in forge.yaml, the generated proto file has
// no google.api.http annotations and no annotations.proto import, even
// for CRUD-prefixed RPCs. This is the default-off contract — existing
// projects regenerate byte-identically.
func TestGeneratePlanProtoFile_RESTDisabled_NoAnnotations(t *testing.T) {
	root := t.TempDir()

	rpcs := []config.PlanRPC{
		{Name: "CreatePatient", Request: []config.PlanField{{Name: "name", Type: "string"}}, Response: []config.PlanField{{Name: "patient", Type: "Patient"}}},
		{Name: "GetPatient", Request: []config.PlanField{{Name: "id", Type: "string"}}, Response: []config.PlanField{{Name: "patient", Type: "Patient"}}},
	}

	// No forge.yaml in root → projectAPIRESTEnabled returns false.
	if err := GeneratePlanProtoFile(root, "example.com/myapp", "careflow", rpcs, nil); err != nil {
		t.Fatalf("GeneratePlanProtoFile() error = %v", err)
	}

	content, err := os.ReadFile(filepath.Join(root, "proto", "services", "careflow", "v1", "careflow.proto"))
	if err != nil {
		t.Fatalf("ReadFile error = %v", err)
	}
	proto := string(content)
	if strings.Contains(proto, "google/api/annotations.proto") {
		t.Errorf("proto should NOT import annotations.proto when api.rest is off; got:\n%s", proto)
	}
	if strings.Contains(proto, "google.api.http") {
		t.Errorf("proto should NOT contain google.api.http annotations when api.rest is off; got:\n%s", proto)
	}
}

// TestGeneratePlanProtoFile_RESTEnabled_CRUDAnnotations verifies that
// CRUD-prefixed RPCs get the standard REST annotations when
// `api.rest: true` is declared in forge.yaml. Non-CRUD RPCs in the
// same service are left unannotated — users add HTTP rules by hand.
func TestGeneratePlanProtoFile_RESTEnabled_CRUDAnnotations(t *testing.T) {
	root := t.TempDir()

	// Write forge.yaml with api.rest enabled.
	yaml := "name: app\nmodule_path: example.com/myapp\napi:\n  rest: true\n"
	if err := os.WriteFile(filepath.Join(root, "forge.yaml"), []byte(yaml), 0644); err != nil {
		t.Fatalf("write forge.yaml: %v", err)
	}

	rpcs := []config.PlanRPC{
		{Name: "CreatePatient", Request: []config.PlanField{{Name: "name", Type: "string"}}, Response: []config.PlanField{{Name: "patient", Type: "Patient"}}},
		{Name: "GetPatient", Request: []config.PlanField{{Name: "id", Type: "string"}}, Response: []config.PlanField{{Name: "patient", Type: "Patient"}}},
		{Name: "ListPatients", Request: []config.PlanField{{Name: "page_size", Type: "int32"}}, Response: []config.PlanField{{Name: "patients", Type: "repeated Patient"}}},
		{Name: "UpdatePatient", Request: []config.PlanField{{Name: "id", Type: "string"}, {Name: "patient", Type: "Patient"}}, Response: []config.PlanField{{Name: "patient", Type: "Patient"}}},
		{Name: "DeletePatient", Request: []config.PlanField{{Name: "id", Type: "string"}}, Response: []config.PlanField{{Name: "ok", Type: "bool"}}},
		// Non-CRUD RPC: should NOT get an annotation.
		{Name: "ApprovePatient", Request: []config.PlanField{{Name: "id", Type: "string"}}, Response: []config.PlanField{{Name: "ok", Type: "bool"}}},
	}

	if err := GeneratePlanProtoFile(root, "example.com/myapp", "careflow", rpcs, nil); err != nil {
		t.Fatalf("GeneratePlanProtoFile() error = %v", err)
	}

	content, err := os.ReadFile(filepath.Join(root, "proto", "services", "careflow", "v1", "careflow.proto"))
	if err != nil {
		t.Fatalf("ReadFile error = %v", err)
	}
	proto := string(content)

	// Annotations import must be present.
	if !strings.Contains(proto, `import "google/api/annotations.proto";`) {
		t.Errorf("proto should import annotations.proto when api.rest is on; got:\n%s", proto)
	}

	cases := []struct {
		method   string
		expected string
	}{
		{"CreatePatient", `option (google.api.http) = { post: "/v1/patients" body: "*" };`},
		{"GetPatient", `option (google.api.http) = { get: "/v1/patients/{id}" };`},
		{"ListPatients", `option (google.api.http) = { get: "/v1/patients" };`},
		{"UpdatePatient", `option (google.api.http) = { patch: "/v1/patients/{id}" body: "*" };`},
		{"DeletePatient", `option (google.api.http) = { delete: "/v1/patients/{id}" };`},
	}
	for _, c := range cases {
		if !strings.Contains(proto, c.expected) {
			t.Errorf("%s should have annotation %q; got proto:\n%s", c.method, c.expected, proto)
		}
	}

	// Non-CRUD RPC must not have an annotation.
	approveBlock := proto[strings.Index(proto, "ApprovePatient"):]
	if idx := strings.Index(approveBlock, "\n}"); idx > 0 {
		approveBlock = approveBlock[:idx]
	}
	if strings.Contains(approveBlock, "google.api.http") {
		t.Errorf("ApprovePatient is non-CRUD and should NOT have an annotation; got:\n%s", approveBlock)
	}
}

// TestUpgradeBufYAML_RESTToggle verifies that buf.yaml switches between the
// bare default and the googleapis-dep block based on `api.rest`. buf.yaml is
// a Tier-2 managed file, so it is (re)rendered by `forge project upgrade` (Upgrade),
// not by the Tier-1-only RegenerateInfraFiles. force=true re-renders it
// regardless of the pristine/edited check, isolating the api.rest→dep linkage
// this test cares about: the dep must appear when REST flips on and vanish
// when it flips off.
func TestUpgradeBufYAML_RESTToggle(t *testing.T) {
	root := t.TempDir()
	g := NewProjectGenerator("buf-toggle", filepath.Join(root, "app"), "example.com/buf-toggle")
	g.ServiceName = "api"
	if err := g.Generate(); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	cfg, err := ReadProjectConfig(filepath.Join(g.Path, "forge.yaml"))
	if err != nil {
		t.Fatalf("ReadProjectConfig error = %v", err)
	}

	// REST off → bare deps comment block.
	if _, err := Upgrade(g.Path, cfg, true, false); err != nil {
		t.Fatalf("Upgrade(rest=off) error = %v", err)
	}
	buf, err := os.ReadFile(filepath.Join(g.Path, "buf.yaml"))
	if err != nil {
		t.Fatalf("ReadFile(buf.yaml) error = %v", err)
	}
	if strings.Contains(string(buf), "\n  - buf.build/googleapis/googleapis") {
		t.Errorf("buf.yaml should NOT declare googleapis dep when api.rest is off; got:\n%s", buf)
	}

	// Flip REST on → re-render — googleapis dep appears.
	cfg.API.REST = true
	if _, err := Upgrade(g.Path, cfg, true, false); err != nil {
		t.Fatalf("Upgrade(rest=on) error = %v", err)
	}
	buf, err = os.ReadFile(filepath.Join(g.Path, "buf.yaml"))
	if err != nil {
		t.Fatalf("ReadFile(buf.yaml) error = %v", err)
	}
	if !strings.Contains(string(buf), "\n  - buf.build/googleapis/googleapis") {
		t.Errorf("buf.yaml should declare googleapis dep when api.rest is on; got:\n%s", buf)
	}

	// Flip REST back off → re-render — dep disappears (no on-disk drift).
	cfg.API.REST = false
	if _, err := Upgrade(g.Path, cfg, true, false); err != nil {
		t.Fatalf("Upgrade(rest=off again) error = %v", err)
	}
	buf, err = os.ReadFile(filepath.Join(g.Path, "buf.yaml"))
	if err != nil {
		t.Fatalf("ReadFile(buf.yaml, off again) error = %v", err)
	}
	if strings.Contains(string(buf), "\n  - buf.build/googleapis/googleapis") {
		t.Errorf("buf.yaml should remove googleapis dep when api.rest flips back off; got:\n%s", buf)
	}
}
