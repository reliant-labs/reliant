package codegen

import (
	"testing"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

// TestBuildEntityDef_CrossFileMessageSchemaResolution pins the cross-proto
// import-resolution contract: when a CRUD service is declared in proto file
// B but its domain ENTITY message is declared in a different file A (the
// "split protos" layout — shared domain messages in one file, per-entity
// CRUD services in their own files), the EntityDef's ProtoFile must point at
// A (the message's DEFINING file), not B (the service file).
//
// Downstream codegen imports the entity's `*Schema` and type from the _pb.ts
// module generated for ProtoFile. Resolving to the service file imports a
// symbol the service's _pb module doesn't export, which fails the frontend
// build (the regression this guards against).
func TestBuildEntityDef_CrossFileMessageSchemaResolution(t *testing.T) {
	const (
		sharedFile  = "proto/controlplane/v1/shared.proto" // declares the Daemon MESSAGE
		serviceFile = "proto/services/daemon/v1/daemon.proto"
		pkg         = "controlplane.v1"
		entity      = "Daemon"
	)

	table := schemadef.Table{
		Name:   "daemons",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			{Name: "id", Type: schemadef.CanonicalType("string"), IsPK: true, NotNull: true},
			{Name: "name", Type: schemadef.CanonicalType("string")},
		},
	}

	svc := ServiceDef{
		Name:      "DaemonService",
		Package:   pkg,
		ProtoFile: serviceFile,
		// SchemaFiles records the file that physically declares each message.
		// The Daemon message lives in shared.proto even though the service
		// lives in daemon.proto.
		SchemaFiles: map[string]string{
			pkg + "." + entity:            sharedFile,
			pkg + ".CreateDaemonRequest":  serviceFile,
			pkg + ".CreateDaemonResponse": serviceFile,
		},
	}

	got := buildEntityDef(entity, table, svc)

	if got.ProtoFile != sharedFile {
		t.Fatalf("EntityDef.ProtoFile = %q, want the message's defining file %q (not the service file %q)",
			got.ProtoFile, sharedFile, serviceFile)
	}

	// And the projected mock-data import path must point at the shared
	// module, so `import { DaemonSchema } from "@/gen/controlplane/v1/shared_pb"`.
	mock := EntityDefToMockData(got, svc, nil)
	wantImport := ProtoFileToTSImportPath(sharedFile)
	if mock.ImportPath != wantImport {
		t.Errorf("mock ImportPath = %q, want %q", mock.ImportPath, wantImport)
	}
	if mock.SchemaImport != entity+"Schema" {
		t.Errorf("mock SchemaImport = %q, want %q", mock.SchemaImport, entity+"Schema")
	}
}

// TestBuildEntityDef_SameFileFallback ensures the resolution is a no-op when
// the entity message and service share a proto file (or when SchemaFiles is
// absent on an older descriptor): ProtoFile stays the service file.
func TestBuildEntityDef_SameFileFallback(t *testing.T) {
	const (
		serviceFile = "proto/services/clinic/v1/clinic.proto"
		pkg         = "clinic.v1"
		entity      = "Patient"
	)

	table := schemadef.Table{
		Name:   "patients",
		PKCols: []string{"id"},
		Columns: []schemadef.Column{
			{Name: "id", Type: schemadef.CanonicalType("string"), IsPK: true, NotNull: true},
		},
	}

	// No SchemaFiles entry → fall back to the service proto file.
	svcNoProvenance := ServiceDef{
		Name:      "ClinicService",
		Package:   pkg,
		ProtoFile: serviceFile,
	}
	if got := buildEntityDef(entity, table, svcNoProvenance); got.ProtoFile != serviceFile {
		t.Errorf("missing SchemaFiles: ProtoFile = %q, want service file %q", got.ProtoFile, serviceFile)
	}

	// Same-file provenance → identical result.
	svcSameFile := svcNoProvenance
	svcSameFile.SchemaFiles = map[string]string{pkg + "." + entity: serviceFile}
	if got := buildEntityDef(entity, table, svcSameFile); got.ProtoFile != serviceFile {
		t.Errorf("same-file SchemaFiles: ProtoFile = %q, want service file %q", got.ProtoFile, serviceFile)
	}
}
