package templates

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// A project whose golangci config enables gosec (reliant's does) got a red
// lint from a file it never wrote: the scaffolded login_broker.go failed G101
// (PasswordPolicyPath, a string constant whose name says "password") and
// G124 (the session cookie's Secure is b.secureCookie, off in local
// development). Neither is a defect, so each carries a //nolint:gosec with its
// reason, the idiom forge's own code uses.
//
// This is the unit-tier tripwire, over every broker variant: it applies
// gosec's two rules to the rendered source (G101's default name pattern over
// string-literal constants and variables; G124 to every http.Cookie literal
// that is not Secure: true, HttpOnly: true and SameSite Lax or Strict) and
// requires an annotation naming the rule on each finding.
func TestLoginBrokerScaffoldPassesGosec(t *testing.T) {
	// gosec G101's default name pattern.
	credentialName := regexp.MustCompile(`(?i)passwd|pass|password|pwd|secret|token|pw|apiKey|bearer|cred`)

	for _, data := range []map[string]any{
		brokerData(nil),
		brokerData(map[string]any{"LoginPolicyField": true, "AudienceField": true, "TrustedProxyHopsField": true, "ScopesField": true}),
		brokerData(map[string]any{"MiddlewareDeclaresSessionCookie": false}),
		brokerData(map[string]any{"MiddlewareDeclaresSecureSessionCookie": true, "SelfSignupField": false}),
	} {
		src := renderBroker(t, data)
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "login_broker.go", src, parser.ParseComments)
		if err != nil {
			t.Fatalf("the rendered broker does not parse: %v", err)
		}
		lines := strings.Split(src, "\n")
		line := func(n int) string {
			if n < 1 || n > len(lines) {
				return ""
			}
			return lines[n-1]
		}
		suppressed := func(rule string, n int) bool {
			return strings.Contains(line(n), "//nolint:gosec // "+rule+":") ||
				strings.Contains(strings.TrimSpace(line(n-1)), "//nolint:gosec // "+rule+":")
		}

		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.ValueSpec:
				for i, name := range n.Names {
					if i >= len(n.Values) || !credentialName.MatchString(name.Name) {
						continue
					}
					if lit, ok := n.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						if at := fset.Position(name.Pos()).Line; !suppressed("G101", at) {
							t.Errorf("line %d: %s = %s fails gosec G101 and carries no //nolint:gosec // G101: <why>", at, name.Name, lit.Value)
						}
					}
				}
			case *ast.CompositeLit:
				sel, ok := n.Type.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Cookie" {
					return true
				}
				if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "http" {
					return true
				}
				if hardenedCookie(n) {
					return true
				}
				if at := fset.Position(n.Pos()).Line; !suppressed("G124", at) {
					t.Errorf("line %d: an http.Cookie that is not Secure, HttpOnly and SameSite Lax/Strict fails gosec G124 and carries no //nolint:gosec // G124: <why>", at)
				}
			}
			return true
		})
	}
}

// hardenedCookie reports whether lit sets Secure: true, HttpOnly: true and
// SameSite to Lax or Strict, literally: what gosec G124 accepts.
func hardenedCookie(lit *ast.CompositeLit) bool {
	set := map[string]string{}
	for _, e := range lit.Elts {
		kv, ok := e.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		switch v := kv.Value.(type) {
		case *ast.Ident:
			set[key.Name] = v.Name
		case *ast.SelectorExpr:
			set[key.Name] = v.Sel.Name
		case *ast.BasicLit:
			set[key.Name], _ = strconv.Unquote(v.Value)
		}
	}
	return set["Secure"] == "true" && set["HttpOnly"] == "true" &&
		(set["SameSite"] == "SameSiteLaxMode" || set["SameSite"] == "SameSiteStrictMode")
}
