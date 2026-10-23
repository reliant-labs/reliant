// File: internal/cli/scaffold/entity_create_nullability_test.go
//
// Regression pin for the NULLABILITY half of the birth contract: a field
// authored `optional` on the entity message must be born `optional` in
// Create<Entity>Request.
//
// Why this is worth its own test even though the behaviour is correct
// today: the label does not travel on a field of its own. entityField
// carries Name / Type / Decl / EnumType / ReadOnly / ValidateOptions and
// NOTHING that spells presence — the label rides INSIDE Type.Proto,
// because protoFieldDecl renders it there ("optional string", not
// "string"). Reading the emit site alone
// (fmt.Fprintf("%s %s = %d", f.Type.Proto, ...) in
// buildEntityCRUDMessagePieces) makes it look like the label is dropped,
// and a well-meant "fix" that added an Optional bool to entityField and
// printed it alongside Type.Proto would emit `optional optional string`.
// This test pins the real invariant — what lands in the request text —
// so the encoding stays free to change but the OUTPUT cannot regress.
//
// The stakes are the reason it is pinned rather than left implicit. A
// dropped label turns a nullable FK into a non-pointer Go string, which
// makes absent and empty-string indistinguishable: the create writes ""
// into a nullable FK column and postgres answers with a foreign-key
// violation, at runtime, far from the proto that caused it.

package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
)

// nullabilityEntityProto declares one entity spanning every label the
// Create request has to reproduce exactly: plain scalars, an optional
// scalar FK (the motivating case), an optional non-string scalar, an
// optional enum (a message-typed label, not just a scalar one), a
// repeated field (proto3 FORBIDS `optional repeated`, so it must never
// acquire the label), and a read-only field (omitted from Create
// entirely — the case rule (a) must not mistake for a dropped label).
const nullabilityEntityProto = `syntax = "proto3";

package services.crm.v1;

import "forge/v1/forge.proto";

option go_package = "example.com/x/gen/services/crm/v1;crmv1";

service CRMService {
  option (forge.v1.service) = {
    name: "CRMService"
  };

  rpc Ping(PingRequest) returns (PingResponse) {}
}

message PingRequest {
  string id = 1;
}

message PingResponse {
  string id = 1;
}

// forge:entity
message Customer {
  string name = 1;
  optional string source_lead_id = 2;
  optional int64 credit_limit_cents = 3;
  optional CustomerTier tier = 4;
  repeated string tags = 5;
  int64 lifetime_value_cents = 6; // forge:read-only
  string id = 7;
}

enum CustomerTier {
  CUSTOMER_TIER_UNSPECIFIED = 0;
  CUSTOMER_TIER_GOLD = 1;
}
`

// birthCustomerCreateRequest runs the REAL birth — raw scan, field
// mapping, quintet injection into an on-disk proto — and returns the
// CreateCustomerRequest message text as it was written to the file.
// Going through the file rather than the piece builder keeps the test
// honest about the whole path: a label lost in the scan, in the mapping,
// or in the emit would all show up here.
func birthCustomerCreateRequest(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "forge.yaml"), []byte("name: x\nmodule: example.com/x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	protoDir := filepath.Join(root, "proto", "services", "crm", "v1")
	if err := os.MkdirAll(protoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	protoPath := filepath.Join(protoDir, "crm.proto")
	if err := os.WriteFile(protoPath, []byte(nullabilityEntityProto), 0o644); err != nil {
		t.Fatal(err)
	}

	scan, err := codegen.ScanRawProtoDir(protoDir)
	if err != nil {
		t.Fatalf("scan authored proto: %v", err)
	}
	m, ok := scan.MessageByName("Customer")
	if !ok {
		t.Fatal("raw scan did not find the Customer entity message")
	}
	fields, _ := entityFieldsFromSchemaDefs("services.crm.v1", m.Fields)
	if _, err := completeEntityCRUDProto(root, protoPath, m.File, "Customer", fields, false); err != nil {
		t.Fatalf("complete CRUD quintet: %v", err)
	}
	out, err := os.ReadFile(protoPath)
	if err != nil {
		t.Fatal(err)
	}
	return messageBody(t, string(out), "CreateCustomerRequest")
}

// messageBody returns the text of one `message <name> { ... }` block.
// Scoping the assertions to the Create request matters: the entity
// message in the same file carries the same field lines, so a
// whole-file strings.Contains would pass even if the Create request
// dropped every label it was supposed to carry.
func messageBody(t *testing.T, content, name string) string {
	t.Helper()
	start := strings.Index(content, "message "+name+" {")
	if start < 0 {
		t.Fatalf("message %s not found in:\n%s", name, content)
	}
	end := strings.Index(content[start:], "\n}")
	if end < 0 {
		t.Fatalf("message %s is unterminated in:\n%s", name, content)
	}
	return content[start : start+end+2]
}

// TestCreateRequestPreservesOptionalLabel pins that every label authored
// on the entity reaches the born Create request unchanged.
func TestCreateRequestPreservesOptionalLabel(t *testing.T) {
	create := birthCustomerCreateRequest(t)

	// Each authored label, reproduced verbatim on the flattened field.
	// Field NUMBERS are deliberately not asserted here (they renumber
	// contiguously around the omitted read-only field, which
	// TestReadOnlyOmittedFromCreateRequest already pins) — this test is
	// about the label.
	for _, want := range []string{
		"optional string source_lead_id",
		"optional int64 credit_limit_cents",
		"optional CustomerTier tier",
	} {
		if !strings.Contains(create, want) {
			t.Errorf("CreateCustomerRequest lost the `optional` label — want %q.\n"+
				"A dropped label generates a non-pointer Go field, so absent and empty-string\n"+
				"become indistinguishable and the create writes \"\" into a nullable column.\nGot:\n%s",
				want, create)
		}
	}

	// proto3 forbids `optional repeated`: the repeated field must carry
	// its own label and only its own. A birth that emitted both would
	// not compile, which is a worse failure than the one above because
	// it takes the whole project's generate down.
	if !strings.Contains(create, "repeated string tags") {
		t.Errorf("CreateCustomerRequest lost the `repeated` label:\n%s", create)
	}
	if strings.Contains(create, "optional repeated") {
		t.Errorf("CreateCustomerRequest emitted `optional repeated`, which proto3 rejects:\n%s", create)
	}

	// The read-only field is absent ENTIRELY — not present-and-unlabelled.
	// This is the shape rule (a) must never read as a nullability
	// disagreement.
	if strings.Contains(create, "lifetime_value_cents") {
		t.Errorf("CreateCustomerRequest must omit the read-only field:\n%s", create)
	}

	// A plain field must NOT acquire a label it was never authored with:
	// the label has to track the authored one, not default on.
	for _, line := range strings.Split(create, "\n") {
		if strings.Contains(line, " name = ") && strings.Contains(line, "optional") {
			t.Errorf("CreateCustomerRequest added `optional` to a field authored without it: %q", line)
		}
	}
}
