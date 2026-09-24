package wfcel

import (
	"fmt"
	"sort"
	"strings"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/operators"
	"github.com/google/cel-go/common/types"
)

// NamespaceRefs describes how a set of CEL expressions reads one namespace.
type NamespaceRefs struct {
	// Referenced is true when the namespace appears at all.
	Referenced bool
	// Whole is true when the namespace is used other than through a static
	// field select — bare (`inputs`), dynamically indexed (`inputs[k]`), or
	// passed to a function. A caller that needs to ship the namespace's data
	// must then ship all of it.
	Whole bool
	// Fields are the statically selected top-level fields (`inputs.foo`,
	// `inputs.?foo`, `has(inputs.foo)`), sorted and de-duplicated.
	Fields []string
}

// TemplateExpressions returns the CEL expressions inside a {{...}} template.
// A string with no template delimiters is treated as one raw CEL expression
// when rawCEL is true (save_message.condition is written that way), and as a
// literal with no expressions otherwise.
func TemplateExpressions(s string, rawCEL bool) []string {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return nil
	}
	matches := extractTemplates(trimmed)
	if len(matches) == 0 {
		if rawCEL {
			return []string{trimmed}
		}
		return nil
	}
	exprs := make([]string, 0, len(matches))
	for _, m := range matches {
		if m.expr != "" {
			exprs = append(exprs, m.expr)
		}
	}
	return exprs
}

// ReferencesOf parses each expression and reports how it reads namespace.
// Parsing only — no type checking — so it works for any namespace without a
// declared environment. A parse error is returned rather than guessed around:
// a caller deciding what data to ship must not under-ship on an expression it
// could not read.
func ReferencesOf(exprs []string, namespace CELNamespace) (NamespaceRefs, error) {
	env, err := cel.NewEnv(cel.OptionalTypes())
	if err != nil {
		return NamespaceRefs{}, fmt.Errorf("creating CEL parse env: %w", err)
	}
	var refs NamespaceRefs
	fields := map[string]bool{}
	for _, expr := range exprs {
		parsed, issues := env.Parse(expr)
		if issues != nil && issues.Err() != nil {
			return NamespaceRefs{}, fmt.Errorf("parsing %q: %w", expr, issues.Err())
		}
		root := ast.NavigateAST(parsed.NativeRep())
		idents := ast.MatchDescendants(root, func(e ast.NavigableExpr) bool {
			return e.Kind() == ast.IdentKind && e.AsIdent() == string(namespace)
		})
		for _, ident := range idents {
			refs.Referenced = true
			if field, ok := staticFieldOf(ident); ok {
				fields[field] = true
			} else {
				refs.Whole = true
			}
		}
	}
	for f := range fields {
		refs.Fields = append(refs.Fields, f)
	}
	sort.Strings(refs.Fields)
	return refs, nil
}

// staticFieldOf reports the field an identifier is statically selected by:
// `ns.field` (including the has() macro, which parses to a test-only select)
// or the optional select `ns.?field`.
func staticFieldOf(ident ast.NavigableExpr) (string, bool) {
	parent, ok := ident.Parent()
	if !ok {
		return "", false
	}
	switch parent.Kind() {
	case ast.SelectKind:
		sel := parent.AsSelect()
		if sel.Operand().ID() == ident.ID() {
			return sel.FieldName(), true
		}
	case ast.CallKind:
		call := parent.AsCall()
		args := call.Args()
		if call.FunctionName() == operators.OptSelect && len(args) == 2 &&
			args[0].ID() == ident.ID() && args[1].Kind() == ast.LiteralKind {
			if s, ok := args[1].AsLiteral().(types.String); ok {
				return string(s), true
			}
		}
	}
	return "", false
}
