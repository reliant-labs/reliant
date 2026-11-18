package codegen

import "strings"

// UnresolvedPlaceholder is an AppExtras field that carries the
// `forge:placeholder` marker but is still typed `any`. The build-time
// gate (forge lint --wire-coverage) treats these as ERRORS — a field
// the user promised to tighten to a real type but hasn't yet.
//
// Retained after the old name-matched wire_gen unit was retired
// (FORGE_SHAPE_REDESIGN §2): the placeholder lint still reads
// pkg/app/app_extras.go and reports markers left typed `any`.
type UnresolvedPlaceholder struct {
	// FieldName is the AppExtras field name.
	FieldName string

	// CurrentType is the type as declared today (typically "any").
	CurrentType string

	// TargetType is the type the user promised to tighten to.
	TargetType string
}

// zeroValueLiteral returns the Go source literal that represents the
// zero value of the given pretty-printed type expression. The mapping
// is intentionally narrow: only the scalar kinds Go can express as a
// single short literal. Composite types (struct{}, [N]T, etc.) and
// every pointer / interface / slice / map / channel / function fall
// through to "nil" — which is the right zero value for the latter
// group and a "compile error points right at the assignment" for the
// former (rare, and the message is exactly what the user wants).
//
// The check is on the source-string form rather than the AST kind so
// callers don't need a *types.Info — the inject_gen pipeline has the
// pretty-printed Deps types already, never re-parses, and stays cheap.
func zeroValueLiteral(typeExpr string) string {
	t := strings.TrimSpace(typeExpr)
	switch t {
	case "string":
		return `""`
	case "bool":
		return "false"
	case "int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64",
		"uintptr", "byte", "rune",
		"float32", "float64",
		"complex64", "complex128":
		return "0"
	case "time.Duration":
		// A frequent enough Deps shape (timeouts, intervals) that
		// hardcoding the well-known case avoids a confusing
		// `nil` → `cannot use nil as time.Duration` build error.
		// Anything else aliased from time.* still falls through to nil
		// and surfaces the same error at the assignment.
		return "0"
	}
	return "nil"
}
