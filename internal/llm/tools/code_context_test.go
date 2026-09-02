// Copyright (c) 2025 Reliant Labs
package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm/tools/names"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// codeContextFixture writes a small Go module whose call graph is known by
// construction, so assertions test resolution rather than a snapshot of this
// repo (which changes under other agents).
//
// Shape:
//
//	Target()   <- called by CallerOne and CallerTwo
//	Target()   -> calls Helper()
//	Speaker    <- implemented by Dog
func codeContextFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	write := func(name, body string) {
		t.Helper()
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
	}

	write("go.mod", "module fixture\n\ngo 1.21\n")
	write("target.go", `package fixture

// Target is the symbol under test.
func Target(n int) int {
	return Helper(n) + 1
}

func Helper(n int) int {
	return n * 2
}
`)
	write("callers.go", `package fixture

func CallerOne() int {
	return Target(1)
}

func CallerTwo() int {
	return Target(2)
}
`)
	write("iface.go", `package fixture

type Speaker interface {
	Speak() string
}

type Dog struct{}

func (d Dog) Speak() string { return "woof" }
`)
	return dir
}

func codeContextCtx(t *testing.T, dir string) *rctx.ToolContext {
	t.Helper()
	return rctx.NewToolContext(context.Background(), "test-chat", "0", nil,
		&rctx.WorktreeInfo{Path: dir})
}

func runCodeContext(t *testing.T, dir string, params CodeContextParams) ToolResponse {
	t.Helper()
	tool := &codeContextTool{}
	resp, err := tool.Execute(codeContextCtx(t, dir), params)
	require.NoError(t, err)
	return resp
}

func requireRipgrep(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep not installed")
	}
}

func requireGopls(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not installed")
	}
}

func TestCodeContext_RequiresSymbol(t *testing.T) {
	resp := runCodeContext(t, t.TempDir(), CodeContextParams{})
	assert.True(t, resp.IsError)
	assert.Contains(t, resp.Content, "symbol is required")
}

func TestCodeContext_FindsDefinition(t *testing.T) {
	requireRipgrep(t)
	dir := codeContextFixture(t)

	resp := runCodeContext(t, dir, CodeContextParams{Symbol: "Target"})

	require.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, "DEFINITION")
	assert.Contains(t, resp.Content, "target.go:4",
		"definition should point at the func declaration line")
}

// The behavior that justifies the tool: callers and callees arrive together,
// in one call, without a per-hop turn.
func TestCodeContext_ResolvesCallersAndCallees(t *testing.T) {
	requireRipgrep(t)
	requireGopls(t)
	dir := codeContextFixture(t)

	resp := runCodeContext(t, dir, CodeContextParams{Symbol: "Target"})
	require.False(t, resp.IsError, resp.Content)
	require.Contains(t, resp.Content, "ENGINE: gopls", "expected Go to resolve via gopls:\n"+resp.Content)

	assert.Contains(t, resp.Content, "CallerOne", "CallerOne calls Target:\n"+resp.Content)
	assert.Contains(t, resp.Content, "CallerTwo", "CallerTwo calls Target:\n"+resp.Content)
	assert.Contains(t, resp.Content, "Helper", "Target calls Helper:\n"+resp.Content)
}

func TestCodeContext_ResolvesImplementations(t *testing.T) {
	requireRipgrep(t)
	requireGopls(t)
	dir := codeContextFixture(t)

	resp := runCodeContext(t, dir, CodeContextParams{
		Symbol: "Speaker",
		Want:   "implementations",
	})
	require.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, "IMPLEMENTATIONS")
	assert.Contains(t, resp.Content, "iface.go", "Dog implements Speaker:\n"+resp.Content)
}

func TestCodeContext_UnknownSymbolExplainsItself(t *testing.T) {
	requireRipgrep(t)
	dir := codeContextFixture(t)

	resp := runCodeContext(t, dir, CodeContextParams{Symbol: "NoSuchSymbolAnywhere"})

	assert.True(t, resp.IsError)
	// A dead end must hand back the next move, not just "not found".
	assert.Contains(t, resp.Content, "rg -n",
		"failure should suggest a concrete fallback search")
}

// An unsupported language must not silently present text matches as resolved
// call edges.
func TestCodeContext_TextEngineIsLabeled(t *testing.T) {
	requireRipgrep(t)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "app.rb"),
		[]byte("def handle_click\n  1\nend\n"), 0o644))

	resp := runCodeContext(t, dir, CodeContextParams{Symbol: "handle_click"})

	require.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, "approximate")
	assert.Contains(t, resp.Content, "TEXT MATCHES",
		"text engine must not be presented as resolved edges")
}

// A TypeScript file with no TypeScript install must FAIL, not degrade.
//
// Degrading looked reasonable until it was used for real: a worktree without
// node_modules returned 29 text matches for a symbol with exactly one caller,
// and nothing in the output let a reader tell that apart from a real answer.
// An error the caller can act on beats a plausible wrong one.
func TestCodeContext_MissingTypeScriptIsAnError(t *testing.T) {
	requireRipgrep(t)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"x"}`), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "src"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "src", "app.ts"),
		[]byte("export function handleClick() {\n  return 1;\n}\n"), 0o644))

	resp := runCodeContext(t, dir, CodeContextParams{Symbol: "handleClick"})

	require.True(t, resp.IsError, "must not silently return text matches:\n"+resp.Content)
	assert.Contains(t, resp.Content, "npm install", "the error must name the fix")
	assert.Contains(t, resp.Content, "gitignored",
		"the worktree case is the common one and should be explained")
	// The location is already known, so a retry should not have to re-find it.
	assert.Contains(t, resp.Content, "app.ts", "report where the symbol is:\n"+resp.Content)
}

// The suggested `npm install` directory must be the package root, not the
// source subdirectory — sending an agent to install in src/ would create a
// stray node_modules in the wrong place.
func TestCodeContext_MissingTypeScriptNamesThePackageDir(t *testing.T) {
	requireRipgrep(t)
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "web", "src", "lib"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "web", "package.json"), []byte(`{"name":"web"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "web", "src", "lib", "util.ts"),
		[]byte("export function helperFn() {\n  return 1;\n}\n"), 0o644))

	resp := runCodeContext(t, dir, CodeContextParams{Symbol: "helperFn"})

	require.True(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, "cd web && npm install",
		"should point at the package root, not src/lib:\n"+resp.Content)
}

// tsFixtureWithTypeScript builds a TypeScript project whose call graph is known
// by construction, and links in a real TypeScript install by symlinking one
// found elsewhere on the machine.
//
// Symlinking rather than `npm install` keeps the test hermetic and fast: the
// behavior under test is tsserver resolution, not package installation. The
// walk-up discovery is exercised for real, because the link is placed at the
// project root while the source file sits in src/.
func tsFixtureWithTypeScript(t *testing.T) string {
	t.Helper()
	tsLib := locateTypeScriptLib(t)

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "src"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "node_modules"), 0o755))
	require.NoError(t, os.Symlink(tsLib, filepath.Join(dir, "node_modules", "typescript")))

	write := func(name, body string) {
		t.Helper()
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
	}
	write("tsconfig.json", `{
  "compilerOptions": {
    "target": "ES2020",
    "module": "ESNext",
    "moduleResolution": "bundler",
    "strict": true,
    "noEmit": true
  },
  "include": ["src/**/*.ts"]
}`)
	write("src/target.ts", `export interface Speaker {
  speak(): string;
}

export class Dog implements Speaker {
  speak(): string {
    return "woof";
  }
}

export function helper(n: number): number {
  return n * 2;
}

export function target(n: number): number {
  return helper(n) + 1;
}
`)
	write("src/callers.ts", `import { target } from "./target";

export function callerOne(): number {
  return target(1);
}

export function callerTwo(): number {
  return target(2);
}
`)
	return dir
}

// locateTypeScriptLib finds a real typescript package on this machine, skipping
// the test when there is none. TypeScript is always a project dependency —
// there is no global install to rely on.
func locateTypeScriptLib(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	// A worktree often has no node_modules of its own; CI and agents can point
	// at a known install rather than silently skipping the TypeScript coverage.
	if override := os.Getenv("RELIANT_TEST_TYPESCRIPT_DIR"); override != "" {
		if _, err := os.Stat(filepath.Join(override, "lib", "tsserver.js")); err == nil {
			return override
		}
		t.Fatalf("RELIANT_TEST_TYPESCRIPT_DIR=%s has no lib/tsserver.js", override)
	}

	wd, err := os.Getwd()
	require.NoError(t, err)

	// Walk up from the package dir looking for any node_modules/typescript.
	dir := wd
	for {
		for _, candidate := range []string{
			filepath.Join(dir, "web", "node_modules", "typescript"),
			filepath.Join(dir, "electron", "node_modules", "typescript"),
			filepath.Join(dir, "node_modules", "typescript"),
		} {
			if _, err := os.Stat(filepath.Join(candidate, "lib", "tsserver.js")); err == nil {
				return candidate
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Skip("no typescript install found (run npm install in web/)")
	return ""
}

func TestCodeContext_TypeScriptResolvesCallersAndCallees(t *testing.T) {
	requireRipgrep(t)
	dir := tsFixtureWithTypeScript(t)

	resp := runCodeContext(t, dir, CodeContextParams{Symbol: "target", File: "target.ts"})

	require.False(t, resp.IsError, resp.Content)
	require.Contains(t, resp.Content, "tsserver", "expected tsserver to resolve:\n"+resp.Content)
	assert.Contains(t, resp.Content, "callerOne", "callerOne calls target:\n"+resp.Content)
	assert.Contains(t, resp.Content, "callerTwo", "callerTwo calls target:\n"+resp.Content)
	assert.Contains(t, resp.Content, "helper", "target calls helper:\n"+resp.Content)
}

func TestCodeContext_TypeScriptResolvesImplementations(t *testing.T) {
	requireRipgrep(t)
	dir := tsFixtureWithTypeScript(t)

	resp := runCodeContext(t, dir, CodeContextParams{
		Symbol: "Speaker",
		File:   "target.ts",
		Want:   "implementations",
	})

	require.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, "IMPLEMENTATIONS")
	assert.Contains(t, resp.Content, "target.ts", "Dog implements Speaker:\n"+resp.Content)
}

// tsserver yields the real signature; the declaration regex only sees the
// source line it matched.
func TestCodeContext_TypeScriptReportsSignature(t *testing.T) {
	requireRipgrep(t)
	dir := tsFixtureWithTypeScript(t)

	resp := runCodeContext(t, dir, CodeContextParams{Symbol: "target", File: "target.ts"})

	require.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, "function target(n: number): number",
		"expected quickinfo signature:\n"+resp.Content)
}

// A file in src/ must be resolved by the TypeScript install at the project
// root — the monorepo case, where each frontend carries its own copy.
func TestCodeContext_FindsTypeScriptByWalkingUp(t *testing.T) {
	dir := tsFixtureWithTypeScript(t)

	found := findTSServer(dir, filepath.Join(dir, "src", "target.ts"))

	require.NotEmpty(t, found, "should find node_modules/typescript above src/")
	assert.True(t, strings.HasSuffix(found, filepath.Join("typescript", "lib", "tsserver.js")))
}

func TestCodeContext_SelectsEngineByExtension(t *testing.T) {
	// A language with no server available anywhere degrades rather than
	// erroring: unlike a missing npm install there is nothing to install, so
	// an approximate answer is genuinely the best result on offer.
	engine, _, err := selectEngine(t.TempDir(), "/x/y/thing.rb")
	require.NoError(t, err)
	assert.False(t, engine.Resolves(), "ruby has no language server here")
	assert.Contains(t, engine.ID(), "approximate")
}

func TestCodeContext_PrefersNonTestDeclaration(t *testing.T) {
	requireRipgrep(t)
	dir := codeContextFixture(t)
	// A same-named helper in a test file must not outrank the real declaration.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "other_test.go"),
		[]byte("package fixture\n\nfunc Target(n int) int { return 0 }\n"), 0o644))

	resp := runCodeContext(t, dir, CodeContextParams{Symbol: "Target"})

	require.False(t, resp.IsError, resp.Content)
	definition := firstLineContaining(t, resp.Content, "target")
	assert.NotContains(t, definition, "_test.go",
		"implementation should outrank a test declaration:\n"+resp.Content)
}

// A multi-level trace is the feature that replaces a turn-per-hop caller walk,
// so the tree must actually reach level 2 — the fixture's callerOne is called
// by entryPoint, which only appears if traversal re-queries from the caller's
// DECLARATION rather than from the call site.
func TestCodeContext_CallMapReachesSecondLevel(t *testing.T) {
	requireRipgrep(t)
	requireGopls(t)
	dir := codeContextFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "entry.go"),
		[]byte("package fixture\n\nfunc EntryPoint() int {\n\treturn CallerOne()\n}\n"), 0o644))

	resp := runCodeContext(t, dir, CodeContextParams{Symbol: "Target", Depth: 3})

	require.False(t, resp.IsError, resp.Content)
	require.Contains(t, resp.Content, "CALL MAP", resp.Content)
	assert.Contains(t, resp.Content, "CallerOne", "level 1:\n"+resp.Content)
	assert.Contains(t, resp.Content, "EntryPoint",
		"level 2 requires traversing from the caller's declaration:\n"+resp.Content)
}

// Regression guard for the bug this design is most prone to: a caller node must
// keep the call site for display AND the enclosing declaration for traversal.
// Losing the second silently flattens every tree to one level.
func TestCodeContext_CallerKeepsDeclarationPosition(t *testing.T) {
	requireRipgrep(t)
	requireGopls(t)
	dir := codeContextFixture(t)

	callers := goplsEngine{}.ResolveEdges(context.Background(), dir,
		mustDeclare(t, dir, "Target"), "callers")

	require.NotEmpty(t, callers, "expected callers of Target")
	for _, caller := range callers {
		require.NotNil(t, caller.Decl,
			"caller %s must carry its enclosing declaration position", caller.Enclosing)
		assert.NotEqual(t, caller.Line, caller.Decl.Line,
			"call site and declaration are different lines for %s", caller.Enclosing)
		assert.Positive(t, caller.Decl.Col, "declaration column is required to re-query")
	}
}

func mustDeclare(t *testing.T, dir, symbol string) codeLocation {
	t.Helper()
	decl, err := findDeclaration(context.Background(), dir, symbol, "")
	require.NoError(t, err)
	return decl
}

// depth is defaulted, not required: the common call is code_context(symbol).
func TestCodeContext_DefaultsToMultiLevelTrace(t *testing.T) {
	requireRipgrep(t)
	requireGopls(t)
	dir := codeContextFixture(t)

	resp := runCodeContext(t, dir, CodeContextParams{Symbol: "Target"})

	require.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, "CALL MAP",
		"a bare call should trace, not return one hop:\n"+resp.Content)
}

// Source is included by default because the measured follow-up to a digest is a
// view call — a whole turn — while the extra input tokens are nearly free.
func TestCodeContext_IncludesSourceByDefault(t *testing.T) {
	requireRipgrep(t)
	dir := codeContextFixture(t)

	resp := runCodeContext(t, dir, CodeContextParams{Symbol: "Target", Want: "definition"})

	require.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, "return Helper(n) + 1",
		"definition body should be inlined:\n"+resp.Content)
}

func TestCodeContext_SourceCanBeDisabled(t *testing.T) {
	requireRipgrep(t)
	dir := codeContextFixture(t)
	off := false

	resp := runCodeContext(t, dir, CodeContextParams{
		Symbol: "Target", Want: "definition", IncludeSource: &off,
	})

	require.False(t, resp.IsError, resp.Content)
	assert.NotContains(t, resp.Content, "return Helper(n) + 1")
}

func TestCodeContext_SourcePreviewIsCapped(t *testing.T) {
	dir := t.TempDir()
	var body strings.Builder
	body.WriteString("package big\n\nfunc Long() {\n")
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&body, "\tprintln(%d)\n", i)
	}
	body.WriteString("}\n")
	path := filepath.Join(dir, "big.go")
	require.NoError(t, os.WriteFile(path, []byte(body.String()), 0o644))

	preview, ok := readSourcePreview(codeLocation{Path: path, Line: 3}, 30)

	require.True(t, ok)
	assert.Equal(t, 31, strings.Count(preview, "\n"),
		"30 source lines plus the truncation notice")
	assert.Contains(t, preview, "truncated at 30 lines")
}

// A brace inside a string must not end the preview early.
func TestCodeContext_SourcePreviewIgnoresBracesInStrings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.go")
	require.NoError(t, os.WriteFile(path, []byte(
		"package s\n\nfunc F() {\n\tprintln(\"}\")\n\tprintln(\"end\")\n}\n"), 0o644))

	preview, ok := readSourcePreview(codeLocation{Path: path, Line: 3}, 30)

	require.True(t, ok)
	assert.Contains(t, preview, `println("end")`,
		"a brace in a string literal must not close the function:\n"+preview)
}

// The node budget is what keeps a deep trace from costing more tokens than the
// turns it saves.
func TestCodeContext_CallMapIsBounded(t *testing.T) {
	wide := make([]codeLocation, 40)
	for i := range wide {
		wide[i] = codeLocation{Path: "a.go", Line: i + 1, Enclosing: fmt.Sprintf("fn%d", i)}
	}
	engine := stubEngine{edges: wide}

	node, elided := buildCallMap(context.Background(), engine, ".",
		codeLocation{Path: "root.go", Line: 1, Enclosing: "root"}, "callers", 3, scopeAll)

	assert.LessOrEqual(t, len(node.Children), codeContextMaxFanout,
		"fanout must be capped per node")
	assert.Positive(t, elided, "elided edges must be reported, not dropped silently")
	assert.LessOrEqual(t, countNodes(node), codeContextMaxNodes+1, "total nodes must be capped")
}

// A cycle must terminate and be labeled rather than expanded forever.
func TestCodeContext_CallMapHandlesCycles(t *testing.T) {
	a := codeLocation{Path: "a.go", Line: 10, Enclosing: "A"}
	b := codeLocation{Path: "b.go", Line: 20, Enclosing: "B"}
	engine := cycleEngine{a: a, b: b}

	node, _ := buildCallMap(context.Background(), engine, ".", a, "callers", 5, scopeAll)

	var sb strings.Builder
	writeCallMap(&sb, ".", node, "callers", 0)
	assert.Contains(t, sb.String(), "already shown",
		"a repeated node must be marked, not re-expanded:\n"+sb.String())
	assert.Less(t, countNodes(node), 20, "cycle must not expand without bound")
}

func countNodes(n *callNode) int {
	total := 1
	for _, c := range n.Children {
		total += countNodes(c)
	}
	return total
}

type stubEngine struct{ edges []codeLocation }

func (stubEngine) ID() string     { return "stub" }
func (stubEngine) Caveat() string { return "" }
func (stubEngine) Resolves() bool { return true }
func (stubEngine) Resolve(context.Context, string, codeLocation, string) symbolGraph {
	return symbolGraph{}
}
func (s stubEngine) ResolveEdges(_ context.Context, _ string, decl codeLocation, _ string) []codeLocation {
	// Fresh identities per level, so the walk is bounded by budget not dedupe.
	out := make([]codeLocation, len(s.edges))
	for i, e := range s.edges {
		e.Enclosing = fmt.Sprintf("%s_%s", decl.Enclosing, e.Enclosing)
		out[i] = e
	}
	return out
}

// cycleEngine returns A -> B -> A forever.
type cycleEngine struct{ a, b codeLocation }

func (cycleEngine) ID() string     { return "cycle" }
func (cycleEngine) Caveat() string { return "" }
func (cycleEngine) Resolves() bool { return true }
func (cycleEngine) Resolve(context.Context, string, codeLocation, string) symbolGraph {
	return symbolGraph{}
}
func (c cycleEngine) ResolveEdges(_ context.Context, _ string, decl codeLocation, _ string) []codeLocation {
	if decl.Enclosing == "A" {
		return []codeLocation{c.b}
	}
	return []codeLocation{c.a}
}

// Scoping is what keeps a trace about the user's code. Sibling repos under one
// workspace root are the user's code — scoping to the module instead would
// break the cross-repo tracing this tool is most useful for.
func TestCodeContext_ScopeKeepsWorkspaceExcludesDependencies(t *testing.T) {
	root := filepath.Join("/ws")
	for _, tc := range []struct {
		path string
		want bool
		why  string
	}{
		{filepath.Join(root, "reliant/internal/x.go"), true, "current repo"},
		{filepath.Join(root, "control-plane/internal/y.go"), true, "linked sibling repo"},
		{filepath.Join(root, "forge/pkg/z.go"), true, "linked sibling repo"},
		{"/usr/local/go/src/fmt/errors.go", false, "standard library"},
		{"/home/u/go/pkg/mod/github.com/x/y@v1/a.go", false, "module cache"},
		{filepath.Join(root, "web/node_modules/@types/react/index.d.ts"), false, "npm dependency inside the root"},
		{filepath.Join(root, "vendor/github.com/a/b.go"), false, "vendored dependency inside the root"},
		{filepath.Join(root, ".venv/lib/python3/site.py"), false, "python venv inside the root"},
	} {
		assert.Equal(t, tc.want, inScope(root, tc.path, scopeProject), tc.why)
	}
}

func TestCodeContext_ScopeAllIncludesEverything(t *testing.T) {
	assert.True(t, inScope("/ws", "/usr/local/go/src/fmt/errors.go", scopeAll))
	assert.True(t, inScope("/ws", "/ws/web/node_modules/react/index.js", scopeAll))
}

// Filtering must be visible: silently dropping edges is indistinguishable from
// a symbol that genuinely has none.
func TestCodeContext_OmittedDependencyEdgesAreReported(t *testing.T) {
	locs := []codeLocation{
		{Path: "/ws/internal/a.go", Line: 1},
		{Path: "/usr/local/go/src/fmt/errors.go", Line: 23},
		{Path: "/ws/web/node_modules/react/index.js", Line: 5},
	}

	kept, hidden := filterInScope("/ws", locs, scopeProject)

	require.Len(t, kept, 1)
	assert.Equal(t, 2, hidden)

	var sb strings.Builder
	writeScopeNote(&sb, scopeProject, hidden)
	assert.Contains(t, sb.String(), "2 edge(s)")
	assert.Contains(t, sb.String(), `scope="all"`, "the escape hatch must be named")
}

// The node budget is the scarce resource in a deep trace, so an out-of-scope
// edge must never consume a slot — otherwise stdlib crowds out the user's code.
func TestCodeContext_CallMapDoesNotSpendBudgetOnDependencies(t *testing.T) {
	engine := stubEngine{edges: []codeLocation{
		{Path: "/ws/internal/real.go", Line: 1, Enclosing: "Real"},
		{Path: "/usr/local/go/src/fmt/errors.go", Line: 23, Enclosing: "Errorf"},
		{Path: "/ws/vendor/x/y.go", Line: 9, Enclosing: "Vendored"},
	}}

	node, _ := buildCallMap(context.Background(), engine, "/ws",
		codeLocation{Path: "/ws/internal/root.go", Line: 1, Enclosing: "root"},
		"callers", 2, scopeProject)

	var sb strings.Builder
	writeCallMap(&sb, "/ws", node, "callers", 0)
	out := sb.String()
	assert.NotContains(t, out, "Errorf", "stdlib must not be traced:\n"+out)
	assert.NotContains(t, out, "Vendored", "vendored code must not be traced:\n"+out)
	assert.Contains(t, out, "Real", "the user's own code must still be traced:\n"+out)
}

// The text engine must refuse to build a map: text matches cannot distinguish a
// call from a mention, and a tree of mentions would be confidently wrong.
func TestCodeContext_TextEngineBuildsNoCallMap(t *testing.T) {
	edges := textEngine{}.ResolveEdges(context.Background(), ".", codeLocation{}, "callers")
	assert.Empty(t, edges)
}

// In Go a name is commonly declared twice — as an interface method and as the
// concrete implementation — and a language server reports callers only at the
// implementation. Picking the interface yields an empty call map, which reads
// as "nothing calls this" on a symbol with many callers.
func TestCodeContext_PrefersDeclarationThatResolves(t *testing.T) {
	requireRipgrep(t)
	requireGopls(t)
	dir := codeContextFixture(t)
	// Declared first alphabetically so path ranking alone would pick it.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "aaa_iface.go"),
		[]byte("package fixture\n\ntype Runner interface {\n\tTarget(n int) int\n}\n"), 0o644))

	resp := runCodeContext(t, dir, CodeContextParams{Symbol: "Target", Depth: 1})

	require.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, "CallerOne",
		"should resolve the implementation, not the interface method:\n"+resp.Content)
}

// Answering about one of many same-named declarations without saying so is how
// a reader ends up confidently reading about the wrong type.
func TestCodeContext_ReportsAmbiguousDeclarations(t *testing.T) {
	requireRipgrep(t)
	dir := codeContextFixture(t)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "other"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "other", "dup.go"),
		[]byte("package other\n\nfunc Target(n int) int {\n\treturn n\n}\n"), 0o644))

	resp := runCodeContext(t, dir, CodeContextParams{Symbol: "Target", Depth: 1})

	require.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, "other declaration(s)",
		"ambiguity must be disclosed:\n"+resp.Content)
	assert.Contains(t, resp.Content, "Pass `file`", "and the disambiguator named")
}

// The description must not promise resolution for languages that cannot get it.
func TestCodeContext_DescriptionNamesSupportedLanguages(t *testing.T) {
	desc := NewCodeContextTool().Description()
	assert.Contains(t, desc, "GO AND TYPESCRIPT/JAVASCRIPT ONLY")
	assert.Contains(t, desc, "rg", "unsupported languages must be pointed at the alternative")
}

// Regression: a design doc containing "Live(state, reason) -- PENDING or ACTIVE"
// outranked two real Go declarations of `Live`, and because the winner was a
// .md the whole query degraded to the text engine. That was an agent's first
// ever use of this tool, and it returned a worse answer than the grep it
// replaced.
func TestCodeContext_PrefersSourceOverProse(t *testing.T) {
	requireRipgrep(t)
	dir := codeContextFixture(t)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "specs"), 0o755))
	// Sorts before target.go and matches the declaration regex.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "specs", "aaa-design.md"),
		[]byte("# Design\n\nfunc Target(n int) int -- the entry point\n"), 0o644))

	resp := runCodeContext(t, dir, CodeContextParams{Symbol: "Target", Depth: 1})

	require.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, "target.go",
		"a .go declaration must outrank a .md mention:\n"+resp.Content)
	assert.NotContains(t, resp.Content, "ENGINE: ripgrep",
		"picking prose silently degrades the whole query:\n"+resp.Content)
}

// Ranking and engine selection must agree on what "resolvable" means. If they
// drift, a file the engine can resolve gets ranked as unresolvable (or worse,
// the reverse) and queries degrade for no visible reason.
func TestCodeContext_RankingMatchesEngines(t *testing.T) {
	for ext := range resolvableSourceExtensions {
		engine, _, err := selectEngine(t.TempDir(), "/x/y/thing"+ext)
		// Either the engine resolves, or the toolchain is missing and that is
		// reported as an actionable error. What must never happen is silently
		// falling through to the text engine, which is what ranking a source
		// file below prose used to cause.
		if err != nil {
			assert.Contains(t, err.Error(), "retry",
				"%s: a missing toolchain must be actionable, not a silent degrade", ext)
			continue
		}
		require.NotNil(t, engine, ext)
		assert.True(t, engine.Resolves(),
			"%s is ranked resolvable but selectEngine returned the text engine", ext)
	}
	// Prose must never be considered source.
	for ext := range proseExtensions {
		assert.False(t, resolvableSourceExtensions[ext],
			"%s cannot be both prose and resolvable source", ext)
	}
}

// The narrowest request must not be the worst answer: asking where a symbol is
// defined should still say who calls it, or the caller spends another turn.
func TestCodeContext_DefinitionStillIncludesGraph(t *testing.T) {
	requireRipgrep(t)
	requireGopls(t)
	dir := codeContextFixture(t)

	resp := runCodeContext(t, dir, CodeContextParams{Symbol: "Target", Want: "definition"})

	require.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, "CallerOne",
		"want=definition should still resolve the neighborhood:\n"+resp.Content)
}

func TestCodeContext_SectionsAreBounded(t *testing.T) {
	locs := make([]codeLocation, 60)
	for i := range locs {
		locs[i] = codeLocation{Path: "a.go", Line: i + 1}
	}
	var sb strings.Builder
	writeSection(&sb, ".", "CALLERS", locs, 25, "none")

	out := sb.String()
	assert.Contains(t, out, "CALLERS (60)", "total count must be reported")
	assert.Contains(t, out, "35 more not shown",
		"truncation must be explicit — a silently cut list reads as complete")
}

func TestCodeContext_RegisteredAsDefaultReadOnlyTool(t *testing.T) {
	assert.Contains(t, names.AllToolNames, names.ToolCodeContext)

	var found *ToolDefinition
	for i, def := range GetToolRegistry() {
		if def.Name == ToolCodeContext {
			found = &GetToolRegistry()[i]
			break
		}
	}
	require.NotNil(t, found, "code_context must be in the tool registry")
	assert.Contains(t, found.Tags, TagDefault, "must be available without discovery")
	assert.Contains(t, found.Tags, TagReadOnly)

	assert.True(t, IsToolReadOnly(NewCodeContextTool()))
}

// The tool only pays off if agents have it without discovering it first. A tag
// alone does not prove that — `tag:default` must actually EXPAND to include it.
func TestCodeContext_IncludedInDefaultToolset(t *testing.T) {
	expanded := ExpandToolFilter([]string{"tag:default"}, nil)
	assert.Contains(t, expanded, ToolCodeContext,
		"code_context must arrive without load_tool; got: %v", expanded)
}

// Presets that navigate code use explicit allowlists, which `tag:default` never
// reaches. Those are exactly the agents that spend turns on grep walks, so the
// tool has to be named in each one.
func TestCodeContext_ReachesCodeNavigationPresets(t *testing.T) {
	for _, preset := range []string{
		"researcher", "planner", "debug", "code_reviewer", "refactor", "tester",
		"general", "implementer",
	} {
		t.Run(preset, func(t *testing.T) {
			filter := presetToolFilter(t, preset)
			require.NotEmpty(t, filter, "preset declares no tools")
			assert.Contains(t, ExpandToolFilter(filter, nil), ToolCodeContext,
				"%s navigates code and must have code_context", preset)
		})
	}
}

// presetToolFilter reads a builtin preset's declared `tools:` list.
func presetToolFilter(t *testing.T, preset string) []string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "workflow", "builtin", "presets", preset+".yaml"))
	require.NoError(t, err)

	var filter []string
	inTools := false
	for _, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "tools:") {
			inTools = true
			continue
		}
		if !inTools {
			continue
		}
		if !strings.HasPrefix(trimmed, "-") {
			// A non-list line that is neither blank nor a comment ends the block.
			if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
				break
			}
			continue
		}
		item := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
		if i := strings.Index(item, "#"); i >= 0 {
			item = strings.TrimSpace(item[:i])
		}
		if item != "" {
			filter = append(filter, item)
		}
	}
	return filter
}

func TestCodeContext_NeedsNoPermission(t *testing.T) {
	// Requiring approval would push agents back to bash — the tool being replaced.
	ok, err := (&codeContextTool{}).RequiresPermission(CodeContextParams{Symbol: "X"})
	require.NoError(t, err)
	assert.False(t, ok)
}

func firstLineContaining(t *testing.T, body, substr string) string {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, substr) {
			return line
		}
	}
	t.Fatalf("no line containing %q in:\n%s", substr, body)
	return ""
}
