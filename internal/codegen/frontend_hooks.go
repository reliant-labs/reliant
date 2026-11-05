package codegen

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/reliant-labs/forge/internal/naming"
)

// FrontendHookTemplateData holds data for rendering a single service's
// TypeScript React Query hooks file.
type FrontendHookTemplateData struct {
	ServiceName      string // e.g., "UserService"
	ServiceNameCamel string // e.g., "userService"
	ImportPath       string // e.g., "services/users/v1/users_pb" — the service's own proto file
	Methods          []FrontendHookMethod
	// HasQueries / HasMutations let the template conditionally import
	// only the hooks it actually uses. Without these flags the emitted
	// file pulls in useMutation/useQueryClient/UseMutationOptions for
	// query-only services, tripping no-unused-vars in eslint configs.
	HasQueries   bool
	HasMutations bool
	// HasPaged gates the createInfiniteQueryHook import: true when any
	// method is Paged.
	HasPaged bool
	// SchemaImports groups input-message `Schema` value imports by their
	// declaring proto file's TS import path. The template emits one
	// `import { ...Schema }` statement per entry. Same-file schemas land
	// under the service's ImportPath; cross-file schemas land under their
	// own proto file's derived path. Sorted for deterministic output.
	SchemaImports []HookImportGroup
	// TypeImports groups output-message `type` imports the same way.
	// Kept separate from SchemaImports because the template emits these
	// as `import type { ... }` — value vs type-only is required so a
	// `--isolatedModules` build still tree-shakes the type-only side.
	TypeImports []HookImportGroup
	// Workspaces is true when the project opted into the pnpm-workspace
	// layout (frontend.workspaces: true). When true, the rendered hook
	// file lives under packages/hooks/src/generated/ and imports
	// connectClient from "../transport" + proto types from the
	// project's @<scope>/api workspace. When false (the default), the
	// file lives under frontends/<name>/src/hooks/ and imports from the
	// frontend-local @/lib/connect + @/gen paths — byte-identical to
	// projects that predate the workspaces flag.
	Workspaces bool
	// APIPackage is the workspace package name for the shared API
	// (e.g. "@myapp/api"). Empty when Workspaces is false.
	APIPackage string
	// EntityScopes is the sorted, deduplicated set of camelCase CRUD
	// entity names ("task", "user") derived from the service's RPC
	// names. The template emits one entity-scope key per entry in the
	// generated query-key factory so mutations can invalidate ONLY the
	// queries for the entity they touched (entity-scoped invalidation)
	// instead of nuking every query on the service.
	EntityScopes []string
	// ServiceRef is the name the file refers to the service descriptor by
	// (tsImportPlan): ServiceName unless another module the file imports
	// declares the same name.
	ServiceRef string
}

// HookImportGroup is one TS import statement: a list of symbols (sorted,
// deduplicated) drawn from a single source proto file. The template emits
// one statement per group so cross-proto-file refs resolve to the
// declaring _pb.ts file.
type HookImportGroup struct {
	ImportPath string   // e.g., "services/users/v1/users_pb" or "shared/v1/types_pb"
	Symbols    []string // sorted, deduplicated identifiers
}

// FrontendHookMethod represents a single unary RPC method for hook generation.
type FrontendHookMethod struct {
	Name      string // PascalCase: "GetUser"
	NameCamel string // camelCase: "getUser" — forge's own query-key / hook spelling
	// ClientMethod is the method the connect-es client has for this RPC —
	// protobuf-es's localName (naming.EsMethodName), which lowers the
	// FIRST CHARACTER ONLY: "LLMChat" → client.lLMChat. NameCamel lowers
	// the whole leading acronym, so it names a method the client lacks.
	ClientMethod string
	InputType    string // "GetUserRequest"
	OutputType   string // "GetUserResponse"
	IsQuery      bool   // true for NO_SIDE_EFFECTS rpcs and Get/List/Search…, false for mutations (isQueryRPC)
	// Paged reports a query that follows forge's list-pagination shape
	// (isPagedRPC): it also gets `use<Name>Infinite`, a useInfiniteQuery
	// hook that owns the page token, keyed under the same scopes.
	Paged bool
	// EntityScope is the camelCase singular CRUD entity this method
	// operates on ("task" for ListTasks/GetTask/CreateTask), derived
	// from the RPC-name CRUD pattern, or declared on a custom read with
	// `// forge:query-scope <Entity>` (ApplyQueryScopes). Empty otherwise.
	// Queries embed it in their query key ([service, entity, method,
	// req]); mutations invalidate the [service, entity] scope when set,
	// falling back to the whole-service scope when empty (a bespoke
	// mutation may touch anything, so over-invalidating is the safe
	// default there).
	EntityScope string
	// InputSchemaRef / OutputTypeRef are the names the hooks file refers to
	// the request schema and response type by: their own, or
	// `<module>_<Name>` when the file imports that name from two modules
	// (tsImportPlan).
	InputSchemaRef string
	OutputTypeRef  string
	// inputPath / outputPath are the TS modules declaring them.
	inputPath, outputPath string
}

// queryPrefixes are the read-only verbs an RPC name may start with.
//
// This is deliberately a QUERY list, not a mutation list: anything that
// does not start with a read verb is a mutation. That default is the safe
// one and is why the list can never miss a domain write verb — Dispense,
// Reconcile, Escalate are all mutations without anyone adding them. A
// missed read costs a cache entry; a missed WRITE would be replayed by
// React Query on every component remount.
var queryPrefixes = []string{
	"Get", "List", "Search", "Find",
	"Check", "Has", "Is", "Count", "Exists",
}

// isQueryMethod reports whether an RPC name begins with a read-only verb.
//
// The match is on whole WORDS, not characters. The prefix must be followed
// by the start of another word — an uppercase letter or a digit — or by
// nothing at all. Character-prefix matching read "Is" out of
// IssuePrescription and generated useQuery for a clinical write, which
// React Query then replayed on every remount. The same collision hides in
// Checkout ("Check"), Countersign ("Count"), Listen ("List") and Hash
// ("Has"): a longer verb list would not have caught any of them, because
// the defect is in how the prefix is compared, not in which verbs are on
// the list.
//
// An imperative RPC that genuinely opens with a read word — or a bespoke
// verb this cannot know about — carries `// forge:mutation` in the proto;
// see internal/cli/generate_frontend_hooks.go. A read whose name is no verb
// at all (Health, Version) declares NO_SIDE_EFFECTS; see isQueryRPC.
func isQueryMethod(name string) bool {
	for _, prefix := range queryPrefixes {
		rest, ok := strings.CutPrefix(name, prefix)
		if !ok {
			continue
		}
		if rest == "" {
			return true
		}
		r, _ := utf8.DecodeRuneInString(rest)
		if unicode.IsUpper(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

// isQueryRPC decides whether m's generated hook is a useQuery.
//
// The proto's own declaration comes first: `option idempotency_level =
// NO_SIDE_EFFECTS;` is protobuf's standard statement that an rpc only reads,
// the one connect-es already acts on, and the one forge/pkg/activity reads
// to exempt a read from declaring an activity event. Without it a read named
// as a noun — Health, Ready, Info, Version — fell through the verb list and
// came out a useMutation, so a caller had to hand-build its query key.
//
// Only NO_SIDE_EFFECTS reads as a read. IDEMPOTENT is a write that is safe
// to repeat (SetFlag), and the unset default promises nothing, so both leave
// the decision to the name, exactly as before the option was consulted.
// `// forge:mutation` is applied after this and still wins.
func isQueryRPC(m Method) bool {
	return m.NoSideEffects || isQueryMethod(m.Name)
}

// pageTokenField / nextPageTokenField are forge's list-pagination
// convention (AIP-158; the proto skill's "List Request Conventions"), the
// shape every scaffolded List rpc is born with.
const (
	pageTokenField     = "page_token"
	nextPageTokenField = "next_page_token"
)

// isPagedRPC reports whether a read follows forge's list-pagination shape:
// a singular `string page_token` on the request and a singular
// `string next_page_token` on the response. Such an rpc also gets an
// infinite-query hook.
//
// The SHAPE is the opt-in, not the name and not a marker. The two fields
// are exactly what useInfiniteQuery needs — the request's token is the page
// param, the response's is getNextPageParam — so any rpc carrying both can
// be paged correctly, whatever it is called (SearchCatalog pages the same
// way ListTasks does), and an rpc without them cannot be paged by the hook
// at all. A marker would only let an author withhold a hook that costs
// nothing unused; the extra export tree-shakes away.
//
// A method already named `<Name>Infinite` would collide with the generated
// hook's name, so its sibling gets no infinite hook rather than a duplicate
// export.
func isPagedRPC(svc ServiceDef, m Method) bool {
	if !hasStringField(svc.Messages[m.InputType], pageTokenField) ||
		!hasStringField(svc.Messages[m.OutputType], nextPageTokenField) {
		return false
	}
	for _, other := range svc.Methods {
		if other.Name == m.Name+"Infinite" {
			return false
		}
	}
	return true
}

// hasStringField reports a singular string field named name. Repeated
// fields are recorded as "[]string" and so never match.
func hasStringField(fields []MessageFieldDef, name string) bool {
	for _, f := range fields {
		if f.Name == name {
			return f.ProtoType == "string"
		}
	}
	return false
}

// ToCamelCaseFromPascalExport is the exported wrapper around the
// package-internal helper. Callers outside this package (the frontend
// hooks barrel generator deriving a namespace alias from a service name)
// use it so the camelCase rules stay in lockstep across packages.
func ToCamelCaseFromPascalExport(s string) string {
	return toCamelCaseFromPascal(s)
}

// toCamelCaseFromPascal converts PascalCase to camelCase by lowering the first
// run of uppercase letters. "GetUser" → "getUser", "RPCMethod" → "rpcMethod".
func toCamelCaseFromPascal(s string) string {
	if s == "" {
		return s
	}
	runes := []rune(s)
	// Find the end of the initial uppercase run.
	i := 0
	for i < len(runes) && unicode.IsUpper(runes[i]) {
		i++
	}
	if i == 0 {
		return s
	}
	// If the entire string is uppercase, lower it all.
	if i == len(runes) {
		return strings.ToLower(s)
	}
	// If multiple uppercase letters precede a lowercase, keep the last
	// uppercase as part of the next word: "RPCMethod" → "rpcMethod".
	if i > 1 {
		i--
	}
	return strings.ToLower(string(runes[:i])) + string(runes[i:])
}

// ProtoFileToTSImportPath converts a proto file path to the TypeScript import
// path used by the buf ES plugin. For example:
//
//	"proto/services/users/v1/users.proto" → "services/users/v1/users_pb"
//
// The buf ES plugin strips the leading proto/ directory and replaces the .proto
// extension with _pb.
func ProtoFileToTSImportPath(protoFile string) string {
	// Strip leading "proto/" if present.
	p := strings.TrimPrefix(protoFile, "proto/")
	// Strip .proto extension and append _pb.
	p = strings.TrimSuffix(p, ".proto") + "_pb"
	return p
}

// ServiceDefToHookData converts a ServiceDef to FrontendHookTemplateData,
// skipping streaming RPCs.
func ServiceDefToHookData(svc ServiceDef) FrontendHookTemplateData {
	data := FrontendHookTemplateData{
		ServiceName:      svc.Name,
		ServiceNameCamel: toCamelCaseFromPascal(svc.Name),
		ImportPath:       ProtoFileToTSImportPath(svc.ProtoFile),
	}

	// schemasByPath / typesByPath collect symbol -> tspath buckets. We use
	// sets keyed on symbol name to dedupe: the same Request type may be
	// referenced by multiple RPCs, and the same Response type may appear
	// in both queries and mutations.
	schemasByPath := map[string]map[string]struct{}{}
	typesByPath := map[string]map[string]struct{}{}

	addSym := func(buckets map[string]map[string]struct{}, path, sym string) {
		set, ok := buckets[path]
		if !ok {
			set = map[string]struct{}{}
			buckets[path] = set
		}
		set[sym] = struct{}{}
	}

	for _, m := range svc.Methods {
		// Skip streaming RPCs — only generate hooks for unary.
		if m.ClientStreaming || m.ServerStreaming {
			continue
		}

		isQuery := isQueryRPC(m)
		if isQuery {
			data.HasQueries = true
		} else {
			data.HasMutations = true
		}
		paged := isQuery && isPagedRPC(svc, m)
		if paged {
			data.HasPaged = true
		}

		// The Service value still lives in the service's own _pb.ts, so
		// ImportPath stays as svc.ProtoFile's derived path. But each
		// RPC's InputSchema (value import) and Output type (type-only
		// import) come from the file that physically declares them,
		// which may differ from svc.ProtoFile for cross-file refs.
		// Falling back to svc.ProtoFile when InputProtoFile/
		// OutputProtoFile are empty keeps legacy descriptor.json files
		// (written before the cross-file fix landed) producing valid
		// imports rather than `import "@/gen/_pb"`.
		inPath := m.InputProtoFile
		if inPath == "" {
			inPath = svc.ProtoFile
		}
		outPath := m.OutputProtoFile
		if outPath == "" {
			outPath = svc.ProtoFile
		}
		addSym(schemasByPath, ProtoFileToTSImportPath(inPath), m.InputType+"Schema")
		addSym(typesByPath, ProtoFileToTSImportPath(outPath), m.OutputType)

		data.Methods = append(data.Methods, FrontendHookMethod{
			Name:           m.Name,
			NameCamel:      toCamelCaseFromPascal(m.Name),
			ClientMethod:   naming.EsMethodName(m.Name),
			InputType:      m.InputType,
			OutputType:     m.OutputType,
			IsQuery:        isQuery,
			Paged:          paged,
			EntityScope:    methodEntityScope(svc, m),
			inputPath:      ProtoFileToTSImportPath(inPath),
			outputPath:     ProtoFileToTSImportPath(outPath),
			InputSchemaRef: m.InputType + "Schema",
			OutputTypeRef:  m.OutputType,
		})
	}

	// The Service descriptor itself is a value import from the service's
	// own _pb.ts. Folding it into the schema buckets (instead of a
	// dedicated template line) guarantees ONE import statement per source
	// module — a separate statement for the service tripped
	// import/no-duplicates whenever a request schema lived in the same
	// file (the common case).
	if len(data.Methods) > 0 {
		addSym(schemasByPath, data.ImportPath, svc.Name)
	}

	// A request type from another proto file can share a name with one of
	// the service's own: the file then imports it as `<module>_<Name>`.
	plan := newTSImportPlan(schemasByPath, typesByPath)
	data.SchemaImports = plan.Groups(schemasByPath)
	data.TypeImports = plan.Groups(typesByPath)
	data.ServiceRef = plan.Local(data.ImportPath, svc.Name)
	for i, m := range data.Methods {
		data.Methods[i].InputSchemaRef = plan.Local(m.inputPath, m.InputType+"Schema")
		data.Methods[i].OutputTypeRef = plan.Local(m.outputPath, m.OutputType)
	}

	scopeSet := map[string]struct{}{}
	for _, m := range data.Methods {
		if m.EntityScope != "" {
			scopeSet[m.EntityScope] = struct{}{}
		}
	}
	for s := range scopeSet {
		data.EntityScopes = append(data.EntityScopes, s)
	}
	sort.Strings(data.EntityScopes)

	return data
}

// ApplyQueryScopes keys each custom read named in scopes (rpc name →
// entity message name, from `// forge:query-scope <Entity>`; see
// ProtoMarkerQueryScope) under that entity's scope, and recomputes the
// service's EntityScopes so the key factory declares it. Call it after the
// `// forge:mutation` overrides: the marker applies to whatever is a read
// once they have been applied.
//
// It refuses — rather than ignores — a marker it cannot honour, because a
// scope that silently does nothing is the stale read the author wrote it to
// prevent: one naming no entity, one on a mutation, one contradicting a CRUD
// rpc's own scope, and one naming an entity the service neither serves CRUD
// for nor declares a message of (a typo, with the nearest name suggested).
func ApplyQueryScopes(data *FrontendHookTemplateData, svc ServiceDef, scopes map[string]string) error {
	if len(scopes) == 0 {
		return nil
	}
	known := queryScopeCandidates(svc)
	for i := range data.Methods {
		m := &data.Methods[i]
		entity, ok := scopes[m.Name]
		if !ok {
			continue
		}
		where := fmt.Sprintf("%s.%s: `// %s %s`", svc.Name, m.Name, ProtoMarkerQueryScope, entity)
		if entity == "" {
			return fmt.Errorf("%s.%s: `// %s` names no entity — write the entity's message name after it, e.g. `// %s Task`",
				svc.Name, m.Name, ProtoMarkerQueryScope, ProtoMarkerQueryScope)
		}
		if !m.IsQuery {
			return fmt.Errorf("%s: the marker scopes a READ's query key, and %s generates a useMutation hook — "+
				"a custom mutation already invalidates every query on %s, so a scope could only narrow that. "+
				"Declare `option idempotency_level = NO_SIDE_EFFECTS;` if the rpc only reads, or delete the marker",
				where, m.Name, svc.Name)
		}
		first, _ := utf8.DecodeRuneInString(entity)
		if !unicode.IsUpper(first) || !declaresEntityMessage(svc, entity) && !known[entity] {
			hint := ""
			names := make([]string, 0, len(known))
			for n := range known {
				names = append(names, n)
			}
			sort.Strings(names)
			if near := ClosestFrom(entity, names); near != "" {
				hint = fmt.Sprintf(" — did you mean %s?", near)
			}
			return fmt.Errorf("%s: %s is not an entity of %s — name the entity's proto MESSAGE, exactly as declared "+
				"(a CRUD entity of the service, or a message it declares)%s", where, entity, svc.Name, hint)
		}
		scope := toCamelCaseFromPascal(entity)
		if m.EntityScope != "" && m.EntityScope != scope {
			return fmt.Errorf("%s: %s is CRUD for %s and already keyed under %q; a second scope would split it from "+
				"its own entity's invalidation. Delete the marker", where, m.Name, m.EntityScope, m.EntityScope)
		}
		m.EntityScope = scope
	}
	set := map[string]struct{}{}
	for _, m := range data.Methods {
		if m.EntityScope != "" {
			set[m.EntityScope] = struct{}{}
		}
	}
	data.EntityScopes = data.EntityScopes[:0]
	for s := range set {
		data.EntityScopes = append(data.EntityScopes, s)
	}
	sort.Strings(data.EntityScopes)
	return nil
}

// queryScopeCandidates is the set of entity message names a query scope
// may name without further evidence: the CRUD entities of the service.
func queryScopeCandidates(svc ServiceDef) map[string]bool {
	out := map[string]bool{}
	for _, m := range svc.Methods {
		if _, entity := crudEntity(svc, m); entity != "" {
			out[entity] = true
		}
	}
	return out
}

// methodEntityScope derives the camelCase singular entity name from a CRUD
// RPC: "ListTasks" → "task", "CreateTask" → "task". Returns "" for a custom
// RPC — including one whose name merely starts with a CRUD verb, like
// "UpdateTaskStatus" (see crudEntity), or one marked `// forge:custom` — and
// those key/invalidate at the service scope.
func methodEntityScope(svc ServiceDef, m Method) string {
	_, entity := crudEntity(svc, m)
	if entity == "" {
		return ""
	}
	return toCamelCaseFromPascal(entity)
}

// tsImportPlan is the local name a generated TS file refers to each symbol
// it imports by.
//
// A TS module's imports share one scope, so a symbol imported from two
// modules is a duplicate declaration (TS2300). Proto keeps message names per
// package, and two services each declaring `PingRequest` is ordinary — so a
// file that imports from several modules (the project-wide scenario handler
// map; a service's hooks, when a cross-file request type shares a name with a
// local one) failed `tsc` the moment that happened. Every symbol imported
// from exactly ONE module keeps its own name, so a file without a clash is
// byte-identical to before; a symbol imported from two or more modules is
// imported `as <module>_<Symbol>` from each (tsModuleIdent), and the file
// names that.
type tsImportPlan map[[2]string]string // {path, symbol} -> local name

// newTSImportPlan plans one file's imports, given every path -> symbol-set
// bucket it imports from (value and type-only alike: they share the scope).
func newTSImportPlan(bucketSets ...map[string]map[string]struct{}) tsImportPlan {
	paths := map[string]map[string]bool{} // symbol -> importing paths
	for _, buckets := range bucketSets {
		for path, syms := range buckets {
			for sym := range syms {
				if paths[sym] == nil {
					paths[sym] = map[string]bool{}
				}
				paths[sym][path] = true
			}
		}
	}
	plan := tsImportPlan{}
	for sym, from := range paths {
		for path := range from {
			local := sym
			if len(from) > 1 {
				local = tsModuleIdent(path) + "_" + sym
			}
			plan[[2]string{path, sym}] = local
		}
	}
	return plan
}

// Local is the name the file refers to symbol sym from module path by.
func (p tsImportPlan) Local(path, sym string) string {
	if local, ok := p[[2]string{path, sym}]; ok {
		return local
	}
	return sym
}

// Groups renders buckets as import statements, writing an aliased symbol as
// `Sym as module_Sym`. Sorted at both levels, like flattenImportGroups.
func (p tsImportPlan) Groups(buckets map[string]map[string]struct{}) []HookImportGroup {
	groups := flattenImportGroups(buckets)
	for i, g := range groups {
		for j, sym := range g.Symbols {
			if local := p.Local(g.ImportPath, sym); local != sym {
				groups[i].Symbols[j] = sym + " as " + local
			}
		}
	}
	return groups
}

// tsModuleIdent is a TS identifier naming a generated module path, unique
// per path: "services/alpha/v1/alpha_pb" → "services_alpha_v1_alpha_pb".
func tsModuleIdent(path string) string {
	var b strings.Builder
	for _, r := range path {
		if r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// flattenImportGroups converts a path -> symbol-set map into a sorted
// []HookImportGroup with sorted, deduplicated symbol slices. Sorting at
// both levels makes the rendered TS deterministic byte-for-byte across
// runs, which the snapshot-style codegen tests rely on.
func flattenImportGroups(buckets map[string]map[string]struct{}) []HookImportGroup {
	if len(buckets) == 0 {
		return nil
	}
	paths := make([]string, 0, len(buckets))
	for p := range buckets {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	out := make([]HookImportGroup, 0, len(paths))
	for _, p := range paths {
		set := buckets[p]
		syms := make([]string, 0, len(set))
		for s := range set {
			syms = append(syms, s)
		}
		sort.Strings(syms)
		out = append(out, HookImportGroup{ImportPath: p, Symbols: syms})
	}
	return out
}
