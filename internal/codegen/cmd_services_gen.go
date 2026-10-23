package codegen

import (
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/naming"
	"github.com/reliant-labs/forge/internal/templates"
)

// reservedSubcommandNames are top-level cobra Use values the generated cmd
// tree already claims (server / version / db, cobra built-ins). A service
// whose runtime name collides is SKIPPED — with a NOTE in the generated
// services/register_gen.go — instead of emitting a subcommand that would
// shadow (or be shadowed by) the built-in. The colliding service is still
// reachable via `server <name>` (the typed MountByName convenience).
var reservedSubcommandNames = map[string]bool{
	"server":     true,
	"version":    true,
	"db":         true,
	"help":       true,
	"completion": true,
}

// CmdGroupItem is one generated command-group entry — one
// cmd/<bin>/cmd/<group>/<name>.go file that self-registers via init().
// It serves services, workers, and operators alike.
type CmdGroupItem struct {
	// Module is the project module path (for the import lines).
	Module string

	// Bin is the primary binary name — the cmd/<bin>/cmd import path segment
	// the group file imports for the shared Deps/Serve helpers.
	Bin string

	// Name is the runtime kebab-case component name — the cobra Use value and
	// the <name>.go filename stem. Identical derivation to the app inventory
	// row Name and the typed Mount<Svc> / Worker<X>() / Operator<X>() accessors.
	Name string

	// FieldName is the exported PascalCase suffix used in the generated
	// command constructor (New<FieldName>Cmd) and self-registration. It is
	// the PLAIN per-role name (ToPascalCase of the trimmed service name) —
	// the constructor is a local symbol in the group package, so it is not
	// subject to the cross-role collision rename. Workers/operators set
	// FieldName == MountFieldName (no Components-field mount, no collision).
	FieldName string

	// MountFieldName is the exported suffix of the typed mount METHOD the
	// service command calls: (*app.Components).Mount<MountFieldName>. For
	// services it MUST equal the name inventory_gen emitted — which is
	// collision-aware (ResolveCollisionNaming), so a service whose handler
	// package collides cross-role with an internal package mounts as
	// MountSvc<Pkg>, not Mount<Pkg>. Keeping this SEPARATE from FieldName lets
	// the constructor stay New<Plain>Cmd while the mount call names the
	// collision-renamed Components method — matching inventory_gen exactly and
	// killing the MountBilling/MountSvcBilling mismatch (BUG 1). For
	// workers/operators it is set equal to FieldName (their template doesn't
	// reference a Components mount method).
	MountFieldName string

	// MountMissing is true when internal/app declares a mount surface but
	// NOT (*Components).Mount<MountFieldName> — a project that disowned
	// mounts_services_gen.go and owns the surface by hand. The mount file then
	// emits a placeholder that compiles and refuses at run time with the exact
	// method to add, instead of a method expression that breaks the build (and
	// with it every `forge generate` and `forge scaffold service`).
	MountMissing bool
}

// CmdServicesTemplateData feeds cmd-svc-register.go.tmpl (the services group
// anchor + collision NOTEs).
type CmdServicesTemplateData struct {
	Skipped []string // runtime names skipped for colliding with built-ins
}

// CmdCtorRef is one group-command constructor reference for the composition
// root — the exported New<X>Cmd func name the main.go template qualifies with
// its group package (services./workers./operators.).
type CmdCtorRef struct {
	Ctor string // e.g. "NewAuditLogCmd"
}

// CmdMainTemplateData feeds cmd-main.go.tmpl — the composition root. main.go is
// no longer a thin blank-import + cmd.Execute(); it names EVERY group
// constructor explicitly and passes them to cmd.Execute. That makes main.go
// inventory-dependent (like the per-component group files), so it is rendered
// here from the SAME service/worker/operator rows the group files are — not
// from the project-level scaffold data — which is why GenerateCmdGroups owns it
// and the upgrade managed-file list does not.
type CmdMainTemplateData struct {
	Module string
	Bin    string
	// Graph renders the composition-graph composition root: no command
	// groups, `server` runs the server root.
	Graph     bool
	Services  []CmdCtorRef
	Workers   []CmdCtorRef
	Operators []CmdCtorRef
}

// cmdSvcFieldName derives the no-collision exported FieldName for a raw
// service-name spelling — the same ToPascalCase(TrimSuffix("Service"))
// derivation GenerateInventory's fallbackField uses BEFORE
// ResolveCollisionNaming. Shared so the two halves can't drift.
func cmdSvcFieldName(name string) string {
	field := naming.ToPascalCase(strings.TrimSuffix(name, "Service"))
	if field == "" {
		field = naming.ToPascalCase(name)
	}
	return field
}

// CmdServiceCommand describes the services/ group command `forge generate`
// emits for a raw service-name spelling: the runtime (kebab) name — which is
// both the cobra Use value and the <name>.go filename stem under
// cmd/<bin>/cmd/services/ — and the exported constructor cmd/<bin>/main.go
// must name to ship it.
//
// ok is false for a name the command tree RESERVES (see
// reservedSubcommandNames): those get no subcommand file at all, so there is
// nothing to wire into the composition root.
//
// It exists so callers that TALK about the command (the `forge scaffold
// service` wiring instruction) derive the symbol from the same place the
// generator does, and can never print a constructor that was not emitted.
func CmdServiceCommand(name string) (runtime, ctor string, ok bool) {
	runtime = naming.ToKebabCase(strings.TrimSuffix(name, "Service"))
	if runtime == "" {
		runtime = naming.ToKebabCase(name)
	}
	if reservedSubcommandNames[runtime] {
		return runtime, "", false
	}
	return runtime, "New" + cmdSvcFieldName(name) + "Cmd", true
}

// cmdServiceItemsFromNames projects raw service names (any spelling: proto
// "AdminServerService", forge.yaml "admin-server") onto group-item rows,
// filtering reserved collisions into the skipped list. The kebab/Pascal
// derivation is identical to GenerateInventory's so the subcommand Name
// always equals the inventory row Name and the typed mount method it selects.
//
// mountOverride, when non-nil, supplies the collision-aware MOUNT method name
// for a given raw service name — the EXACT name inventory_gen's
// ResolveCollisionNaming produced for that service (the Svc-prefixed name when
// its handler package collides cross-role with an internal package). The
// cmd-group command's typed mount call ((*app.Components).Mount<MountFieldName>)
// MUST match the method inventory_gen emitted, so when a collision exists the
// caller passes the resolved name here. The constructor name (FieldName) is
// always the PLAIN name — it's a local group-package symbol, not a Components
// method, so it isn't collision-renamed. When mountOverride is nil or lacks an
// entry for a name, MountFieldName == FieldName (the no-collision common case
// and disk-less unit fixtures).
func cmdServiceItemsFromNames(module, bin string, names []string, mountOverride map[string]string) ([]CmdGroupItem, []string) {
	var items []CmdGroupItem
	var skipped []string
	for _, name := range names {
		runtime, _, emitted := CmdServiceCommand(name)
		if !emitted {
			skipped = append(skipped, runtime)
			continue
		}
		field := cmdSvcFieldName(name)
		mountField := field
		if mountOverride != nil {
			if override, ok := mountOverride[name]; ok {
				mountField = override
			}
		}
		items = append(items, CmdGroupItem{Module: module, Bin: bin, Name: runtime, FieldName: field, MountFieldName: mountField})
	}
	return items, skipped
}

// CmdServiceGroupInput drives GenerateCmdGroups: the primary binary name plus
// the SERVICE rows (the SAME proto-derived rows the app mount surface is
// generated from, so every service subcommand lines up with a typed
// (*app.Components).Mount<Svc>). Workers/operators are deliberately absent —
// generate performs no worker/operator discovery; their subcommands are
// scaffold-once OWNED code written by `forge scaffold worker/operator`.
type CmdServiceGroupInput struct {
	Bin      string
	Services []string               // raw service-name spellings
	Packages []BootstrapPackageData // internal packages — for cross-role collision counts
	// Workers and Operators get no subcommand here (theirs are owned code
	// `forge scaffold` writes), but they take part in the collision counts:
	// compose.go names a service SvcX when a worker or operator shares its
	// package, and the mount this group calls has to be that one.
	Workers   []BootstrapWorkerData
	Operators []BootstrapOperatorData
}

// GenerateCmdGroups renders the dir-nested command groups under
// cmd/<bin>/cmd (devspace idiom): ONE FILE PER SERVICE in the services/
// SUBPACKAGE, each `New<X>Cmd(cmd.Deps)` whose RunE calls cmd.Serve() with a
// TYPED (*app.Components).Mount<Svc> selection. Each of the services/, workers/,
// and operators/ groups gets an anchor file so the package compiles with zero
// items. The services anchor is Tier-1 (register_gen.go) because it projects an
// input — the built-in collision NOTEs; the workers/ and operators/ anchors are
// SCAFFOLD-ONCE (register.go) because they project nothing. Selection is
// compile-time typed — no string positional arg, no string→inventory lookup.
//
// It ALSO scaffolds cmd/<bin>/main.go — the composition root — ONCE
// (write-if-absent) from the service rows. main.go is OWNED code thereafter:
// `forge scaffold worker/operator` appends the constructor ref by hand. The
// per-worker/operator subcommand files are NOT emitted here — they are
// scaffold-once OWNED code (ScaffoldWorkerCmd / ScaffoldOperatorCmd).
//
// cs is the project's checksum tracker — passing it keeps the (proto-derived)
// service files out of `forge project audit`'s orphan list. A nil cs is tolerated.
func GenerateCmdGroups(in CmdServiceGroupInput, targetDir string, cs *checksums.FileChecksums) error {
	modulePath, err := GetModulePath(targetDir)
	if err != nil {
		return fmt.Errorf("read module path: %w", err)
	}

	groupDir := func(group string) string {
		return filepath.Join("cmd", in.Bin, "cmd", group)
	}

	// The composition graph retires the per-service, per-worker and
	// per-operator subcommands (design §9.5): a process is a ROOT, and
	// `server` runs the default one. main.go is the bare composition root.
	if GraphComposition(targetDir) {
		if err := generateCmdMain(targetDir, modulePath, in.Bin, nil); err != nil {
			return err
		}
		return SyncLegacyRootGen(targetDir, modulePath, in.Bin, cs)
	}

	// Derive the collision-aware MOUNT method name for each service from the
	// SAME single source GenerateInventory uses (ResolveCollisionNaming over
	// CollisionCounts), so the cmd-group command's typed mount call
	// ((*app.Components).Mount<MountFieldName>) always names the EXACT method
	// inventory_gen emitted. A service whose handler package collides
	// cross-role with an internal package (control-plane: handler
	// internal/handlers/billing `package billing` vs domain internal/billing
	// `package billing`) renames to MountSvcBilling in inventory_gen; without
	// this the cmd-group would emit MountBilling and fail to compile.
	//
	// Disk-resolve each service's handler package clause (same as inventory),
	// then count cross-role collisions over services + packages + workers +
	// operators. Resolution failures are non-fatal here: fall back to the
	// plain name (covers disk-less fixtures); a genuinely missing handler dir
	// surfaces as a build error downstream, not a silent miscompile.
	mountOverride := cmdServiceMountOverrides(targetDir, in)

	// services/ — per service: the SCAFFOLD-ONCE subcommand plus the Tier-1
	// mount reference it calls, then the group anchor (with collision NOTEs).
	//
	// THE SPLIT. <name>.go is the user's cobra command — flags, RunE, help
	// text — and users customize it (a per-service flag, an extra env read).
	// Written once, never rewritten. <name>_mount_gen.go holds the single
	// expression forge must keep re-deriving: the collision-aware
	// (*app.Components).Mount<Svc> method, whose spelling can change when an
	// unrelated package is added. Splitting them is what lets the command be
	// owned without stranding the mount reference at whatever name it had on
	// the day the service was born.
	svcItems, skipped := cmdServiceItemsFromNames(modulePath, in.Bin, in.Services, mountOverride)
	markMissingMounts(targetDir, svcItems)
	for _, item := range svcItems {
		mountContent, rerr := templates.ProjectTemplates().Render("cmd-svc-mount-gen.go.tmpl", item)
		if rerr != nil {
			return fmt.Errorf("render cmd-svc-mount-gen.go.tmpl (%s): %w", item.Name, rerr)
		}
		mountDest := filepath.Join(groupDir("services"), item.Name+"_mount_gen.go")
		if werr := writeForgeOwned(targetDir, mountDest, mountContent, cs); werr != nil {
			return fmt.Errorf("write %s: %w", mountDest, werr)
		}

		content, rerr := templates.ProjectTemplates().Render("cmd-svc-group.go.tmpl", item)
		if rerr != nil {
			return fmt.Errorf("render cmd-svc-group.go.tmpl (%s): %w", item.Name, rerr)
		}
		dest := filepath.Join(groupDir("services"), item.Name+".go")
		if _, werr := writeForgeScaffoldOnce(targetDir, dest, content); werr != nil {
			return fmt.Errorf("write %s: %w", dest, werr)
		}
	}
	svcAnchor, err := templates.ProjectTemplates().Render("cmd-svc-register.go.tmpl", CmdServicesTemplateData{Skipped: skipped})
	if err != nil {
		return fmt.Errorf("render cmd-svc-register.go.tmpl: %w", err)
	}
	if err := writeForgeOwned(targetDir, filepath.Join(groupDir("services"), "register_gen.go"), svcAnchor, cs); err != nil {
		return fmt.Errorf("write services/register_gen.go: %w", err)
	}

	// workers/ and operators/ — the group anchors, SCAFFOLD-ONCE.
	//
	// WHY THESE ARE NOT TIER-1, unlike the services anchor beside them.
	// `forge generate` performs ZERO worker/operator discovery: the
	// per-component subcommand files are scaffold-once OWNED code written by
	// `forge scaffold worker/operator` (ScaffoldWorkerCmd /
	// ScaffoldOperatorCmd), and neither template takes any data — both render
	// from struct{}{}. So these two files carry no derived fact at all. Their
	// entire content is a package clause and a doc comment, byte-identical in
	// every project forge has ever generated.
	//
	// That is precisely what internal/tierguard exists to reject. A Tier-1
	// file that is not a function of user input gains nothing from
	// regeneration — there is nothing to re-derive — while still imposing
	// Tier-1's cost: a user who edits it puts it in permanent drift and
	// silently falls off the upgrade path forever. The services anchor keeps
	// Tier-1 honestly because it DOES project an input (the collision NOTE
	// for a service whose kebab name shadows a built-in); these two have no
	// equivalent, because a worker's name cannot collide with anything here.
	//
	// Scaffold-once is the tier that matches what they are: forge writes the
	// anchor at birth so the subpackage compiles with zero components, and
	// then it is the user's — which is already true of every other file in
	// these directories.
	for _, group := range []struct{ name, tmpl string }{
		{"workers", "cmd-worker-register.go.tmpl"},
		{"operators", "cmd-operator-register.go.tmpl"},
	} {
		anchor, rerr := templates.ProjectTemplates().Render(group.tmpl, struct{}{})
		if rerr != nil {
			return fmt.Errorf("render %s: %w", group.tmpl, rerr)
		}
		// Reclaim the Tier-1 copy these were emitted as before the tier
		// change. Only a copy that still verifies as forge's own render is
		// removed; a hand-edited or disowned one is the user's and is left
		// alone. Retiring the old name is what keeps the package from
		// carrying two anchors declaring the same package clause.
		RetireGenerated(targetDir, filepath.Join(groupDir(group.name), "register_gen.go"), cs)
		dest := filepath.Join(groupDir(group.name), "register.go")
		if _, werr := writeForgeScaffoldOnce(targetDir, dest, anchor); werr != nil {
			return fmt.Errorf("write %s: %w", dest, werr)
		}
	}

	// cmd/<bin>/main.go — the COMPOSITION ROOT, SCAFFOLD-ONCE owned code. It names
	// every group constructor explicitly and passes them to cmd.Execute (no
	// init() self-registration, no dynamic registry). Forge emits it ONCE with
	// the services known at scaffold time; thereafter it is hand-maintained
	// (`forge scaffold worker/operator` appends the constructor ref). It carries NO
	// worker/operator refs on the initial emit — generate does no worker/operator
	// discovery — and an existing main.go is left untouched.
	if err := generateCmdMain(targetDir, modulePath, in.Bin, svcItems); err != nil {
		return err
	}

	// cmd/<bin>/cmd/root_gen.go is RETIRED for new projects: the command tree
	// has no generated file anymore. ServiceName is a const in the
	// scaffold-once root.go, `db migrate` is registered by the db.go that
	// defines it, and the one genuinely re-derived fact (whether an embedded
	// migration set exists) moved to db.Source().
	//
	// A project born BEFORE that change keeps its own scaffold-once root.go,
	// which calls generatedCommands() — and forge does not rewrite files it
	// promised never to touch. So this keeps emitting root_gen.go for exactly
	// those projects, and deletes it once root.go no longer needs it. See
	// cmd_root_legacy.go.
	if err := SyncLegacyRootGen(targetDir, modulePath, in.Bin, cs); err != nil {
		return err
	}

	return nil
}

// ScaffoldWorkerCmd writes the scaffold-once per-worker subcommand file
// cmd/<bin>/cmd/workers/<name>.go for a SINGLE worker, write-if-absent. This is
// the `forge scaffold worker` counterpart to the retired generate-time worker loop:
// one new component, known by name, scaffolded once as OWNED code (the dev then
// hand-wires it into main.go / lifecycle.go / compose.go). Returns true when it
// wrote a fresh file (false when one already existed).
func ScaffoldWorkerCmd(targetDir, bin, name string) (bool, error) {
	return scaffoldComponentCmd(targetDir, bin, name, "workers", "cmd-worker-group.go.tmpl")
}

// ScaffoldOperatorCmd is the operator-side analog of ScaffoldWorkerCmd — writes
// cmd/<bin>/cmd/operators/<name>.go once for a single operator.
func ScaffoldOperatorCmd(targetDir, bin, name string) (bool, error) {
	return scaffoldComponentCmd(targetDir, bin, name, "operators", "cmd-operator-group.go.tmpl")
}

func scaffoldComponentCmd(targetDir, bin, name, group, tmpl string) (bool, error) {
	modulePath, err := GetModulePath(targetDir)
	if err != nil {
		return false, fmt.Errorf("read module path: %w", err)
	}
	fieldName := naming.ToPascalCase(name)
	item := CmdGroupItem{Module: modulePath, Bin: bin, Name: name, FieldName: fieldName, MountFieldName: fieldName}
	content, err := templates.ProjectTemplates().Render(tmpl, item)
	if err != nil {
		return false, fmt.Errorf("render %s (%s): %w", tmpl, name, err)
	}
	dest := filepath.Join("cmd", bin, "cmd", group, name+".go")
	return writeForgeScaffoldOnce(targetDir, dest, content)
}

// GenerateCmdMainRoot renders ONLY cmd/<bin>/main.go — the composition root —
// as a bare cmd.Execute() with no component constructors. The generate pipeline
// emits main.go via GenerateCmdGroups (which knows the full inventory); this
// exported entry lets the scaffold drop a composition root for a service
// project that has no proto pipeline (features.codegen=false), preserving the
// pre-refactor contract that main.go is always scaffolded for service kind.
func GenerateCmdMainRoot(targetDir, bin string, cs *checksums.FileChecksums) error {
	modulePath, err := GetModulePath(targetDir)
	if err != nil {
		return fmt.Errorf("read module path: %w", err)
	}
	return generateCmdMain(targetDir, modulePath, bin, nil)
}

// generateCmdMain renders cmd/<bin>/main.go (the SCAFFOLD-ONCE composition root)
// from the resolved service rows. It carries NO worker/operator refs — generate
// performs zero worker/operator discovery; those are hand-wired by `forge scaffold`.
// The output is gofmt-canonicalized so the arg list collapses cleanly (an empty
// component set yields a bare `cmd.Execute()`). Written write-if-absent: forge
// emits it once, then never overwrites the owned file.
func generateCmdMain(targetDir, modulePath, bin string, svcItems []CmdGroupItem) error {
	data := CmdMainTemplateData{Module: modulePath, Bin: bin, Graph: GraphComposition(targetDir)}
	for _, item := range svcItems {
		data.Services = append(data.Services, CmdCtorRef{Ctor: "New" + item.FieldName + "Cmd"})
	}

	rendered, err := templates.ProjectTemplates().Render("cmd-main.go.tmpl", data)
	if err != nil {
		return fmt.Errorf("render cmd-main.go.tmpl: %w", err)
	}
	formatted, err := format.Source(rendered)
	if err != nil {
		return fmt.Errorf("gofmt cmd/%s/main.go: %w\n%s", bin, err, rendered)
	}
	if _, err := writeForgeScaffoldOnce(targetDir, filepath.Join("cmd", bin, "main.go"), formatted); err != nil {
		return fmt.Errorf("write cmd/%s/main.go: %w", bin, err)
	}
	return nil
}

// cmdServiceMountOverrides returns, keyed by raw service-name spelling, the
// collision-aware exported suffix each service's typed mount METHOD carries in
// inventory_gen.go. It is the SINGLE shared derivation point between the
// cmd-group generator and inventory_gen: both feed the resolved handler
// package clause + the cross-role CollisionCounts into ResolveCollisionNaming,
// so the cmd-group's `(*app.Components).Mount<MountFieldName>` call can never
// name a method inventory_gen didn't emit.
//
// Only services whose name maps to a NON-plain field (i.e. a real collision)
// get an entry; the no-collision majority fall through to the plain
// ToPascalCase derivation in cmdServiceItemsFromNames (MountFieldName ==
// FieldName), keeping output byte-identical for collision-free projects.
// Disk-resolution failures are skipped (no entry) rather than fatal: disk-less
// unit fixtures and transient half-scaffolded trees fall back to the plain
// name, and a truly missing handler dir surfaces as a downstream build error.
func cmdServiceMountOverrides(targetDir string, in CmdServiceGroupInput) map[string]string {
	type resolvedSvc struct {
		name string // raw service-name spelling (map key)
		pkg  string // resolved handler package clause
	}
	resolved := make([]resolvedSvc, 0, len(in.Services))
	svcComponents := make([]BootstrapServiceData, 0, len(in.Services))
	for _, name := range in.Services {
		res, err := ResolveServiceComponent(targetDir, name)
		if err != nil {
			// Can't read the package clause — skip (plain-name fallback).
			continue
		}
		resolved = append(resolved, resolvedSvc{name: name, pkg: res.PackageName})
		svcComponents = append(svcComponents, BootstrapServiceData{Package: res.PackageName})
	}

	// Every role is counted, as compose.go (assembleBuildComponents) and
	// GenerateInventory count them. A worker has no Mount method, but sharing
	// a service's package still renames the service's field to SvcX in
	// compose.go, and the inventory's mount follows the field.
	counts := CollisionCounts(svcComponents, in.Packages, in.Workers, in.Operators)

	overrides := map[string]string{}
	for _, rs := range resolved {
		_, fieldName := ResolveCollisionNaming(rs.pkg, cmdSvcFieldName(rs.name), "svc", counts)
		// Record only genuine collision renames; the plain case is the
		// cmdServiceItemsFromNames default.
		if counts[rs.pkg] > 1 {
			overrides[rs.name] = fieldName
		}
	}
	if len(overrides) == 0 {
		return nil
	}
	return overrides
}

// markMissingMounts sets MountMissing on every item whose mount method
// internal/app does not declare. It stands down (marks nothing) when
// internal/app declares no Mount method on *Components at all or cannot be
// read: that is a tree mid-generate, not a hand-owned surface, and the
// ordinary method expression plus the compiler is the right signal there.
func markMissingMounts(targetDir string, items []CmdGroupItem) {
	declared := componentsMountMethods(filepath.Join(targetDir, "internal", "app"))
	if len(declared) == 0 {
		return
	}
	for i := range items {
		if !declared["Mount"+items[i].MountFieldName] {
			items[i].MountMissing = true
		}
	}
}

// componentsMountMethods returns the names of every `Mount*` method declared
// on Components (value or pointer receiver) in dir's non-test Go files.
func componentsMountMethods(dir string) map[string]bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := map[string]bool{}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if perr != nil {
			continue
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 || !strings.HasPrefix(fn.Name.Name, "Mount") {
				continue
			}
			recv := fn.Recv.List[0].Type
			if star, ok := recv.(*ast.StarExpr); ok {
				recv = star.X
			}
			if id, ok := recv.(*ast.Ident); ok && id.Name == "Components" {
				out[fn.Name.Name] = true
			}
		}
	}
	return out
}

// mountPlaceholderRE finds the method a placeholder mount file is waiting for.
var mountPlaceholderRE = regexp.MustCompile(`internal/app declares no \(\*Components\)\.(Mount\w+)`)

// PendingServiceMount reports the `(*Components).Mount<Svc>` method the
// service's generated mount file is waiting for — non-empty exactly when that
// file is the MountMissing placeholder (see cmd-svc-mount-gen.go.tmpl). It
// reads the file generate just wrote rather than re-deriving the answer, so the
// scaffold's report can never disagree with what was emitted.
func PendingServiceMount(root, bin, runtimeName string) string {
	raw, err := os.ReadFile(filepath.Join(root, "cmd", bin, "cmd", "services", runtimeName+"_mount_gen.go"))
	if err != nil {
		return ""
	}
	if m := mountPlaceholderRE.FindSubmatch(raw); m != nil {
		return string(m[1])
	}
	return ""
}
