package codegen

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/naming"
	"github.com/reliant-labs/forge/internal/templates"
)

// componentAccessorRole is the per-kind half of a supervised component's
// subcommand binding: the cmd group it lives in and the accessor shape
// lifecycle.go declares for it.
type componentAccessorRole struct {
	kind, group, prefix, aggregate, returnType, zero string
}

var componentAccessorRoles = []componentAccessorRole{
	{kind: "worker", group: "workers", prefix: "Worker", aggregate: "AllWorkers", returnType: "serverkit.Worker", zero: "nil"},
	{kind: "operator", group: "operators", prefix: "Operator", aggregate: "AllOperators", returnType: "app.OperatorEntry", zero: "app.OperatorEntry{}"},
}

// componentAccessorData feeds cmd-component-accessor-gen.go.tmpl.
type componentAccessorData struct {
	Module, Kind, Group, Name, FieldName string
	Method, Var, Accessor, Aggregate     string
	ReturnType, Zero                     string
	// Missing is true when internal/app declares no (*Components).<Method>:
	// the file then holds a placeholder that compiles and refuses to start,
	// instead of a method expression that would break the build.
	Missing bool
}

// accessorGenSuffix names the generated half of a component subcommand.
const accessorGenSuffix = "_accessor_gen.go"

// componentAccessorPath is where the accessor binding for one component's
// subcommand lives, next to the owned <name>.go.
func componentAccessorPath(bin, group, name string) string {
	return filepath.Join("cmd", bin, "cmd", group, name+accessorGenSuffix)
}

// GenerateComponentAccessors re-derives the accessor binding of every owned
// per-worker / per-operator subcommand under cmd/<bin>/cmd/{workers,operators}/.
//
// THE SPLIT, as for services (cmd-svc-mount-gen.go.tmpl). <name>.go is the
// user's cobra command and calls worker<Field>(c) / operator<Field>(c);
// <name>_accessor_gen.go declares that var as the method expression
// (*app.Components).Worker<Field> — or, while internal/app declares no such
// method, as a placeholder that compiles and refuses to start with what to
// add. forge reconciles the accessor into lifecycle.go itself, so the
// placeholder is the state of exactly one kind of project: one that owns its
// composition (compose.go / lifecycle.go disowned) and has not added the
// component by hand yet. Without the split, scaffolding a worker there wrote
// a subcommand naming an accessor nobody would write, and every later
// `forge generate` failed its validate step on it and rolled back.
//
// A subcommand that names the accessor directly (c.Worker<Field>(), the shape
// scaffolded before this split) references no binding, gets none, and has
// any stale one retired — an unreferenced package var is a lint finding.
func GenerateComponentAccessors(targetDir, bin string, cs *checksums.FileChecksums) error {
	modulePath, err := GetModulePath(targetDir)
	if err != nil {
		return fmt.Errorf("read module path: %w", err)
	}
	declared, readable := componentsMethods(filepath.Join(targetDir, "internal", "app"))
	for _, role := range componentAccessorRoles {
		dir := filepath.Join(targetDir, "cmd", bin, "cmd", role.group)
		for _, name := range ownedComponentCommands(dir) {
			field := naming.ToPascalCase(name)
			varName := role.kind + field
			rel := componentAccessorPath(bin, role.group, name)
			owned, rerr := os.ReadFile(filepath.Join(dir, name+".go"))
			if rerr != nil {
				return rerr
			}
			if !regexp.MustCompile(`\b` + varName + `\(`).Match(owned) {
				RetireGenerated(targetDir, rel, cs)
				continue
			}
			data := componentAccessorData{
				Module: modulePath, Kind: role.kind, Group: role.group, Name: name, FieldName: field,
				Method: role.prefix + field, Var: varName, Aggregate: role.aggregate,
				ReturnType: role.returnType, Zero: role.zero,
				Accessor: componentAccessorSnippet(role, name, field),
				// An unreadable internal/app is a tree mid-generate, not a
				// hand-owned surface: the method expression and the
				// compiler are the right signal there.
				Missing: readable && !declared[role.prefix+field],
			}
			content, err := templates.ProjectTemplates().Render("cmd-component-accessor-gen.go.tmpl", data)
			if err != nil {
				return fmt.Errorf("render cmd-component-accessor-gen.go.tmpl (%s %s): %w", role.kind, name, err)
			}
			if err := writeSourceDerived(targetDir, rel, content, cs); err != nil {
				return fmt.Errorf("write %s: %w", rel, err)
			}
		}
	}
	return nil
}

// PendingComponentAccessor reports the accessor method a component
// subcommand's binding is waiting for — non-empty exactly when the binding
// GenerateComponentAccessors wrote is the placeholder. It reads that file
// rather than re-deriving the answer, so the scaffold's report can never
// disagree with what was emitted.
func PendingComponentAccessor(root, bin, group, name string) string {
	raw, err := os.ReadFile(filepath.Join(root, componentAccessorPath(bin, group, name)))
	if err != nil {
		return ""
	}
	if m := accessorPlaceholderRE.FindSubmatch(raw); m != nil {
		return string(m[1])
	}
	return ""
}

// ComponentAccessorSnippet is the accessor a project that owns lifecycle.go
// adds by hand for a supervised component: the same code forge's own
// reconcile writes (lifecycleWorkerAccessor / lifecycleOperatorAccessor),
// on one line.
func ComponentAccessorSnippet(kind, name string) string {
	for _, role := range componentAccessorRoles {
		if role.kind == kind {
			return componentAccessorSnippet(role, name, naming.ToPascalCase(name))
		}
	}
	return ""
}

var accessorPlaceholderRE = regexp.MustCompile(`internal/app declares no \(\*Components\)\.((?:Worker|Operator)\w+)\(\)`)

func componentAccessorSnippet(role componentAccessorRole, name, field string) string {
	if role.kind == "operator" {
		return fmt.Sprintf(`func (c *Components) Operator%[2]s() OperatorEntry { return OperatorEntry{Name: %[1]q, AddToScheme: %[3]s.AddToScheme, SetupWithManager: c.%[2]s.SetupWithManager} }`, name, field, name)
	}
	return fmt.Sprintf(`func (c *Components) Worker%[2]s() serverkit.Worker { return lifecyclekit.WrapWorker(%[1]q, c.%[2]s) }`, name, field)
}

// ownedComponentCommands lists the owned subcommand files of one cmd group
// (<name>.go), skipping the group anchor and every generated or test file.
func ownedComponentCommands(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") || strings.HasSuffix(n, "_gen.go") {
			continue
		}
		if n == "register.go" {
			continue
		}
		names = append(names, strings.TrimSuffix(n, ".go"))
	}
	sort.Strings(names)
	return names
}

// componentsMethods returns the name of every method declared on Components
// (value or pointer receiver) in dir's non-test Go files, and whether dir
// held any Go file to read.
func componentsMethods(dir string) (map[string]bool, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, false
	}
	out := map[string]bool{}
	readable := false
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if perr != nil {
			continue
		}
		readable = true
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 {
				continue
			}
			recv := fn.Recv.List[0].Type
			if star, ok := recv.(*ast.StarExpr); ok {
				recv = star.X
			}
			if id, ok := recv.(*ast.Ident); ok && id.Name == "Components" {
				out[fn.Name.Name] = true
			}
		}
	}
	return out, readable
}
