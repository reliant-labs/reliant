// File: internal/cli/scaffold/entity_validate_test.go
//
// The contract test for the WIRE half of protovalidate's "one declaration,
// three enforcement points": a rule declared on an entity field must reach
// every request message that FLATTENS that field, because protovalidate
// validates the message it is handed and recurses only into NESTED ones.
// Update<Entity>Request wraps the entity, so it inherits the rules for
// free; Create<Entity>Request flattens, so it must repeat them or nothing
// is enforced — an over-length name would pass the wire, reach the DB, trip
// the CHECK, and come back as Internal instead of InvalidArgument.
//
// The assertions are STRUCTURAL, not textual: the emitted proto is re-read
// with the same raw scanner that read the entity, and each Create field's
// rules are compared against the entity field's. That pins the contract
// ("Create carries the entity's rules for exactly the fields it includes")
// without pinning the spelling of any option, so a change in how the rules
// are rendered cannot break the test while a change in WHICH rules travel
// always does.

package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
)

// validateEntityProto is a service proto whose entity message carries the
// span of rule spellings an author actually writes: two dotted options on
// one field, a braced aggregate, a quoted pattern (embedded brackets and a
// comma — the two characters a naive splitter would break on), a boolean
// rule, a rule on a read-only field, and a field with no rules at all.
const validateEntityProto = `syntax = "proto3";

package services.catalog.v1;

import "forge/v1/forge.proto";
import "buf/validate/validate.proto";

option go_package = "example.com/x/gen/services/catalog/v1;catalogv1";

service CatalogService {
  option (forge.v1.service) = {
    name: "CatalogService"
  };
}

// forge:entity
message Product {
  string id = 1;
  string name = 2 [(buf.validate.field).string.min_len = 1, (buf.validate.field).string.max_len = 120];
  int64 price_cents = 3 [(buf.validate.field).int64 = {gte: 1, lte: 1000000}];
  string sku = 4 [(buf.validate.field).string.pattern = "^SKU-[0-9]{2,8}$"];
  string owner_email = 5 [(buf.validate.field).string.email = true];
  // forge:read-only
  string internal_status = 6 [(buf.validate.field).string.min_len = 3];
  int64 plain_count = 7;
}
`

// completeQuintetForTest runs the real quintet completion over a temp
// project holding validateEntityProto, and returns the re-scanned result:
// the raw messages as forge would next read them, plus the file's text.
func completeQuintetForTest(t *testing.T, proto string) (*codegen.RawProtoScan, string) {
	t.Helper()
	root := t.TempDir()
	protoDir := filepath.Join(root, "proto", "services", "catalog", "v1")
	if err := os.MkdirAll(protoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	protoPath := filepath.Join(protoDir, "catalog.proto")
	if err := os.WriteFile(protoPath, []byte(proto), 0o644); err != nil {
		t.Fatal(err)
	}

	scan, err := codegen.ScanRawProtoDir(protoDir)
	if err != nil {
		t.Fatalf("scan authored proto: %v", err)
	}
	entity, ok := scan.MessageByName("Product")
	if !ok {
		t.Fatal("raw scan did not find the Product entity message")
	}
	fields, _ := entityFieldsFromSchemaDefs("services.catalog.v1", entity.Fields)
	if _, err := completeEntityCRUDProto(root, protoPath, entity.File, "Product", fields, false); err != nil {
		t.Fatalf("complete CRUD quintet: %v", err)
	}

	out, err := codegen.ScanRawProtoDir(protoDir)
	if err != nil {
		t.Fatalf("re-scan completed proto: %v", err)
	}
	raw, err := os.ReadFile(protoPath)
	if err != nil {
		t.Fatal(err)
	}
	return out, string(raw)
}

// rulesByField indexes a scanned message's fields by name -> authored
// `(buf.validate.field)` options block ("" when the field has none).
func rulesByField(t *testing.T, scan *codegen.RawProtoScan, msg string) map[string]string {
	t.Helper()
	m, ok := scan.MessageByName(msg)
	if !ok {
		t.Fatalf("message %s not found in the completed proto", msg)
	}
	out := make(map[string]string, len(m.Fields))
	for _, f := range m.Fields {
		out[f.Name] = f.ValidateOptions
	}
	return out
}

// TestCreateRequestCarriesEntityFieldRules is the contract: every field
// Create<Entity>Request flattens out of the entity carries that entity
// field's protovalidate rules — no more fields, no fewer rules.
func TestCreateRequestCarriesEntityFieldRules(t *testing.T) {
	scan, _ := completeQuintetForTest(t, validateEntityProto)

	entityRules := rulesByField(t, scan, "Product")
	createRules := rulesByField(t, scan, "CreateProductRequest")

	// The field set Create is supposed to carry: every entity field except
	// the managed ones (provided by the CRUD envelopes) and the read-only
	// one (the client must not send it).
	wantFields := []string{"name", "price_cents", "sku", "owner_email", "plain_count"}
	if len(createRules) != len(wantFields) {
		t.Fatalf("CreateProductRequest carries %d field(s), want %d: %v", len(createRules), len(wantFields), createRules)
	}
	for _, name := range wantFields {
		got, present := createRules[name]
		if !present {
			t.Errorf("CreateProductRequest is missing flattened field %q", name)
			continue
		}
		// THE contract: the rules that reached the wire are the rules the
		// entity declared — identical, for every field Create includes.
		if want := entityRules[name]; got != want {
			t.Errorf("CreateProductRequest.%s rules = %q, want the entity's %q", name, got, want)
		}
	}

	// Every rule-bearing field must actually have carried rules — a bug
	// that dropped them ALL would otherwise pass the equality loop above
	// if the entity had none to begin with.
	for _, name := range []string{"name", "price_cents", "sku", "owner_email"} {
		if createRules[name] == "" {
			t.Errorf("CreateProductRequest.%s carries no rules; the entity declares %q", name, entityRules[name])
		}
	}
	// Negative control: an unconstrained field stays bare — rules are
	// copied, never invented.
	if createRules["plain_count"] != "" {
		t.Errorf("unconstrained plain_count gained rules %q", createRules["plain_count"])
	}
}

// TestCreateRequestExcludesReadOnlyFieldAndItsRules pins the exclusion:
// a `// forge:read-only` field is absent from Create, and so is its rule —
// a rule may never outlive the field it constrains.
func TestCreateRequestExcludesReadOnlyFieldAndItsRules(t *testing.T) {
	scan, text := completeQuintetForTest(t, validateEntityProto)

	createRules := rulesByField(t, scan, "CreateProductRequest")
	if _, present := createRules["internal_status"]; present {
		t.Errorf("CreateProductRequest carries the read-only field internal_status: %v", createRules)
	}

	// The read-only field's rule (min_len = 3) is unique in this proto, so
	// finding it inside the Create message text means it leaked in detached
	// from its field.
	create := messageBlock(t, text, "CreateProductRequest")
	if strings.Contains(create, "min_len = 3") {
		t.Errorf("the read-only field's rule leaked into CreateProductRequest:\n%s", create)
	}
	// It is still declared on the entity — excluded from the request
	// surface, never deleted from the wire truth.
	if entityRules := rulesByField(t, scan, "Product"); entityRules["internal_status"] == "" {
		t.Error("the read-only field lost its rules on the entity message")
	}
}

// TestNestingAndFilterRequestsCarryNoFieldRules is the other half of the
// contract — the shapes that must NOT copy. Update NESTS the entity (the
// interceptor recurses into it, so repeating rules would be duplication),
// and List flattens FILTERS rather than values (a rule on the entity's
// value is not a rule on a predicate over it).
func TestNestingAndFilterRequestsCarryNoFieldRules(t *testing.T) {
	_, text := completeQuintetForTest(t, validateEntityProto)

	for _, msg := range []string{"UpdateProductRequest", "ListProductsRequest", "GetProductRequest", "DeleteProductRequest"} {
		if block := messageBlock(t, text, msg); strings.Contains(block, "buf.validate.field") {
			t.Errorf("%s must carry no field rules:\n%s", msg, block)
		}
	}
}

// TestCompletedProtoImportsValidateWhenRulesTravel pins the import the
// emitted options need, in the case that actually needs it: split protos,
// where the entity (and its validate import) live in one file and the
// service block — which receives the flattening Create request — lives in
// another that imports nothing of the sort. It is keyed on what was
// emitted, so a rule-free entity never drags the import in.
func TestCompletedProtoImportsValidateWhenRulesTravel(t *testing.T) {
	const serviceOnly = `syntax = "proto3";

package services.catalog.v1;

import "forge/v1/forge.proto";

option go_package = "example.com/x/gen/services/catalog/v1;catalogv1";

service CatalogService {
  option (forge.v1.service) = {
    name: "CatalogService"
  };
}
`
	const entityOnly = `syntax = "proto3";

package services.catalog.v1;

import "buf/validate/validate.proto";

option go_package = "example.com/x/gen/services/catalog/v1;catalogv1";

// forge:entity
message Product {
  string id = 1;
  string name = 2 [(buf.validate.field).string.min_len = 1, (buf.validate.field).string.max_len = 120];
}
`
	completeSplit := func(t *testing.T, entityProto string) string {
		t.Helper()
		root := t.TempDir()
		protoDir := filepath.Join(root, "proto", "services", "catalog", "v1")
		if err := os.MkdirAll(protoDir, 0o755); err != nil {
			t.Fatal(err)
		}
		servicePath := filepath.Join(protoDir, "catalog.proto")
		entityPath := filepath.Join(protoDir, "product.proto")
		if err := os.WriteFile(servicePath, []byte(serviceOnly), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(entityPath, []byte(entityProto), 0o644); err != nil {
			t.Fatal(err)
		}
		scan, err := codegen.ScanRawProtoDir(protoDir)
		if err != nil {
			t.Fatalf("scan: %v", err)
		}
		entity, ok := scan.MessageByName("Product")
		if !ok {
			t.Fatal("raw scan did not find Product")
		}
		fields, _ := entityFieldsFromSchemaDefs("services.catalog.v1", entity.Fields)
		if _, err := completeEntityCRUDProto(root, servicePath, entity.File, "Product", fields, false); err != nil {
			t.Fatalf("complete CRUD quintet: %v", err)
		}
		out, err := os.ReadFile(servicePath)
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}

	svc := completeSplit(t, entityOnly)
	if n := strings.Count(svc, `import "buf/validate/validate.proto";`); n != 1 {
		t.Errorf("service file imports buf/validate/validate.proto %d times, want 1:\n%s", n, svc)
	}

	// A rule-free entity: nothing to carry, so nothing to import.
	bare := strings.NewReplacer(
		` [(buf.validate.field).string.min_len = 1, (buf.validate.field).string.max_len = 120]`, "",
		"import \"buf/validate/validate.proto\";\n\n", "",
	).Replace(entityOnly)
	if strings.Contains(bare, "buf.validate") {
		t.Fatalf("test fixture still carries rules after stripping:\n%s", bare)
	}
	if svc := completeSplit(t, bare); strings.Contains(svc, "buf/validate/validate.proto") {
		t.Errorf("a rule-free entity must not pull the validate import into the service file:\n%s", svc)
	}
}

// messageBlock returns the text of `message <name> { ... }` from a proto
// file, brace-matched.
func messageBlock(t *testing.T, proto, name string) string {
	t.Helper()
	start := strings.Index(proto, "message "+name+" {")
	if start < 0 {
		t.Fatalf("message %s not found in the completed proto:\n%s", name, proto)
	}
	depth := 0
	for i := start; i < len(proto); i++ {
		switch proto[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return proto[start : i+1]
			}
		}
	}
	t.Fatalf("message %s is unterminated:\n%s", name, proto)
	return ""
}
