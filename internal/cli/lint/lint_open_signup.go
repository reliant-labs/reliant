// File: internal/cli/lint/lint_open_signup.go
//
// forgeconv-open-signup — a login broker that mounts its public sign-up route
// with no condition around it.
//
// The scaffolded internal/app/login_broker.go is the project's own file, so a
// fix to its template never reaches a project scaffolded before it. Brokers
// scaffolded before v1.0.16 mount POST /auth/register in EVERY environment,
// and outside development the account it creates is signed in with an
// address nobody verified. An app that binds invitations or memberships by
// email is then open to takeover by whoever registers the address first.
//
// Since v1.0.16 the broker mounts the route behind `if broker.selfSignup`
// (development, or the env's auth_self_signup). This lane finds the mount and
// warns when no `if` encloses it. A project whose deployed app is MEANT to be
// open still gets the warning until the mount names its switch — which is the
// point: an unconditional mount cannot be turned off per environment.
//
// ADVISORY: a warning, never a gate by default. lint.rules can raise it.
package lint

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ruleOpenSignup is the rule ID this lane emits.
const ruleOpenSignup = "forgeconv-open-signup"

// openSignupRoute is the sign-up route the scaffolded frontend posts to.
const openSignupRoute = "/auth/register"

// openSignupFinding is one unconditional sign-up mount.
type openSignupFinding struct {
	Path string // slash-separated, relative to the project root
	Line int
}

// findOpenSignupMounts reports every mount of the sign-up route under
// <root>/internal that no if statement encloses. A mount is a Handle or
// HandleFunc call whose pattern is the RegisterPath constant or a string
// literal naming /auth/register.
func findOpenSignupMounts(root string) ([]openSignupFinding, error) {
	var out []openSignupFinding
	dir := filepath.Join(root, "internal")
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if path != dir && (dotenvSkipDirs[d.Name()] || d.Name() == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil || (!strings.Contains(string(src), "RegisterPath") && !strings.Contains(string(src), openSignupRoute)) {
			return nil
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
		if perr != nil {
			return nil // a file that does not parse is the compiler's to report
		}
		rel, _ := filepath.Rel(root, path)
		for _, line := range unconditionalSignupMounts(fset, f) {
			out = append(out, openSignupFinding{Path: filepath.ToSlash(rel), Line: line})
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Line < out[j].Line
	})
	return out, nil
}

// unconditionalSignupMounts returns the line of every sign-up mount in f
// that is not inside the body or else branch of an if statement.
func unconditionalSignupMounts(fset *token.FileSet, f *ast.File) []int {
	var lines []int
	var stack []ast.Node
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)
		call, ok := n.(*ast.CallExpr)
		if !ok || !isSignupMount(call) {
			return true
		}
		for _, anc := range stack[:len(stack)-1] {
			if _, ok := anc.(*ast.IfStmt); ok {
				return true
			}
		}
		lines = append(lines, fset.Position(call.Pos()).Line)
		return true
	})
	return lines
}

// isSignupMount reports whether call is x.Handle / x.HandleFunc with the
// sign-up route as its pattern.
func isSignupMount(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || (sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc") || len(call.Args) == 0 {
		return false
	}
	switch arg := call.Args[0].(type) {
	case *ast.Ident:
		return arg.Name == "RegisterPath"
	case *ast.BasicLit:
		if arg.Kind != token.STRING {
			return false
		}
		pattern, err := strconv.Unquote(arg.Value)
		if err != nil {
			return false
		}
		// "POST /auth/register", "/auth/register", or a host-qualified form.
		fields := strings.Fields(pattern)
		return len(fields) > 0 && strings.HasSuffix(fields[len(fields)-1], openSignupRoute)
	}
	return false
}

func collectOpenSignupJSON(root string) ([]lintJSONFinding, error) {
	if root == "" {
		return nil, nil
	}
	found, err := findOpenSignupMounts(root)
	if err != nil {
		return nil, fmt.Errorf("scan for sign-up mounts: %w", err)
	}
	out := make([]lintJSONFinding, 0, len(found))
	for _, f := range found {
		out = append(out, lintJSONFinding{
			Rule:     ruleOpenSignup,
			Severity: "warning",
			File:     f.Path,
			Line:     f.Line,
			Message: "public sign-up (" + openSignupRoute + ") is mounted in every environment: anyone who reaches a " +
				"deployed app can create an account under an address nobody verified and is signed straight in",
			FixHint: "mount it behind a switch that is off outside development — `if broker.selfSignup { mux.Handle(RegisterPath, ...) }` " +
				"with selfSignup: cfg.Environment == \"development\" || cfg.AuthSelfSignup — and have every bind-by-email " +
				"check devidp.User.EmailVerified; `forge skill load migrations/v1.0.16` has the exact edit",
		})
	}
	return out, nil
}
