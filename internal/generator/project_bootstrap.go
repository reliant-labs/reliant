package generator

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/reliant-labs/forge/internal/naming"
	"github.com/reliant-labs/forge/internal/templates"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// generateBootstrapTesting writes the per-service helpers_gen_test.go test
// harness beside each service.
//
// This used to write a single pkg/app/testing.go. That file was a NON-test
// .go file importing "testing", in a package cmd/<proj> imports, so every
// scaffolded project linked package `testing` into its production binary.
// The `_test.go` suffix is the fix — see writeComponentTestHelpers in
// internal/codegen.
func (g *ProjectGenerator) generateBootstrapTesting() error {
	type autoStub struct {
		FieldName          string
		StubType           string
		InterfaceQualified string
		Methods            []struct {
			Name            string
			Params          string
			Results         string
			ReturnStatement string
		}
	}
	type unresolvedStub struct {
		FieldName string
		TypeExpr  string
	}
	// funcDefault / funcTodo mirror codegen.DepsFuncDefault /
	// UnresolvedAutoStub for the Clock/IDGen func-seam harness fill. Always
	// empty at initial scaffold time — the service has no func-typed Deps
	// yet; the post-codegen GenerateBootstrapTesting pass derives them from
	// the on-disk Deps once handlers/<svc>/service.go grows such a field.
	type funcDefault struct {
		FieldName string
		Expr      string
		NeedsTime bool
		NeedsULID bool
	}
	type bootstrapTestService struct {
		Name                   string
		Package                string
		ImportPath             string // handlers/ dir leaf; see generateBootstrap's bootstrapService
		FieldName              string
		ConnectServiceGoName   string
		ProtoConnectImportPath string
		ProtoConnectPkg        string
		MountMethod            string
		Fallible               bool
		HasDB                  bool
		Alias                  string
		VarName                string
		// ConstructorName is the selector the template emits —
		// `<Alias>.<ConstructorName>(...)`. Mirrors
		// codegen.BootstrapTestServiceData.ConstructorName, which resolves the
		// `// forge:constructor` marker; a just-scaffolded service always has
		// the canonical `New`, and the post-codegen GenerateBootstrapTesting
		// pass re-renders with whatever the author's marker says.
		ConstructorName string
		// AutoStubs is always empty at the project-scaffold step; the
		// service has no Deps fields beyond the bare-Deps pair at this
		// point. The post-codegen GenerateBootstrapTesting pass populates
		// it once handlers/<svc>/service.go exists.
		AutoStubs       []autoStub
		UnresolvedStubs []unresolvedStub
		// FuncDefaults / FuncTodos: the Clock/IDGen func-seam harness fill.
		// Empty at scaffold time (see funcDefault doc); the template ranges
		// over them so the fields must exist.
		FuncDefaults []funcDefault
		FuncTodos    []unresolvedStub
	}

	var services []bootstrapTestService
	if g.ServiceName != "" {
		pkg := naming.ServicePackage(g.ServiceName)
		fieldName := naming.ToPascalCase(g.ServiceName)
		// The proto template emits `service {{.ServiceName | pascalCase}}Service`
		// (PascalCase handles hyphens); the client is spelled the way
		// protoc-gen-connect-go spells it from that name.
		connectService := naming.ConnectServiceGoName(naming.ToPascalCase(g.ServiceName) + "Service")
		// Project bootstrap is the first scaffold pass before any descriptor
		// exists; use the convention path. The codegen-pass regenerator will
		// later replace this file with descriptor-derived imports.
		connectPkg := pkg + "v1connect"
		connectImport := g.ModulePath + "/gen/services/" + pkg + "/v1/" + connectPkg
		services = []bootstrapTestService{
			{
				Name:                   g.ServiceName,
				Package:                pkg,
				ImportPath:             pkg,
				FieldName:              fieldName,
				ConnectServiceGoName:   connectService,
				ProtoConnectImportPath: connectImport,
				ProtoConnectPkg:        connectPkg,
				MountMethod:            "Register",
				Alias:                  pkg,
				VarName:                lowerFirstRune(fieldName),
				// Spelled out rather than taken from
				// codegen.DefaultConstructorName: generator is upstream of
				// codegen in the build graph (see extraImport below).
				ConstructorName: "New",
			},
		}
	}

	// extraImport mirrors codegen.ExtraImport. We can't pull the codegen
	// type directly (the generator package is upstream of codegen in the
	// build graph), but the template only reads .Alias / .Path so a
	// structurally-identical local type works. The initial-scaffold
	// pass never has cross-package auto-stubs, so this stays nil here.
	type extraImport struct {
		Alias string
		Path  string
	}

	// One helper file per service, in that service's own directory and
	// package clause. Mirrors codegen.writeComponentTestHelpers; the two
	// render the same template, so a project that is scaffolded and then
	// `forge generate`d converges on identical bytes.
	for _, svc := range services {
		data := struct {
			Module    string
			Package   string
			Name      string
			FieldName string
			IsService bool

			ConstructorName string
			Fallible        bool
			HasDB           bool
			HasLogger       bool
			HasMigrationsFS bool
			NeedsTime       bool
			NeedsULID       bool

			ConnectServiceGoName   string
			ProtoConnectImportPath string
			ProtoConnectPkg        string
			MountMethod            string

			ConfigImport      string
			ConfigImportAlias string
			ConfigDefaults    bool

			// The template ranges over these stub/func-seam slices. They
			// are always empty at initial-scaffold time (the service has no
			// Deps fields beyond the bare pair yet), but the fields must
			// exist for the template to execute — the post-codegen
			// GenerateBootstrapTesting pass populates them for real.
			AutoStubs       []autoStub
			AutoMocks       []autoStub
			UnresolvedStubs []unresolvedStub
			FuncDefaults    []funcDefault
			FuncTodos       []unresolvedStub
			UnfilledDeps    []unresolvedStub

			ExtraImports []extraImport
		}{
			Module:    g.ModulePath,
			Package:   svc.Package,
			Name:      svc.Name,
			FieldName: svc.FieldName,
			IsService: true,

			ConstructorName: svc.ConstructorName,
			Fallible:        svc.Fallible,
			HasDB:           false, // DB deps are added later by forge generate
			HasMigrationsFS: false, // no migrations exist at project creation
			NeedsTime:       false, // Clock/IDGen func defaults are derived post-codegen
			NeedsULID:       false, // (GenerateBootstrapTesting) once services declare them

			ConnectServiceGoName:   svc.ConnectServiceGoName,
			ProtoConnectImportPath: svc.ProtoConnectImportPath,
			ProtoConnectPkg:        svc.ProtoConnectPkg,
			MountMethod:            svc.MountMethod,

			// The config the scaffolded service.go's Deps takes
			// (`Config *config.Config`, from the project's pkg/config).
			// forge generate reads the same field back from the source
			// (codegen.DetectDepsConfigImport), so the two renders agree.
			ConfigImport: g.ModulePath + "/pkg/config",
			// That pkg/config is generated from the scaffolded
			// proto/<project>/config/v1 (an AppConfig root), so it declares Defaults():
			// the harness builds the config production loads with nothing
			// set, not an empty one. forge generate reads it back
			// (codegen.ConfigPackageDeclaresDefaults) to the same answer.
			ConfigDefaults: true,

			ExtraImports: nil, // No cross-package auto-stubs at initial scaffold time
		}

		content, err := templates.ProjectTemplates().Render("component_test_helpers.go.tmpl", data)
		if err != nil {
			return fmt.Errorf("render component_test_helpers.go.tmpl for %s: %w", svc.Name, err)
		}

		destDir := filepath.Join(g.Path, "internal", "handlers", svc.ImportPath)
		if err := os.MkdirAll(destDir, 0755); err != nil {
			return err
		}
		if err := atomicfile.WriteFile(filepath.Join(destDir, "helpers_gen_test.go"), content, 0644); err != nil {
			return err
		}
	}
	return nil
}

// lowerFirstRune returns s with the first rune lowercased — used to
// derive a lowerCamel local-var prefix from a PascalCase FieldName.
// Mirrors codegen.lowerFirst (kept private here to avoid an import
// cycle from generator → codegen).
func lowerFirstRune(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	if r[0] >= 'A' && r[0] <= 'Z' {
		r[0] += 'a' - 'A'
	}
	return string(r)
}
