package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
)

// Emitting `(buf.validate.field)` options REQUIRES importing
// buf/validate/validate.proto. Without it buf fails the entire `forge
// generate` with "unknown extension buf.validate.field" — the proto text looks
// perfectly correct and nothing compiles.
//
// This is the repo's most expensive recurring bug shape: an assertion on the
// emitted TEXT passes while the emitted PROJECT does not build.
//
// The import is also what makes the rules work at all, not just parse: the
// generate pipeline vendors proto/buf/validate/validate.proto ON FIRST USE,
// keyed off a project proto importing it, which is how a fresh scaffold needs
// no BSR dependency.
//
// Both directions matter. Rules must pull the import in; an entity with NO
// rules must not drag in an import it never uses, which buf lint flags. The
// case here is the one production actually hits — the born Create request
// FLATTENS the entity's fields and repeats their rules, so the rules reach a
// message the author did not write, in a file that may not import validate.
//
// This test is the cheap necessary condition. The sufficient one is a real
// scaffold + generate, which lives in the e2e lane.
func TestCompleteEntityCRUDProto_ImportsValidateOnlyWhenRulesAreEmitted(t *testing.T) {
	// The SERVICE file, which never imports validate itself. Splitting the
	// entity into its own file is what makes the test meaningful: the
	// service file must acquire the import because of what was EMITTED into
	// it, not inherit it from the entity's declaration.
	const serviceOnly = `syntax = "proto3";

package services.catalog.v1;

import "forge/v1/forge.proto";

option go_package = "example.com/x/gen/services/catalog/v1;catalogv1";

service CatalogService {
}
`
	for _, tc := range []struct {
		name        string
		entityProto string
		wantImport  bool
	}{
		{
			name: "fields carrying rules pull the import in",
			entityProto: `syntax = "proto3";

package services.catalog.v1;

import "buf/validate/validate.proto";

option go_package = "example.com/x/gen/services/catalog/v1;catalogv1";

// forge:entity
message Widget {
  string id = 1;
  string name = 2 [(buf.validate.field).string = {min_len: 2, max_len: 60}];
}
`,
			wantImport: true,
		},
		{
			name: "fields without rules must NOT drag in an unused import",
			entityProto: `syntax = "proto3";

package services.catalog.v1;

option go_package = "example.com/x/gen/services/catalog/v1;catalogv1";

// forge:entity
message Widget {
  string id = 1;
  string name = 2;
}
`,
			wantImport: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			protoDir := filepath.Join(root, "proto", "services", "catalog", "v1")
			if err := os.MkdirAll(protoDir, 0o755); err != nil {
				t.Fatal(err)
			}
			servicePath := filepath.Join(protoDir, "catalog.proto")
			if err := os.WriteFile(servicePath, []byte(serviceOnly), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(protoDir, "widget.proto"), []byte(tc.entityProto), 0o644); err != nil {
				t.Fatal(err)
			}

			scan, err := codegen.ScanRawProtoDir(protoDir)
			if err != nil {
				t.Fatalf("scan: %v", err)
			}
			entity, ok := scan.MessageByName("Widget")
			if !ok {
				t.Fatal("raw scan did not find the Widget entity message")
			}
			fields, _ := entityFieldsFromSchemaDefs("services.catalog.v1", entity.Fields)
			if _, err := completeEntityCRUDProto(root, servicePath, entity.File, "Widget", fields, false); err != nil {
				t.Fatalf("complete CRUD quintet: %v", err)
			}

			out, err := os.ReadFile(servicePath)
			if err != nil {
				t.Fatal(err)
			}
			got := strings.Contains(string(out), `import "buf/validate/validate.proto";`)

			switch {
			case tc.wantImport && !got:
				t.Errorf("emitted (buf.validate.field) options WITHOUT importing buf/validate/validate.proto — "+
					"buf fails the whole generate with \"unknown extension buf.validate.field\".\n%s", out)
			case !tc.wantImport && got:
				t.Errorf("imported buf/validate/validate.proto with no rules to use it — buf lint flags the unused import.\n%s", out)
			}
		})
	}
}
