// Codemod: rewrite hand-rolled `tests := []struct{name; call}` Connect-RPC
// test scaffolds into per-RPC `tdd.RunRPCCases[Req, Resp]` functions.
//
// The hand-rolled shape ports were producing during early-migration handler
// scaffolding (see FORGE_BACKLOG.md "tdd.RunRPCCases migration codemod missing").
// Each `TestHandlers` function declares an inline slice of {name, call} rows
// that each invoke a single RPC and check the err shape uniformly. The pattern
// is mechanical-but-tedious to convert by hand, so this codemod does it.
//
// Two input shapes are supported, distinguished by the `call` field type:
//
//   1. service-receiver: `call func() error` — body invokes `svc.Method(ctx,
//      connect.NewRequest(&pb.XxxRequest{}))`. Lifted into per-RPC test
//      functions that pass `svc.Method` straight to `tdd.RunRPCCases`.
//
//   2. client-receiver: `call func(client X) error` — body invokes
//      `c.Method(ctx, connect.NewRequest(&pb.XxxRequest{}))`. Lifted the same
//      way, swapping `client.Method` for the handler.
//
// The codemod is intentionally conservative: it only transforms files whose
// shape exactly matches one of the two recognised forms. Anything else is
// skipped with a printed reason — never a partial / corrupting rewrite.

package cli

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// migrateTDDFlags holds the flag values for `forge project migrate tdd`.
type migrateTDDFlags struct {
	path   string
	dryRun bool
}

// newMigrateTDDCmd is `forge project migrate tdd`. It lives under the
// `project migrate` noun — beside `import`, the goose/dbmate converter —
// because it is a CODEMOD that rewrites files in the working tree, not a way
// to run tests. It sat under the removed `forge test` only by accident of
// sharing the word "test" with it, which is also why deleting that command
// must not take it along.
func newMigrateTDDCmd() *cobra.Command {
	var flags migrateTDDFlags

	cmd := &cobra.Command{
		Use:   "tdd",
		Short: "Rewrite hand-rolled handler tests to use tdd.RunRPCCases",
		Long: `Codemod hand-rolled tests := []struct{name, call}{...} Connect-RPC
test scaffolds into per-RPC TestXxx_Generated functions that delegate to
forge/pkg/tdd.RunRPCCases.

Walks every *_test.go file under handlers/<svc>/ in the project root (or
--path) and transforms files that match the recognised hand-rolled shape.
Files that don't match are skipped with a clear reason and never partially
rewritten.

Examples:
  forge project migrate tdd                  # Apply codemod under handlers/
  forge project migrate tdd --dry-run        # Show summary without writing
  forge project migrate tdd --path some/dir  # Walk a specific subtree`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMigrateTDD(flags)
		},
	}

	cmd.Flags().StringVar(&flags.path, "path", "", "Project root or subtree to walk (default: cwd)")
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false, "Print actions without writing files")

	return cmd
}

func runMigrateTDD(flags migrateTDDFlags) error {
	root := flags.path
	if root == "" {
		var err error
		root, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("getwd: %w", err)
		}
	}

	handlersDir := filepath.Join(root, "internal", "handlers")
	if !dirExists(handlersDir) {
		// Fall back to walking root itself; lets the codemod work from
		// inside a single service dir or any layout that lacks internal/handlers/.
		handlersDir = root
	}

	var files []string
	err := filepath.Walk(handlersDir, func(p string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(p, "_test.go") {
			return nil
		}
		files = append(files, p)
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk %s: %w", handlersDir, err)
	}
	sort.Strings(files)

	var (
		transformed int
		skipped     int
		failed      int
	)

	for _, f := range files {
		res, err := migrateTDDFile(f, flags.dryRun)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[migrate-tdd] %s: ERROR %v\n", relPath(root, f), err)
			failed++
			continue
		}
		switch res.Status {
		case migrateStatusTransformed:
			transformed++
			action := "rewrote"
			if flags.dryRun {
				action = "would rewrite"
			}
			fmt.Printf("[migrate-tdd] %s: %s — %d RPC case(s) lifted from %s\n",
				relPath(root, f), action, len(res.Rows), res.OriginFunc)
		case migrateStatusSkippedNoMatch:
			skipped++
			// Suppress the noise for files that obviously aren't candidates.
			// Only mention skips for files that LOOK like they might match
			// (i.e. mention `tests := []struct` somewhere).
			if res.Reason != "" && res.LooksLikeCandidate {
				fmt.Printf("[migrate-tdd] %s: skipped — %s\n", relPath(root, f), res.Reason)
			}
		}
	}

	fmt.Println()
	fmt.Printf("[migrate-tdd] Summary: %d transformed, %d skipped, %d errors\n",
		transformed, skipped, failed)

	if failed > 0 {
		return fmt.Errorf("%d file(s) failed", failed)
	}
	return nil
}

func relPath(root, p string) string {
	if rp, err := filepath.Rel(root, p); err == nil {
		return rp
	}
	return p
}

// migrateStatus describes the outcome of attempting to rewrite a single file.
type migrateStatus int

const (
	migrateStatusTransformed migrateStatus = iota
	migrateStatusSkippedNoMatch
)

// migrateResult is the outcome of a single-file codemod attempt.
type migrateResult struct {
	Status             migrateStatus
	Reason             string
	Rows               []rpcRow
	OriginFunc         string // name of the source TestHandlers/TestIntegration func
	LooksLikeCandidate bool   // true if file mentions the hand-rolled shape
}

// rpcRow is a single extracted RPC case row from the hand-rolled slice.
type rpcRow struct {
	Name         string // value of the `name:` field, e.g. "GetCurrentUser"
	Method       string // RPC method name, e.g. "GetCurrentUser"
	RequestType  string // request type name without package prefix, e.g. "GetCurrentUserRequest"
	ResponseType string // response type name, derived from RequestType (Request → Response)
	PbAlias      string // package alias used for proto types (usually "pb")
}

// migrateTDDFile rewrites a single test file in-place if it matches the
// hand-rolled shape. Returns migrateStatusSkippedNoMatch (with a Reason) if
// the file does not match and was left untouched.
func migrateTDDFile(path string, dryRun bool) (migrateResult, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return migrateResult{}, err
	}

	looksLikeCandidate := bytes.Contains(src, []byte("tests := []struct"))

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return migrateResult{}, fmt.Errorf("parse: %w", err)
	}

	// Find the candidate function. We accept the first function whose body
	// contains the recognised hand-rolled shape.
	plan, planErr := planMigration(file)
	if planErr != nil {
		return migrateResult{
			Status:             migrateStatusSkippedNoMatch,
			Reason:             planErr.Error(),
			LooksLikeCandidate: looksLikeCandidate,
		}, nil
	}

	// Render new file content.
	newSrc, err := renderMigratedFile(file, src, plan)
	if err != nil {
		return migrateResult{}, fmt.Errorf("render: %w", err)
	}

	formatted, fmtErr := format.Source(newSrc)
	if fmtErr != nil {
		// Save the unformatted version under .bad so the user can inspect.
		// Don't overwrite the original — that would be the worst kind of
		// silent corruption.
		_ = atomicfile.WriteFile(path+".migrate-tdd.bad", newSrc, 0o644)
		return migrateResult{}, fmt.Errorf("gofmt failed (saved as %s.migrate-tdd.bad): %w", path, fmtErr)
	}

	if !dryRun {
		if err := atomicfile.WriteFile(path, formatted, 0o644); err != nil {
			return migrateResult{}, fmt.Errorf("write: %w", err)
		}
	}

	return migrateResult{
		Status:             migrateStatusTransformed,
		Rows:               plan.Rows,
		OriginFunc:         plan.OriginFunc,
		LooksLikeCandidate: true,
	}, nil
}

// migrationPlan is the extracted shape of a hand-rolled handler test, ready
// for re-emission as per-RPC `RunRPCCases` functions.
type migrationPlan struct {
	OriginFunc       string // name of the source func, e.g. "TestHandlers"
	IsClientVariant  bool   // true if `call func(client X) error` shape
	ReceiverVarName  string // "svc" or "client" — the variable feeding the handler
	ConstructorCall  string // verbatim "app.NewTestUser(t)" / "_, client := app.NewTestUserServer(t)"
	ConstructorIsTwo bool   // ConstructorCall has the (_, client) := tuple form
	Rows             []rpcRow
	PbImportPath     string // import path under pb alias, e.g. ".../gen/services/user/v1"
	PbAlias          string // alias used (typically "pb")
	BuildTagComment  string // verbatim build tag comment block (for integration)
	OriginFuncDecl   *ast.FuncDecl
}

func planMigration(file *ast.File) (migrationPlan, error) {
	plan := migrationPlan{}

	// Find the proto-pb import alias. Many files alias the gen pkg as "pb".
	for _, imp := range file.Imports {
		if imp.Name != nil && imp.Path != nil {
			if imp.Name.Name == "pb" {
				plan.PbAlias = "pb"
				plan.PbImportPath = strings.Trim(imp.Path.Value, `"`)
				break
			}
		}
	}
	if plan.PbAlias == "" {
		// Fallback: first import that looks like *.gen.services.<svc>.v1.
		for _, imp := range file.Imports {
			if imp.Path != nil && strings.Contains(imp.Path.Value, "/gen/services/") &&
				strings.HasSuffix(strings.Trim(imp.Path.Value, `"`), "/v1") {
				if imp.Name != nil {
					plan.PbAlias = imp.Name.Name
				} else {
					plan.PbAlias = "v1"
				}
				plan.PbImportPath = strings.Trim(imp.Path.Value, `"`)
				break
			}
		}
	}

	// Walk top-level decls looking for the candidate function.
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if !isLikelyHandlerTest(fn) {
			continue
		}
		if err := extractFromFuncBody(fn, &plan); err != nil {
			// Recognised name but body didn't match — surface why.
			return migrationPlan{}, fmt.Errorf("function %s: %w", fn.Name.Name, err)
		}
		plan.OriginFunc = fn.Name.Name
		plan.OriginFuncDecl = fn
		break
	}

	if plan.OriginFunc == "" {
		return migrationPlan{}, fmt.Errorf("no recognised TestHandlers/TestIntegration body found")
	}
	if len(plan.Rows) == 0 {
		return migrationPlan{}, fmt.Errorf("matched function %s but extracted zero RPC rows", plan.OriginFunc)
	}

	return plan, nil
}

// isLikelyHandlerTest is a fast pre-filter that gates the more expensive
// body inspection. Functions named `TestHandlers` or `TestIntegration` are
// always candidates; other Test* functions are inspected only if they
// contain a top-level `tests := []struct` slice.
func isLikelyHandlerTest(fn *ast.FuncDecl) bool {
	if fn.Body == nil {
		return false
	}
	if !strings.HasPrefix(fn.Name.Name, "Test") {
		return false
	}
	if fn.Name.Name == "TestHandlers" || fn.Name.Name == "TestIntegration" {
		return true
	}
	for _, stmt := range fn.Body.List {
		assign, ok := stmt.(*ast.AssignStmt)
		if !ok {
			continue
		}
		for _, lhs := range assign.Lhs {
			if id, ok := lhs.(*ast.Ident); ok && id.Name == "tests" {
				return true
			}
		}
	}
	return false
}

// extractFromFuncBody walks the body of a candidate function and fills in
// the plan's Rows / receiver / constructor info. Returns a non-nil error
// describing exactly what part of the expected shape was missing.
func extractFromFuncBody(fn *ast.FuncDecl, plan *migrationPlan) error {
	var (
		foundTestsSlice bool
		foundForLoop    bool
	)

	for _, stmt := range fn.Body.List {
		switch s := stmt.(type) {
		case *ast.AssignStmt:
			// Look for: <receiver> := app.NewTestX(t)  OR  _, client := app.NewTestXServer(t)
			if isShortAssign(s) {
				if rec, ctor, two, ok := matchConstructor(s); ok {
					plan.ReceiverVarName = rec
					plan.ConstructorCall = ctor
					plan.ConstructorIsTwo = two
					continue
				}
				if isTestsSliceAssign(s) {
					rows, isClient, err := extractRows(s, plan)
					if err != nil {
						return err
					}
					plan.Rows = rows
					plan.IsClientVariant = isClient
					foundTestsSlice = true
					continue
				}
			}
		case *ast.RangeStmt:
			if isTestsRangeLoop(s) {
				foundForLoop = true
			}
		}
	}

	if !foundTestsSlice {
		return fmt.Errorf("no `tests := []struct{...}{...}` assignment found")
	}
	if !foundForLoop {
		return fmt.Errorf("no `for _, tt := range tests` loop found")
	}
	if plan.ReceiverVarName == "" {
		return fmt.Errorf("no `app.NewTestX(t)` constructor found")
	}
	return nil
}

func isShortAssign(s *ast.AssignStmt) bool { return s.Tok == token.DEFINE }

// matchConstructor recognises the two constructor shapes used by
// control-plane-next handler tests:
//
//	svc := app.NewTestUser(t)
//	_, client := app.NewTestUserServer(t)
//
// Returns receiver var name, the verbatim constructor source line, whether
// the "two-value" form was used, and ok.
func matchConstructor(s *ast.AssignStmt) (string, string, bool, bool) {
	if len(s.Rhs) != 1 {
		return "", "", false, false
	}
	call, ok := s.Rhs[0].(*ast.CallExpr)
	if !ok {
		return "", "", false, false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", "", false, false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "app" {
		return "", "", false, false
	}
	if !strings.HasPrefix(sel.Sel.Name, "NewTest") {
		return "", "", false, false
	}

	// 1-value form: `<receiver> := app.NewTestX(t)`
	if len(s.Lhs) == 1 {
		id, ok := s.Lhs[0].(*ast.Ident)
		if !ok {
			return "", "", false, false
		}
		return id.Name, exprText(s.Rhs[0]), false, true
	}

	// 2-value form: `_, client := app.NewTestXServer(t)`
	if len(s.Lhs) == 2 {
		recID, ok := s.Lhs[1].(*ast.Ident)
		if !ok {
			return "", "", false, false
		}
		return recID.Name, exprText(s.Rhs[0]), true, true
	}

	return "", "", false, false
}

func isTestsSliceAssign(s *ast.AssignStmt) bool {
	if len(s.Lhs) != 1 || len(s.Rhs) != 1 {
		return false
	}
	id, ok := s.Lhs[0].(*ast.Ident)
	if !ok || id.Name != "tests" {
		return false
	}
	cl, ok := s.Rhs[0].(*ast.CompositeLit)
	if !ok {
		return false
	}
	at, ok := cl.Type.(*ast.ArrayType)
	if !ok {
		return false
	}
	_, ok = at.Elt.(*ast.StructType)
	return ok
}

func isTestsRangeLoop(s *ast.RangeStmt) bool {
	id, ok := s.X.(*ast.Ident)
	return ok && id.Name == "tests"
}

// extractRows parses each {name: ..., call: func() error { ... }} composite
// literal into an rpcRow. Returns (rows, isClientVariant, err).
func extractRows(assign *ast.AssignStmt, plan *migrationPlan) ([]rpcRow, bool, error) {
	cl := assign.Rhs[0].(*ast.CompositeLit)
	at := cl.Type.(*ast.ArrayType)
	st := at.Elt.(*ast.StructType)

	// Determine variant by the struct field type for `call`.
	isClient := false
	for _, field := range st.Fields.List {
		if len(field.Names) == 0 {
			continue
		}
		if field.Names[0].Name != "call" {
			continue
		}
		ft, ok := field.Type.(*ast.FuncType)
		if !ok {
			return nil, false, fmt.Errorf("`call` field is not a func type")
		}
		if ft.Params != nil && len(ft.Params.List) > 0 {
			isClient = true
		}
	}

	var rows []rpcRow
	for i, elt := range cl.Elts {
		row, err := extractRow(elt, plan, isClient)
		if err != nil {
			return nil, false, fmt.Errorf("row %d: %w", i, err)
		}
		rows = append(rows, row)
	}
	return rows, isClient, nil
}

func extractRow(elt ast.Expr, plan *migrationPlan, isClient bool) (rpcRow, error) {
	rowLit, ok := elt.(*ast.CompositeLit)
	if !ok {
		return rpcRow{}, fmt.Errorf("not a composite literal")
	}

	var (
		name   string
		callFn *ast.FuncLit
	)
	for _, kv := range rowLit.Elts {
		kvExpr, ok := kv.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kvExpr.Key.(*ast.Ident)
		if !ok {
			continue
		}
		switch key.Name {
		case "name":
			lit, ok := kvExpr.Value.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return rpcRow{}, fmt.Errorf("`name` must be a string literal")
			}
			name = strings.Trim(lit.Value, `"`)
		case "call":
			fn, ok := kvExpr.Value.(*ast.FuncLit)
			if !ok {
				return rpcRow{}, fmt.Errorf("`call` must be a func literal")
			}
			callFn = fn
		}
	}
	if name == "" || callFn == nil {
		return rpcRow{}, fmt.Errorf("missing name or call field")
	}

	// Body shape: `_, err := <recv>.<Method>(ctx, connect.NewRequest(&pb.<Req>{}))`
	// followed by `return err`. The receiver may be `svc`, the loop var (`c`),
	// or anything else — we read the literal selector.
	method, reqType, err := extractRPCCall(callFn, plan, isClient)
	if err != nil {
		return rpcRow{}, fmt.Errorf("body: %w", err)
	}

	row := rpcRow{
		Name:         name,
		Method:       method,
		RequestType:  reqType,
		ResponseType: deriveResponseType(reqType),
		PbAlias:      plan.PbAlias,
	}
	return row, nil
}

// extractRPCCall walks the body of `call: func(...) error { ... }` and
// pulls out (method name, request type name without pb. prefix).
func extractRPCCall(fn *ast.FuncLit, plan *migrationPlan, _ bool) (string, string, error) {
	var rpcCall *ast.CallExpr

	for _, stmt := range fn.Body.List {
		assign, ok := stmt.(*ast.AssignStmt)
		if !ok {
			continue
		}
		if len(assign.Rhs) != 1 {
			continue
		}
		call, ok := assign.Rhs[0].(*ast.CallExpr)
		if !ok {
			continue
		}
		// Selector .Method on the receiver.
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			continue
		}
		// Two args: (ctx, connect.NewRequest(&pb.X{}))
		if len(call.Args) != 2 {
			continue
		}
		// Confirm the second arg is connect.NewRequest(&pb.X{}).
		if _, _, ok := matchConnectNewRequest(call.Args[1]); !ok {
			continue
		}
		_ = sel
		rpcCall = call
		break
	}
	if rpcCall == nil {
		return "", "", fmt.Errorf("no recognised RPC call shape")
	}

	sel := rpcCall.Fun.(*ast.SelectorExpr)
	method := sel.Sel.Name
	pbAlias, reqType, _ := matchConnectNewRequest(rpcCall.Args[1])

	if pbAlias != "" && plan.PbAlias != "" && pbAlias != plan.PbAlias {
		return "", "", fmt.Errorf("request uses pb alias %q but file imports %q", pbAlias, plan.PbAlias)
	}
	if plan.PbAlias == "" && pbAlias != "" {
		plan.PbAlias = pbAlias
	}

	return method, reqType, nil
}

// matchConnectNewRequest returns (pbAlias, reqType, ok) if the expression is
// `connect.NewRequest(&<alias>.<RequestType>{})`.
func matchConnectNewRequest(expr ast.Expr) (string, string, bool) {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return "", "", false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", "", false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "connect" || sel.Sel.Name != "NewRequest" {
		return "", "", false
	}
	if len(call.Args) != 1 {
		return "", "", false
	}
	ua, ok := call.Args[0].(*ast.UnaryExpr)
	if !ok || ua.Op != token.AND {
		return "", "", false
	}
	cl, ok := ua.X.(*ast.CompositeLit)
	if !ok {
		return "", "", false
	}
	tsel, ok := cl.Type.(*ast.SelectorExpr)
	if !ok {
		return "", "", false
	}
	tpkg, ok := tsel.X.(*ast.Ident)
	if !ok {
		return "", "", false
	}
	return tpkg.Name, tsel.Sel.Name, true
}

func deriveResponseType(reqType string) string {
	if strings.HasSuffix(reqType, "Request") {
		return strings.TrimSuffix(reqType, "Request") + "Response"
	}
	// Best-effort fallback: append Response (will fail to compile and be
	// loud about it, which is what we want).
	return reqType + "Response"
}

// exprText returns the source text of an expression by re-printing the AST.
// Used to capture the verbatim constructor call.
func exprText(e ast.Expr) string {
	var buf bytes.Buffer
	if err := format.Node(&buf, token.NewFileSet(), e); err != nil {
		return ""
	}
	return buf.String()
}

// renderMigratedFile rewrites the file source: strips the original
// TestHandlers/TestIntegration function and emits per-RPC TestXxx_Generated
// functions in its place. Imports are rewritten to add `forge/pkg/tdd` and
// drop unused packages.
func renderMigratedFile(_ *ast.File, src []byte, plan migrationPlan) ([]byte, error) {
	// Strategy: textual splice. We locate the byte range of the origin
	// function (including its leading doc-comment) and replace it with the
	// rendered per-RPC functions. Sibling test funcs stay verbatim.
	// Then we patch the import block.

	fset := token.NewFileSet()
	// Re-parse with positions tied to src so we get byte offsets.
	reparsed, err := parser.ParseFile(fset, "in.go", src, parser.ParseComments)
	if err != nil {
		return nil, err
	}

	var (
		startPos = plan.OriginFuncDecl.Pos()
		endPos   = plan.OriginFuncDecl.End()
	)
	// Pull in the leading doc comment if present.
	for _, cg := range reparsed.Comments {
		if cg.End() < startPos && cg.End()+1 >= startPos {
			startPos = cg.Pos()
		}
	}
	// Re-find the function in the reparsed AST so the positions match the
	// offsets we'll splice into.
	for _, d := range reparsed.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fn.Name.Name == plan.OriginFunc {
			startPos = fn.Pos()
			endPos = fn.End()
			if fn.Doc != nil && fn.Doc.Pos() < startPos {
				startPos = fn.Doc.Pos()
			}
			break
		}
	}

	startOffset := fset.Position(startPos).Offset
	endOffset := fset.Position(endPos).Offset

	// Generate the replacement text.
	replacement := generateReplacementFuncs(plan)

	var out bytes.Buffer
	out.Write(src[:startOffset])
	out.WriteString(replacement)
	// Skip any trailing newlines that immediately followed the original
	// function so we don't leave a giant gap.
	tail := src[endOffset:]
	for len(tail) > 0 && (tail[0] == '\n' || tail[0] == '\r') {
		tail = tail[1:]
	}
	out.WriteString("\n\n")
	out.Write(tail)

	// Patch imports.
	patched, err := patchImports(out.Bytes(), plan)
	if err != nil {
		return nil, err
	}
	return patched, nil
}

// generateReplacementFuncs renders one TestXxx_Generated function per RPC row.
// Each function constructs a fresh service/client (matching the original
// constructor) and delegates to tdd.RunRPCCases.
func generateReplacementFuncs(plan migrationPlan) string {
	var b strings.Builder

	for i, row := range plan.Rows {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "// Test%s_Generated — self-destructing scaffold row: it passes only while\n// the handler is the generated CodeUnimplemented stub. If %s is already\n// implemented this row FAILS — replace it with real WantErr/Check assertions.\n", row.Method, row.Method)
		fmt.Fprintf(&b, "func Test%s_Generated(t *testing.T) {\n", row.Method)
		b.WriteString("\tt.Parallel()\n\n")
		if plan.ConstructorIsTwo {
			fmt.Fprintf(&b, "\t_, %s := %s\n\n", plan.ReceiverVarName, plan.ConstructorCall)
		} else {
			fmt.Fprintf(&b, "\t%s := %s\n\n", plan.ReceiverVarName, plan.ConstructorCall)
		}
		fmt.Fprintf(&b, "\ttdd.RunRPCCases(t, []tdd.RPCCase[%s.%s, %s.%s]{\n",
			row.PbAlias, row.RequestType, row.PbAlias, row.ResponseType)
		// Emit the self-destructing scaffold row (WantErr:
		// CodeUnimplemented). The hand-rolled shape this codemod replaces
		// tolerated any outcome — pkg/tdd deliberately has no such mode
		// (a row that cannot fail teaches green-means-nothing), so the
		// strictest stub-compatible assertion is emitted instead. For
		// already-implemented handlers the row fails immediately, telling
		// the user to write the real assertion the old test never had.
		fmt.Fprintf(&b, "\t\t{Name: %q, Req: connect.NewRequest(&%s.%s{}), WantErr: connect.CodeUnimplemented},\n",
			"unimplemented_scaffold", row.PbAlias, row.RequestType)
		fmt.Fprintf(&b, "\t}, %s.%s)\n", plan.ReceiverVarName, row.Method)
		b.WriteString("}")
	}

	return b.String()
}

// patchImports removes "context" (no longer needed) and adds forge/pkg/tdd.
// Other imports are left as-is.
func patchImports(src []byte, _ migrationPlan) ([]byte, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "in.go", src, parser.ParseComments)
	if err != nil {
		return nil, err
	}

	// Determine whether `context` is still referenced anywhere in the new file.
	contextStillUsed := identStillReferenced(file, "context")

	// Locate the import block.
	var importDecl *ast.GenDecl
	for _, d := range file.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.IMPORT {
			continue
		}
		importDecl = gd
		break
	}
	if importDecl == nil {
		return src, nil
	}

	// Build a new sorted list of import paths, with desired changes.
	type impEntry struct {
		Name string // alias, may be empty
		Path string // unquoted path
	}

	var entries []impEntry
	for _, sp := range importDecl.Specs {
		is := sp.(*ast.ImportSpec)
		path := strings.Trim(is.Path.Value, `"`)
		alias := ""
		if is.Name != nil {
			alias = is.Name.Name
		}
		// Drop "context" if no longer referenced.
		if path == "context" && !contextStillUsed {
			continue
		}
		// Drop dot-imports / blank-imports as-is; they have side effects we
		// can't reason about textually.
		if alias == "_" || alias == "." {
			entries = append(entries, impEntry{Name: alias, Path: path})
			continue
		}
		// Drop any import whose package alias is no longer referenced in
		// the post-rewrite file. The local name is `alias` if set,
		// otherwise the import-path basename. We compute the local name
		// the same way the Go compiler does.
		localName := alias
		if localName == "" {
			localName = importLocalName(path)
		}
		if !identStillReferenced(file, localName) {
			continue
		}
		entries = append(entries, impEntry{Name: alias, Path: path})
	}

	// Add forge/pkg/tdd if not already present.
	hasTDD := false
	for _, e := range entries {
		if e.Path == "github.com/reliant-labs/forge/pkg/tdd" {
			hasTDD = true
			break
		}
	}
	if !hasTDD {
		entries = append(entries, impEntry{Path: "github.com/reliant-labs/forge/pkg/tdd"})
	}

	// Sort the entries: stdlib first, then third-party, then internal —
	// goimports-style. We approximate by:
	//   - no slash → stdlib
	//   - first segment contains '.' → third-party
	stdlib := func(p string) bool { return !strings.Contains(strings.SplitN(p, "/", 2)[0], ".") }

	sort.Slice(entries, func(i, j int) bool {
		ai, aj := stdlib(entries[i].Path), stdlib(entries[j].Path)
		if ai != aj {
			return ai
		}
		return entries[i].Path < entries[j].Path
	})

	// Render new import block.
	var ib strings.Builder
	ib.WriteString("import (\n")
	prevWasStd := true
	first := true
	for _, e := range entries {
		isStd := stdlib(e.Path)
		if !first && prevWasStd && !isStd {
			ib.WriteString("\n")
		}
		ib.WriteString("\t")
		if e.Name != "" {
			ib.WriteString(e.Name + " ")
		}
		ib.WriteString(`"` + e.Path + `"`)
		ib.WriteString("\n")
		prevWasStd = isStd
		first = false
	}
	ib.WriteString(")")

	// Splice the import block back into the source by byte offsets.
	startOffset := fset.Position(importDecl.Pos()).Offset
	endOffset := fset.Position(importDecl.End()).Offset

	var out bytes.Buffer
	out.Write(src[:startOffset])
	out.WriteString(ib.String())
	out.Write(src[endOffset:])
	return out.Bytes(), nil
}

// importLocalName mirrors the Go-compiler convention: the local name of an
// unaliased import path is the last `/`-separated segment, with leading
// digits trimmed. We use a simple form (no digit trimming) which is enough
// for the import paths produced by buf/connect codegen.
func importLocalName(path string) string {
	parts := strings.Split(path, "/")
	return parts[len(parts)-1]
}

// identStillReferenced returns true if any `<name>.X` selector appears in
// the file (excluding the import declarations themselves). Selector usage is
// the only way a Go file references an imported package, so a bare-ident
// search would over-report (e.g. parameter named after a package).
func identStillReferenced(file *ast.File, name string) bool {
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		if found {
			return false
		}
		if _, ok := n.(*ast.ImportSpec); ok {
			return false
		}
		if x, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := x.X.(*ast.Ident); ok && id.Name == name {
				found = true
				return false
			}
		}
		return true
	})
	return found
}
