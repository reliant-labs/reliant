package generator

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/codegen"
)

// A file's OWNERSHIP CLAIM and its CERTIFICATION have to be the same fact.
//
// Every file in the frontend mock surface opens with one of two banners:
//
//	// forge-owned: regenerated every run — do not edit (forge project disown to take ownership)
//	// yours: scaffolded once, never touched again — forge will not overwrite this file
//
// The first names a command; that command read the FILE, not the banner,
// and the whole surface was written with a bare os.WriteFile:
//
//	$ forge project disown frontends/web/src/mocks/products.ts
//	Error: 1 path(s) carry no forge certification (no embedded forge:hash marker)
//	Fix: only forge-certified (Tier-1) files can be disowned; scaffold-once
//	     files are yours from birth — there is nothing to disown.
//
// Two lies in one answer: the header hands you a command that refuses the
// file, and the refusal explains the refusal with something false of it —
// `forge generate` rewrites that file on every run. The same gap made the
// drift lint report "no hand-edits to forge-generated files" for a tree in
// which every one of them had been hand-edited, because that scan reads
// embedded markers too.
//
// The set below is DERIVED from the banner each emitted file carries, not
// from a list of paths: a file added to the surface tomorrow is covered
// the moment it declares which tier it is in.

// TestFrontendMockSurface_CertificationMatchesItsOwnBanner emits the whole
// surface and holds every file to the claim in its own header.
func TestFrontendMockSurface_CertificationMatchesItsOwnBanner(t *testing.T) {
	const (
		tier1Banner    = "forge-owned: regenerated every run"
		scaffoldBanner = "yours: scaffolded once"
	)

	root := t.TempDir()
	cs, err := LoadChecksums(root)
	if err != nil {
		t.Fatalf("load ownership state: %v", err)
	}

	svc := codegen.ServiceDef{
		Name:      "ThingService",
		Package:   "demo.v1",
		ProtoFile: "proto/demo/v1/demo.proto",
		Methods: []codegen.Method{
			{Name: "ListThings", InputType: "ListThingsRequest", OutputType: "ListThingsResponse"},
			{Name: "GetThing", InputType: "GetThingRequest", OutputType: "GetThingResponse"},
			{Name: "CreateThing", InputType: "CreateThingRequest", OutputType: "CreateThingResponse"},
		},
	}
	entity := codegen.EntityDef{
		Name: "Thing", TableName: "things", PkField: "id",
		ProtoFile: "proto/demo/v1/demo.proto",
		Fields: []codegen.EntityField{
			{Name: "id", ProtoType: "string", GoType: "string", Kind: codegen.FieldKindScalar},
			{Name: "name", ProtoType: "string", GoType: "string", Kind: codegen.FieldKindScalar},
		},
	}

	const feRel = "frontends/web"
	if _, err := EmitFrontendMockSurface(root, feRel,
		[]codegen.ServiceDef{svc}, []codegen.EntityDef{entity}, nil, cs); err != nil {
		t.Fatalf("emit mock surface: %v", err)
	}

	var tier1, scaffold int
	err = filepath.WalkDir(filepath.Join(root, feRel), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		rel, _ := filepath.Rel(root, path)
		body := string(content)
		_, marked := checksums.ExtractMarker(content)

		switch {
		case strings.Contains(body, tier1Banner):
			tier1++
			if !marked {
				t.Errorf("%s says it is regenerated every run and points at `forge project disown`, "+
					"but carries no forge:hash marker — disown refuses it and the drift lint cannot see it", rel)
			}
		case strings.Contains(body, scaffoldBanner):
			scaffold++
			if marked {
				t.Errorf("%s says it is yours from birth but carries a forge:hash marker — "+
					"the drift lint will fail the build the first time you edit it", rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk emitted surface: %v", err)
	}

	// Fail loudly rather than pass vacuously: a rename that drops a banner
	// would otherwise silently empty the set this test is about.
	if tier1 == 0 {
		t.Fatal("no emitted file claimed the forge-owned banner — the assertion had nothing to check")
	}
	if scaffold == 0 {
		t.Fatal("no emitted file claimed the scaffold-once banner — the assertion had nothing to check")
	}
}

// TestFrontendMockSurface_ScaffoldOnceFileSurvivesRegeneration pins the
// other half of the claim: the file whose banner says forge will never
// overwrite it must survive a re-emit, now that it goes through the
// scaffold writer rather than an os.Stat guard around os.WriteFile.
func TestFrontendMockSurface_ScaffoldOnceFileSurvivesRegeneration(t *testing.T) {
	root := t.TempDir()
	cs, err := LoadChecksums(root)
	if err != nil {
		t.Fatalf("load ownership state: %v", err)
	}
	const feRel = "frontends/web"
	if _, err := EmitFrontendMockSurface(root, feRel, nil, nil, nil, cs); err != nil {
		t.Fatalf("first emit: %v", err)
	}

	defaultPath := filepath.Join(root, feRel, "src", "mocks", "scenarios", "default.ts")
	const edited = "// HAND-EDITED\n"
	if err := os.WriteFile(defaultPath, []byte(edited), 0o644); err != nil {
		t.Fatalf("hand-edit default.ts: %v", err)
	}

	if _, err := EmitFrontendMockSurface(root, feRel, nil, nil, nil, cs); err != nil {
		t.Fatalf("second emit: %v", err)
	}
	got, err := os.ReadFile(defaultPath)
	if err != nil {
		t.Fatalf("read default.ts: %v", err)
	}
	if string(got) != edited {
		t.Errorf("default.ts was overwritten on re-emit; its banner promises it is yours:\n%s", got)
	}
}

// TestFrontendMockSurface_DisownedFixtureIsNotOverwritten pins the
// contract the header's own command implies: after `forge project disown`,
// the next `forge generate` leaves the file alone.
func TestFrontendMockSurface_DisownedFixtureIsNotOverwritten(t *testing.T) {
	root := t.TempDir()
	cs, err := LoadChecksums(root)
	if err != nil {
		t.Fatalf("load ownership state: %v", err)
	}
	const feRel = "frontends/web"
	svc := codegen.ServiceDef{
		Name: "ThingService", Package: "demo.v1", ProtoFile: "proto/demo/v1/demo.proto",
		Methods: []codegen.Method{
			{Name: "ListThings", InputType: "ListThingsRequest", OutputType: "ListThingsResponse"},
			{Name: "GetThing", InputType: "GetThingRequest", OutputType: "GetThingResponse"},
			{Name: "CreateThing", InputType: "CreateThingRequest", OutputType: "CreateThingResponse"},
		},
	}
	entity := codegen.EntityDef{
		Name: "Thing", TableName: "things", PkField: "id", ProtoFile: "proto/demo/v1/demo.proto",
		Fields: []codegen.EntityField{{Name: "id", ProtoType: "string", GoType: "string", Kind: codegen.FieldKindScalar}},
	}
	services, entities := []codegen.ServiceDef{svc}, []codegen.EntityDef{entity}

	if _, err := EmitFrontendMockSurface(root, feRel, services, entities, nil, cs); err != nil {
		t.Fatalf("first emit: %v", err)
	}

	rel := filepath.Join(feRel, "src", "mocks", "things_gen.ts")
	if err := cs.DisownPaths(root, []string{filepath.ToSlash(rel)}, "test"); err != nil {
		t.Fatalf("disown: %v", err)
	}
	const mine = "// MINE NOW\n"
	if err := os.WriteFile(filepath.Join(root, rel), []byte(mine), 0o644); err != nil {
		t.Fatalf("rewrite disowned fixture: %v", err)
	}

	if _, err := EmitFrontendMockSurface(root, feRel, services, entities, nil, cs); err != nil {
		t.Fatalf("second emit: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if string(got) != mine {
		t.Errorf("a disowned fixture was regenerated; disown promises forge never touches it again:\n%s", got)
	}
}
