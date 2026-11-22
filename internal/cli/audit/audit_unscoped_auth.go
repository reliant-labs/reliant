// File: internal/cli/audit/audit_unscoped_auth.go
//
// The `unscoped_auth` audit category: an RPC whose proto declares
// auth_required: true, whose handler body never resolves the caller.
//
// Why this is a category and not a comment
//
// The CRUD shim template already says the thing. Its file header explains
// it once, and every delegating method carries the pointer:
//
//	Auth: AUTHENTICATED, UNSCOPED — nothing reads the caller; scope with middleware.GetUser(ctx) (see file header).
//
// (It used to repeat the full four-line explanation above every method.)
// A measured run read that comment, wrote a plan that repeated it back
// ("later work can add authorization/row scoping in owned CRUD
// delegations"), and shipped sixteen delegations that read no caller. One
// signed-in user could read, list, update and delete another user's rows;
// the update path let them reassign a row to themselves outright. Every
// gate the run ran was green, because a comment cannot fail. That is the
// defect this file addresses: not that forge failed to diagnose the hole,
// but that its diagnosis was unfalsifiable.
//
// What it derives from
//
// Both sides of the comparison are computed by the producer, never
// grepped out of rendered prose:
//
//   - The AUTHENTICATED set comes from gen/forge_descriptor/ —
//     protoc-gen-forge's own projection of (forge.v1.method).auth_required,
//     the same field that drives pkg/middleware/procedures_gen.go and the
//     interceptor's fail-closed set. Reading it through
//     codegen.ParseServicesFromProtos means this category and the
//     interceptor can never disagree about which RPCs are authenticated.
//   - The READS-THE-CALLER set comes from the go/ast of the user-owned
//     handler files, resolved through the SAME auth seam the scaffold
//     names (codegen.CRUDAuthSeamFunc / CRUDAuthSeamPkg) plus the claims
//     accessors that seam re-exports. Renaming the seam moves this check
//     with it; nothing here hardcodes the string "GetUser".
//
// A handler counts as reading the caller if its body reaches the seam
// directly, OR passes something derived from it into a callee, OR calls a
// helper in its own package that reaches the seam at ANY depth — the
// reachable set is a fixed point over the package's call graph, so
// `ListCustomers -> scopedList(s, …) -> s.caller(ctx) -> GetUser(ctx)`
// resolves the same as a handler that calls the seam itself.
//
// That depth is not generality for its own sake. This check shipped with
// a single hop, and against a real downstream app it reported all 63 of
// its correctly-scoped CRUD RPCs as unscoped: the app had factored the
// rule into one generic free helper per operation — the DRY way to scope
// a generated surface, and the shape forge's own authorization guidance
// steers people toward — which put the seam two hops away. A one-hop
// reading of that is a false alarm whose suggested remediation is
// ALREADY IMPLEMENTED, leaving the developer nothing to do but disable
// the check. At warn that was an over-warning; with the `forge:owner`
// gate below it breaks the build, and a gate that fails on the
// recommended factoring does not keep its authority long.
//
// False positives cost more than false negatives here
//
// A legitimately global authenticated RPC exists: an admin list, a lookup
// already keyed by something caller-scoped, an RPC whose scoping lives in
// a row-level-security policy. Those are not defects and must not be
// reported forever, so the author can say so IN CODE:
//
//	// forge:auth-unscoped-ok: operator console list; every caller of this
//	// RPC is an admin, so there is no narrower scope to apply.
//	func (s *Service) ListAuditEvents(...)
//
// A directive with no reason after the colon does not count — an
// acknowledgement that says nothing is the comment problem again. It
// lives in the handler file rather than a config file specifically so it
// shows up in the diff that introduces it, next to the code it excuses.
//
// Advisory by default, GATING where row ownership was declared
//
// This category shipped warn-never-error, for a reason that was sound as
// far as it went: a freshly-scaffolded project is ENTIRELY unscoped by
// construction — forge emits the delegations, the user writes the
// scoping — so erroring unconditionally would make forge's own output
// fail forge's own gate, the invariant TestE2EFreshScaffoldLintExitsZero
// pins.
//
// It was also not enough. Measured, against a real downstream app built
// with this file already in place: forge reported
//
//	warn  unscoped_auth: 63 of 74 authenticated RPC(s) never resolve the caller
//
// and exited 0. Any authenticated user could read, UPDATE, DELETE and
// CREATE rows belonging to every other one. The header above this one
// documents the SAME incident at 16 RPCs and states the principle — "every
// gate the run ran was green, because a comment cannot fail" — and then
// the control it introduced was itself non-gating, so the incident
// recurred at four times the scale. A warning nobody must act on is a
// comment with a JSON schema.
//
// What was actually missing was not severity but a PREDICATE. "Unscoped"
// is a fact about handlers; "these rows belong to someone" is a fact
// about data, and this category could observe only the first. A project
// that never says any row belongs to anyone has no boundary to cross, and
// treating it the same as one that does is what forced the choice between
// "always warn" and "break every fresh scaffold".
//
// schemadef.ColumnMarkerOwner — `forge:owner` in a column's COMMENT — is
// the developer supplying the missing half:
//
//	COMMENT ON COLUMN customers.company_id IS 'forge:owner';
//
// No table declares it        → status is warn, exit 0. A fresh scaffold,
//                               an app whose rows belong to nobody in
//                               particular, a CLI: untouched, and the
//                               finding is still reported.
// At least one table does     → an authenticated RPC over an owned entity
//                               that never resolves the caller is an
//                               ERROR. Every OTHER unscoped RPC stays a warning.
//
// The gate is therefore armed by a sentence the developer wrote, never by
// a heuristic forge guessed, and it arms per-entity rather than
// project-wide: declaring an owner column on `customers` does not make an
// unscoped RPC over a global product catalog a failure. The column marker
// injects no WHERE clause — it declares that a boundary EXISTS, and the
// developer writes what it is.
//
// The table-level form, schemadef.TableMarkerOwner, declares the boundary
// AND what it is, and the generated ops enforce it:
//
//	COMMENT ON TABLE plans IS 'forge:owner=thread_id>chat_id>user_id';
//
// It arms the gate exactly as the column marker does (and the raw-SQL
// check, by the first column of its first owner). It is also an answer to
// the gate: the op of an owned table resolves the caller itself, through
// the service's `s.deps.Owners` (forge/pkg/crud's Owned* helpers), so a
// handler that reaches its OWN `s.crud<Method>Op()` — directly or through
// package helpers — counts as reading the caller, provided that op really
// names `s.deps.Owners`. An op generated before the declaration names
// nothing, and the rpc keeps gating until `forge generate` rewrites it. An
// authenticated rpc over an owned table whose handler neither delegates to
// its op nor reads the caller still gates, and its remediation is the
// delegation. See audit_unscoped_auth_owned_op.go.
//
// The `forge:auth-unscoped-ok: <reason>` escape hatch suppresses the
// ERROR, not merely the warning — a gate with no way to say "this one is
// deliberate" is a gate people disable wholesale. The reason is still
// mandatory: a bare directive is the unfalsifiable comment again, and
// must not become a one-line bypass.
//
// What this does NOT catch, stated plainly
//
// The RPC→table join is the CRUD-name derivation (ParseCRUDOperation plus
// naming, exactly as BuildSchemaEntities does it), so a custom RPC —
// `TransferOrder`, `ArchiveWorkspace` — maps to no entity and cannot arm
// the gate however many owned tables it touches. That is a real coverage
// hole, not a design intent: the gate covers the generated CRUD surface,
// which is where the measured incidents happened, and a custom RPC over
// owned data is still reported as a warning.

package audit

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jinzhu/inflection"

	"github.com/reliant-labs/forge/internal/cli/audittype"
	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/naming"
	"github.com/reliant-labs/forge/internal/sqlident"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

// AuthUnscopedOKDirective is the in-code acknowledgement that an
// authenticated RPC is intentionally global. It must be followed by a
// reason: the directive alone re-creates the unfalsifiable comment this
// category exists to replace.
const AuthUnscopedOKDirective = "forge:auth-unscoped-ok:"

// unscopedRPC is one authenticated RPC whose handler never reaches the
// auth seam.
type unscopedRPC struct {
	Service string `json:"service"`
	Method  string `json:"method"`
	File    string `json:"file"`
	// Delegating is true when the body is a bare forge CRUD delegation
	// (the shape the scaffold emits and the run shipped unchanged).
	// Consumers can use it to separate "never touched" from "written by
	// hand and still unscoped".
	Delegating bool `json:"delegating"`
	// Entity and Table name the owned entity this RPC operates
	// on, and are set ONLY for a finding that arms the gate. They are
	// what makes the error message falsifiable: the reader can open the
	// named migration, see the `forge:owner` declaration, and either
	// scope the handler or withdraw the declaration. Empty on an
	// advisory finding — no owned table sits behind it.
	Entity string `json:"entity,omitempty"`
	Table  string `json:"table,omitempty"`
	// OwnerSpec is the table-level `forge:owner=` declaration that armed
	// the gate, when it was that form rather than a `forge:owner` column.
	// Its generated op already scopes by it, so the remediation is to
	// delegate to the op rather than to hand-write a predicate.
	OwnerSpec string `json:"owner_spec,omitempty"`
	// Remediation is the owner-scoping wrapper for this RPC, as code —
	// the caller resolution, the placeholder owner expression, and the
	// op-seam override that puts Table's declared owner column into the
	// query.
	//
	// It is carried here rather than left to the auth skill because the
	// skill's remediation is a scaffold that CANNOT fire on an existing
	// project. The CRUD shim writes the wrapper only at
	// handlers_crud.go's birth, and `forge:owner` arrives in a later
	// migration, so any project that declares ownership after scaffolding
	// its handlers — the normal order of work — gets the refusal and no
	// wrapper. A measured run hit exactly that and had nothing to act on.
	//
	// The text is rendered from the SAME template the scaffolder uses
	// (codegen.RenderScopedCRUDShim), so pasting it produces what a
	// greenfield project would have received. Empty when forge cannot
	// render an honest one: an advisory finding names no column, and a
	// non-CRUD RPC has no generated op seam to wrap.
	Remediation string `json:"remediation,omitempty"`
}

// unimplementedRPC is one authenticated RPC whose handler only answers
// CodeUnimplemented, so it has no data to scope.
type unimplementedRPC struct {
	Service string `json:"service"`
	Method  string `json:"method"`
	File    string `json:"file"`
}

// acknowledgedRPC is one authenticated RPC the author has explicitly
// declared global, with the reason they gave.
type acknowledgedRPC struct {
	Service string `json:"service"`
	Method  string `json:"method"`
	File    string `json:"file"`
	Reason  string `json:"reason"`
}

// malformedAck is a `forge:auth-unscoped-ok` the audit read and did NOT
// accept: no reason, an empty one, or a lookalike separator where the colon
// goes. Such a marker acknowledges nothing, so the RPC stays where it was
// classified; this says why the author's acknowledgement did not count.
type malformedAck struct {
	Service  string `json:"service"`
	Method   string `json:"method"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Written  string `json:"written"`
	Expected string `json:"expected"`
}

// auditUnscopedAuth reports authenticated RPCs whose handlers never
// resolve the caller, and FAILS on the subset of them that cross a
// ownership boundary the project declared with `forge:owner`.
//
// cfg supplies the migrations directory override and may be nil (the
// "db/migrations" default is used then), which is what every test that
// does not exercise the gate passes.
//
// It is a no-op (status ok, explicit summary) for any project that has no
// Connect services, no descriptor, or no handler tree — a worker-only
// project, a CLI, a library. Those are not "clean", they are not subject,
// and the summary says which.
// length is the number of unscoped-auth patterns recognized, and each arm
// is a few lines; extracting them would add indirection without removing
// anything a reader has to follow.
//
//nolint:funlen // One pass over the AST collecting one finding shape. The
func auditUnscopedAuth(cfg *config.ProjectConfig, projectDir string) (cat audittype.Category) {
	// A malformed acknowledgement is reported whichever branch returns:
	// it is a defect in its own right (a marker that does nothing), and
	// the branch that classified its RPC says nothing about the marker.
	var malformed []malformedAck
	defer func() { cat = withMalformedAcks(cat, malformed) }()

	services, err := codegen.ParseServicesFromProtos("", projectDir)
	if err != nil {
		return audittype.Category{
			Status:  audittype.StatusWarn,
			Summary: fmt.Sprintf("could not read the forge descriptor: %v", err),
			Details: map[string]any{"hint": fmt.Sprintf("run `%s generate` to produce gen/forge_descriptor/", cmdutil.Name())},
		}
	}
	if len(services) == 0 {
		return audittype.Category{
			Status:  audittype.StatusOK,
			Summary: "no Connect services declared (n/a)",
			Details: map[string]any{"authenticated_rpcs": 0},
		}
	}

	handlersRoot := filepath.Join(projectDir, "internal", "handlers")
	if !dirExists(handlersRoot) {
		return audittype.Category{
			Status:  audittype.StatusOK,
			Summary: "no internal/handlers/ directory (n/a)",
			Details: map[string]any{"authenticated_rpcs": 0},
		}
	}

	var (
		unscoped     []unscopedRPC
		acknowledged []acknowledgedRPC
		// unimplemented lists the handlers that only answer
		// CodeUnimplemented: counted, never a finding (see
		// audit_unscoped_auth_unimplemented.go).
		unimplemented []unimplementedRPC
		authTotal     int
		scopedTotal   int
		// The two raw-SQL states. Kept apart from `unscoped` because
		// they are a different finding with a different remedy: these
		// RPCs DID resolve the caller, and what is missing is the
		// predicate inside a query forge cannot read.
		rawSQLUnscoped     []rawSQLRPC
		rawSQLUnverifiable []rawSQLRPC
		// declaredAuth counts authenticated RPCs the DESCRIPTOR knows
		// about, independent of whether a handler was found for them.
		// The gap between it and authTotal is how this category detects
		// that its own derivation broke — see the guard below.
		declaredAuth   int
		unresolvedSvcs []string
	)

	// The DECLARATION half of the gate, read straight from the
	// migrations — the applied schema's own source of truth, and the
	// same text `forge lint --column-markers` reads its vocabulary out
	// of. Deliberately NOT read from gen/forge_descriptor/'s entity
	// columns: this must answer correctly on a project whose descriptor
	// is absent or stale, which is exactly the state a security gate is
	// least entitled to be silent in.
	ownerTables := ownerScopedTables(cfg, projectDir)
	// The table-level form (`COMMENT ON TABLE t IS 'forge:owner=…'`) arms
	// the gate the same way, and its generated ops resolve the caller
	// themselves (audit_unscoped_auth_owned_op.go). The raw-SQL check reads
	// both forms through one table → owner-column map.
	tableOwners := ownerTableSpecs(migrationTexts(cfg, projectDir))
	sqlOwners := sqlOwnerColumns(ownerTables, tableOwners)
	var ownedOpScoped []string

	// The CALLER half's vocabulary: forge's own seam, plus whatever the
	// project declared resolves the caller (//forge:resolves-caller in
	// code, lint.auth.caller_resolvers in forge.yaml). Read once — the
	// declarations are module-wide, not per service. mod follows a
	// handler's calls into the module's other packages (see
	// audit_unscoped_auth_module.go).
	mod := newModuleReach(projectDir)
	declaredResolvers, ignoredMarkers := codegen.DeclaredCallerResolvers(projectDir)
	configuredResolvers, invalidConfig := configuredCallerResolvers(cfg, mod)
	resolvers := mergeCallerResolvers(declaredResolvers, configuredResolvers)
	callers := newCallerSeam(resolvers)
	callers.mod = mod
	if mod != nil {
		mod.seam = callers
	}

	for _, svc := range services {
		// Only RPCs the proto declares authenticated are in scope. A
		// public RPC reading no claims is correct by construction, and
		// the scaffold says so in its own PUBLIC branch.
		authMethods := map[string]codegen.Method{}
		for _, m := range svc.Methods {
			if m.AuthRequired {
				authMethods[m.Name] = m
			}
		}
		if len(authMethods) == 0 {
			continue
		}
		declaredAuth += len(authMethods)

		// The descriptor names the PROTO service ("StorefrontService");
		// the handler package on disk is naming.ServicePackage's form of
		// it ("storefront"), which is what the scaffolders create. Going
		// through naming here — rather than lowercasing the proto name —
		// is why a multi-word service (AdminServerService →
		// internal/handlers/admin_server) resolves at all.
		dir, derr := codegen.ResolveComponentDir(projectDir, "internal/handlers", naming.ServicePackage(svc.Name))
		if derr != nil || !dirExists(dir.Dir) {
			// The service declares authenticated RPCs but has no handler
			// directory on disk yet (pre-scaffold). Nothing to inspect.
			unresolvedSvcs = append(unresolvedSvcs, svc.Name)
			continue
		}

		handlers, herr := scanHandlerAuthUse(dir.Dir, callers.forPackage(handlerImportPath(projectDir, dir.Dir)), svc)
		if herr != nil {
			unresolvedSvcs = append(unresolvedSvcs, svc.Name)
			continue
		}

		for name, method := range authMethods {
			// handlers is keyed by the Go method a handler declares, which
			// connect-go derives from the rpc name by its own casing
			// (GetOauth2token → GetOauth2Token) — never by the proto name.
			h, ok := handlers[method.GoName()]
			if !ok {
				// No handler method for this RPC in the user-owned tree —
				// an unwired stub, or a method forge has not scaffolded.
				// orphan_stubs owns that condition; this category does not
				// double-report it.
				continue
			}
			authTotal++
			rel := relPath(projectDir, h.File)
			if h.AckMalformed != "" {
				malformed = append(malformed, malformedAck{
					Service: svc.Name, Method: name, File: rel, Line: h.AckLine,
					Written: h.AckMalformed, Expected: "// " + AuthUnscopedOKDirective + " <reason>",
				})
			}
			switch {
			case h.AckReason != "":
				acknowledged = append(acknowledged, acknowledgedRPC{
					Service: svc.Name, Method: name, File: rel, Reason: h.AckReason,
				})
			case h.Unimplemented:
				unimplemented = append(unimplemented, unimplementedRPC{
					Service: svc.Name, Method: name, File: rel,
				})
			case h.ReadsCaller:
				// Resolving the caller is the WHOLE answer only when
				// forge can also see the query. When the query is a
				// hand-written string over a table the project declared
				// owned, it proves the caller was identified and nothing
				// about whether that identity reached the rows — so the
				// RPC is classified by what is in the SQL rather than
				// counted as verified.
				ev, hasOwned := classifyHandlerSQL(h.SQL, sqlOwners)
				if !hasOwned {
					scopedTotal++
					break
				}
				finding := rawSQLRPC{
					Service: svc.Name, Method: name, File: rel,
					Tables: ev.Tables, OwnerColumns: ev.OwnerColumns,
					MentionsOwnerColumn: ev.MentionsOwnerColumn,
				}
				if ev.MentionsOwnerColumn {
					rawSQLUnverifiable = append(rawSQLUnverifiable, finding)
				} else {
					rawSQLUnscoped = append(rawSQLUnscoped, finding)
				}
			case h.DelegatesOwnedOp && ownerSpecFor(method, tableOwners) != "":
				// The handler delegates to the generated op of a table
				// that declares `forge:owner=`, and that op resolves the
				// caller through s.deps.Owners: scoped by construction.
				scopedTotal++
				ownedOpScoped = append(ownedOpScoped, svc.Name+"."+name)
			default:
				unscoped = append(unscoped, newUnscopedFinding(svc.Name, method, rel, h.Delegating, ownerTables, tableOwners))
			}
		}
	}

	sortUnscoped(unscoped)
	sortAcknowledged(acknowledged)
	sort.Slice(unimplemented, func(i, j int) bool {
		if unimplemented[i].Service != unimplemented[j].Service {
			return unimplemented[i].Service < unimplemented[j].Service
		}
		return unimplemented[i].Method < unimplemented[j].Method
	})
	sortRawSQL(rawSQLUnscoped)
	sortRawSQL(rawSQLUnverifiable)

	// The gating subset: findings that cross a boundary the project
	// DECLARED. Kept as its own list rather than a flag on each finding
	// so a consumer can act on exactly the set that fails, without
	// re-deriving the predicate from the advisory set.
	var gating []unscopedRPC
	for _, u := range unscoped {
		if u.Table != "" {
			gating = append(gating, u)
		}
	}

	// The seam the findings and the hint name: the project's own declared
	// resolvers when it has any (they are what its handlers should call),
	// forge's scaffolded one otherwise.
	seam := codegen.CRUDAuthSeam()
	if len(resolvers) > 0 {
		names := make([]string, 0, len(resolvers))
		for _, r := range resolvers {
			names = append(names, r.String())
		}
		seam = strings.Join(names, " / ")
	}
	declared := make([]string, 0, len(resolvers))
	for _, r := range resolvers {
		declared = append(declared, r.ImportPath+"."+r.Func)
	}
	details := map[string]any{
		"authenticated_rpcs":         authTotal,
		"scoped_rpcs":                scopedTotal,
		"unscoped_rpcs":              unscoped,
		"owner_scoped_unscoped_rpcs": gating,
		"owner_scoped_tables":        sortedKeys(sqlOwners),
		"owner_marker":               schemadef.ColumnMarkerOwner,
		"table_owner_specs":          tableOwners,
		"owned_op_rpcs":              sortedStrings(ownedOpScoped),
		"acknowledged_rpcs":          acknowledged,
		"unimplemented_rpcs":         unimplemented,
		"raw_sql_unscoped_rpcs":      rawSQLUnscoped,
		"raw_sql_unverifiable_rpcs":  rawSQLUnverifiable,
		"auth_seam":                  seam,
		"caller_resolvers":           declared,
		"caller_resolver_marker":     codegen.ResolvesCallerDirective,
		"acknowledge_marker":         AuthUnscopedOKDirective,
		"hint": fmt.Sprintf(
			"resolve the caller with %s(ctx) and scope what the handler touches to those claims; "+
				"if an RPC is intentionally global, say so in code above it with `// %s <reason>`",
			seam, AuthUnscopedOKDirective),
	}
	if len(ignoredMarkers) > 0 {
		details["caller_resolver_markers_ignored"] = ignoredMarkers
	}
	if len(invalidConfig) > 0 {
		details["caller_resolvers_config_invalid"] = invalidConfig
	}

	// FAIL LOUDLY ON AN EMPTY DERIVATION. The project declares
	// authenticated RPCs, and this category matched a handler for NONE of
	// them — so every assertion below would pass vacuously and the audit
	// would report a clean auth surface it never actually looked at.
	//
	// This is not hypothetical: the first cut of this file resolved the
	// handler directory from the descriptor's proto service name
	// ("StorefrontService") instead of naming.ServicePackage's on-disk
	// form ("storefront"). It found zero handlers and reported
	// `"status": "ok"` against the very project whose IDOR it was written
	// to catch. A silent zero is the exact failure mode this category
	// exists to eliminate, so it reports warn and says what broke.
	if declaredAuth > 0 && authTotal == 0 {
		sort.Strings(unresolvedSvcs)
		details["declared_authenticated_rpcs"] = declaredAuth
		details["unresolved_services"] = unresolvedSvcs
		return audittype.Category{
			Status: audittype.StatusWarn,
			Summary: fmt.Sprintf(
				"%d authenticated RPC(s) declared, but no handler was matched for ANY of them — this check inspected nothing (services: %s)",
				declaredAuth, strings.Join(unresolvedSvcs, ", ")),
			Details: details,
		}
	}

	// THE RAW-SQL ERROR, reported before the CRUD gate because it is the
	// more specific claim: forge knows the table, knows the column the
	// migration declared, and has read a query that names the table and
	// not the column. That is a leak forge can point at, where the CRUD
	// gate reports the absence of a step.
	if len(rawSQLUnscoped) > 0 {
		// The generic hint is wrong advice here — it says to resolve the
		// caller, which these handlers already did, and following it
		// would leave the leak exactly where it is. Replace it rather
		// than print both.
		details["hint"] = rawSQLScopingHint()
		delete(details, "raw_sql_hint")
		return audittype.Category{
			Status: audittype.StatusError,
			Summary: fmt.Sprintf(
				"%d authenticated RPC(s) resolve the caller and then query %s-declared table(s) through hand-written SQL that never names the owner column — the caller was identified but the rows were not filtered by who they belong to: %s",
				len(rawSQLUnscoped), schemadef.ColumnMarkerOwner, describeRawSQL(rawSQLUnscoped)),
			Details: details,
		}
	}

	if len(unscoped) == 0 {
		// UNVERIFIABLE IS NOT CLEAN. These RPCs resolve the caller and
		// their SQL does name the owner column, but forge cannot see
		// that the value bound to it came from the caller's claims
		// rather than from the request. Reporting ok here would be the
		// same false confidence that let the measured leak ship — just
		// with better odds. It warns rather than errors because there is
		// no evidence of a defect, only an absence of evidence of
		// correctness, and those two deserve different words.
		if len(rawSQLUnverifiable) > 0 {
			details["hint"] = rawSQLScopingHint()
			return audittype.Category{
				Status: audittype.StatusWarn,
				Summary: fmt.Sprintf(
					"%d authenticated RPC(s) query %s-declared table(s) through hand-written SQL — forge cannot see inside the query, so their scoping was NOT verified: %s",
					len(rawSQLUnverifiable), schemadef.ColumnMarkerOwner, describeRawSQL(rawSQLUnverifiable)),
				Details: details,
			}
		}
		// An acknowledged RPC never resolves the caller: it is declared
		// global with the marker, and is counted apart so the summary does
		// not claim it does.
		summary := fmt.Sprintf("all %d authenticated RPC(s) resolve the caller", authTotal)
		implemented, noun := authTotal, "authenticated"
		if len(unimplemented) > 0 {
			implemented, noun = authTotal-len(unimplemented), "implemented authenticated"
			summary = fmt.Sprintf("all %d %s RPC(s) resolve the caller", implemented, noun)
		}
		if len(acknowledged) > 0 {
			summary = fmt.Sprintf("%d of %d %s RPC(s) resolve the caller; the other %d are acknowledged global with `// %s`",
				scopedTotal, implemented, noun, len(acknowledged), AuthUnscopedOKDirective)
		}
		summary += unimplementedNote(len(unimplemented))
		if authTotal == 0 {
			summary = "no authenticated RPCs with handlers to inspect (n/a)"
		}
		return audittype.Category{Status: audittype.StatusOK, Summary: summary, Details: details}
	}

	// GATING BRANCH. At least one of these RPCs reads a table the
	// project declared owned, so this is one principal reading or
	// writing another's rows, not a style note. The summary names the
	// tables so the finding is falsifiable at the migration, and names
	// the acknowledgement so the escape hatch is discoverable at the
	// moment someone needs it rather than buried in a skill.
	if len(gating) > 0 {
		// Only on the armed branch: the hint explains where a
		// `remediation` snippet goes, and an advisory finding carries
		// none, so printing it there would name a field that is not
		// present.
		details["owner_scoping_hint"] = ownerScopingHint(gating)
		return audittype.Category{
			Status: audittype.StatusError,
			Summary: fmt.Sprintf(
				"%d authenticated RPC(s) over %s-declared data never resolve the caller — every signed-in caller is treated identically regardless of who the rows belong to (%s): %s",
				len(gating), schemadef.ColumnMarkerOwner, seam, describeGating(gating)),
			Details: details,
		}
	}

	return audittype.Category{
		Status: audittype.StatusWarn,
		// Deliberately domain-neutral: this category also fires on
		// streaming and batch RPCs that touch no rows at all, and a
		// summary that says "rows" would read as a CRUD-only finding.
		Summary: fmt.Sprintf(
			"%d of %d authenticated RPC(s) never resolve the caller — every signed-in caller is treated identically (%s)",
			len(unscoped), authTotal-len(unimplemented), seam) + unimplementedNote(len(unimplemented)),
		Details: details,
	}
}

// commentOnColumnOwnerRE matches a `COMMENT ON COLUMN <table>.<col> IS
// '...'` statement, capturing the dotted object and the comment body.
// It mirrors internal/cli/lint's commentOnColumnRe — the same statement
// form, read for a different question.
var commentOnColumnOwnerRE = regexp.MustCompile(`(?is)\bcomment\s+on\s+column\s+(` + sqlident.Qualified + `)\s+is\s+'((?:[^']|'')*)'`)

// ownerMarkerTokenRE finds a `forge:owner` declaration as a WHOLE
// token. The trailing boundary is what keeps a future or unrelated
// marker whose name merely begins with this one (`forge:owner-hint`)
// from arming a gate its author never declared — the same exact-match
// discipline internal/cli/lint's unknownMarkerFinding applies.
var ownerMarkerTokenRE = regexp.MustCompile(regexp.QuoteMeta(schemadef.ColumnMarkerOwner) + `(?:[^\w:-]|$)`)

// ownerScopedTables maps each table carrying a `forge:owner` column
// declaration to that column's name, read from the project's migrations.
// An unreadable or absent migrations directory yields the empty map —
// which disarms the gate, the correct direction for a filesystem problem
// that is not itself a security finding.
//
// The COLUMN is carried, not merely the fact that one exists, because
// the remediation the gate prints names it in a WHERE predicate. A gate
// that knows a boundary exists but cannot say which column expresses it
// can only restate the problem.
func ownerScopedTables(cfg *config.ProjectConfig, projectDir string) map[string]string {
	out := map[string]string{}
	for _, sql := range migrationTexts(cfg, projectDir) {
		for table, column := range ownerScopedTablesIn(sql) {
			out[table] = column
		}
	}
	return out
}

// migrationTexts reads the primary database's up migrations in name order —
// which is timestamp order — so a later migration that moves ownership wins
// over the one it replaced, matching the applied schema. An unreadable or
// absent migrations directory yields nothing, which disarms the gate: the
// correct direction for a filesystem problem that is not itself a security
// finding.
func migrationTexts(cfg *config.ProjectConfig, projectDir string) []string {
	migDir := config.MigrationsPathIn(projectDir)
	if cfg != nil {
		migDir = filepath.Join(projectDir, filepath.FromSlash(cfg.PrimaryDatabase().MigrationsPath()))
	}
	var paths []string
	_ = filepath.WalkDir(migDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // a missing/unreadable migration tree disarms the gate, it is not a finding
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".up.sql") {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	sort.Strings(paths)
	var out []string
	for _, path := range paths {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		out = append(out, string(data))
	}
	return out
}

// ownerScopedTablesIn maps the owner-declaring tables in one migration's
// SQL text to the column each one declared. Split out from the walk so
// the parsing rule — which is the whole correctness surface here — is
// testable without a filesystem.
func ownerScopedTablesIn(sql string) map[string]string {
	out := map[string]string{}
	for _, m := range commentOnColumnOwnerRE.FindAllStringSubmatch(sql, -1) {
		object, body := m[1], m[2]
		if !ownerMarkerTokenRE.MatchString(body) {
			continue
		}
		// object is `table.column`, or `schema.table.column` when
		// qualified, any part possibly quoted. The table is the
		// second-to-last part; a bare column name (no dot) names no
		// table and is skipped.
		parts := sqlident.Parts(object)
		if len(parts) < 2 {
			continue
		}
		table, column := parts[len(parts)-2], parts[len(parts)-1]
		// First declaration within one file wins, mirroring
		// codegen.OwnerColumn, which takes the entity's first owner
		// column. A table declaring two is expressing a composite scope
		// neither this gate nor the scaffold can guess at.
		if _, seen := out[table]; !seen {
			out[table] = column
		}
	}
	return out
}

// ownerEntityFor maps a CRUD RPC name to the owned entity and
// table it operates on, returning empty strings when the RPC is not CRUD
// or its table declares no owner column.
//
// The derivation is codegen.CRUDShape plus naming, which is EXACTLY how
// BuildSchemaEntities joins protos to tables — including the
// singularize-then-pluralize step a List RPC needs, and a `// forge:custom`
// rpc mapping to no entity. Reusing that derivation rather than
// re-inventing one is what keeps the gate's idea of "which table does this
// RPC touch" from drifting away from the generator's.
func ownerEntityFor(method codegen.Method, ownerTables, tableOwners map[string]string) (entity, table, column, spec string) {
	if len(ownerTables) == 0 && len(tableOwners) == 0 {
		return "", "", "", ""
	}
	op, name := codegen.CRUDShape(method)
	if op == "" {
		return "", "", "", ""
	}
	if op == "list" {
		name = inflection.Singular(name)
	}
	for _, candidate := range codegen.EntityTableCandidates(name) {
		// The table-level form wins: it is the enforced one, and the
		// generated op honours it whatever the columns say.
		if s, owned := tableOwners[candidate]; owned {
			return name, candidate, "", s
		}
		if col, owned := ownerTables[candidate]; owned {
			return name, candidate, col, ""
		}
	}
	return "", "", "", ""
}

// ownerSpecFor is the table-level `forge:owner=` declaration of the table a
// CRUD rpc operates on, "" when it declares none.
func ownerSpecFor(method codegen.Method, tableOwners map[string]string) string {
	_, _, _, spec := ownerEntityFor(method, nil, tableOwners)
	return spec
}

// newUnscopedFinding builds one finding, resolving the owned entity
// behind the RPC and the wrapper that would scope it.
//
// Both derivations live here rather than at the call site so the
// gating fields cannot be set independently of each other: Table is what
// arms the gate and Remediation is what the user does about it, and a
// finding carrying one without the other is the failure this whole
// change exists to fix.
func newUnscopedFinding(service string, method codegen.Method, file string, delegating bool, ownerTables, tableOwners map[string]string) unscopedRPC {
	entity, table, column, spec := ownerEntityFor(method, ownerTables, tableOwners)
	remediation := scopingRemediation(method, entity, column)
	if spec != "" {
		remediation = ownedOpRemediation(method, table, spec)
	}
	return unscopedRPC{
		Service:     service,
		Method:      method.Name,
		File:        file,
		Delegating:  delegating,
		Entity:      entity,
		Table:       table,
		OwnerSpec:   spec,
		Remediation: remediation,
	}
}

// scopingRemediation renders the owner-scoping wrapper this RPC needs,
// or "" when forge cannot render an honest one.
//
// Empty is the correct answer in two cases, and emitting a plausible
// block in either would re-create the dead end this carries the user out
// of. An advisory finding (no entity/column, because no table declared
// ownership) has no column to name in a predicate — forge would be
// inventing a policy the project never declared. A custom RPC maps to no
// generated op, so there is no Fetch/Filters/Persist seam to wrap;
// RenderScopedCRUDShim reports that as an error and it is dropped here
// rather than surfaced, since "this RPC is unscoped" is still a true and
// useful finding on its own.
func scopingRemediation(method codegen.Method, entity, column string) string {
	if entity == "" || column == "" {
		return ""
	}
	// The audit reads owner declarations off the migration text, which
	// does not say which column is the key. Forge keys every table it
	// births on `id`, so an `id` owner is a table owned by itself, and its
	// Update must not stamp the key (see CRUDMethodTemplateData.OwnerIsPK).
	snippet, err := codegen.RenderScopedCRUDShim(method.Name, method.InputType, method.OutputType, entity, column, column == "id")
	if err != nil {
		return ""
	}
	return snippet
}

// ownerScopingHint explains why re-running generate will not produce the
// wrapper, and where the snippet in each finding goes instead.
//
// Without this sentence the reader's first move is to run `forge
// generate` — the auth skill used to say the wrapper appears that way —
// see nothing change, and conclude forge is broken. The claim is
// falsifiable at their own file: handlers_crud.go opens with "yours:
// scaffolded once, never touched again".
func ownerScopingHint(gating []unscopedRPC) string {
	for _, g := range gating {
		if g.OwnerSpec != "" {
			return ownerScopingHintText() + " For a table that declares `" + schemadef.TableMarkerOwner + "=`, the generated op already " +
				"scopes by it through s.deps.Owners: the finding's remediation is the plain delegation to `s.crud<Method>Op()` " +
				"(run `" + cmdutil.Name() + " generate` first if the op predates the declaration)."
		}
	}
	return ownerScopingHintText()
}

func ownerScopingHintText() string {
	return "forge scaffolds this wrapper into internal/handlers/<service>/handlers_crud.go only when that file is first created, " +
		"and a `" + schemadef.ColumnMarkerOwner + "` declaration lives in a migration written later — so on an existing project the scaffold " +
		"cannot fire and `" + cmdutil.Name() + " generate` will not add it. Paste each finding's `remediation` into the named method in that " +
		"file instead, replacing the placeholder owner expression with your real claims-to-column mapping."
}

// describeGating renders the gating findings as `Service.Method (table)`
// pairs for the summary line, capped so a project with dozens of them
// prints a usable sentence rather than a wall. The full set is always in
// details.owner_scoped_unscoped_rpcs.
func describeGating(gating []unscopedRPC) string {
	const max = 5
	parts := make([]string, 0, max)
	for i, g := range gating {
		if i == max {
			parts = append(parts, fmt.Sprintf("... (%d total)", len(gating)))
			break
		}
		parts = append(parts, fmt.Sprintf("%s.%s (%s)", g.Service, g.Method, g.Table))
	}
	return strings.Join(parts, ", ")
}

// sortedKeys returns a set's members in a stable order, so the emitted
// JSON does not churn between runs on map iteration order alone.
func sortedKeys[V any](set map[string]V) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortUnscoped(v []unscopedRPC) {
	sort.SliceStable(v, func(i, j int) bool {
		if v[i].Service != v[j].Service {
			return v[i].Service < v[j].Service
		}
		return v[i].Method < v[j].Method
	})
}

func sortAcknowledged(v []acknowledgedRPC) {
	sort.SliceStable(v, func(i, j int) bool {
		if v[i].Service != v[j].Service {
			return v[i].Service < v[j].Service
		}
		return v[i].Method < v[j].Method
	})
}

func relPath(projectDir, path string) string {
	rel, err := filepath.Rel(projectDir, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

// handlerAuthUse is what the AST pass learned about one handler method.
type handlerAuthUse struct {
	File        string
	ReadsCaller bool
	Delegating  bool
	AckReason   string
	// DelegatesOwnedOp is true when the handler reaches its own generated
	// op and that op resolves the caller through s.deps.Owners — the
	// shape of an op over a table declaring `forge:owner=`
	// (audit_unscoped_auth_owned_op.go).
	DelegatesOwnedOp bool
	// Unimplemented is true when the body only answers CodeUnimplemented
	// (bodyServesUnimplemented): there is nothing behind it to scope.
	Unimplemented bool
	// AckMalformed is a `forge:auth-unscoped-ok` comment on the handler
	// that ackReason did not accept, as written; AckLine is its line.
	AckMalformed string
	AckLine      int
	// SQL and HasSQL describe the hand-written queries this handler can
	// reach. They are carried separately from ReadsCaller because they
	// answer a different question: ReadsCaller says the caller was
	// resolved, SQL says whether that resolution could possibly have
	// reached the rows. For a delegating handler the two are the same
	// answer; for an aggregation over a string constant they are not,
	// and collapsing them is what let a cross-boundary read ship behind
	// a ✓. See audit_unscoped_auth_rawsql.go.
	SQL    []string
	HasSQL bool
}

// scanHandlerAuthUse parses every non-test .go file in a handler package
// and reports, per method on the service receiver, whether its body
// reaches the auth seam.
//
// Two passes. The first computes the set of names in this package that
// reach the seam, as a fixed point (seamReachingNames); the second
// re-checks each handler, clearing it if its body calls any name in
// that set. The second pass stays a single lookup because the first has
// already done the transitive work.
//
// The NAME analysis is package-local: a bare or receiver call
// (`scope(ctx)`, `s.caller(ctx)`) is resolved only against this package's
// declarations, because this pass resolves names, not types, and following
// a bare name across a package boundary would clear handlers on
// coincidence.
//
// Two crossings are exact rather than inferred. A function the project
// declared a resolver (`//forge:resolves-caller`, or forge.yaml
// lint.auth.caller_resolvers) is matched through the import that binds its
// package in each file, so `auth.GetUserIDFromContext` counts only where
// `auth` IS that package. And an import-qualified call into another
// package of the same module (`auth.MustGetUserID(ctx)`) is followed into
// that package, up to maxWrapperHops packages deep — a package-level
// function is unique in its package, so the import names it exactly (see
// audit_unscoped_auth_module.go).
func scanHandlerAuthUse(dir string, callers packageCallerSeam, svc codegen.ServiceDef) (map[string]handlerAuthUse, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	fset := token.NewFileSet()
	type parsedFile struct {
		path string
		file *ast.File
	}
	var files []parsedFile
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		f, perr := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
		if perr != nil {
			// A handler package that does not parse is a build problem,
			// not an auth finding. Skip the file rather than guess.
			continue
		}
		files = append(files, parsedFile{path: path, file: f})
	}

	// Pass 1: which names in this package reach the seam, at any depth?
	// Each declaration is judged against its OWN file's imports, which is
	// how a declared resolver's package identifier is bound.
	var decls []*ast.FuncDecl
	fileSeams := map[*ast.FuncDecl]fileCallerSeam{}
	fileOf := map[*ast.FuncDecl]*ast.File{}
	for _, pf := range files {
		fs := callers.forFile(pf.file)
		for _, decl := range pf.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			decls = append(decls, fn)
			fileSeams[fn] = fs
			fileOf[fn] = pf.file
		}
	}
	// reachesDirectly: the body names the seam or a declared resolver, or
	// calls a function in another package of the module that reaches one.
	reachesDirectly := func(fn *ast.FuncDecl) bool {
		return bodyReachesAuthSeam(fn.Body, fileSeams[fn]) ||
			callers.mod.callsReachingImport(fn, fileOf[fn], maxWrapperHops)
	}
	seamReaching := seamReachingNames(decls, func(fn *ast.FuncDecl) bool {
		return callers.declaresLocal(fn) || reachesDirectly(fn)
	})

	// The SQL half, computed over the SAME declaration set and the same
	// package-local call graph, so the two analyses cannot disagree
	// about how far a handler's body reaches.
	astFiles := make([]*ast.File, 0, len(files))
	for _, pf := range files {
		astFiles = append(astFiles, pf.file)
	}
	sqlByName := reachableSQL(decls, sqlLiteralsByDecl(decls, packageSQLConsts(astFiles)))
	delegatesOwnedOp := ownedOpDelegations(decls)
	embeddedReceivers := embeddedUnimplementedReceivers(astFiles, svc)

	out := map[string]handlerAuthUse{}
	rank := map[string]int{}
	for _, pf := range files {
		imports := importIdents(pf.file)
		for _, decl := range pf.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Recv == nil || len(fn.Recv.List) == 0 {
				continue
			}
			if !fn.Name.IsExported() {
				continue
			}
			use := handlerAuthUse{
				File:       pf.path,
				Delegating: bodyIsCRUDDelegation(fn.Body),
				AckReason:  ackReason(fn.Doc),
			}
			use.Unimplemented = bodyServesUnimplemented(fn, imports, embeddedReceivers, svc.Name)
			if use.AckReason == "" {
				if c := malformedAckComment(fn.Doc); c != nil {
					use.AckMalformed = strings.TrimSpace(c.Text)
					use.AckLine = fset.Position(c.Pos()).Line
				}
			}
			use.ReadsCaller = reachesDirectly(fn) ||
				bodyCallsSeamReachingHelper(fn.Body, seamReaching, fn.Name.Name)
			use.DelegatesOwnedOp = delegatesOwnedOp(fn.Name.Name)
			use.SQL = sqlByName[fn.Name.Name]
			use.HasSQL = len(use.SQL) > 0
			// Two exported methods can share an rpc's name — the handler
			// and a generated mock of a dependency (mock_gen.go's
			// MockRepository.GetChat). The handler is the one to judge,
			// so the scaffolded receiver wins, then hand-written code.
			if r := handlerRank(pf.path, fn); r >= rank[fn.Name.Name] {
				rank[fn.Name.Name] = r
				out[fn.Name.Name] = use
			}
		}
	}
	return out, nil
}

// handlerRank orders the exported methods sharing one name by how likely
// each is to be the rpc's handler: a method on the scaffolded `Service`
// type, then one in a hand-written file, then one in a generated file.
func handlerRank(path string, fn *ast.FuncDecl) int {
	recv := fn.Recv.List[0].Type
	if star, ok := recv.(*ast.StarExpr); ok {
		recv = star.X
	}
	if id, ok := recv.(*ast.Ident); ok && id.Name == "Service" {
		return 2
	}
	if !strings.HasSuffix(path, "_gen.go") {
		return 1
	}
	return 0
}

// seamReachingNames returns the names in one handler package that
// resolve the caller — directly, or through any chain of calls that
// stays inside the package.
//
// It is a fixed point, not a walk: start from the funcs whose own body
// names the seam, then repeatedly admit any func that calls a name
// already admitted, until a round adds nothing. Termination is
// structural — the set only ever grows, and it is bounded by the number
// of declarations in the package — so a recursive or mutually recursive
// helper converges instead of spinning. Doing it this way rather than
// recursing down call edges is also why no cycle detection is needed:
// there is no stack to blow.
//
// Computing the closure HERE, rather than deepening pass 2's per-handler
// check, is what keeps the cost linear in declarations rather than
// exponential in path count, and leaves pass 2 a single set lookup.
//
// # Ambiguous names vouch for nobody
//
// The set is keyed by NAME, and a name is not unique in a Go package:
// two package-level funcs cannot share one, but a method and a free
// function can, as can methods on two different receiver types. This
// pass resolves names, not types — a call site does not name its
// receiver's type, and type-checking a handler package that may not even
// compile is not something a security audit should depend on.
//
// So a name is admitted only when EVERY declaration carrying it reaches
// the seam. If `scope` names both a method that resolves the caller and
// a free function that does not, a handler calling `scope(ctx)` is NOT
// cleared. The alternative — admitting a name if any declaration
// reaches — would clear a handler that called the unscoped spelling,
// which is precisely the silent pass this category exists to prevent.
// Ambiguity is rare; a false clear is unrecoverable.
func seamReachingNames(decls []*ast.FuncDecl, reachesDirectly func(*ast.FuncDecl) bool) map[string]bool {
	// reaches[i] tracks declaration i, not its name, so that two
	// same-named funcs are judged separately before the name-level
	// question is asked.
	reaches := make([]bool, len(decls))
	for i, fn := range decls {
		reaches[i] = reachesDirectly(fn)
	}

	// admitted collapses the per-declaration truth onto names, keeping
	// only names whose every declaration reaches. Recomputed each round
	// because a newly-reaching declaration can complete a name.
	admitted := func() map[string]bool {
		total := map[string]int{}
		reaching := map[string]int{}
		for i, fn := range decls {
			total[fn.Name.Name]++
			if reaches[i] {
				reaching[fn.Name.Name]++
			}
		}
		out := map[string]bool{}
		for name, n := range reaching {
			if n == total[name] {
				out[name] = true
			}
		}
		return out
	}

	// Bounded by construction: every round that continues has flipped at
	// least one declaration from false to true, and a declaration never
	// flips back, so there can be at most len(decls) productive rounds.
	// The bound is written as a loop condition anyway — a fixed point
	// that silently depended on its own monotonicity would be one edit
	// away from hanging the audit.
	for round := 0; round <= len(decls); round++ {
		names := admitted()
		changed := false
		for i, fn := range decls {
			if reaches[i] {
				continue
			}
			// The self guard, preserved: a function calling itself has
			// resolved nobody, so it must not appear in its own
			// justification.
			if bodyCallsSeamReachingHelper(fn.Body, names, fn.Name.Name) {
				reaches[i] = true
				changed = true
			}
		}
		if !changed {
			break
		}
	}

	return admitted()
}

// ackReason extracts the reason from a `forge:auth-unscoped-ok:` doc
// comment. Returns "" when the directive is absent OR carries no reason —
// a bare directive is not an acknowledgement, it is the unfalsifiable
// comment again.
func ackReason(doc *ast.CommentGroup) string {
	if doc == nil {
		return ""
	}
	for _, c := range doc.List {
		text := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(c.Text), "//"))
		rest, ok := strings.CutPrefix(text, AuthUnscopedOKDirective)
		if !ok {
			continue
		}
		if reason := strings.TrimSpace(rest); reason != "" {
			return reason
		}
	}
	return ""
}

// malformedAckComment returns the first comment in doc that starts the
// `forge:auth-unscoped-ok` marker — `//` or `// `, then the name and nothing
// that continues it — but is not an acknowledgement ackReason accepts:
// `// forge:auth-unscoped-ok` alone, `// forge:auth-unscoped-ok:` with no
// reason, or `// forge:auth-unscoped-ok — reason`. nil when there is none.
// The caller checks ackReason first, so a doc that ALSO carries a valid
// acknowledgement is not reported.
//
// A bare marker used to be silently ignored: the RPC stayed in
// unscoped_rpcs and nothing said the line above it had been read and
// refused (FG-32).
func malformedAckComment(doc *ast.CommentGroup) *ast.Comment {
	if doc == nil {
		return nil
	}
	name := strings.TrimSuffix(AuthUnscopedOKDirective, ":")
	for _, c := range doc.List {
		text := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(c.Text), "//"))
		rest, ok := strings.CutPrefix(text, name)
		if !ok {
			continue
		}
		if rest != "" && isMarkerNameByte(rest[0]) {
			continue // a different marker whose name starts with this one
		}
		if ackReason(&ast.CommentGroup{List: []*ast.Comment{c}}) == "" {
			return c
		}
	}
	return nil
}

func isMarkerNameByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '-' || b == '_'
}

// withMalformedAcks adds the malformed acknowledgements to a finished
// category: listed under details.malformed_acknowledgements, named in the
// summary, and an ok category becomes warn. It never raises warn to error —
// the RPC behind a malformed marker is graded by what its handler does, as
// if the marker were absent, which is exactly what forge did with it.
func withMalformedAcks(cat audittype.Category, malformed []malformedAck) audittype.Category {
	if len(malformed) == 0 {
		return cat
	}
	sort.SliceStable(malformed, func(i, j int) bool {
		if malformed[i].Service != malformed[j].Service {
			return malformed[i].Service < malformed[j].Service
		}
		return malformed[i].Method < malformed[j].Method
	})
	if cat.Details == nil {
		cat.Details = map[string]any{}
	}
	cat.Details["malformed_acknowledgements"] = malformed
	parts := make([]string, 0, len(malformed))
	for _, m := range malformed {
		parts = append(parts, fmt.Sprintf("%s.%s (%s:%d)", m.Service, m.Method, m.File, m.Line))
	}
	cat.Summary += fmt.Sprintf("; %d %s marker(s) acknowledge nothing — no reason after the colon, so forge ignored them: %s. Write `// %s <reason>`",
		len(malformed), strings.TrimSuffix(AuthUnscopedOKDirective, ":"), strings.Join(parts, ", "), AuthUnscopedOKDirective)
	if cat.Status == audittype.StatusOK {
		cat.Status = audittype.StatusWarn
	}
	return cat
}

// bodyReachesAuthSeam reports whether a function body names the auth seam,
// one of the claims accessors it re-exports, or a resolver the project
// declared. forge's own identifiers come from codegen, which is what stamps
// the seam into the scaffold — renaming it there moves this check — and
// match by name, as they always have. A declared resolver matches only
// through the import that binds its package in this file.
func bodyReachesAuthSeam(body *ast.BlockStmt, fs fileCallerSeam) bool {
	seamFuncs := codegen.CRUDAuthSeamFuncs()
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		switch x := n.(type) {
		case *ast.SelectorExpr:
			if seamFuncs[x.Sel.Name] {
				found = true
			} else if id, ok := x.X.(*ast.Ident); ok && fs.qualified[id.Name][x.Sel.Name] {
				found = true
			}
		case *ast.CallExpr:
			// A dot-imported resolver is called by its bare name.
			if id, ok := x.Fun.(*ast.Ident); ok && fs.dotted[id.Name] {
				found = true
			}
		}
		return !found
	})
	return found
}

// callerSeam indexes the project's declared caller resolvers
// (//forge:resolves-caller) by the import path of the package declaring
// them.
type callerSeam struct {
	byPath map[string]declaredPackage
	// mod follows import-qualified calls into the module's other
	// packages; nil disables that (no go.mod, or a test of the local pass).
	mod *moduleReach
}

type declaredPackage struct {
	name  string          // the package clause: what an unaliased import binds
	funcs map[string]bool // the declared resolvers in it
}

func newCallerSeam(resolvers []codegen.CallerResolver) callerSeam {
	s := callerSeam{byPath: map[string]declaredPackage{}}
	for _, r := range resolvers {
		p, ok := s.byPath[r.ImportPath]
		if !ok {
			p = declaredPackage{name: r.Package, funcs: map[string]bool{}}
		}
		p.funcs[r.Func] = true
		s.byPath[r.ImportPath] = p
	}
	return s
}

// forPackage narrows the seam to one handler package, so a resolver
// declared IN that package (called by its bare name) is recognised too.
func (s callerSeam) forPackage(importPath string) packageCallerSeam {
	return packageCallerSeam{callerSeam: s, self: s.byPath[importPath].funcs}
}

// packageCallerSeam is callerSeam as seen from one handler package.
type packageCallerSeam struct {
	callerSeam
	self map[string]bool
}

// declaresLocal reports whether fn is itself a resolver the project
// declared in this package: calling it resolves the caller, so it seeds the
// package's fixed point like a direct call to the seam.
func (s packageCallerSeam) declaresLocal(fn *ast.FuncDecl) bool {
	return fn.Recv == nil && s.self[fn.Name.Name]
}

// fileCallerSeam is the seam as one file's imports bind it: the identifier
// each declaring package is imported under, and the resolvers a dot import
// brings in by bare name.
type fileCallerSeam struct {
	qualified map[string]map[string]bool
	dotted    map[string]bool
}

func (s packageCallerSeam) forFile(f *ast.File) fileCallerSeam {
	fs := fileCallerSeam{qualified: map[string]map[string]bool{}, dotted: map[string]bool{}}
	for _, imp := range f.Imports {
		importPath, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		p, ok := s.byPath[importPath]
		if !ok {
			continue
		}
		ident := p.name
		if imp.Name != nil {
			ident = imp.Name.Name
		}
		switch ident {
		case "_":
		case ".":
			for fn := range p.funcs {
				fs.dotted[fn] = true
			}
		default:
			fs.qualified[ident] = p.funcs
		}
	}
	return fs
}

// handlerImportPath is the import path of the handler package in dir, or
// "" when the module path cannot be read.
func handlerImportPath(projectDir, dir string) string {
	module, err := codegen.GetModulePath(projectDir)
	if err != nil {
		return ""
	}
	rel, err := filepath.Rel(projectDir, dir)
	if err != nil {
		return ""
	}
	return path.Join(module, filepath.ToSlash(rel))
}

// bodyCallsSeamReachingHelper reports whether the body calls a function in
// the same package that itself reaches the seam. Self-recursion is
// excluded so a method cannot vouch for itself.
func bodyCallsSeamReachingHelper(body *ast.BlockStmt, seamReaching map[string]bool, self string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		var name string
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			name = fn.Name
		case *ast.SelectorExpr:
			// s.helper(...) — the receiver-method factoring.
			name = fn.Sel.Name
		}
		if name != "" && name != self && seamReaching[name] {
			found = true
			return false
		}
		return true
	})
	return found
}

// bodyIsCRUDDelegation reports whether a body is exactly the shape the
// CRUD shim template emits: a single return of a crud.Handle*(...) call.
// It is metadata on the finding, never the finding itself — a hand-written
// unscoped handler is just as exposed as a delegating one.
func bodyIsCRUDDelegation(body *ast.BlockStmt) bool {
	if len(body.List) != 1 {
		return false
	}
	ret, ok := body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return false
	}
	outer, ok := ret.Results[0].(*ast.CallExpr)
	if !ok {
		return false
	}
	inner, ok := outer.Fun.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := inner.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return pkg.Name == codegen.CRUDDelegatePkgName() && strings.HasPrefix(sel.Sel.Name, "Handle")
}
