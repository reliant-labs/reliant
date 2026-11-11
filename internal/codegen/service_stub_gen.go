package codegen

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/ast/astutil"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/naming"
	"github.com/reliant-labs/forge/internal/templates"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// ReconcileScaffoldTestHelperName re-points the born scaffold tests'
// references to the forge-generated `app.NewTest<X>` / `app.With<X>Deps`
// factories at the collision-aware helper name pkg/app/testing.go actually
// emits for this service, and reports whether it rewrote anything.
//
// It sweeps every scaffold test in the directory (IsScaffoldTestFile), not
// one fixed filename: the scaffold is one file per RPC.
//
// GenerateServiceStub writes the scaffold test ONCE, at `forge project new` /
// `forge scaffold service`, using whatever ComputeTestHelperName resolved
// THEN — before any same-named domain package exists, so the plain form
// (`NewTestWidget`). When the RPC-vertical sweep later creates the
// `internal/<svc>` domain package, GenerateBootstrapTesting / ComputeTestHelperName
// flip the factory to the Svc-prefixed form (`NewTestSvcWidget`) to disambiguate
// it from the domain package's own `NewTestPkg<X>` factory — but the
// scaffold-once test is frozen on the stale name, an undefined reference that
// fails `go vet` on the very next generate. Reconciling ONLY these forge-owned
// factory identifiers on every generate keeps the owned scaffold test in
// lockstep with the regenerated testing.go, without touching the user's own
// test logic. It is idempotent (a no-op once the names already agree) and
// symmetric — it also rewrites back to the plain form if the domain package is
// later removed.
func ReconcileScaffoldTestHelperName(projectDir, servicePkg, targetDir string) (bool, error) {
	entries, err := os.ReadDir(targetDir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	current := ComputeTestHelperName(servicePkg, projectDir)
	pascal := naming.ToPascalCase(servicePkg)
	// A service's factory name is either the plain pascal or the "Svc"-prefixed
	// collision form; the stale name is whichever the current one is not.
	stale := "Svc" + pascal
	if current == stale {
		stale = pascal
	}
	if stale == current { // degenerate (empty servicePkg) — nothing to reconcile
		return false, nil
	}

	rewroteAny := false
	for _, e := range entries {
		if e.IsDir() || !IsScaffoldTestFile(e.Name()) {
			continue
		}
		path := filepath.Join(targetDir, e.Name())
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return rewroteAny, rerr
		}

		// The two prefix rewrites cover every forge-owned factory identifier
		// the scaffold references: `NewTest<X>` (the service under test) and
		// `With<X>Deps` (named in the override comment). `NewTest<X>` also
		// subsumes any `NewTest<X>Server`.
		out := strings.ReplaceAll(string(src), "NewTest"+stale, "NewTest"+current)
		out = strings.ReplaceAll(out, "With"+stale+"Deps", "With"+current+"Deps")
		if out == string(src) {
			continue
		}

		formatted, ferr := format.Source([]byte(out))
		if ferr != nil {
			// A pure identifier rename never breaks the parse; if it somehow
			// did, leave the file untouched rather than write unparseable Go.
			continue
		}
		if werr := atomicfile.WriteFile(path, formatted, 0o644); werr != nil {
			return rewroteAny, werr
		}
		rewroteAny = true
	}
	return rewroteAny, nil
}

// GenerateServiceStub generates service.go plus one handler file per custom
// RPC (RPCHandlerFileName) for a new service, using the embedded FS
// templates. crudMethodNames lists methods that CRUD gen will implement;
// these are excluded from the initial stubs.
func GenerateServiceStub(svc ServiceDef, targetDir string, crudMethodNames ...map[string]bool) error {
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return err
	}

	// Derive projectDir from targetDir's <projectDir>/internal/handlers/<svc>
	// shape so the test-helper-name collision check can probe internal/<pkg>.
	// Day-0, no caller passes a non-conventional targetDir.
	projectDir := filepath.Dir(filepath.Dir(filepath.Dir(targetDir)))
	data := mapServiceDefToTemplateData(svc, projectDir)

	// Render service.go from embedded template
	serviceContent, err := templates.ServiceTemplates().Render("service.go.tmpl", data)
	if err != nil {
		return fmt.Errorf("render service.go.tmpl: %w", err)
	}
	if err := writeUserScaffold(filepath.Join(targetDir, "service.go"), serviceContent); err != nil {
		return err
	}

	// For the handler stubs, filter out methods that CRUD gen will implement.
	var crudNames map[string]bool
	if len(crudMethodNames) > 0 {
		crudNames = crudMethodNames[0]
	}
	handlersData := data
	if len(crudNames) > 0 {
		var nonCRUD []MethodTemplateData
		for _, m := range data.Methods {
			if !crudNames[m.Name] {
				nonCRUD = append(nonCRUD, m)
			}
		}
		handlersData.Methods = nonCRUD
	}

	// One handler file per custom RPC. Zero methods writes zero files — the
	// loop is the guard, so a service with no custom RPCs gets no empty
	// placeholder to delete.
	if err := writeRPCHandlerStubs(targetDir, handlersData); err != nil {
		return err
	}

	// One scaffold test file per RPC (same filter as the stubs — skip CRUD
	// methods). The qualified filenames free the canonical handlers_test.go
	// slot for user-owned tests; forge never touches handlers_test.go.
	if err := writeScaffoldTests(targetDir, handlersData); err != nil {
		return err
	}

	// NOTE: no one-shot integration_test.go scaffold is emitted — see
	// GenerateMissingHandlerStubs for the rationale (one test philosophy
	// per service).

	return nil
}

// RegenerateServiceFile regenerates only service.go for an existing service
// directory, using the proto-derived HandlerName so that Connect RPC references
// (Unimplemented*Handler, New*Handler) match the actual proto service name.
func RegenerateServiceFile(svc ServiceDef, targetDir string) error {
	data := mapServiceDefToTemplateData(svc, filepath.Dir(filepath.Dir(filepath.Dir(targetDir))))

	serviceContent, err := templates.ServiceTemplates().Render("service.go.tmpl", data)
	if err != nil {
		return fmt.Errorf("render service.go.tmpl: %w", err)
	}
	return writeUserScaffold(filepath.Join(targetDir, "service.go"), serviceContent)
}

// MissingHandlerResult holds the result of scanning for missing handler stubs.
type MissingHandlerResult struct {
	NewMethods  []string // names of methods that were generated
	AllUpToDate bool     // true if no new methods were needed
}

// GenerateMissingHandlerStubs scans the existing service directory for implemented
// methods on *Service, compares against the proto ServiceDef, and scaffolds stubs
// only for missing (non-CRUD, not-yet-implemented) methods into their own
// USER-OWNED per-RPC files — "scaffold and forget", not a forge-owned holding pen.
// If all methods are already implemented, it returns AllUpToDate=true.
//
// One RPC, one file (RPCHandlerFileName) — see writeRPCHandlerStubs for the
// per-file write semantics and why the layout is the way it is.
//
// If the user deletes an RPC's file, forge re-scaffolding that stub on the next
// run is acceptable/desired. There is no more handlers_gen.go — ever.
//
// crudMethodNames optionally lists method names that CRUD gen will implement;
// stubs are skipped for these even if they don't exist yet in the package.
//
// cs (the project checksum tracker) is retained for signature stability; the
// user-owned handler files are deliberately NOT checksum-tracked. The
// per-RPC scaffold tests likewise record no checksum: they become user-owned
// once the placeholder is filled in. The canonical handlers_test.go filename
// is reserved for the user. A nil cs is tolerated.
func GenerateMissingHandlerStubs(svc ServiceDef, projectDir, targetDir string, crudMethodNames map[string]bool, cs *checksums.FileChecksums) (*MissingHandlerResult, error) {
	if err := reconcileConnectIdentity(svc, projectDir, targetDir, cs); err != nil {
		return nil, fmt.Errorf("reconcile Connect service identity: %w", err)
	}
	existing, err := ScanExistingMethods(targetDir, false)
	if err != nil {
		return nil, fmt.Errorf("scan existing methods: %w", err)
	}

	// handlers_crud.go is skipped by ScanExistingMethods so its delegating
	// CRUD shims don't masquerade as "user implemented this RPC by hand" and
	// suppress regeneration of the very ops they delegate to. But the file is
	// also the user's own (the scaffold header says so), and a user can hand-
	// implement a non-CRUD RPC there (kalshi fr-fba0c4be8d: a custom-shape
	// ListSettlements with no entity behind it). That hand impl IS a real
	// implementation and MUST suppress the stub, or handlers_gen.go re-emits a
	// duplicate method and the package fails to compile. Discriminate by name:
	// a method in handlers_crud.go whose name is NOT a CRUD method is a hand
	// impl (the CRUD-shaped delegating shims are exactly crudMethodNames).
	//
	// crudMethodNames is keyed by proto RPC name; the scans return Go method
	// names, so every comparison against them goes through Method.GoName.
	crudGoNames := make(map[string]bool, len(crudMethodNames))
	for _, m := range svc.Methods {
		if crudMethodNames[m.Name] {
			crudGoNames[m.GoName()] = true
		}
	}
	for name := range ScanHandlersCrudMethods(targetDir) {
		if !crudGoNames[name] {
			existing[name] = true
		}
	}

	var missing []Method
	for _, m := range svc.Methods {
		if !existing[m.GoName()] && !crudMethodNames[m.Name] {
			missing = append(missing, m)
		}
	}

	if len(missing) == 0 {
		return &MissingHandlerResult{AllUpToDate: true}, nil
	}

	// Build a ServiceDef with only the missing methods for template rendering
	missingSvc := svc
	missingSvc.Methods = missing
	data := mapServiceDefToTemplateData(missingSvc, projectDir)

	// Disk-first: the scaffold lands inside the EXISTING targetDir and MUST
	// declare the same package as the files already there — the synthesized
	// clause from mapServiceDefToTemplateData only holds for fresh scaffolds.
	// Parsing the live clause here keeps a snake_case handler dir (or one whose
	// clause differs from its dir name) from getting a conflicting `package x`
	// stamped into it on regenerate. The import-path leaf for the *_test
	// scaffolds likewise comes from the real directory name.
	diskPkg, perr := ParsePackageClause(targetDir)
	if perr != nil {
		return nil, fmt.Errorf("resolving handler package clause: %w", perr)
	}
	applyDiskIdentity := func(d *ServiceTemplateData) {
		d.ServicePackage = diskPkg
		d.ServiceImportPath = filepath.Base(targetDir)
		d.TestHelperName = ComputeTestHelperName(diskPkg, projectDir)
	}
	applyDiskIdentity(&data)

	if err := writeRPCHandlerStubs(targetDir, data); err != nil {
		return nil, err
	}

	// If integration_test.go / handlers_scaffold_test.go are still placeholders (no RPCs when
	// first generated), regenerate them with actual test scaffolding now that RPCs exist.
	// These files become user-owned after the placeholder is filled in, so we
	// don't checksum them — we want forge project audit to leave them alone.
	fullData := mapServiceDefToTemplateData(svc, projectDir)
	applyDiskIdentity(&fullData)

	// Filter CRUD methods out of the unit-test scaffold so per-RPC rows
	// don't overlap with handlers_crud_test.go (the user-owned lifecycle
	// test that owns CRUD coverage). Same filter rule as the initial-gen
	// path in GenerateServiceStub — one source of truth per method, no
	// duplication.
	unitTestData := fullData
	if len(crudMethodNames) > 0 {
		var nonCRUD []MethodTemplateData
		for _, m := range fullData.Methods {
			if !crudMethodNames[m.Name] {
				nonCRUD = append(nonCRUD, m)
			}
		}
		unitTestData.Methods = nonCRUD
	}

	// NOTE: forge no longer emits a one-shot integration_test.go scaffold.
	// One test philosophy per service: the per-RPC unit scaffolds
	// (handlers_scaffold_<rpc>_test.go) own the self-destructing rows, and
	// handlers_crud_test.go owns the DB-bound CRUD surface.
	// Existing user-owned integration_test.go files are left untouched.
	//
	// The scaffold test lands in the same pass that wrote the RPC's
	// Unimplemented stub, and only for the RPCs this pass stubbed — the
	// scaffold row asserts CodeUnimplemented, so offering it for an RPC
	// somebody has already implemented would hand them a red suite. Absent
	// file + fresh stub is the whole condition; there is no marker comment
	// deciding whether forge may overwrite your file.
	missingNames := make(map[string]bool, len(missing))
	for _, m := range missing {
		missingNames[m.Name] = true
	}
	var freshlyStubbed []MethodTemplateData
	for _, m := range unitTestData.Methods {
		if missingNames[m.Name] {
			freshlyStubbed = append(freshlyStubbed, m)
		}
	}
	unitTestData.Methods = freshlyStubbed
	if err := writeScaffoldTests(targetDir, unitTestData); err != nil {
		return nil, err
	}

	var names []string
	for _, m := range missing {
		names = append(names, m.Name)
	}

	return &MissingHandlerResult{NewMethods: names}, nil
}

// writeRPCHandlerStubs writes ONE FILE PER RPC for the (already-filtered)
// missing methods in data.Methods, into targetDir.
//
// WHY ONE FILE PER RPC. A handler package's file layout is invisible to Go
// and to every reader forge ships (ScanExistingMethods, ScanUnwiredStubMethods,
// ExciseUnwiredStubs, audit, mock_gen all walk the directory), so the layout is
// free to be chosen for the ONE thing it does affect: who has to merge with
// whom. Piling every custom RPC into a single handlers.go makes that file the
// serialization point for the whole service — two authors implementing two
// unrelated RPCs collide on it, and any parallel workflow has to hand-split it
// first, re-emitting each stub byte-identically into its own file before it can
// start. That is work forge created and then asked the user to undo. The
// scaffold TESTS were split per RPC for exactly this reason
// (ScaffoldTestFileName); the stubs they test were the last thing left sharing.
//
// It also makes the two scaffold paths agree. `forge scaffold rpc` has always
// written rpc_<snake>.go for an RPC that is not in the proto yet; declaring the
// RPC in the proto first and letting `forge generate` emit it used to produce
// the shared file instead. Same artifact, same marker, two layouts, chosen by
// the order the user happened to work in.
//
// Per file, the two arms are the ones the shared file used to have:
//
//   - The RPC's file does NOT exist (the overwhelmingly common case): render
//     the full handlers.go.tmpl for that one method — package clause, imports,
//     the method — and write it. A per-RPC file is a complete compilation unit,
//     not a fragment.
//   - The RPC's file EXISTS but does not declare the method (the user emptied
//     it, or renamed the method away and kept the file): render a method-only
//     fragment and APPEND it, then re-parse and ensure the imports the stub
//     needs (context, connectrpc.com/connect, forge/pkg/svcerr, and the
//     aliased proto pkg `pb`) are present before gofmt-ing the whole file.
//     Every stub body references all of them, so none is left unused — which
//     is why `fmt` is NOT among them: svcerr.ScaffoldStub composes the message
//     from the RPC name, so the stub no longer formats anything itself, and
//     adding fmt here would leave an unused import in a file that has to
//     compile. Using
//     go/ast + astutil (rather than a filesystem-scanning goimports pass) keeps
//     the import fix deterministic and handles the one import goimports cannot
//     infer from an alias: `pb`. Merging rather than skipping matters: skipping
//     would leave *Service without a method the Connect handler interface
//     requires, and the package would stop compiling.
//
// Overwriting is never an option in either arm — the file is the user's.
//
// The result is written via writeUserScaffold (raw os.WriteFile, NOT
// checksum-tracked): forge scaffolds it once per missing method and then leaves
// it to the user.
func writeRPCHandlerStubs(targetDir string, data ServiceTemplateData) error {
	for _, m := range data.Methods {
		one := data
		one.Methods = []MethodTemplateData{m}
		path := filepath.Join(targetDir, RPCHandlerFileName(m.Name))
		if err := scaffoldHandlerStubs(path, one); err != nil {
			return fmt.Errorf("scaffold %s: %w", RPCHandlerFileName(m.Name), err)
		}
	}
	return nil
}

// scaffoldHandlerStubs renders data.Methods into the user-owned handler file at
// handlersPath, creating it or merging into it. See writeRPCHandlerStubs — its
// only caller — for the two arms and why the file layout is per-RPC.
func scaffoldHandlerStubs(handlersPath string, data ServiceTemplateData) error {
	if _, statErr := os.Stat(handlersPath); os.IsNotExist(statErr) {
		content, err := templates.ServiceTemplates().Render("handlers.go.tmpl", data)
		if err != nil {
			return fmt.Errorf("render handlers.go.tmpl: %w", err)
		}
		if err := writeUserScaffold(handlersPath, content); err != nil {
			return fmt.Errorf("write %s: %w", filepath.Base(handlersPath), err)
		}
		return nil
	}

	fragment, err := templates.ServiceTemplates().Render("handlers_methods.go.tmpl", data)
	if err != nil {
		return fmt.Errorf("render handlers_methods.go.tmpl: %w", err)
	}

	existing, err := os.ReadFile(handlersPath)
	if err != nil {
		return fmt.Errorf("read %s for append: %w", filepath.Base(handlersPath), err)
	}

	// Concatenate existing file + a blank-line separator + the new methods, then
	// re-parse so we can normalize imports and gofmt the whole thing.
	combined := make([]byte, 0, len(existing)+len(fragment)+1)
	combined = append(combined, existing...)
	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		combined = append(combined, '\n')
	}
	combined = append(combined, fragment...)

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, handlersPath, combined, parser.ParseComments)
	if err != nil {
		return fmt.Errorf("parse appended %s: %w", filepath.Base(handlersPath), err)
	}

	// Ensure the stubs' imports exist. AddImport / AddNamedImport are no-ops
	// when the import (by path, and by name for the alias) is already present —
	// which it is for any handler file that already declares handler methods.
	astutil.AddImport(fset, file, "context")
	astutil.AddImport(fset, file, "connectrpc.com/connect")
	astutil.AddImport(fset, file, "github.com/reliant-labs/forge/pkg/svcerr")
	astutil.AddNamedImport(fset, file, "pb", data.Module+"/gen/"+data.ProtoPackage+"/v1")

	var buf bytes.Buffer
	if err := format.Node(&buf, fset, file); err != nil {
		return fmt.Errorf("format appended %s: %w", filepath.Base(handlersPath), err)
	}
	if err := writeUserScaffold(handlersPath, buf.Bytes()); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(handlersPath), err)
	}
	return nil
}

// RPCHandlerFileName is the born handler file for one custom RPC —
// `rpc_<snake_name>.go`.
//
// Same rule, same reason, as ScaffoldTestFileName next door: the name is
// derived from the RPC alone, so the mapping is total and reversible
// without a manifest, and two authors implementing two RPCs of the same
// service have nothing to merge. `forge scaffold rpc` has always written
// this name for an RPC that is not in the proto yet
// (internal/cli/scaffold/rpc.go); the generate pipeline now writes it too,
// so the two paths agree instead of disagreeing by which order the user
// happened to do things in.
//
// There is no legacy-name predicate to go with it (no RPCHandlerFile
// analogue of IsScaffoldTestFile) because nothing needs one: every reader
// of these files — ScanExistingMethods, ScanUnwiredStubMethods,
// ExciseUnwiredStubs, `forge project audit`, mock_gen — walks the handler
// directory and is already indifferent to filenames. Projects born before
// the split keep their handlers.go and keep working, untouched.
func RPCHandlerFileName(rpc string) string {
	return rpcHandlerPrefix + naming.ToSnakeCase(rpc) + ".go"
}

const rpcHandlerPrefix = "rpc_"

// ScaffoldTestFileName is the born unit-test file for one RPC.
//
// One RPC per file, and the name is derived from the RPC alone, so the
// mapping is total and reversible without a manifest: a reader seeing
// handlers_scaffold_ship_order_test.go knows exactly which RPC it covers,
// and a second author implementing a different RPC in the same package has
// nothing to merge.
//
// The prefix is stable and is what every consumer matches on
// (IsScaffoldTestFile) — including the pre-split
// `handlers_scaffold_test.go` that older projects still carry on disk.
func ScaffoldTestFileName(rpc string) string {
	return scaffoldTestPrefix + naming.ToSnakeCase(rpc) + "_test.go"
}

const scaffoldTestPrefix = "handlers_scaffold_"

// IsScaffoldTestFile reports whether a bare filename is one of forge's
// born per-RPC scaffold tests. It also accepts the single-file
// `handlers_scaffold_test.go` that projects born before the per-RPC split
// still have: that file is on real users' disks and the consumers of this
// predicate (stale-pb-reference detection, test-helper reconciliation)
// have to keep working on it.
func IsScaffoldTestFile(name string) bool {
	return name == "handlers_scaffold_test.go" ||
		(strings.HasPrefix(name, scaffoldTestPrefix) && strings.HasSuffix(name, "_test.go"))
}

// writeScaffoldTests renders one scaffold test file per method in
// data.Methods, writing each ONLY when it is absent.
//
// Absence is the entire ownership rule. There is no marker comment and no
// "forge refreshes it while you have not touched it" clause: a file that
// exists is yours, full stop, and the caller decides which RPCs are
// eligible (the ones it just stubbed). That replaces a mechanism where a
// comment string in a user-owned file silently controlled whether forge
// would overwrite it.
func writeScaffoldTests(targetDir string, data ServiceTemplateData) error {
	unfilled := UnfilledDepsFor(targetDir)
	for _, m := range data.Methods {
		one := data
		one.Methods = []MethodTemplateData{m}
		one.UnfilledDeps = unfilled
		content, err := templates.ServiceTemplates().Render("unit_test.go.tmpl", one)
		if err != nil {
			return fmt.Errorf("render unit_test.go.tmpl for %s: %w", m.Name, err)
		}
		path := filepath.Join(targetDir, ScaffoldTestFileName(m.Name))
		if _, err := writeUserScaffoldIfAbsent(path, content); err != nil {
			return fmt.Errorf("write %s: %w", ScaffoldTestFileName(m.Name), err)
		}
	}
	return nil
}

// ScanExistingMethods reads all .go files in dir and returns a set of
// method names that are already implemented on *Service. It uses
// go/parser so that multi-line receivers, comments, and strings
// containing "*Service" are handled correctly.
//
// This is the dedup that lets a user's `handlers.go` claim a method
// (e.g. `func (s *Service) CreateUser(...) ...`) and have the next
// `forge generate` automatically drop the matching stub from
// `handlers_gen.go`. Same shape closes the FORGE_REVIEW_PROCESS.md §2.3
// git_credential drift class — gen-files and user-files share the
// `*Service` receiver, so a method declared in either is sufficient
// signal that the proto RPC is implemented.
//
// An individual file that fails to parse is skipped with a warning
// rather than failing the whole pass: a transient syntax error in a
// sibling file must not brick the dedup for the entire package, since
// losing dedup means the user's just-written `CreateUser` would be
// re-stubbed in handlers_gen.go and the package would fail to compile
// (duplicate method).
//
// Exported for internal/scaffold (the typed RPC vertical shares this
// hand-implemented-method detection with the stub generator).
func ScanExistingMethods(dir string, includeGeneratedStubs bool) (map[string]bool, error) {
	existing := make(map[string]bool)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	fset := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		// Skip test files
		if strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		if !includeGeneratedStubs && (entry.Name() == "handlers_gen.go" || entry.Name() == "handlers_crud_gen.go" ||
			entry.Name() == "handlers_crud.go" || entry.Name() == "handlers_crud_ops_gen.go") {
			// handlers_crud.go holds the forge-scaffolded thin CRUD shims:
			// its methods delegate to generated ops, so they must not count
			// as "user implemented this RPC by hand" for the stub and CRUD
			// test scaffolds. CRUD op EMISSION does not use this scan at
			// all: it reads which ops the package calls, in any file
			// (scanHandlerPackage) — a file-name rule there dropped the op
			// of every shim moved out of this file.
			continue
		}

		path := filepath.Join(dir, entry.Name())
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			// Intentional soft warning (no --strict promotion): per-file
			// parse errors mustn't unwind the dedup — a transient
			// syntax error elsewhere in the package would otherwise
			// strand the user with no scaffold regen. See func doc for
			// the full rationale. Lives in internal/codegen so no
			// pipelineContext reach.
			fmt.Fprintf(os.Stderr, "Warning: ScanExistingMethods skipping %s (parse error): %v\n", path, err)
			continue
		}

		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 {
				continue
			}
			// Receiver must be a pointer: *Service
			star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
			if !ok {
				continue
			}
			ident, ok := star.X.(*ast.Ident)
			if !ok || ident.Name != "Service" {
				continue
			}
			if fn.Name != nil && fn.Name.Name != "" {
				existing[fn.Name.Name] = true
			}
		}
	}

	return existing, nil
}

// ScanHandlersCrudMethods returns the set of *Service method names declared in
// handlers_crud.go specifically. ScanExistingMethods skips that file wholesale
// (its delegating shims must not suppress ops regen); this lets the stub
// generator look inside it to find HAND-WRITTEN (non-CRUD) impls that DO need
// to suppress a duplicate stub. Returns an empty set if the file is absent or
// unparseable — losing this signal only risks a duplicate-method compile error
// surfacing at the validate step, never a silent wrong result.
func ScanHandlersCrudMethods(dir string) map[string]bool {
	out := map[string]bool{}
	path := filepath.Join(dir, "handlers_crud.go")
	if _, err := os.Stat(path); err != nil {
		return out
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: ScanHandlersCrudMethods skipping %s (parse error): %v\n", path, err)
		return out
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 {
			continue
		}
		star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		ident, ok := star.X.(*ast.Ident)
		if !ok || ident.Name != "Service" {
			continue
		}
		if fn.Name != nil && fn.Name.Name != "" {
			out[fn.Name.Name] = true
		}
	}
	return out
}
