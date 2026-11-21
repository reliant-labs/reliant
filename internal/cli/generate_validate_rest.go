package cli

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/internal/config"
)

// googleapisBufDep is the BSR module that holds google/api/annotations.proto,
// the import every google.api.http annotation needs.
const googleapisBufDep = "buf.build/googleapis/googleapis"

var httpAnnotation = regexp.MustCompile(`\(\s*google\.api\.http\s*\)`)

// restWiringWarnings reports, for a project with `api.rest: true`, each piece
// of REST wiring that is missing, naming the manual edit that adds it.
//
// Every piece lives in a file the project owns: the server's REST switch
// (internal/app/providers.go, or cmd/<bin>/cmd/serve.go in a project that
// predates the composition graph) is scaffolded once with the api.rest value
// of its birth, buf.yaml is Tier-2 (hand edits respected), and the
// annotations are the user's proto. Flipping api.rest on after scaffold
// therefore changes none of them, and forge does not rewrite them — so
// generate says what to change instead of claiming REST is on (obs 4e9ce6c3).
//
// hasServices says whether the caller's service metadata lists any; when it
// lists none (the directory-scan fallback), a .proto under proto/services/
// counts, so missing metadata never silences the warnings.
//
// Advisory: a check that cannot read its file reports nothing.
func restWiringWarnings(projectDir string, cfg *config.ProjectConfig, hasServices bool) []string {
	if cfg == nil || !cfg.API.REST || !(hasServices || hasServiceProtos(projectDir)) {
		return nil
	}
	var warnings []string
	if w := restServerWarning(projectDir); w != "" {
		warnings = append(warnings, w)
	}
	if w := restBufDepWarning(projectDir); w != "" {
		warnings = append(warnings, w)
	}
	if w := restAnnotationWarning(projectDir); w != "" {
		warnings = append(warnings, w)
	}
	return warnings
}

// hasServiceProtos reports whether proto/services/ holds a .proto file.
func hasServiceProtos(projectDir string) bool {
	found := false
	_ = filepath.WalkDir(filepath.Join(projectDir, "proto", "services"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".proto") {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// restServerWarning reads the PRIMARY server's own wiring, as Go syntax (so
// a comment, a string or another binary never counts): the Go files of
// internal/app/ and of the primary binary's cmd/<bin>/cmd/. REST is on when a
// serverkit.ListenerDeps literal sets REST to true (or to an expression forge
// cannot evaluate, which is the project's own choice), or when a
// serverkit.RESTHandler result is assigned to a server's Handler. It is
// unwired when a literal sets `REST: false` and nothing turns it on, or when
// neither appears (a legacy serve.go born without the swap).
func restServerWarning(projectDir string) string {
	dirs := []string{filepath.Join(projectDir, "internal", "app")}
	if bin := bootstrapBinaryName(projectDir); bin != "" {
		dirs = append(dirs, filepath.Join(projectDir, "cmd", bin, "cmd"))
	}
	var on bool
	offAt := ""
	fset := token.NewFileSet()
	for _, dir := range dirs {
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
			if err != nil {
				return nil
			}
			fileOn, offPos := restWiring(f)
			on = on || fileOn
			if offPos.IsValid() && offAt == "" {
				rel, _ := filepath.Rel(projectDir, p)
				offAt = fmt.Sprintf("%s:%d", filepath.ToSlash(rel), fset.Position(offPos).Line)
			}
			return nil
		})
	}
	switch {
	case on:
		return ""
	case offAt != "":
		return fmt.Sprintf("api.rest: true, but the server does not serve REST: %s sets `REST: false` in serverkit.ListenerDeps. "+
			"Set it to `REST: true` (the file is yours; forge never rewrites it).", offAt)
	default:
		return "api.rest: true, but nothing under cmd/<bin>/cmd/ or internal/app/ serves REST. " +
			"In cmd/<bin>/cmd/serve.go, after the services are mounted, add " +
			"`if rest := serverkit.RESTHandler(srv.Mux, mounted, logger); rest != nil { srv.Handler = rest }` " +
			"(or `REST: true` in internal/app/providers.go's serverkit.ListenerDeps, in a composition-graph project)."
	}
}

// restWiring reads one file: whether it turns REST on, and the position of a
// ListenerDeps `REST: false`, if any.
func restWiring(f *ast.File) (on bool, off token.Pos) {
	isCall := func(e ast.Expr, name string) bool {
		c, ok := e.(*ast.CallExpr)
		if !ok {
			return false
		}
		sel, ok := c.Fun.(*ast.SelectorExpr)
		return ok && sel.Sel.Name == name
	}
	isHandler := func(e ast.Expr) bool {
		sel, ok := e.(*ast.SelectorExpr)
		return ok && sel.Sel.Name == "Handler"
	}
	restVars := map[string]bool{} // idents a RESTHandler result is bound to
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CompositeLit:
			sel, ok := n.Type.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "ListenerDeps" {
				return true
			}
			for _, el := range n.Elts {
				kv, ok := el.(*ast.KeyValueExpr)
				if k, isIdent := kv.Key.(*ast.Ident); !ok || !isIdent || k.Name != "REST" {
					continue
				}
				if v, isIdent := kv.Value.(*ast.Ident); isIdent && v.Name == "false" {
					if !off.IsValid() {
						off = kv.Pos()
					}
				} else {
					on = true
				}
			}
		case *ast.AssignStmt:
			for i, rhs := range n.Rhs {
				if !isCall(rhs, "RESTHandler") || i >= len(n.Lhs) {
					continue
				}
				if isHandler(n.Lhs[i]) {
					on = true
				} else if id, ok := n.Lhs[i].(*ast.Ident); ok {
					restVars[id.Name] = true
				}
			}
		}
		return true
	})
	// A bound result counts once it reaches a server's Handler.
	ast.Inspect(f, func(n ast.Node) bool {
		if a, ok := n.(*ast.AssignStmt); ok {
			for i, rhs := range a.Rhs {
				if id, isIdent := rhs.(*ast.Ident); isIdent && restVars[id.Name] && i < len(a.Lhs) && isHandler(a.Lhs[i]) {
					on = true
				}
			}
		}
		return true
	})
	return on, off
}

// restBufDepWarning: google/api/annotations.proto resolves only when buf.yaml
// declares the googleapis dep and buf.lock pins it.
func restBufDepWarning(projectDir string) string {
	data, err := os.ReadFile(filepath.Join(projectDir, "buf.yaml"))
	if err != nil {
		return ""
	}
	var buf struct {
		Deps []string `yaml:"deps"`
	}
	if yaml.Unmarshal(data, &buf) != nil {
		return ""
	}
	for _, d := range buf.Deps {
		if strings.TrimSpace(strings.SplitN(d, ":", 2)[0]) == googleapisBufDep {
			if fileExists(filepath.Join(projectDir, "buf.lock")) {
				return ""
			}
			return "api.rest: true, and buf.yaml declares " + googleapisBufDep + ", but there is no buf.lock: " +
				"run `buf dep update` so `import \"google/api/annotations.proto\"` resolves."
		}
	}
	return "api.rest: true, but buf.yaml has no " + googleapisBufDep + " dep, so `import \"google/api/annotations.proto\"` cannot resolve. " +
		"Add `deps: [" + googleapisBufDep + "]` to buf.yaml (yours; forge does not rewrite an edited buf.yaml), then run `buf dep update` to write buf.lock."
}

// restAnnotationWarning: vanguard routes only rpcs that carry a
// google.api.http option; with none, REST serves no routes.
func restAnnotationWarning(projectDir string) string {
	found := false
	_ = filepath.WalkDir(filepath.Join(projectDir, "proto", "services"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || found || d.IsDir() || !strings.HasSuffix(p, ".proto") {
			return nil
		}
		if src, err := os.ReadFile(p); err == nil && httpAnnotation.Match(src) {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	if found || !dirExists(filepath.Join(projectDir, "proto", "services")) {
		return ""
	}
	return "api.rest: true, but no rpc under proto/services/ has a `google.api.http` option, so REST serves no routes. " +
		"Add `option (google.api.http) = { get: \"/v1/<entities>/{id}\" };` (and so on) to each rpc to expose, " +
		"with `import \"google/api/annotations.proto\";` — `forge skill load api-rest` lists the CRUD defaults."
}
