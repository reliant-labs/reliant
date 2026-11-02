package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/internal/linter/forgeconv"
	"github.com/reliant-labs/forge/internal/naming"
	"github.com/reliant-labs/forge/internal/templates"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// mutationMarkerRE matches the `forge:mutation` comment convention. The
// marker is a plain proto comment (deliberately NOT a proto option) placed in
// an RPC's leading doc-comment — or trailing on its `rpc` line — to force the
// hook generator to emit a useMutation wrapper instead of the useQuery the
// classifier (codegen.isQueryRPC) chose.
//
// The other direction — a read the name does not reveal (Health, Version) —
// is not a forge marker: the rpc declares protobuf's standard
// `option idempotency_level = NO_SIDE_EFFECTS;`, which the classifier reads
// first. This marker still wins over it.
//
// It exists because the RPC-name heuristic (codegen.isQueryMethod) can't be
// right for every imperative RPC: verbs that happen to start with a query
// prefix are misclassified as reads — IssueRefund/IssuePrescription ("Is…"),
// CheckoutCart ("Check…"), CountersignAgreement ("Count…") — and a non-CRUD
// custom RPC (PlaceOrder, ReviewSubmission) can mutate state the heuristic has
// no way to know about. The marker is the author's explicit override.
//
// The marker NAME comes from codegen.KnownProtoMarkers (proto_markers.go) so
// this pass and `forge lint --proto-markers` share one vocabulary. The PATTERN
// stays this package's own: scanRPCHookMarkers does its own comment-block
// attachment walk and hands this expression an already-isolated comment, so it
// needs no `//` prefix and no full-line anchor — unlike the entity-birth and
// descriptor recognizers. Only the spelling is shared, not the scanning.
var mutationMarkerRE = regexp.MustCompile(regexp.QuoteMeta(codegen.ProtoMarkerMutation) + `\b`)

// rpcDeclRE matches a proto `rpc <Name>(` service-method declaration and
// captures the method name.
var rpcDeclRE = regexp.MustCompile(`^\s*rpc\s+([A-Za-z_]\w*)\s*\(`)

// queryScopeMarkerRE matches `forge:query-scope [<Entity>]` (see
// codegen.ProtoMarkerQueryScope) and captures the entity. A hyphen or word
// character right after the token is a different, longer token, so it never
// matches; a bare marker captures "" for ApplyQueryScopes to refuse.
var queryScopeMarkerRE = regexp.MustCompile(regexp.QuoteMeta(codegen.ProtoMarkerQueryScope) +
	`(?:[ \t]+([A-Za-z_]\w*))?(?:[^\w-]|$)`)

// rpcHookMarkers is what an rpc's comments tell the hook generator.
type rpcHookMarkers struct {
	// Mutation: `// forge:mutation`.
	Mutation map[string]bool
	// QueryScope: rpc → the entity `// forge:query-scope <Entity>` names
	// ("" for a marker naming none).
	QueryScope map[string]string
}

// scanRPCHookMarkers scans a service's proto source for the rpc comment
// markers the hook generator reads: `// forge:mutation` and
// `// forge:query-scope <Entity>`.
//
// A marker attaches like a protoc leading doc-comment: it counts when it
// appears in the contiguous comment block directly above the `rpc` line, or
// as a trailing comment on the `rpc` line itself. A blank (non-comment) line
// between a comment and the rpc breaks the attachment, matching how protoc
// associates leading comments — so a marker can't leak onto an unrelated RPC
// further down the file.
//
// A missing or unreadable proto file is NOT an error: hooks still generate
// with the heuristic query/mutation classification. (Unit-test ServiceDefs,
// for one, point at proto paths with no file on disk.)
func scanRPCHookMarkers(protoPath string) rpcHookMarkers {
	out := rpcHookMarkers{Mutation: map[string]bool{}, QueryScope: map[string]string{}}
	content, err := os.ReadFile(protoPath)
	if err != nil {
		return out
	}
	pendingMutation := false
	pendingScope, havePendingScope := "", false
	reset := func() { pendingMutation, pendingScope, havePendingScope = false, "", false }
	for _, line := range strings.Split(string(content), "\n") {
		if m := rpcDeclRE.FindStringSubmatch(line); m != nil {
			// Marker in the attached comment block above, or trailing on the
			// rpc line itself.
			if pendingMutation || mutationMarkerRE.MatchString(line) {
				out.Mutation[m[1]] = true
			}
			if sm := queryScopeMarkerRE.FindStringSubmatch(line); sm != nil {
				out.QueryScope[m[1]] = sm[1]
			} else if havePendingScope {
				out.QueryScope[m[1]] = pendingScope
			}
			reset()
			continue
		}
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "//"),
			strings.HasPrefix(trimmed, "/*"),
			strings.HasPrefix(trimmed, "*"):
			// Comment line within (or opening) the block above the rpc.
			if mutationMarkerRE.MatchString(trimmed) {
				pendingMutation = true
			}
			if sm := queryScopeMarkerRE.FindStringSubmatch(trimmed); sm != nil {
				pendingScope, havePendingScope = sm[1], true
			}
		default:
			// A blank line breaks leading-comment attachment, and any
			// other statement resets it too.
			reset()
		}
	}
	return out
}

// serviceProtoSourcePath is the .proto file on disk that declares a service.
// The descriptor records ProtoFile relative to the buf module root —
// `services/item/v1/item.proto`, the module being proto/ — so joining it to
// the project root names a file that does not exist. Every marker the hook
// generator reads was dropped that way in a real project (forge:mutation
// included), silently, because a missing file is the documented fallback;
// only fixtures that spelled the path `proto/...` ever saw one. A path that
// already resolves from the project root is kept as it is.
func serviceProtoSourcePath(projectDir, protoFile string) string {
	direct := filepath.Join(projectDir, protoFile)
	if _, err := os.Stat(direct); err == nil {
		return direct
	}
	return filepath.Join(projectDir, "proto", protoFile)
}

// serviceHookData is a service's hook template data with every proto
// comment marker applied: `// forge:mutation` first, so that
// `// forge:query-scope` is checked against the rpc's final kind.
func serviceHookData(svc codegen.ServiceDef, projectDir string) (codegen.FrontendHookTemplateData, error) {
	data := codegen.ServiceDefToHookData(svc)
	markers := scanRPCHookMarkers(serviceProtoSourcePath(projectDir, svc.ProtoFile))
	applyMutationMarkers(&data, markers.Mutation)
	if err := codegen.ApplyQueryScopes(&data, svc, markers.QueryScope); err != nil {
		return data, fmt.Errorf("%s: %w", svc.ProtoFile, err)
	}
	return data, nil
}

// applyMutationMarkers rewrites hook classification for RPCs the proto marked
// with `// forge:mutation`: each query flips to a mutation hook (useMutation +
// entity-scoped invalidateQueries) instead of a useQuery. The
// HasQueries/HasMutations flags — which gate the template's imports — are
// recomputed from the post-override method set, so a service left with no
// queries stops importing useQuery/requestKey (and vice versa) and the file
// still type-checks with no unused imports.
func applyMutationMarkers(data *codegen.FrontendHookTemplateData, marked map[string]bool) {
	if len(marked) == 0 {
		return
	}
	changed := false
	for i := range data.Methods {
		if marked[data.Methods[i].Name] && data.Methods[i].IsQuery {
			data.Methods[i].IsQuery = false
			// A mutation is never paged: useInfiniteQuery would replay it.
			data.Methods[i].Paged = false
			changed = true
		}
	}
	if !changed {
		return
	}
	data.HasQueries = false
	data.HasMutations = false
	data.HasPaged = false
	for _, m := range data.Methods {
		if m.IsQuery {
			data.HasQueries = true
		} else {
			data.HasMutations = true
		}
		if m.Paged {
			data.HasPaged = true
		}
	}
}

// generateFrontendHooks generates TypeScript React Query hook files for
// every Connect-driven service.
//
// Two output modes, picked by cfg.IsFrontendWorkspacesEnabled():
//
//   - workspaces=false (default): one file per service per frontend, at
//     frontends/<name>/src/hooks/<svc>-hooks.ts. Each file imports
//     connectClient from @/lib/connect and proto types from @/gen.
//     Snapshot-stable with projects scaffolded before the flag landed.
//
//   - workspaces=true: one file per service at packages/hooks/src/
//     generated/<svc>-hooks.ts (shared across all frontends). Each file
//     imports connectClient from ../transport and proto types from the
//     project's @<scope>/api workspace. The per-frontend hooks dir is
//     not touched in this mode — frontends consume the workspace
//     package instead.
func generateFrontendHooks(cfg *config.ProjectConfig, services []codegen.ServiceDef, projectDir string, cs *checksums.FileChecksums) error {
	if len(services) == 0 {
		return nil
	}

	tmplContent, err := templates.FrontendTemplates().Get("hooks.ts.tmpl")
	if err != nil {
		return fmt.Errorf("read hooks template: %w", err)
	}

	tmpl, err := template.New("hooks.ts.tmpl").Funcs(templates.FuncMap()).Parse(string(tmplContent))
	if err != nil {
		return fmt.Errorf("parse hooks template: %w", err)
	}

	if cfg.IsFrontendWorkspacesEnabled() {
		return generateFrontendHooksWorkspace(cfg, services, projectDir, tmpl, cs)
	}

	for _, fe := range cfg.Frontends {
		if !strings.EqualFold(fe.Type, "nextjs") && !strings.EqualFold(fe.Type, "react-native") && !strings.EqualFold(fe.Type, "vite-spa") {
			continue
		}

		feDir, ok := fe.Dir(projectDir)
		if !ok {
			// No directory in this repository — a cross-repo
			// source pin, or a path outside the project root.
			continue
		}

		hooksDir := filepath.Join(projectDir, feDir, "src", "hooks")
		if err := os.MkdirAll(hooksDir, 0o755); err != nil {
			return fmt.Errorf("create hooks directory: %w", err)
		}

		var hookFiles []hookFileEntry
		reportUnknownHookAllowlist(fe, services)

		for _, svc := range services {
			// The frontend's package.json `forge.hooks` allowlist: a
			// frontend gets hooks only for the services it calls. A
			// browser app has no use for its daemon's internal services,
			// and every hook module it does not import is still compiled,
			// tested and kept current.
			if !fe.WantsHooksFor(svc.Name, qualifiedServiceName(svc)) {
				continue
			}
			// The proto comment markers: `// forge:mutation` flips a read to
			// a mutation hook, `// forge:query-scope` keys a custom read
			// under an entity.
			data, err := serviceHookData(svc, projectDir)
			if err != nil {
				return err
			}
			if len(data.Methods) == 0 {
				continue
			}

			var buf bytes.Buffer
			if err := tmpl.Execute(&buf, data); err != nil {
				return fmt.Errorf("render hooks for %s: %w", svc.Name, err)
			}

			fileName := naming.ServiceHookFile(svc.Name)
			hookFiles = append(hookFiles, hookFileEntry{
				fileName: fileName,
				// nsAlias is the namespace we re-export the file as when a
				// collision forces namespacing. Derived from the service
				// name so it stays stable and readable: UserService ->
				// userService.
				nsAlias: codegen.ToCamelCaseFromPascalExport(svc.Name),
				symbols: hookFileExportedSymbols(data),
			})

			// Through the certification chokepoint, not a bare
			// os.WriteFile: the banner already claims "forge-owned,
			// regenerated every run" (Tier-1), and only a
			// checksums.WriteGeneratedFile write embeds the forge:hash
			// marker that claim depends on. An uncertified Tier-1 file is
			// invisible to the marker-driven stale-artifact sweep
			// (cleanupStaleArtifacts) — exactly how a renamed service's
			// old hook file survived a rename on disk with nothing to
			// delete it. See reportOrphanedHookFiles below for the
			// legacy files this fix cannot retroactively certify.
			outRel := filepath.Join(feDir, "src", "hooks", fileName)
			reportHookKindFlips(projectDir, outRel, data)
			if _, err := checksums.WriteGeneratedFile(projectDir, outRel, buf.Bytes(), cs, true); err != nil {
				return fmt.Errorf("write hooks file %s: %w", outRel, err)
			}
			// Retire the pre-`_gen` copy. Two live hook modules would both
			// be re-exported by the barrel below, so every hook symbol
			// would be bound twice and the barrel would not compile.
			codegen.RetireRenamedGenerated(projectDir,
				filepath.Join(feDir, "src", "hooks", naming.ServiceHookFileLegacy(svc.Name)), cs)

			// Emit a live happy-path test next to the generated hooks
			// file. React Native uses a different rendering target (no
			// DOM) and the test-utils.tsx helper doesn't apply there, so
			// skip for mobile frontends. Scaffold-once: written only when
			// the user has no test of their own, so regen never
			// overwrites their work.
			if isWebFrontendType(fe.Type) {
				if err := writeHookStarterTest(hooksDir, fileName, svc, data); err != nil {
					return fmt.Errorf("write hook starter test for %s: %w", svc.Name, err)
				}
			}
		}

		// Generate barrel index.ts
		if len(hookFiles) > 0 {
			if err := writeHooksBarrel(projectDir, filepath.Join(feDir, "src", "hooks", "index.ts"), hookFiles, cs); err != nil {
				return fmt.Errorf("write hooks index: %w", err)
			}
		}

		fmt.Printf("  ✅ Generated %d hook file(s) for frontend %s\n", len(hookFiles), fe.Name)

		reportOrphanedHookFiles(hooksDir, hookFiles)
	}

	return nil
}

// qualifiedServiceName is svc's fully-qualified proto name
// ("reliant.v1.ChatService"), or its bare name when it has no package.
func qualifiedServiceName(svc codegen.ServiceDef) string {
	if svc.Package == "" {
		return svc.Name
	}
	return svc.Package + "." + svc.Name
}

// reportUnknownHookAllowlist warns about `forge.hooks` entries that name no
// service, the same way an unmatched `routes` slug is reported: a typo would
// otherwise yield a frontend silently missing the hooks it asked for.
func reportUnknownHookAllowlist(fe config.FrontendConfig, services []codegen.ServiceDef) {
	if len(fe.Hooks) == 0 {
		return
	}
	known := make(map[string]bool, 2*len(services))
	for _, svc := range services {
		known[svc.Name] = true
		known[qualifiedServiceName(svc)] = true
	}
	if unknown := fe.UnknownHooks(known); len(unknown) > 0 {
		names := make([]string, 0, len(services))
		for _, svc := range services {
			names = append(names, svc.Name)
		}
		sort.Strings(names)
		fmt.Printf("  ⚠️  frontend %s: package.json forge.hooks %v name no service (known: %v)\n", fe.Name, unknown, names)
	}
}

// reportOrphanedHookFiles warns about *-hooks.ts files sitting in hooksDir
// that this run did NOT (re)write and that carry no forge:hash marker —
// left behind by a forge build old enough to predate routing hook writes
// through the certification chokepoint (WriteGeneratedFile).
//
// It is report-only, never destructive, and deliberately narrower than
// the marker-driven stale-artifact sweep (cleanupStaleArtifacts): a
// hook file THIS run writes now carries a forge:hash marker, so any
// future rename/removal is that sweep's job, automatically, with no
// code here. The gap this function closes is the one the sweep cannot
// close on its own — files written by a forge predating this fix, which
// carry the Tier-1 "forge-owned, regenerated every run" banner but no
// marker to certify it. Un-marked means forge cannot prove authorship,
// so deleting one would be a guess; naming it, with the reason and the
// remedy, is the guess-free half forge can own.
func reportOrphanedHookFiles(hooksDir string, live []hookFileEntry) {
	liveNames := make(map[string]bool, len(live))
	for _, f := range live {
		liveNames[f.fileName] = true
	}

	entries, err := os.ReadDir(hooksDir)
	if err != nil {
		return
	}

	var orphans []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, "-hooks.ts") || liveNames[name] {
			continue
		}
		content, rerr := os.ReadFile(filepath.Join(hooksDir, name))
		if rerr != nil {
			continue
		}
		// A marker-bearing file is the marker sweep's territory
		// (cleanupStaleArtifacts), whether pristine or hand-edited —
		// reporting it here too would just be a second, redundant
		// notice for the same file.
		if _, found := checksums.ExtractMarker(content); found {
			continue
		}
		orphans = append(orphans, name)
	}
	if len(orphans) == 0 {
		return
	}
	sort.Strings(orphans)

	fmt.Fprintf(os.Stderr, "\n⚠️  %d stale hook file(s) in %s carry the \"forge-owned, regenerated every run\" banner "+
		"but predate forge's hash-marker certification, so forge cannot prove it still owns the bytes and won't "+
		"delete them automatically. forge no longer generates these (their service was renamed or removed) — "+
		"delete them by hand:\n", len(orphans), hooksDir)
	for _, name := range orphans {
		fmt.Fprintf(os.Stderr, "   - %s\n", filepath.Join(hooksDir, name))
	}
}

// hookFileEntry describes one generated hook file for index.ts generation.
type hookFileEntry struct {
	fileName string   // "user-service-hooks.ts"
	nsAlias  string   // "userService" — used when collisions force namespace re-exports
	symbols  []string // exported identifiers: ["useGetUser", "useCreateUser", ...]
}

// hookFileExportedSymbols returns the names of identifiers a rendered
// hooks.ts.tmpl file exposes (one `useXxx` per unary RPC, plus
// `useXxxInfinite` per paged read). Keeping this in
// sync with the template is intentional: the template is the only thing
// that decides what gets exported, so we read its output shape here
// rather than re-parsing the rendered .ts.
func hookFileExportedSymbols(data codegen.FrontendHookTemplateData) []string {
	out := make([]string, 0, len(data.Methods))
	for _, m := range data.Methods {
		out = append(out, "use"+m.Name)
		if m.Paged {
			out = append(out, "use"+m.Name+"Infinite")
		}
	}
	return out
}

// generateFrontendHooksWorkspace emits the workspace-mode hooks: one
// file per service at packages/hooks/src/generated/<svc>-hooks.ts,
// plus a barrel index.ts. Shared by every frontend in the project.
func generateFrontendHooksWorkspace(cfg *config.ProjectConfig, services []codegen.ServiceDef, projectDir string, tmpl *template.Template, cs *checksums.FileChecksums) error {
	layout := generator.NewFrontendWorkspaceLayout(cfg.Name)
	generatedDir := filepath.Join(projectDir, "packages", "hooks", "src", "generated")
	if err := os.MkdirAll(generatedDir, 0o755); err != nil {
		return fmt.Errorf("create packages/hooks/src/generated: %w", err)
	}

	var hookFiles []hookFileEntry
	for _, svc := range services {
		data, err := serviceHookData(svc, projectDir)
		if err != nil {
			return err
		}
		if len(data.Methods) == 0 {
			continue
		}
		data.Workspaces = true
		data.APIPackage = layout.APIPackage

		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, data); err != nil {
			return fmt.Errorf("render workspace hooks for %s: %w", svc.Name, err)
		}

		fileName := naming.ServiceHookFile(svc.Name)
		hookFiles = append(hookFiles, hookFileEntry{
			fileName: fileName,
			nsAlias:  codegen.ToCamelCaseFromPascalExport(svc.Name),
			symbols:  hookFileExportedSymbols(data),
		})

		// See the per-frontend loop's write for why this goes through
		// the certification chokepoint rather than os.WriteFile.
		outRel := filepath.Join("packages", "hooks", "src", "generated", fileName)
		reportHookKindFlips(projectDir, outRel, data)
		if _, err := checksums.WriteGeneratedFile(projectDir, outRel, buf.Bytes(), cs, true); err != nil {
			return fmt.Errorf("write hooks file %s: %w", outRel, err)
		}
		codegen.RetireRenamedGenerated(projectDir,
			filepath.Join("packages", "hooks", "src", "generated", naming.ServiceHookFileLegacy(svc.Name)), cs)
	}

	if len(hookFiles) > 0 {
		if err := writeHooksBarrel(projectDir, filepath.Join("packages", "hooks", "src", "generated", "index.ts"), hookFiles, cs); err != nil {
			return fmt.Errorf("write hooks index: %w", err)
		}
	}

	fmt.Printf("  ✅ Generated %d hook file(s) at packages/hooks/src/generated\n", len(hookFiles))

	reportOrphanedHookFiles(generatedDir, hookFiles)
	return nil
}

// writeHooksIndex writes a barrel index.ts that re-exports all hook files.
//
// Two emission modes:
//
//   - Flat wildcard re-exports (`export * from "./users-hooks"`) when no
//     two hook files would re-export the same identifier. This is the
//     historic shape and keeps `import { useGetUser } from "@/hooks"`
//     working.
//   - Namespaced re-exports (`export * as userService from "./users-hooks"`)
//     when at least one identifier (e.g. a generic `useList` on two
//     services) is exported by multiple files. In flat mode TypeScript
//     rejects the duplicate `export *`; switching the ENTIRE barrel to
//     namespaces is simpler — and more predictable for consumers — than
//     mixing flat + namespace per file. Callers update to
//     `import { userService } from "@/hooks"; userService.useList(...)`.
//
// The chosen mode is recorded in a comment at the top of index.ts so a
// reader of generated output understands why their imports changed.
func writeHooksIndex(path string, hookFiles []hookFileEntry) error {
	collisions := detectIndexCollisions(hookFiles)

	var buf bytes.Buffer
	buf.WriteString("// Code generated by forge. DO NOT EDIT.\n")
	buf.WriteString("// forge-owned: regenerated every run — do not edit (forge project disown to take ownership)\n")
	if len(collisions) > 0 {
		buf.WriteString("//\n")
		buf.WriteString("// Mode: namespaced re-exports.\n")
		buf.WriteString("// Reason: two or more hook files export the same identifier(s):\n")
		for _, c := range collisions {
			buf.WriteString(fmt.Sprintf("//   - %s (from %s)\n", c.symbol, strings.Join(c.files, ", ")))
		}
		buf.WriteString("// To call a hook, namespace it: `import { <alias> } from \"@/hooks\"; <alias>.useFoo(...)`.\n\n")
		for _, f := range hookFiles {
			module := strings.TrimSuffix(f.fileName, ".ts")
			buf.WriteString(fmt.Sprintf("export * as %s from \"./%s\";\n", f.nsAlias, module))
		}
	} else {
		buf.WriteString("// Mode: flat wildcard re-exports (no symbol collisions).\n\n")
		for _, f := range hookFiles {
			module := strings.TrimSuffix(f.fileName, ".ts")
			buf.WriteString(fmt.Sprintf("export * from \"./%s\";\n", module))
		}
	}

	if existing, err := os.ReadFile(path); err == nil && isHandWrittenBarrel(existing) {
		return errHandWrittenBarrel
	}
	checksums.RecordPreWriteAbs(path) // journaled: a failed run restores it
	return atomicfile.WriteFile(path, buf.Bytes(), 0o644)
}

// errHandWrittenBarrel is writeHooksIndex declining a path someone else's
// barrel already holds.
var errHandWrittenBarrel = errors.New("hand-written hooks barrel")

// isHandWrittenBarrel reports whether an existing src/hooks/index.ts is not
// forge's: it carries neither the forge:hash marker nor forge's generated
// banner, which every barrel forge has ever written opens with.
//
// src/hooks/index.ts is the one path forge's hook generator writes that an
// app is likely to have written itself — it is the conventional barrel
// name, not a `_gen` file. reliant's web app had a 17-line one re-exporting
// its own hooks (useCodexOAuth, useFullscreen, …); generate replaced it with
// forge's re-exports, and seven importers lost 26 exports (TS2305), with no
// drift refusal and nothing in the run's summary naming the file.
func isHandWrittenBarrel(content []byte) bool {
	if _, marked := checksums.ExtractMarker(content); marked {
		return false
	}
	head := content
	if len(head) > 1024 {
		head = head[:1024]
	}
	return !bytes.Contains(head, []byte("Code generated by forge"))
}

// isHooksBarrelRelPath reports whether a project-relative path is one
// writeHooksBarrel targets: a frontend's src/hooks/index.ts, or the shared
// hooks package's src/generated/index.ts.
//
// `forge project disown` admits these without a forge:hash marker. The
// barrel a hand-written file holds is exactly the unmarked case — the
// left-alone notice below tells the user to disown it, and a disowned barrel
// is what silences that notice — so the marker rule would refuse the very
// advice this generator prints (the vendored-proto exception, again).
func isHooksBarrelRelPath(p string) bool {
	p = filepath.ToSlash(p)
	return p == "src/hooks/index.ts" || strings.HasSuffix(p, "/src/hooks/index.ts") ||
		p == "packages/hooks/src/generated/index.ts"
}

// writeHooksBarrel writes the hooks barrel at relPath (project-relative),
// or — when a hand-written file already holds the path — leaves it exactly
// as it is and names it in the run's closing summary. A barrel the project
// disowned is the user's by declaration and is skipped silently.
//
// The barrel is regenerated every run, but writeHooksIndex writes it directly
// rather than through a Tier-1 writer, so it is recorded as a Tier-1 target
// here: when forge writes it, and when a disown is all that stops forge from
// writing it. Without that, obsolete-disown retirement saw a disowned barrel
// as a path forge no longer emits and dropped the disown, and the next run
// printed the left-alone notice that told the user to disown it again.
func writeHooksBarrel(projectDir, relPath string, hookFiles []hookFileEntry, cs *checksums.FileChecksums) error {
	if cs != nil && cs.IsDisowned(filepath.ToSlash(relPath)) {
		checksums.MarkTier1Target(relPath)
		return nil
	}
	err := writeHooksIndex(filepath.Join(projectDir, relPath), hookFiles)
	if err == nil {
		checksums.MarkTier1Target(relPath)
	}
	if !errors.Is(err, errHandWrittenBarrel) {
		return err
	}
	why := "a hand-written file (no forge marker or banner) holds the path of forge's hooks barrel, so forge left it " +
		"and wrote no barrel. Import generated hooks from their own modules (./<svc>-service-hooks_gen), or move " +
		"your file and re-run to let forge own the barrel; `" + Name() + " project disown " + filepath.ToSlash(relPath) +
		" --reason \"<why>\"` keeps yours and silences this"
	checksums.NoteLeftAlone(relPath, why)
	fmt.Printf("  ⚠️  %s is hand-written — forge did not overwrite it (see the summary at the end)\n", filepath.ToSlash(relPath))
	return nil
}

// indexCollision records one symbol that is exported by 2+ hook files.
type indexCollision struct {
	symbol string
	files  []string // sorted, deterministic
}

// detectIndexCollisions returns every exported identifier that appears in
// more than one hook file. An empty result means the flat wildcard barrel
// will compile. We surface ALL of them — not just the first — so a single
// regen tells the user about every conflict instead of leaving them to
// fix collisions one-by-one across cycles.
func detectIndexCollisions(hookFiles []hookFileEntry) []indexCollision {
	bySymbol := map[string]map[string]struct{}{}
	for _, f := range hookFiles {
		for _, s := range f.symbols {
			set, ok := bySymbol[s]
			if !ok {
				set = map[string]struct{}{}
				bySymbol[s] = set
			}
			set[f.fileName] = struct{}{}
		}
	}

	var out []indexCollision
	for sym, files := range bySymbol {
		if len(files) < 2 {
			continue
		}
		names := make([]string, 0, len(files))
		for n := range files {
			names = append(names, n)
		}
		sort.Strings(names)
		out = append(out, indexCollision{symbol: sym, files: names})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].symbol < out[j].symbol })
	return out
}

// isWebFrontendType returns true for frontends that target the browser
// DOM and therefore can use the Vitest + Testing Library starter test
// emitted by writeHookStarterTest. React Native uses a different runner
// (no jsdom) and ignores the starter.
func isWebFrontendType(t string) bool {
	return strings.EqualFold(t, "nextjs") || strings.EqualFold(t, "vite-spa")
}

// hookStarterImport is one `import { use<Method> } from "./<file>";`
// statement the starter template renders.
type hookStarterImport struct {
	Symbol string // "useGetUser"
	Module string // "user-service-hooks" (no .ts suffix)
}

// hookStarterMethod is one RPC's row in the starter test template.
type hookStarterMethod struct {
	Name     string // "GetUser"
	HookName string // "useGetUser"
	IsQuery  bool
	// Infinite marks the row for a paged read's useXxxInfinite hook, whose
	// data is { pages, pageParams } rather than one response.
	Infinite bool
}

// hookStarterData is the template data for hooks.test.tsx.tmpl.
type hookStarterData struct {
	ServiceName string
	HookImports []hookStarterImport
	Methods     []hookStarterMethod
}

// HasMutation gates the template's waitForSettled helper, which only the
// mutation rows call. Emitting it into a query-only service would hand that
// file an unused declaration for no-unused-vars to flag.
func (d hookStarterData) HasMutation() bool {
	for _, m := range d.Methods {
		if !m.IsQuery {
			return true
		}
	}
	return false
}

// hookKinds maps every hook data exports to its kind: true for a useQuery
// (an infinite hook included), false for a useMutation.
func hookKinds(data codegen.FrontendHookTemplateData) map[string]bool {
	out := make(map[string]bool, len(data.Methods))
	for _, m := range data.Methods {
		out["use"+m.Name] = m.IsQuery
		if m.Paged {
			out["use"+m.Name+"Infinite"] = true
		}
	}
	return out
}

// reportHookKindFlips prints one line per hook whose kind this run changes,
// read off the hooks file forge is about to overwrite: a call site written
// for the old kind (`useX()` + `mutate`, or `useX(req)` + `data`) must
// change with it. tsc catches most of them; a test that casts `as never`
// does not. The rows of the starter test this flips are reported, with
// their fixes, by reportStaleHookTestRows in the closing summary.
func reportHookKindFlips(projectDir, outRel string, data codegen.FrontendHookTemplateData) {
	prev, err := os.ReadFile(filepath.Join(projectDir, outRel))
	if err != nil {
		return
	}
	was := forgeconv.ParseHookKinds(string(prev))
	now := hookKinds(data)
	names := make([]string, 0, len(now))
	for name := range now {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if old, ok := was[name]; ok && old != now[name] {
			fmt.Printf("  ↻ %s is now a %s (was a %s) — update every call site written for the old kind\n",
				name, forgeconv.HookKind(now[name]), forgeconv.HookKind(old))
		}
	}
}

// reportStaleHookTestRows names, in the closing summary, every row of a
// service's scaffold-once starter test that asserts the opposite kind from
// the hook this run generated — with its line and the body that replaces
// it. forge does not rewrite the file (it is the user's), so the report is
// how a hook-kind flip (a NO_SIDE_EFFECTS read that was a mutation, a
// `// forge:mutation` write that was a query) reaches its author before
// vitest does. The comparison is forgeconv.StaleHookTestRows, the one
// `forge lint --conventions` gates on, so the two cannot disagree.
func reportStaleHookTestRows(testPath, displayPath string, data codegen.FrontendHookTemplateData) {
	src, err := os.ReadFile(testPath)
	if err != nil {
		return
	}
	kinds := hookKinds(data)
	stale := forgeconv.StaleHookTestRows(string(src), kinds)
	if len(stale) == 0 {
		return
	}
	var detail []string
	for _, row := range stale {
		detail = append(detail, fmt.Sprintf("line %d: %s is now a %s — replace the row's body with:", row.Line, row.Hook, forgeconv.HookKind(kinds[row.Hook])))
		for _, l := range strings.Split(forgeconv.HookTestRowFix(row.Hook, kinds[row.Hook]), "\n") {
			detail = append(detail, "    "+l)
		}
	}
	if forgeconv.StaleRowsOrphanSettleHelper(string(src), kinds) {
		detail = append(detail, "then delete the waitForSettled helper: no mutation row is left to call it")
	}
	why := fmt.Sprintf("a scaffold-once starter test, so forge did not rewrite it — but %d row(s) assert a hook kind "+
		"this run no longer generates and can never pass (or delete it and run `%s project rescaffold %s`)",
		len(stale), Name(), filepath.ToSlash(displayPath))
	checksums.NoteLeftAloneDetail(displayPath, why, detail)
}

// writeHookStarterTest emits `<file>.test.tsx` next to the generated hooks
// file IF the user does not already have one. Scaffold-once: written when
// absent, never overwritten, so hand-edits and hand-written replacements
// survive every `forge generate`.
//
// It USED to be written as `<file>.test.tsx.starter`, inert until someone
// renamed it. Nobody ever did. The dogfood run shipped a 17 KB generated
// suite per service that had never executed once, so the layer all sixteen
// pages sat on had zero active tests while `forge test` reported green. A
// test disabled by its file extension is decoration; forge's other
// scaffolded tests (page.test.tsx, status-badge.test.tsx) ship live, and
// this one now does too. An unrenamed starter left by an older forge is
// renamed into place rather than left beside the new file.
//
// `fileName` is the hooks filename (e.g. "user-service-hooks_gen.ts"); the
// test goes next to it as "user-service-hooks.test.tsx".
//
// The test deliberately does NOT carry the `_gen` suffix its subject does.
// `_gen` means "forge owns this and rewrites it every run", and this file is
// the opposite: scaffold-once, yours the moment it lands. Naming it
// `*_gen.test.tsx` would advertise a guarantee forge does not make about it —
// and would make the drift lint's mechanical filename rule claim a file it
// must never stomp.
func writeHookStarterTest(hooksDir, fileName string, svc codegen.ServiceDef, data codegen.FrontendHookTemplateData) error {
	base := strings.TrimSuffix(fileName, ".ts")
	// The module the test IMPORTS is the generated one (with `_gen`); the
	// test's own name drops it.
	testBase := strings.TrimSuffix(base, "_gen")
	testPath := filepath.Join(hooksDir, testBase+".test.tsx")
	legacyStarterPath := filepath.Join(hooksDir, testBase+".test.tsx.starter")

	scaffoldRoot, scaffoldRel, haveLedger := checksums.SplitScaffoldPath(testPath)

	// Scaffold-once, decided by the ledger rather than by presence: the
	// user's test — hand-written, a scaffold they have since edited, or one
	// they DELETED because they did not want forge's starter — is theirs
	// either way. A presence check would read the deletion as "absent, so
	// write it" and hand the file back on every generate, against the
	// banner the template carries.
	// Outside a project root there is no ledger to key, so the decision
	// degrades to the historical presence check — which still protects an
	// EXISTING file. It cannot honour a deletion, but a path with no
	// project around it has no committed state to have recorded one.
	skip := false
	if haveLedger {
		skip = !checksums.ScaffoldOnceDecision(scaffoldRoot, scaffoldRel)
	} else if _, err := os.Stat(testPath); err == nil {
		skip = true
	}
	if skip {
		// A leftover .starter beside a live test is dead weight and goes.
		if _, err := os.Stat(testPath); err == nil {
			if _, err := os.Stat(legacyStarterPath); err == nil {
				if err := checksums.RemoveJournaled(legacyStarterPath); err != nil {
					return fmt.Errorf("remove superseded starter test %s: %w", legacyStarterPath, err)
				}
			}
		}
		displayPath := testPath
		if haveLedger {
			displayPath = scaffoldRel
		}
		reportStaleHookTestRows(testPath, displayPath, data)
		return nil
	}

	// An older forge left an inert starter and no active test: activate it
	// in place instead of writing a second copy beside it. The user may
	// have edited it while it sat there.
	if _, err := os.Stat(legacyStarterPath); err == nil {
		checksums.RecordPreWriteAbs(legacyStarterPath)
		checksums.RecordPreWriteAbs(testPath)
		if err := os.Rename(legacyStarterPath, testPath); err != nil {
			return fmt.Errorf("activate starter test %s: %w", legacyStarterPath, err)
		}
		fmt.Printf("  ✅ Activated %s (it was an inert .starter that never ran)\n", testPath)
		return nil
	}

	tmplBytes, err := templates.FrontendTemplates().Get("hooks.test.tsx.tmpl")
	if err != nil {
		return fmt.Errorf("read starter test template: %w", err)
	}
	tmpl, err := template.New("hooks.test.tsx.tmpl").Funcs(templates.FuncMap()).Parse(string(tmplBytes))
	if err != nil {
		return fmt.Errorf("parse starter test template: %w", err)
	}

	starterData := hookStarterData{
		ServiceName: svc.Name,
		HookImports: []hookStarterImport{{
			Symbol: "/* one of */ " + strings.Join(hookFileExportedSymbols(data), ", "),
			Module: base,
		}},
		Methods: make([]hookStarterMethod, 0, len(data.Methods)),
	}
	// Replace the placeholder one-import-row with a real per-symbol list:
	// each generated hook gets its own import line so renaming a single
	// hook later doesn't leave the test importing a wildcard.
	starterData.HookImports = starterData.HookImports[:0]
	for _, m := range data.Methods {
		starterData.HookImports = append(starterData.HookImports, hookStarterImport{
			Symbol: "use" + m.Name,
			Module: base,
		})
		starterData.Methods = append(starterData.Methods, hookStarterMethod{
			Name:     m.Name,
			HookName: "use" + m.Name,
			IsQuery:  m.IsQuery,
		})
		if m.Paged {
			starterData.HookImports = append(starterData.HookImports, hookStarterImport{
				Symbol: "use" + m.Name + "Infinite",
				Module: base,
			})
			starterData.Methods = append(starterData.Methods, hookStarterMethod{
				Name:     m.Name,
				HookName: "use" + m.Name + "Infinite",
				IsQuery:  true,
				Infinite: true,
			})
		}
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, starterData); err != nil {
		return fmt.Errorf("render hook test for %s: %w", svc.Name, err)
	}
	checksums.RecordPreWriteAbs(testPath) // journaled: a failed run removes it
	if err := atomicfile.WriteFile(testPath, templates.CanonicalTSImportOrder(buf.Bytes()), 0o644); err != nil {
		return fmt.Errorf("write hook test %s: %w", testPath, err)
	}
	// The template is prettier-clean at the scaffold's own printWidth for
	// ordinary names, but no fixed text can be: where prettier wraps
	// `renderHook(() => useX(...), { wrapper })` depends on the hook name's
	// length and on the project's prettier config, both of which forge only
	// learns here. So the file forge just created is handed to the project's
	// own formatter, which is the one its CI checks against.
	formatScaffoldedFrontendFile(testPath)
	if haveLedger {
		checksums.RecordScaffold(scaffoldRoot, scaffoldRel)
	}
	return nil
}
