// What a binary serves is CODE, not config.
//
// The serving decision lives in the user-owned pkg/app/services.go:
// RegisteredServices lists one generated serviceRow<X> call per service
// this binary serves. forge scaffolds that file once (listing every
// current service — zero semantic change for existing projects) and
// never rewrites it; from then on the row list is the single source of
// truth, and this file derives the served-set from it by AST-parsing
// the registration file (the same user-file-parsing posture as
// codegen.ParseServiceDeps and the setup.go scans).
//
// Classification per service (see serviceRegistry.state):
//
//	REGISTERED — a serviceRow<X> identifier matching the service is
//	  referenced in services.go. Full treatment: handlers scaffold,
//	  CRUD/mocks, wire/diagnostics,
//	  auth-middleware skip-list, bootstrap row (via the user's list).
//	UNLISTED — the service name appears NOWHERE in services.go. Treated
//	  as newly added (forge scaffold service / hand-edited forge.yaml): the
//	  handlers scaffold and row constructor still generate so the user
//	  can implement-then-register, but the service is NOT served — no
//	  auth skip-list entries, `forge env up` skips it, and
//	  `forge project audit` warns that the row constructor is unreferenced.
//	  The registration line is written by the USER (or their agent) —
//	  forge prints it but never edits the file. That's the design: the
//	  LLM writes the one decision line; forge generates the guardrails.
//	TOMBSTONED — the service name appears in services.go only in a
//	  comment (the scaffold instructs: delete the row, leave a comment
//	  naming the binary that serves it). Types-only: proto types,
//	  Connect client, frontend hooks, and descriptor entries still
//	  generate (callers need them); the handlers/<svc>/ scaffold, row
//	  constructor, wire/diagnostics, and auth registration
//	  are gated off. The comment is load-bearing — without any mention
//	  the service reverts to UNLISTED and its scaffold regenerates.
//
// Fallbacks: a missing services.go means "everything registered"
// (pre-migration projects keep today's behavior; the generate pipeline
// then scaffolds the file with exactly that meaning). A services.go
// that does not PARSE is a hard error in the generate pipeline (the
// build would fail anyway; better to name the file) and fail-open
// (everything registered) for read-only commands like audit/graph,
// which must not die on a broken tree.
//
// Retirement of a pre-existing handlers/<svc>/ scaffold reuses the
// manifest-driven stale sweep (generate_cleanup.go): the gated emitters
// stop writing the tracked Tier-1 files, so they fall out of
// WrittenThisRun and are removed (the hand-edited ones only under
// --force-cleanup); Tier-2 user-owned files in the same dir are
// never candidates. `forge project audit` surfaces both halves via the codegen
// category's unregistered_services finding.
package cli

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/naming"
)

// serviceRegistryRelPath is where the user-owned registration file
// lives, relative to the project root.
const serviceRegistryRelPath = "pkg/app/services.go"

// serviceRegistration classifies one service against the registration
// file. See the package doc for the full semantics of each state.
type serviceRegistration int

const (
	registrationRegistered serviceRegistration = iota
	registrationTombstoned
	registrationUnlisted
)

// serviceRegistry is the parsed view of pkg/app/services.go.
type serviceRegistry struct {
	// Exists is false when the file is absent — every lookup then
	// reports REGISTERED (fail-open: pre-migration trees keep the
	// declared ⇒ served behavior until generate scaffolds the file).
	Exists bool

	// rowRefs holds the canonical (naming.ServicePackage) form of every
	// serviceRow<X> identifier referenced anywhere in the file —
	// including the cross-role-collision "Svc"-prefixed FieldName shape
	// (serviceRowSvcBilling), which is registered under both readings.
	rowRefs map[string]bool

	// mentions holds the canonical form of every word token appearing
	// in the file's comments. A service mentioned here but absent from
	// rowRefs is TOMBSTONED ("deliberately not served — see comment").
	mentions map[string]bool
}

// registryWordRe tokenizes comment text into name-shaped words. Hyphens
// and underscores stay inside a token so "admin-server" and
// "admin_server" survive as single tokens for canonicalization.
var registryWordRe = regexp.MustCompile(`[A-Za-z0-9][A-Za-z0-9_-]*`)

// loadServiceRegistry parses projectDir's pkg/app/services.go. A
// missing file returns Exists=false with a nil error; a file that does
// not parse returns the parse error (callers choose fail-loud vs
// fail-open — see the package doc).
func loadServiceRegistry(projectDir string) (*serviceRegistry, error) {
	path := filepath.Join(projectDir, filepath.FromSlash(serviceRegistryRelPath))
	src, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &serviceRegistry{Exists: false}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", serviceRegistryRelPath, err)
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse %s (the user-owned service registration file must be valid Go): %w", serviceRegistryRelPath, err)
	}

	reg := &serviceRegistry{
		Exists:   true,
		rowRefs:  map[string]bool{},
		mentions: map[string]bool{},
	}

	ast.Inspect(file, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok || !strings.HasPrefix(id.Name, codegen.ServiceRowPrefix) {
			return true
		}
		suffix := strings.TrimPrefix(id.Name, codegen.ServiceRowPrefix)
		if suffix == "" {
			return true
		}
		reg.rowRefs[naming.ServicePackage(suffix)] = true
		// Collision-renamed constructors (service pkg colliding with an
		// internal package: FieldName "SvcBilling") register the bare
		// name too so the spelling-agnostic lookup still resolves.
		if rest := strings.TrimPrefix(suffix, "Svc"); rest != suffix && rest != "" {
			reg.rowRefs[naming.ServicePackage(rest)] = true
		}
		return true
	})

	for _, cg := range file.Comments {
		for _, tok := range registryWordRe.FindAllString(cg.Text(), -1) {
			reg.mentions[naming.ServicePackage(tok)] = true
		}
	}
	return reg, nil
}

// state classifies a service name (any spelling: proto
// "AdminServerService", forge.yaml "admin-server", snake
// "admin_server") against the registration file.
func (r *serviceRegistry) state(svcName string) serviceRegistration {
	if r == nil || !r.Exists {
		return registrationRegistered
	}
	canonical := naming.ServicePackage(svcName)
	if r.rowRefs[canonical] {
		return registrationRegistered
	}
	if r.mentions[canonical] {
		return registrationTombstoned
	}
	return registrationUnlisted
}

// registered reports whether this binary serves the named service.
func (r *serviceRegistry) registered(svcName string) bool {
	return r.state(svcName) == registrationRegistered
}

// serviceRegistry returns the pipeline's memoized registration view.
// Parse failures are hard errors inside generate: the file is Go code
// the project must compile anyway, and silently treating a broken
// registry as "serve everything" could mount a service the user just
// retired.
func (ctx *pipelineContext) serviceRegistry() (*serviceRegistry, error) {
	if !ctx.registryLoaded {
		ctx.registry, ctx.registryErr = loadServiceRegistry(ctx.ProjectDir)
		ctx.registryLoaded = true
	}
	return ctx.registry, ctx.registryErr
}

// splitServiceDefs partitions proto-parsed defs into the row set (the
// services that get handlers scaffolds + serviceRow constructors:
// REGISTERED ∪ UNLISTED) and the tombstoned set (types-only).
func splitServiceDefs(reg *serviceRegistry, services []codegen.ServiceDef) (rows, tombstoned []codegen.ServiceDef) {
	for _, svc := range services {
		if reg.state(svc.Name) == registrationTombstoned {
			tombstoned = append(tombstoned, svc)
		} else {
			rows = append(rows, svc)
		}
	}
	return rows, tombstoned
}

// rowServiceDefs is the pipeline view over splitServiceDefs' first
// half: every service that gets generated artifacts on disk this run
// (handlers scaffold, CRUD, mocks, wire/diagnostics, row
// constructor, testing helpers).
func (ctx *pipelineContext) rowServiceDefs() ([]codegen.ServiceDef, error) {
	reg, err := ctx.serviceRegistry()
	if err != nil {
		return nil, err
	}
	rows, _ := splitServiceDefs(reg, ctx.Services)
	return rows, nil
}

// registeredServiceDefs is the pipeline view of what this binary
// SERVES: only REGISTERED services. Steps whose output advertises or
// mounts the service (auth-middleware skip-list,
// introspect) consume this; steps that emit caller-side artifacts
// (frontend hooks/pages/mocks, descriptor) keep reading ctx.Services
// unfiltered.
func (ctx *pipelineContext) registeredServiceDefs() ([]codegen.ServiceDef, error) {
	reg, err := ctx.serviceRegistry()
	if err != nil {
		return nil, err
	}
	var out []codegen.ServiceDef
	for _, svc := range ctx.Services {
		if reg.registered(svc.Name) {
			out = append(out, svc)
		}
	}
	return out, nil
}

// allServiceRuntimeNames projects defs into the runtime kebab names the
// bootstrap registration guard compares filter args against — the same
// derivation GenerateBootstrap uses for row Name fields.
func allServiceRuntimeNames(services []codegen.ServiceDef) []string {
	var out []string
	for _, svc := range services {
		name := naming.ToKebabCase(strings.TrimSuffix(svc.Name, "Service"))
		if name == "" {
			name = naming.ToKebabCase(svc.Name)
		}
		out = append(out, name)
	}
	return out
}

// isConnectServiceConfig reports whether a forge.yaml components[] entry
// is a Connect server (the only kind the registration file governs).
// Workers, crons, operators and binaries have no Connect surface and
// never appear in components.go, so registry lookups must not be applied
// to them.
func isConnectServiceConfig(c config.ComponentConfig) bool {
	return c.IsServer()
}
