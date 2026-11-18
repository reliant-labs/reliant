package codegen

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// IsComponentConstructor reports whether fn is a constructor forge should
// treat as a component entry point.
//
// The rule, in priority order:
//
//  1. a func carrying `// forge:constructor` IS one, whatever it is named;
//  2. otherwise a func named `New` is one, so an unmarked package still works.
//
// WHY THIS EXISTS. Three separate scanners used to spell this as
// `fn.Name.Name != "New"`, which made `// forge:constructor` unreadable on any
// other name — the marker whose entire job is to identify a constructor was
// gated on the constructor already being called `New`. Tagging `NewReadOnly`,
// `Open` or `Connect` did nothing, silently.
//
// It also forced a name. forge detects the CONTRACT name
// (DetectServiceInterfaceName, with `Service` only as a fallback) precisely so
// a package can call its interface `Mailer` or `Repository`; the constructor
// deserves the same freedom. forge is meant to promote good practice, not to
// require that every component be `New` returning `Service`.
func IsComponentConstructor(fn *ast.FuncDecl) bool {
	if fn == nil || fn.Recv != nil || fn.Name == nil {
		return false // methods are never component constructors
	}
	if anyCommentGroupHasDirective(directiveConstructorMarker, fn.Doc) {
		return true
	}
	return fn.Name.Name == "New"
}

// HasConstructorMarkerOn reports whether THIS declaration carries
// `// forge:constructor`, as opposed to the package containing one somewhere
// (HasConstructorMarker).
//
// The per-decl answer is what tells a lint whether the marker is advice the
// author has already taken. The marker frees a constructor's NAME and has
// never affected its signature, so prescribing it for a signature gap on a
// func that already carries it is advice that cannot work — and an author who
// tries it concludes the rule is unsatisfiable.
func HasConstructorMarkerOn(fn *ast.FuncDecl) bool {
	if fn == nil {
		return false
	}
	return anyCommentGroupHasDirective(directiveConstructorMarker, fn.Doc)
}

// DefaultConstructorName is the constructor name forge assumes when a package
// declares no `// forge:constructor` marker — the zero-annotation shape every
// scaffold is born with.
const DefaultConstructorName = "New"

// DetectConstructorName returns the name of the package's component
// constructor: the `// forge:constructor`-marked func under whatever name its
// author chose, else `New`.
//
// This is the EMIT-side counterpart of [IsComponentConstructor]. Freeing the
// name on the discovery side alone would be worse than not freeing it at all:
// lint would bless `// forge:constructor func Open(Deps) (Mailer, error)` while
// the injector kept emitting `pkg.New(pkg.Deps{...})`, so `forge generate`
// would write a compile-broken internal/app/compose.go pointing the user at
// generated code they don't own. Detection and emission read the same marker.
//
// Falls back to [DefaultConstructorName] whenever the directory is missing,
// unparseable, or declares no constructor — the caller then emits the shape a
// freshly scaffolded package has, which is the correct guess.
func DetectConstructorName(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return DefaultConstructorName
	}
	fset := token.NewFileSet()
	// A MARKED func wins outright, wherever it sits in the package: a package
	// that keeps an unmarked `New` alongside a marked `Open` means the marked
	// one. Scanning file-by-file and returning the first hit would let file
	// ordering pick the answer. os.ReadDir sorts by name, so ties between two
	// marked funcs resolve deterministically.
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") ||
			strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, "_gen.go") {
			continue
		}
		file, perr := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ParseComments|parser.SkipObjectResolution)
		if perr != nil {
			continue
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Name == nil {
				continue
			}
			if anyCommentGroupHasDirective(directiveConstructorMarker, fn.Doc) {
				return fn.Name.Name
			}
		}
	}
	return DefaultConstructorName
}
