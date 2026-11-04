package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/internal/naming"
)

// crudMethodSet is, per service (keyed by svc.Name), the rpc names the CRUD
// handler step will implement.
type crudMethodSet map[string]map[string]bool

// collectCRUDMethodNames returns, per service, the RPC method names the CRUD
// handler step will implement. The stub generator skips exactly these, so the
// two steps must agree on BOTH halves of the answer or an rpc vanishes —
// skipped by the stub step and never implemented by the CRUD step, serving
// Unimplemented through the embedded handler with no stub saying so:
//
//   - crudProjected is the CRUD step's own gate (gateCRUDProjection). Under
//     `//forge:no-orm` the CRUD step never runs, so no rpc is CRUD and every
//     one gets a stub. Before this the stub step skipped a CRUD-named rpc
//     anyway, and the rpc got neither.
//   - the set is per service. A flat name set let one service's CRUD
//     CreateTask suppress another service's custom CreateTask stub.
func collectCRUDMethodNames(services []codegen.ServiceDef, projectDir string, crudProjected bool) crudMethodSet {
	if !crudProjected {
		return nil
	}
	entities, err := codegen.ParseEntityProtos(projectDir)
	if err != nil || len(entities) == 0 {
		return nil
	}
	return crudMethodsByService(services, entities)
}

// crudMethodsByService is collectCRUDMethodNames' join, with the entity set
// already resolved.
func crudMethodsByService(services []codegen.ServiceDef, entities []codegen.EntityDef) crudMethodSet {
	names := crudMethodSet{}
	for _, svc := range services {
		for _, cm := range codegen.MatchCRUDMethods(svc, entities) {
			if names[svc.Name] == nil {
				names[svc.Name] = map[string]bool{}
			}
			names[svc.Name][cm.Method.Name] = true
		}
	}
	return names
}

// checkCustomRPCsDelegate refuses a `// forge:custom` rpc whose handler
// package still uses what forge generated while it was CRUD: its op, or (for
// a Create) the create-request factory the scaffolded lifecycle test calls.
//
// Marking an rpc custom stops forge generating both, but the code using them
// is the author's and stays. Left alone, generate would run to its validate
// build, fail on `s.crudCreateTaskOp undefined`, and revert everything it
// wrote — or pass, and leave `go test` failing on the factory. This gives the
// one-line answer before anything is written.
func checkCustomRPCsDelegate(services []codegen.ServiceDef, projectDir string) error {
	var problems []string
	for _, svc := range services {
		var custom []codegen.Method
		for _, m := range svc.Methods {
			if m.Custom {
				custom = append(custom, m)
			}
		}
		if len(custom) == 0 {
			continue
		}
		res, err := codegen.ResolveServiceComponent(projectDir, svc.Name)
		if err != nil {
			return err
		}
		refs, err := codegen.CRUDProjectionReferences(res.Dir, custom)
		if err != nil {
			return fmt.Errorf("scan %s for CRUD projection references: %w", svc.Name, err)
		}
		for _, m := range custom {
			if ref, ok := refs[m.GoName()]; ok {
				problems = append(problems, fmt.Sprintf("%s.%s (internal/handlers/%s/%s uses %s)", svc.Name, m.Name, res.ImportLeaf, ref.At, ref.Symbol))
			}
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("rpc(s) marked `// %s` are still used as CRUD: %s. Forge generates an rpc's crud<Rpc>Op, and a Create's New<Request> test factory, "+
		"only while the rpc is CRUD. Replace the method's body with your own implementation (or delete the method and re-run generate for a pb-through stub), "+
		"and build the test's request yourself; or remove the marker to keep the rpc CRUD",
		codegen.ProtoMarkerCustom, strings.Join(problems, "; "))
}

// checkCustomMarkerPlacement refuses a `// forge:custom` the author wrote above
// an rpc that the compiled descriptor did not attach to it.
//
// The descriptor is the authority, but protobuf's comment attribution is not
// where-you-wrote-it: a block directly under one rpc and followed by a blank
// line becomes THAT rpc's trailing comment (refused at extraction, see
// checkCustomMarkers), and when that rpc already carries a same-line comment
// the block is dropped altogether. Dropped, the marker leaves no trace in the
// descriptor, so the rpc it was written for would quietly stay CRUD. Only the
// source shows it, so this reads the source: every rpc whose raw comment
// blocks carry the marker must be custom in the descriptor too. A proto file
// not under proto/ (an unusual layout) is skipped rather than guessed at.
func checkCustomMarkerPlacement(services []codegen.ServiceDef, projectDir string) error {
	scans := map[string]*codegen.RawProtoScan{}
	var problems []string
	for _, svc := range services {
		path := filepath.Join(projectDir, "proto", filepath.FromSlash(svc.ProtoFile))
		scan, seen := scans[path]
		if !seen {
			if _, err := os.Stat(path); err == nil {
				scan, _ = codegen.ScanRawProtoFiles([]string{path})
			}
			scans[path] = scan
		}
		if scan == nil {
			continue
		}
		written := map[string]bool{}
		for _, r := range scan.RPCs {
			if r.Service == svc.Name && r.Custom {
				written[r.Name] = true
			}
		}
		for _, m := range svc.Methods {
			if written[m.Name] && !m.Custom {
				problems = append(problems, fmt.Sprintf("%s.%s (proto/%s)", svc.Name, m.Name, svc.ProtoFile))
			}
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("the `// %s` written above %s does not reach the compiled descriptor: protobuf gives a comment block that directly follows an rpc, "+
		"and is followed by a blank line, to the rpc ABOVE it (or drops it when that rpc already has a same-line comment). "+
		"Put the marker directly above the rpc it describes, with no blank line between",
		codegen.ProtoMarkerCustom, strings.Join(problems, ", "))
}

// webhookOnlyServiceNames returns the set of service Go-names (e.g.
// "AdminServerService") for services declared in forge.yaml as
// webhook-only — i.e. the forge.yaml service entry has at least one
// webhook AND every RPC in the proto is a CRUD-shaped placeholder with
// no backing entity. These services scaffolded a default CRUD proto
// (Create/Get/Update/Delete/List) at `forge project new --service` time, then
// later got webhooks added; the original CRUD RPCs are now leak-only
// stubs that ship as Unimplemented errors but never get called.
//
// Detection rule (matches FORGE_REVIEW_REBUILD.md §3.5):
//
//  1. service has webhooks declared in forge.yaml, AND
//  2. every RPC in the proto is one of {Create, Get, Update, Delete,
//     List} with NO matching entity definition (i.e. crudMethodNames
//     from collectCRUDMethodNames does NOT contain the RPC name).
//
// When both hold, GenerateMissingHandlerStubs treats the proto's RPCs
// as logically-absent: no per-RPC handler stubs are scaffolded.
//
// The return is keyed by the service's Go name (svc.Name) — the same
// key used elsewhere in the stub-generation pass.
func webhookOnlyServiceNames(services []codegen.ServiceDef, projectDir string, crudMethodNames crudMethodSet) map[string]bool {
	out := make(map[string]bool)
	for _, svc := range services {
		// Webhooks are discovered from the webhook_<name>.go files in the
		// service's handler dir (naming.ServicePackage(svc.Name) is the dir
		// leaf), not a declared config list. No webhook files → not a
		// webhook-only service.
		handlerDir := filepath.Join(projectDir, "internal", "handlers", naming.ServicePackage(svc.Name))
		if !codegen.ServiceHasWebhooks(handlerDir) {
			continue
		}
		// Every RPC must be a CRUD-shaped scaffold with no entity.
		// crudMethodNames lists RPCs that DID match an entity; if any
		// of the service's RPCs appear there, the service has at
		// least one real CRUD handler and we leave the stub block alone.
		//
		// "CRUD-shaped" here means either:
		//   - bare names: Create / Get / Update / Delete / List
		//     (the historical user-example.proto.tmpl scaffold default,
		//     pre-convention-fix — kept for projects scaffolded by older
		//     forge versions), OR
		//   - entity-suffixed: CreateItem, GetUser, ... (the current
		//     scaffold default and the typical real-handler shape —
		//     handled by ParseCRUDOperation).
		// Anything else is project-specific surface; we leave the
		// stub block alone.
		bareCRUD := map[string]bool{
			"Create": true, "Get": true, "Update": true,
			"Delete": true, "List": true,
		}
		allOrphan := true
		for _, m := range svc.Methods {
			if crudMethodNames[svc.Name][m.Name] {
				allOrphan = false
				break
			}
			if bareCRUD[m.Name] && !m.Custom {
				continue
			}
			op, _ := codegen.CRUDShape(m)
			if op == "" {
				// Non-CRUD-shaped RPC (e.g. webhook receiver helpers,
				// search endpoints), or one declared `// forge:custom`.
				// The service has real RPC surface; keep emitting stubs
				// so the user has something to fill in.
				allOrphan = false
				break
			}
		}
		if allOrphan && len(svc.Methods) > 0 {
			out[svc.Name] = true
		}
	}
	return out
}

// generateServiceStubs creates service.go, one rpc_<name>.go per custom RPC,
// and wrapper.go for new services.
// For existing service directories, it generates stubs only for missing RPC handlers.
// crudMethodNames contains method names that CRUD gen will implement; stubs are skipped for these.
//
// Webhook-only services (forge.yaml `webhooks:` declared, every proto RPC is a
// CRUD-shaped scaffold-leftover with no entity behind it) get the stub block
// suppressed entirely — their proto's Create/Get/Update/Delete/List RPCs are
// the leftover scaffolding from `forge project new --service`, never the real handler
// surface. See FORGE_REVIEW_REBUILD.md §3.5 (admin_server CRUD-stub leak).
func generateServiceStubs(cfg *config.ProjectConfig, services []codegen.ServiceDef, projectDir string, crudMethodNames crudMethodSet, cs *generator.FileChecksums) error {
	fmt.Println("\n🔧 Generating service stubs...")

	if len(services) == 0 {
		fmt.Println("  No services found in proto/services/")
		return nil
	}

	webhookOnly := webhookOnlyServiceNames(services, projectDir, crudMethodNames)

	hasNewStubs := false
	for _, svc := range services {
		// Disk-first: target the handler directory that actually exists
		// (snake/compact/kebab — whatever era scaffolded it). Pre-fix this
		// synthesized a snake_case dir while the scaffolder created the
		// compact form, so `forge generate` on an AdminServerService could
		// scaffold a SECOND handlers/admin_server next to the scaffolder's
		// handlers/adminserver — the duplicate-dir bug class. The fallback
		// (no dir yet) is the compact form, matching
		// generator.ServicePackageName so scaffold + generate agree.
		res, err := codegen.ResolveServiceComponent(projectDir, svc.Name)
		if err != nil {
			return err
		}
		relServiceDir := "internal/handlers/" + res.ImportLeaf
		absServiceDir := res.Dir

		// Build the per-service skip set: anything CRUD-shaped that
		// matched an entity (already there from crudMethodNames) PLUS
		// every RPC of a webhook-only service. The stub scaffolder's
		// filter drops exactly the methods listed here.
		skipNames := crudMethodNames[svc.Name]
		if webhookOnly[svc.Name] {
			skipNames = make(map[string]bool, len(svc.Methods))
			for _, m := range svc.Methods {
				skipNames[m.Name] = true
			}
		}

		if dirExists(absServiceDir) { //nolint:nestif // per-service existence probe: each arm reports a different reason the service dir cannot be generated into.
			// Incremental: scaffold stubs only for missing RPC methods,
			// one user-owned rpc_<name>.go each (no forge-owned gen file).
			// cs is threaded for signature stability only.
			result, err := codegen.GenerateMissingHandlerStubs(svc, projectDir, absServiceDir, skipNames, cs)
			if err != nil {
				return fmt.Errorf("failed to generate missing stubs for %s: %w", svc.Name, err)
			}
			// Keep the scaffold test's app.NewTest<X>/With<X>Deps references in
			// lockstep with the collision-aware factory name pkg/app/testing.go
			// emits. A domain package registered AFTER the scaffold test was
			// first written (the RPC-vertical sweep's internal/<svc>) flips that
			// name (Widget → SvcWidget); without this the frozen scaffold-once
			// test references an undefined factory and `go vet` breaks.
			if renamed, rerr := codegen.ReconcileScaffoldTestHelperName(projectDir, res.PackageName, absServiceDir); rerr != nil {
				return fmt.Errorf("reconcile scaffold test helper name for %s: %w", svc.Name, rerr)
			} else if renamed {
				fmt.Printf("  ✅ Re-pointed %s/handlers_scaffold_*_test.go at the collision-aware app.NewTest factory\n", relServiceDir)
			}
			if result.AllUpToDate {
				if webhookOnly[svc.Name] {
					routinef("  ⏭️  Skipped %s/ (webhook-only — no CRUD stubs emitted)\n", relServiceDir)
				} else {
					routinef("  ⏭️  Skipped %s/ (all handlers up to date)\n", relServiceDir)
				}
			} else {
				fmt.Printf("  ✅ Scaffolded %d new handler stub(s) under %s/, one file per RPC (yours to edit): %s\n",
					len(result.NewMethods), relServiceDir, strings.Join(result.NewMethods, ", "))
				hasNewStubs = true
			}
			continue
		}

		if err := codegen.GenerateServiceStub(svc, absServiceDir, skipNames); err != nil {
			return fmt.Errorf("failed to generate stub for %s: %w", svc.Name, err)
		}
		fmt.Printf("  ✅ Created %s/\n", relServiceDir)
	}

	if hasNewStubs {
		fmt.Println("  💡 Run 'go build ./...' to verify the new stubs compile")
	}

	return nil
}

// generateCRUDHandlers generates CRUD handler implementations by matching
// service RPC methods against entity protos in proto/db/.
func generateCRUDHandlers(services []codegen.ServiceDef, modulePath string, projectDir string, cs *generator.FileChecksums) error {
	entities, err := codegen.ParseEntityProtos(projectDir)
	if err != nil {
		return fmt.Errorf("parse entity protos: %w", err)
	}
	if len(entities) == 0 {
		return nil
	}

	fmt.Println("\n🔧 Generating CRUD handlers...")
	generated := 0
	for _, svc := range services {
		crudMethods := codegen.MatchCRUDMethods(svc, entities)
		if len(crudMethods) == 0 {
			continue
		}

		pkg := naming.ServicePackage(svc.Name)
		// Each line reports what the call DID (codegen.CRUDResult), never
		// what was asked of it: these used to print "✅ Generated" after
		// calls that wrote nothing, naming files that did not exist.
		wired, err := codegen.GenerateCRUDHandlers(svc, crudMethods, modulePath, projectDir, cs)
		if err != nil {
			// Hard failure by design: the CRUD generator only errors on
			// real wiring problems (e.g. a list filter field that maps to
			// no declared entity column). Shipping a phantom-column query
			// that silently returns nothing is the worse outcome.
			return fmt.Errorf("CRUD generation for %s: %w", svc.Name, err)
		}
		if wired.Wrote {
			fmt.Printf("  ✅ Generated handlers/%s CRUD wiring (%d methods; ops in handlers_crud_ops_gen.go, shims in user-owned handlers_crud.go)\n", pkg, len(crudMethods))
			generated++
		} else {
			fmt.Printf("  ⏭️  handlers/%s: no CRUD wiring written — %s\n", pkg, wired.Skipped)
		}

		lifecycle, err := codegen.GenerateCRUDTests(svc, crudMethods, modulePath, projectDir, cs)
		switch {
		case err != nil:
			// A scaffold problem is a warning: a project without a lifecycle
			// test still generates. The lifecycle test carries no fixture data
			// to get wrong — its rows come from the regenerated
			// create-request factories, which report a fixture the schema
			// rejects themselves (see codegen.GenerateEntityFactories).
			fmt.Fprintf(os.Stderr, "  ⚠️  CRUD test generation for %s failed: %v\n", svc.Name, err)
		case lifecycle.Wrote:
			fmt.Printf("  ✅ Generated handlers/%s/handlers_crud_test.go (CRUD lifecycle against real postgres — scaffolded once, yours from line one)\n", pkg)
		case lifecycle.Routine:
			routinef("  ⏭️  handlers/%s/handlers_crud_test.go not written — %s\n", pkg, lifecycle.Skipped)
		default:
			fmt.Printf("  ⏭️  handlers/%s/handlers_crud_test.go not written — %s\n", pkg, lifecycle.Skipped)
		}
	}

	if generated == 0 {
		fmt.Println("  ℹ️  No CRUD patterns matched")
	}
	return nil
}

// mockOutcomeLine is generateServiceMocks' progress line for svc, naming
// the file GenerateMock writes (codegen.MockRelPath) and saying what the
// call did with it.
func mockOutcomeLine(svc codegen.ServiceDef, res codegen.MockResult) string {
	switch {
	case res.Changed:
		return fmt.Sprintf("  ✅ Updated %s\n", codegen.MockRelPath(svc))
	case res.Wrote:
		return fmt.Sprintf("  ⏭️  %s unchanged\n", codegen.MockRelPath(svc))
	}
	return fmt.Sprintf("  ⏭️  %s not written — %s\n", codegen.MockRelPath(svc), res.Skipped)
}

// generateServiceMocks always regenerates mocks from proto definitions.
// Only a mock whose bytes changed is reported by default: every re-run
// renders every mock, and printing "Updated" for the byte-identical ones
// buried the one that did change (the run's closing summary already
// counted them as unchanged).
func generateServiceMocks(services []codegen.ServiceDef, projectDir string, cs *checksums.FileChecksums) error {
	fmt.Println("🔧 Generating service mocks...")

	if len(services) == 0 {
		return nil
	}

	for _, svc := range services {
		res, err := codegen.GenerateMock(svc, projectDir, filepath.Join(projectDir, "internal", "handlers", "mocks"), cs)
		if err != nil {
			return fmt.Errorf("failed to generate mock for %s: %w", svc.Name, err)
		}
		if res.Changed {
			fmt.Print(mockOutcomeLine(svc, res))
		} else {
			routinef("%s", mockOutcomeLine(svc, res))
		}
	}

	return nil
}

// Service-name → handlers/<dir> mapping is no longer synthesized here:
// generateServiceStubs resolves the existing directory disk-first via
// codegen.ResolveServiceComponent (and only synthesizes the compact form
// for brand-new services). The old toServiceDir helper snake_cased the
// proto name, which disagreed with the compact scaffold form and could
// create duplicate handler dirs on regenerate.
