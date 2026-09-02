// Copyright (c) 2025 Reliant Labs
package tools

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/rctx"
)

// --- code_context ---
//
// One call answers the three questions an agent asks about a symbol before it
// can change anything: where is it defined, who calls it, and what does it
// call. Answering them separately is what makes code navigation expensive.
//
// The cost being targeted is NOT tool latency — measured across a real session,
// tool execution was 1.9% of wall clock. It is the per-turn deliberation tax.
// Instrumented model-call durations correlate with generated reasoning at
// r=0.97 and with context size at only r=0.14, and calls emitting >=12k
// characters of reasoning were 16% of calls but 66% of total model time. A grep
// walk spends a turn per hop, and every hop pays that tax to decide what to
// grep next. Collapsing the walk into one result removes the decisions, not
// just the subprocess calls.
//
// Language servers are the engine because these questions are not greppable: a
// call site does not name its receiver's type, and an implementation never
// names the interface it satisfies. In this repo `Execute` resolves to dozens
// of unrelated methods, so a grep-based caller walk compounds error at every
// level.
//
// Engines are pluggable (see code_context_engines.go) because "which language"
// is a property of the file, not of the tool. Go and TypeScript both resolve
// real edges; everything else degrades to a text engine that SAYS it is
// approximate. A confident wrong answer is worse than a labeled partial one.

type CodeContextParams struct {
	Symbol string `json:"symbol" jsonschema:"required,description=Symbol to look up (function type method class or variable name) e.g. 'ResolveDaemon'"`
	File   string `json:"file,omitempty" jsonschema:"description=Optional file path fragment to disambiguate when the symbol is declared in several places"`
	Repo   string `json:"repo,omitempty" jsonschema:"description=Multi-repo only. Which repo to search: 'root' or a repo name. Omit in single-repo projects."`
	Want   string `json:"want,omitempty" jsonschema:"enum=all,enum=definition,enum=callers,enum=callees,enum=implementations,description=Which relationships to return (default: all)"`
	Limit  int    `json:"limit,omitempty" jsonschema:"description=Max entries per section (default: 25)"`

	// Depth turns the single-hop answer into a call map. Defaults to a real
	// trace rather than one hop: the whole point is to answer "how is this
	// reached" without a turn per level, and a reader who wanted one level can
	// ignore the extra lines far more cheaply than they can spend another turn.
	// Direction follows `want` (callers by default).
	Depth int `json:"depth,omitempty" jsonschema:"description=How many levels of the call graph to expand as a tree (1-5; default 3). 1 gives flat lists; 3+ traces a full request path in one call."`

	// IncludeSource answers the most common follow-up ("what does it actually
	// do") in the same call, instead of costing a second turn on view. On by
	// default because input tokens are cheap and a follow-up turn is not.
	IncludeSource *bool `json:"include_source,omitempty" jsonschema:"description=Include the first ~30 lines of the definition's source (default: true)"`

	// Scope keeps results inside the user's own code by default. Sibling repos
	// under the same workspace root count as "own code"; the standard library
	// and installed dependencies do not.
	Scope string `json:"scope,omitempty" jsonschema:"enum=project,enum=all,description=Limit results to this workspace's code (default: project) or include the standard library and dependencies (all)"`
}

type codeContextTool struct{}

const (
	CodeContextToolName        = "code_context"
	codeContextToolDescription = `Resolve a code symbol to its definition, callers, callees, and implementations in ONE call.

⚠️ GO AND TYPESCRIPT/JAVASCRIPT ONLY. Call graphs are resolved by a language server
(gopls for .go, tsserver for .ts/.tsx/.js/.jsx), and only those two are supported.
For any other language — Python, Ruby, Java, Rust, C# — this falls back to a text
search that CANNOT resolve call edges, and you should use bash + rg instead.

Use this INSTEAD of a multi-step grep walk whenever the question is about a symbol's
relationships. A grep walk costs one turn per hop and each turn re-derives what to
search next; this returns the whole neighborhood at once.

WHEN TO USE THIS TOOL:
- "Who calls X?" / "What does X call?" — the answers grep cannot compute, because a
  call site never names the receiver's type.
- "What implements this interface?" — neither Go nor TypeScript records that at the
  implementation site, so there is no text to search for.
- "Where is X defined?" when the name is ambiguous across packages or modules.
- Orienting in unfamiliar code: one call replaces the definition->callers->callees walk.

LANGUAGE SUPPORT:
- Go (.go) — resolved by gopls. Authoritative.
- TypeScript / JavaScript (.ts .tsx .js .jsx .mts .cts) — resolved by tsserver from
  the project's own TypeScript install. Authoritative.
- Everything else (Python, Ruby, Java, ...) — a text engine reports call sites and
  their enclosing function. Useful for locating, but approximate: it cannot see
  dynamic dispatch and may include same-named methods on unrelated types. The
  response labels this explicitly.

WHEN NOT TO USE THIS TOOL:
- Full-text or pattern search ("find every TODO", "which files mention retry") — use bash + rg.
- Reading a file you already located — use view.

PREFER THIS OVER GREP FOR ANY NAMED SYMBOL. If you are about to run
` + "`rg 'SomeFunction'`" + ` or ` + "`grep -n 'SomeType'`" + ` against Go or TypeScript,
call this instead — it returns everything that search would have found plus the
things it cannot find, in one call rather than one call per hop.

HOW TO USE:
Usually just: code_context(symbol: "ResolveDaemon"). The defaults are the common case —
the definition, its source, and a 3-level call map, which is what "how does this work"
actually needs. Every parameter below is optional.

- symbol (REQUIRED): the identifier alone, unqualified and with no regex.
  Good: "ResolveDaemon", "classifyDaemonWait", "DaemonRegistryService".
  Bad: "svc.ResolveDaemon" (drop the receiver), "Resolve.*" (not a pattern),
  "func ResolveDaemon" (just the name).

- want: which relationships to return. Default "all".
  "all"             definition + source + call map + implementations
  "callers"         who reaches this — trace a request path inbound
  "callees"         what this drives — see the work a function delegates
  "implementations" on an interface: who implements it. On a concrete method:
                    which interfaces it satisfies (the reverse lookup).
  "definition"      still includes the graph; narrows the emphasis, not the answer.

- depth: levels of call graph to expand as a tree. Default 3, max 5. Depth 3
  answers "who ultimately triggers this" for typical handler->service->repo
  layering. Use depth 1 when you only want the immediate neighbors as flat lists.

- file: a path FRAGMENT to disambiguate when a name is declared many times
  (` + "`Execute`" + ` has dozens here). The response tells you when this is needed and
  lists the alternatives — pass e.g. file: "daemon_router_nats.go".

- include_source: first ~30 lines of the definition. On by default, because the
  usual next question is "what does it do" and that would cost another turn.
  Set false when you want only the graph.

- scope: "project" (default) keeps results in this workspace INCLUDING sibling
  repos linked into it, and excludes the standard library, node_modules and
  vendored code. Use "all" only to deliberately read into a dependency.

- repo: multi-repo projects only — "root" or a repo name. Omit otherwise.

- limit: max entries per flat section. Default 25, max 200. The response says
  when a section was truncated.

OUTPUT:
An ENGINE line naming what resolved the query and how far to trust it, a DEFINITION
line with the first ~30 lines of source, then either a CALL MAP tree (depth > 1) or
flat CALLERS / CALLEES / IMPLEMENTATIONS sections. Truncation is always stated.

NOTES:
- The first call in a large repo pays a one-time index cost (a few seconds); later calls are fast.
- Results include test files; they are real callers.
- If a language server is missing, the response says so and names the install command.`

	// codeContextDefaultLimit bounds each section. The point of the tool is to
	// answer a question in one turn, and a 400-entry caller dump neither fits
	// that nor gets read — it just moves the cost from turns to tokens.
	codeContextDefaultLimit = 25

	// codeContextMaxLimit caps what a caller can ask for. A symbol with
	// thousands of references is a signal to narrow the question, not to page
	// the whole graph through a prompt.
	codeContextMaxLimit = 200

	// codeContextTimeout covers a cold language-server index on a large repo.
	// Measured here: gopls ~9s cold, tsserver ~2s; both well under a second warm.
	codeContextTimeout = 120 * time.Second
)

func NewCodeContextTool() Tool {
	return NewToolWrapper[CodeContextParams, ToolResponse](&codeContextTool{})
}

func (t *codeContextTool) Name() string { return CodeContextToolName }

func (t *codeContextTool) Description() string { return codeContextToolDescription }

// RequiresPermission is false: this tool only reads source already on disk and
// runs no user-supplied command. Gating it behind a prompt would push agents
// back to bash, which is both broader in power and the thing being replaced.
func (t *codeContextTool) RequiresPermission(_ CodeContextParams) (bool, error) {
	return false, nil
}

func (t *codeContextTool) IsReadOnly() bool { return true }

// codeLocation is one resolved point in the code graph.
//
// A caller has TWO interesting positions and they are not the same one: the
// call site (what a reader wants to look at) and the declaration of the
// function containing it (the only position a language server can be queried
// at). Conflating them silently breaks graph traversal — every node resolves to
// zero children, which looks like "nothing calls this" rather than like a bug.
type codeLocation struct {
	Path      string // absolute while resolving; relativized at render time
	Line      int
	Col       int    // 1-based; language servers resolve a position, so this must be exact
	EndLine   int    // last line of the declaration, when the engine reports one (0 = unknown)
	Enclosing string // enclosing function/type, when the engine reports one
	Detail    string // declaration text or other engine detail

	// Decl is the enclosing declaration's position, set when it differs from
	// Path/Line/Col. Traversal uses it; display does not.
	Decl *codePosition
}

type codePosition struct {
	Path string
	Line int
	Col  int
}

// traversalPoint returns the position to query a language server at, which is
// the enclosing declaration when one was reported.
func (l codeLocation) traversalPoint() codeLocation {
	if l.Decl == nil {
		return l
	}
	return codeLocation{
		Path:      l.Decl.Path,
		Line:      l.Decl.Line,
		Col:       l.Decl.Col,
		Enclosing: l.Enclosing,
		Detail:    l.Detail,
	}
}

// symbolGraph is what every engine returns. References is populated only by
// approximate engines, which cannot separate a call from a mention.
type symbolGraph struct {
	Detail            string // declaration text, when the engine can produce a better one
	DefinitionName    string // resolved symbol name, used to label a call map's root
	DefinitionEndLine int    // last line of the declaration, when the engine knows it
	Callers           []codeLocation
	Callees           []codeLocation
	Implementations   []codeLocation
	References        []codeLocation
}

func (t *codeContextTool) Execute(tc *rctx.ToolContext, params CodeContextParams) (ToolResponse, error) {
	symbol := strings.TrimSpace(params.Symbol)
	if symbol == "" {
		return NewTextErrorResponse("symbol is required"), nil
	}

	root, err := ResolveRepoPath(tc, params.Repo)
	if err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}

	limit := params.Limit
	if limit <= 0 {
		limit = codeContextDefaultLimit
	}
	if limit > codeContextMaxLimit {
		limit = codeContextMaxLimit
	}

	want := strings.ToLower(strings.TrimSpace(params.Want))
	if want == "" {
		want = "all"
	}

	depth := params.Depth
	if depth <= 0 {
		depth = codeContextDefaultDepth
	}
	if depth > codeContextMaxDepth {
		depth = codeContextMaxDepth
	}

	scope := parseScope(params.Scope)

	ctx, cancel := context.WithTimeout(context.Background(), codeContextTimeout)
	defer cancel()

	// Locating the declaration is the prerequisite for every other question:
	// language servers are positional, so without a file:line:col there is
	// nothing to ask them.
	candidates, declErr := declarationCandidates(ctx, root, symbol, params.File)
	if declErr != nil {
		return NewTextErrorResponse(fmt.Sprintf(
			"could not locate symbol %q under %s: %v\n\nIf the name is a substring of the real identifier, or lives in an excluded directory, search with: rg -n '%s' .",
			symbol, root, declErr, symbol)), nil
	}
	decl := candidates[0]

	engine, degraded, engineErr := selectEngine(root, decl.Path)
	if engineErr != nil {
		// Report where the symbol IS, even though its graph could not be
		// resolved: that much is already known, and it saves the retry from
		// starting over.
		return NewTextErrorResponse(fmt.Sprintf(
			"%s\n\nThe symbol itself was located at %s:%d",
			engineErr, relativizePath(root, decl.Path), decl.Line)), nil
	}

	// An interface method and its implementation are both real declarations of
	// the same name, but only the implementation has callers. Pick the one that
	// can answer before reporting "nothing calls this".
	if want != "definition" {
		direction := "callers"
		if want == "callees" {
			direction = "callees"
		}
		decl = preferResolvableDeclaration(ctx, engine, root, candidates, direction)
	}

	var out strings.Builder
	fmt.Fprintf(&out, "SYMBOL: %s\n", symbol)
	fmt.Fprintf(&out, "ENGINE: %s\n", engine.ID())
	if caveat := engine.Caveat(); caveat != "" {
		out.WriteString(caveat)
	}
	if degraded != "" {
		// Naming the missing binary turns a silently worse answer into a
		// one-line fix the reader can act on.
		fmt.Fprintf(&out, "%s\n", degraded)
	}
	out.WriteString("\n")

	// The engine may know the declaration better than a regex does (a real
	// signature rather than the matched source line).
	// Resolve the graph even for want="definition".
	//
	// Skipping it made the narrowest request the WORST answer: an agent asking
	// "where is Live defined" got one line and had to spend another turn asking
	// who calls it. The engine query is the same cost either way, and the
	// caller asked a question about a symbol — the neighborhood is context they
	// almost always want next.
	graph := symbolGraph{}
	externalHidden := 0
	{
		graph = engine.Resolve(ctx, root, decl, want)

		var n int
		graph.Callers, n = filterInScope(root, graph.Callers, scope)
		externalHidden += n
		graph.Callees, n = filterInScope(root, graph.Callees, scope)
		externalHidden += n
		graph.Implementations, n = filterInScope(root, graph.Implementations, scope)
		externalHidden += n
		graph.References, n = filterInScope(root, graph.References, scope)
		externalHidden += n
	}
	detail := decl.Detail
	if graph.Detail != "" {
		detail = graph.Detail
	}

	fmt.Fprintf(&out, "DEFINITION\n  %s:%d", relativizePath(root, decl.Path), decl.Line)
	if detail != "" {
		fmt.Fprintf(&out, "  %s", truncateDetail(detail))
	}
	out.WriteString("\n")

	// Silently choosing among several same-named declarations is how a reader
	// ends up confidently reading about the wrong `Execute`. Name the others.
	writeAmbiguityNote(&out, root, decl, candidates)

	if params.IncludeSource == nil || *params.IncludeSource {
		// The engine may know the exact extent (tsserver does); pass it through
		// so the preview stops at the real end of the declaration.
		previewLoc := decl
		previewLoc.EndLine = graph.DefinitionEndLine
		if body, ok := readSourcePreview(previewLoc, codeContextSourceLines); ok {
			out.WriteString("\n")
			out.WriteString(body)
		}
	}
	out.WriteString("\n")

	// A call map replaces the flat caller list, because it already contains
	// depth 1 — printing both would say the same thing twice. It applies only
	// to call-graph questions: "what implements this" has no depth, and
	// rendering an empty tree for it would hide the answer that was asked for.
	// "definition" maps too: the caller wants to understand a symbol, and how it
	// is reached is part of that.
	mappable := want == "all" || want == "callers" || want == "callees" || want == "definition"
	if depth > 1 && engine.Resolves() && mappable {
		direction := "callers"
		if want == "callees" {
			direction = "callees"
		}
		node, elided := buildCallMap(ctx, engine, root, declFor(decl, graph), direction, depth, scope)
		writeCallMap(&out, root, node, direction, elided)
		writeScopeNote(&out, scope, externalHidden)

		if want == "all" && len(graph.Implementations) > 0 {
			writeSection(&out, root, "IMPLEMENTATIONS", graph.Implementations, limit,
				"No implementations found.")
		}
		return NewTextResponse(out.String()), nil
	}

	switch {
	case len(graph.References) > 0 || (!engine.Resolves() && want == "all"):
		writeSection(&out, root, "REFERENCES (text matches)", graph.References, limit,
			"No other references found.")
	default:
		if want == "all" || want == "definition" || want == "callers" {
			writeSection(&out, root, "CALLERS", graph.Callers, limit,
				"Nothing calls this symbol directly. It may be an entry point, wired by\n  reflection/DI, or reached only through an interface — try want=implementations.")
			// Advertise the deeper query at the moment it would help. A tool
			// description gets skimmed; a line in the result the reader is
			// already looking at does not.
			if depth == 1 && len(graph.Callers) > 0 && engine.Resolves() {
				fmt.Fprintf(&out, "  → depth=3 traces these callers to their own callers in one call.\n\n")
			}
		}
		if want == "all" || want == "definition" || want == "callees" {
			writeSection(&out, root, "CALLEES", graph.Callees, limit,
				"This symbol calls nothing in the analyzed packages.")
		}
		if want == "implementations" || ((want == "all" || want == "definition") && len(graph.Implementations) > 0) {
			// The same query answers both directions, and which one you got
			// depends on what the symbol is: asked on an interface it returns
			// implementors, asked on a concrete method it returns the
			// interfaces that method satisfies. Naming that in the heading
			// saves the reader from guessing which they are looking at.
			heading := "IMPLEMENTATIONS"
			if isConcreteDeclaration(detail) {
				heading = "IMPLEMENTS (interfaces this satisfies)"
			}
			writeSection(&out, root, heading, graph.Implementations, limit,
				"No implementations found. This is expected unless the symbol is an interface.")
		}
	}

	if want != "definition" {
		writeScopeNote(&out, scope, externalHidden)
	}
	return NewTextResponse(out.String()), nil
}

// writeAmbiguityNote lists other declarations of the same name.
//
// A name like `Execute` has dozens of declarations in a Go codebase, and
// answering about one of them with no indication the others exist is worse than
// asking: the reader has no signal that they are reading about the wrong type.
const ambiguityNoteLimit = 4

func writeAmbiguityNote(out *strings.Builder, root string, chosen codeLocation, candidates []codeLocation) {
	others := make([]codeLocation, 0, len(candidates))
	for _, c := range candidates {
		if c.Path == chosen.Path && c.Line == chosen.Line {
			continue
		}
		others = append(others, c)
	}
	if len(others) == 0 {
		return
	}

	fmt.Fprintf(out, "\n  NOTE: %d other declaration(s) of %q exist. Pass `file` to choose one:\n",
		len(others), strings.TrimSpace(symbolAt(chosen)))
	shown := others
	if len(shown) > ambiguityNoteLimit {
		shown = shown[:ambiguityNoteLimit]
	}
	for _, o := range shown {
		fmt.Fprintf(out, "    %s:%d\n", relativizePath(root, o.Path), o.Line)
	}
	if len(others) > len(shown) {
		fmt.Fprintf(out, "    ... and %d more\n", len(others)-len(shown))
	}
}

// writeScopeNote states what scoping removed. Silently dropping edges would be
// indistinguishable from a symbol that genuinely has none, which is the failure
// mode this tool exists to avoid.
func writeScopeNote(out *strings.Builder, scope scopeMode, hidden int) {
	if scope != scopeProject || hidden == 0 {
		return
	}
	fmt.Fprintf(out, "(%d edge(s) in the standard library or dependencies were omitted; scope=\"all\" includes them)\n\n", hidden)
}

// declFor returns the location to seed a call map with, preferring the name the
// engine resolved so the tree's root is labeled like its children.
func declFor(decl codeLocation, graph symbolGraph) codeLocation {
	if graph.DefinitionName != "" {
		decl.Enclosing = graph.DefinitionName
	}
	return decl
}

// isConcreteDeclaration reports whether a declaration is an implementation
// rather than an interface, which flips what an implementation query means.
func isConcreteDeclaration(detail string) bool {
	lower := strings.ToLower(detail)
	if strings.Contains(lower, "interface") {
		return false
	}
	// A Go method carries a receiver; a TS/JS method is reported as a method.
	return strings.HasPrefix(strings.TrimSpace(detail), "func (") ||
		strings.Contains(lower, "(method)")
}

// findDeclaration locates the symbol's declaration site by text search. Every
// engine needs a position to start from, and a declaration-shaped regex is the
// one language-agnostic way to get one without first knowing the language.
func findDeclaration(ctx context.Context, root, symbol, fileHint string) (codeLocation, error) {
	candidates, err := declarationCandidates(ctx, root, symbol, fileHint)
	if err != nil {
		return codeLocation{}, err
	}
	if len(candidates) == 0 {
		return codeLocation{}, fmt.Errorf("no declaration found")
	}
	return candidates[0], nil
}

// preferResolvableDeclaration picks the candidate that can actually answer the
// question, not merely the best-looking one.
//
// The case that forced this: in Go, `Foo` is commonly declared twice — once as
// an interface method and once as the concrete implementation. Both are real
// declarations, but a language server reports callers only at the CONCRETE one;
// asking at the interface method returns nothing (verified: gopls answers
// "identifier not found" there). Ranking by path alone can therefore pick the
// declaration that yields an empty call map, which reads as "nothing calls this"
// — the exact wrong conclusion, on a symbol with a dozen callers.
//
// So when the top candidate resolves to no edges and another candidate does,
// prefer the one that resolves. The cost is one extra query, paid only in the
// ambiguous case, and only when the first answer was empty.
func preferResolvableDeclaration(
	ctx context.Context,
	engine languageEngine,
	root string,
	candidates []codeLocation,
	direction string,
) codeLocation {
	best := candidates[0]
	if !engine.Resolves() || len(candidates) == 1 {
		return best
	}
	if len(engine.ResolveEdges(ctx, root, best, direction)) > 0 {
		return best
	}
	// Bounded: a symbol declared in many places is usually a naming collision,
	// and probing all of them would cost more than the answer is worth.
	const maxAlternatives = 3
	for i := 1; i < len(candidates) && i <= maxAlternatives; i++ {
		if len(engine.ResolveEdges(ctx, root, candidates[i], direction)) > 0 {
			return candidates[i]
		}
	}
	return best
}

// declSearchRe builds a pattern matching a DECLARATION of symbol across the
// languages this tool sees, rather than every mention of it.
func declSearchRe(symbol string) string {
	q := regexp.QuoteMeta(symbol)
	return strings.Join([]string{
		`func\s+(\([^)]*\)\s*)?` + q + `\b`, // Go func / method
		`type\s+` + q + `\b`,                // Go type
		`(var|const)\s+` + q + `\b`,         // Go var / const
		`(class|interface|enum|struct|trait)\s+` + q + `\b`,
		`(function|def)\s+` + q + `\b`,
		`(const|let|var)\s+` + q + `\s*[=:]`,
		// Class methods and object properties. Anchored to line start with
		// optional modifiers so a bare call site does not match.
		//
		// `(` and `<` only — NOT `:` or `=`. Allowing those matched a Go struct
		// literal field (`Live: s.Connected,`) and ranked it above the real
		// `func (s WorkflowStatus) Live() bool`, so the tool reported an
		// assignment as the definition. A TypeScript method or property worth
		// finding is followed by a paren or a type parameter list.
		`^\s*(export\s+)?(default\s+)?(public\s+|private\s+|protected\s+|static\s+|async\s+|readonly\s+)*` + q + `\s*[(<]`,
	}, "|")
}

// declarationCandidates finds plausible declaration sites, best first.
func declarationCandidates(ctx context.Context, root, symbol, fileHint string) ([]codeLocation, error) {
	args := []string{
		"--line-number", "--column", "--no-heading", "--color", "never",
		"--max-count", "50",
		"--glob", "!node_modules", "--glob", "!.git", "--glob", "!vendor",
		"--glob", "!dist", "--glob", "!build", "--glob", "!.next",
		"-e", declSearchRe(symbol),
	}
	if strings.TrimSpace(fileHint) != "" {
		args = append(args, "--glob", "*"+strings.TrimSpace(fileHint)+"*")
	}
	args = append(args, ".")

	outStr, err := runTool(ctx, root, "rg", args...)
	if err != nil && strings.TrimSpace(outStr) == "" {
		return nil, fmt.Errorf("search failed: %w", err)
	}

	var found []codeLocation
	for _, line := range strings.Split(outStr, "\n") {
		loc, ok := parseRipgrepLine(root, line)
		if !ok {
			continue
		}
		// Point the column at the identifier itself. A language server
		// resolves a position, and the match start is the keyword ("func",
		// "export"), not the name.
		if idx := strings.Index(loc.Detail, symbol); idx >= 0 {
			loc.Col = idx + 1
		}
		found = append(found, loc)
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("no declaration matched")
	}

	// Prefer real source over tests and generated code: the definition a reader
	// wants is the implementation, and a same-named test helper outranks nothing.
	sort.SliceStable(found, func(i, j int) bool {
		return declRank(found[i].Path) < declRank(found[j].Path)
	})
	return found, nil
}

// proseExtensions are files that can CONTAIN a symbol's name in something that
// looks like a declaration, while never being one.
//
// This is not hypothetical: a design doc containing the line
// "Live(state, reason) -- PENDING or ACTIVE" outranked two real Go declarations
// of `Live`, and because the winner was a .md the whole query then degraded to
// the text engine. The agent's first-ever use of this tool returned a worse
// answer than the grep it replaced.
var proseExtensions = map[string]bool{
	".md": true, ".markdown": true, ".txt": true, ".rst": true, ".adoc": true,
	".json": true, ".yaml": true, ".yml": true, ".toml": true, ".csv": true,
	".html": true, ".css": true, ".scss": true, ".sql": true, ".proto": true,
}

// declRank orders declaration candidates, lowest first.
//
// The dominant term is whether a language server can RESOLVE the file: a
// candidate that cannot be resolved makes every other section of the response
// empty or approximate, so it loses to any real source file.
func declRank(path string) int {
	base := filepath.Base(path)
	ext := strings.ToLower(filepath.Ext(path))

	switch {
	case proseExtensions[ext]:
		return 6 // documentation and data — never a declaration
	case !resolvableSourceExtensions[ext]:
		return 5 // real code, but no language server for it
	case strings.HasSuffix(path, "_test.go"),
		strings.Contains(base, ".test."), strings.Contains(base, ".spec."):
		return 4
	case strings.Contains(path, "/mock"), strings.HasSuffix(path, "_gen.go"),
		strings.HasSuffix(path, ".d.ts"):
		return 3
	case strings.Contains(path, "/gen/"):
		return 2
	default:
		return 0
	}
}

// resolvableSourceExtensions are the languages with a real engine behind them.
// Kept in sync with selectEngine by TestCodeContext_RankingMatchesEngines.
var resolvableSourceExtensions = map[string]bool{
	".go": true, ".ts": true, ".tsx": true, ".mts": true, ".cts": true,
	".js": true, ".jsx": true, ".mjs": true, ".cjs": true,
}

// ripgrepLineRe parses `path:line:col:text`.
var ripgrepLineRe = regexp.MustCompile(`^(.+?):(\d+):(\d+):(.*)$`)

func parseRipgrepLine(root, line string) (codeLocation, bool) {
	m := ripgrepLineRe.FindStringSubmatch(strings.TrimSpace(line))
	if m == nil {
		return codeLocation{}, false
	}
	ln, err := strconv.Atoi(m[2])
	if err != nil {
		return codeLocation{}, false
	}
	col, err := strconv.Atoi(m[3])
	if err != nil {
		return codeLocation{}, false
	}
	path := m[1]
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	return codeLocation{Path: path, Line: ln, Col: col, Detail: strings.TrimSpace(m[4])}, true
}

// writeSection renders one section, always stating truncation. A silently
// truncated list reads as a complete one and produces confidently wrong
// conclusions.
func writeSection(out *strings.Builder, root, title string, locs []codeLocation, limit int, emptyNote string) {
	fmt.Fprintf(out, "%s (%d)\n", title, len(locs))
	if len(locs) == 0 {
		fmt.Fprintf(out, "  %s\n\n", emptyNote)
		return
	}
	shown := locs
	if len(shown) > limit {
		shown = shown[:limit]
	}
	for _, l := range shown {
		fmt.Fprintf(out, "  %s:%d", relativizePath(root, l.Path), l.Line)
		if l.Enclosing != "" {
			fmt.Fprintf(out, "  in %s", l.Enclosing)
		}
		if l.Detail != "" {
			fmt.Fprintf(out, "  %s", truncateDetail(l.Detail))
		}
		out.WriteString("\n")
	}
	if len(locs) > len(shown) {
		fmt.Fprintf(out, "  ... %d more not shown (raise `limit`, or narrow with `file`)\n", len(locs)-len(shown))
	}
	out.WriteString("\n")
}

const codeContextDetailWidth = 120

func truncateDetail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= codeContextDetailWidth {
		return s
	}
	return s[:codeContextDetailWidth] + "..."
}

// relativizePath shortens absolute paths to repo-relative form. Absolute paths
// are noise in a digest whose whole purpose is to be scanned quickly.
func relativizePath(root, path string) string {
	if rel, err := filepath.Rel(root, path); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return path
}

// runTool executes a helper binary in root and returns combined stdout. A
// non-zero exit is returned as an error but any output is preserved: ripgrep
// exits 1 on "no matches", which is a valid empty answer rather than a failure.
func runTool(ctx context.Context, root, name string, args ...string) (string, error) {
	if _, err := exec.LookPath(name); err != nil {
		return "", fmt.Errorf("%s not found on PATH", name)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = root
	out, err := cmd.Output()

	var sb strings.Builder
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		sb.WriteString(scanner.Text())
		sb.WriteString("\n")
	}
	return sb.String(), err
}
