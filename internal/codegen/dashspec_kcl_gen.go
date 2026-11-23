// Package codegen — dashspec_kcl_gen.go generates forge's KCL mirror of the
// dashboard vocabulary (kcl/dashspec/dashspec_gen.k) from the Go types in
// pkg/dashspec.
//
// # GO IS THE SOURCE, KCL IS A PROJECTION
//
// A dashboard is declared in forge.dashboards/v1, and dashspec.ParseDashboard
// is the closed-world judge every reader runs (generate, a release cut, the
// control plane). An author who declares a dashboard in KCL must write the
// same field names and the same enum values, or the JSON that KCL serializes
// is refused at generate, far from the line that is wrong. So the KCL schemas
// are derived from the Go structs, the same way kcl/tiers/tiers_gen.k is
// derived from pkg/deploy/v1alpha1, and TestDashspecKCLIsCurrent fails when
// the two drift.
//
// # HOW
//
// pkg/dashspec carries no kubebuilder markers, so there is no OpenAPI schema
// to lower. The generator reads the package's SOURCE with go/parser (no type
// checker, no `go list`), which keeps it fast enough for the -short tier and
// makes the currency test cache-sound: every input is a file this process
// opens.
//
//   - A struct reachable from Dashboard becomes a CLOSED schema. Its fields are
//     the struct's JSON names, in declaration order; a field without
//     `omitempty` is required, every other one optional.
//   - A named string type with constants (Signal, PanelKind, Agg, ...) is an
//     enum: its typed constants, in declaration order, become
//     <TYPE>_VALUES and a check on every field typed with it.
//   - A plain string field whose values are untyped constants (Series.metricType,
//     PostgresQuery.draw) names them with a `//dashspec:enum <Prefix>` directive
//     on the field: every untyped string constant whose name starts with
//     Prefix is a value. The directive keeps the value set beside the field,
//     where the validator reads the same constants.
//   - Each schema has a <snake>_json lambda that drops unset optionals, so the
//     output is exactly the JSON a dashboard file carries.
//
// Cross-field rules (a quantile needs a histogram, which kinds take which
// fields, id patterns, bounds) are not lowered: ParseDashboard runs on every
// path that reads a dashboard, and restating them by hand in KCL would be the
// second declaration this generator exists to delete.
package codegen

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// DashspecPackageDir is the dashboard vocabulary's package, relative to
// forge's repo root.
const DashspecPackageDir = "pkg/dashspec"

// DashspecKCLPath is where the generated module lives, relative to forge's
// repo root. It is embedded with the rest of the KCL module and imported by
// projects as `forge.dashspec` (and by forge's own root package as
// `.dashspec`).
const DashspecKCLPath = "kcl/dashspec/dashspec_gen.k"

// DashspecRoots are the types whose schemas are generated. Every struct they
// reach is generated too, transitively.
var DashspecRoots = []string{"Dashboard"}

// enumDirective names a plain string field's value constants (see the file
// comment).
const enumDirective = "//dashspec:enum "

// DashspecModel is the lowered input of GenerateDashspecKCL.
type DashspecModel struct {
	// SpecVersion is dashspec.SpecVersion.
	SpecVersion string
	// Structs are the generated schemas: the roots first, then the rest
	// sorted by name.
	Structs []DashspecStruct
	// Enums are the enums some field reads, sorted by name.
	Enums []DashspecEnum
}

// DashspecStruct is one generated schema.
type DashspecStruct struct {
	Name   string
	Doc    string
	Fields []DashspecField
}

// DashspecField is one field of a generated schema.
type DashspecField struct {
	JSON     string
	Doc      []string // the first paragraph of the Go doc, line by line
	Optional bool
	Type     DashspecType
}

// DashspecType is a field's lowered type.
type DashspecType struct {
	// Kind is "scalar", "struct", "list" or "map".
	Kind string
	// Scalar is the KCL scalar type (str, int, float, bool) of a scalar.
	Scalar string
	// Struct names a struct's schema.
	Struct string
	// Enum names the enum a str scalar takes its value from, if any.
	Enum string
	// Elem is a list's item or a map's value type.
	Elem *DashspecType
}

// DashspecEnum is one enum and its values, in declaration order.
type DashspecEnum struct {
	Name   string
	Doc    string
	Values []string
}

// dashspecSource is what the parser collected from the package.
type dashspecSource struct {
	types        map[string]*ast.TypeSpec
	typeDocs     map[string]string
	typedConsts  map[string][]string // enum type -> values, declaration order
	untypedNames []string            // untyped string consts, declaration order
	untyped      map[string]string   // untyped string const -> value
}

// LoadDashspecModel parses pkg/dashspec in forgeRoot and lowers every type
// reachable from DashspecRoots.
func LoadDashspecModel(forgeRoot string) (*DashspecModel, error) {
	src, err := parseDashspecSource(filepath.Join(forgeRoot, DashspecPackageDir))
	if err != nil {
		return nil, err
	}
	l := &dashspecLowering{src: src, structs: map[string]*DashspecStruct{}, enums: map[string]*DashspecEnum{}}
	for _, root := range DashspecRoots {
		if err := l.needStruct(root); err != nil {
			return nil, err
		}
	}
	m := &DashspecModel{SpecVersion: src.untyped["SpecVersion"]}
	if m.SpecVersion == "" {
		return nil, fmt.Errorf("%s declares no SpecVersion constant", DashspecPackageDir)
	}
	rootIdx := map[string]int{}
	for i, r := range DashspecRoots {
		rootIdx[r] = i
	}
	names := make([]string, 0, len(l.structs))
	for n := range l.structs {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		ri, iok := rootIdx[names[i]]
		rj, jok := rootIdx[names[j]]
		switch {
		case iok && jok:
			return ri < rj
		case iok != jok:
			return iok
		default:
			return names[i] < names[j]
		}
	})
	for _, n := range names {
		m.Structs = append(m.Structs, *l.structs[n])
	}
	enumNames := make([]string, 0, len(l.enums))
	for n := range l.enums {
		enumNames = append(enumNames, n)
	}
	sort.Strings(enumNames)
	for _, n := range enumNames {
		m.Enums = append(m.Enums, *l.enums[n])
	}
	return m, nil
}

func parseDashspecSource(dir string) (*dashspecSource, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		files = append(files, n)
	}
	sort.Strings(files)
	src := &dashspecSource{
		types: map[string]*ast.TypeSpec{}, typeDocs: map[string]string{},
		typedConsts: map[string][]string{}, untyped: map[string]string{},
	}
	fset := token.NewFileSet()
	var consts []*ast.GenDecl
	for _, name := range files {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		f, err := parser.ParseFile(fset, name, body, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok {
				continue
			}
			switch gd.Tok {
			case token.TYPE:
				for _, s := range gd.Specs {
					ts := s.(*ast.TypeSpec)
					src.types[ts.Name.Name] = ts
					doc := ts.Doc
					if doc == nil && len(gd.Specs) == 1 {
						doc = gd.Doc
					}
					src.typeDocs[ts.Name.Name] = firstParagraph(doc.Text())
				}
			case token.CONST:
				consts = append(consts, gd)
			}
		}
	}
	// Constants are read after every type is known, so a const declared in a
	// file before its type's file is still recognised.
	for _, gd := range consts {
		if err := src.addConsts(fset, gd); err != nil {
			return nil, err
		}
	}
	return src, nil
}

// isStringType reports whether name is a type of this package declared as a
// string.
func (src *dashspecSource) isStringType(name string) bool {
	ts, ok := src.types[name]
	if !ok {
		return false
	}
	id, ok := ts.Type.(*ast.Ident)
	return ok && id.Name == "string"
}

func (src *dashspecSource) addConsts(fset *token.FileSet, gd *ast.GenDecl) error {
	for _, s := range gd.Specs {
		vs := s.(*ast.ValueSpec)
		var typ string
		if id, ok := vs.Type.(*ast.Ident); ok {
			typ = id.Name
		}
		if len(vs.Values) == 0 {
			// Implicit repetition of the previous spec (an iota run). A
			// string enum is never declared that way; refuse rather than
			// guess its values.
			continue
		}
		for i, name := range vs.Names {
			if i >= len(vs.Values) {
				break
			}
			lit, isLit := vs.Values[i].(*ast.BasicLit)
			isStr := isLit && lit.Kind == token.STRING
			switch {
			case typ != "" && src.isStringType(typ):
				if !isStr {
					return fmt.Errorf("%s: const %s %s is not a string literal; the KCL mirror reads enum values from literals (teach dashspec_kcl_gen.go the new form)",
						fset.Position(name.Pos()), name.Name, typ)
				}
				v, err := strconv.Unquote(lit.Value)
				if err != nil {
					return err
				}
				if !containsString(src.typedConsts[typ], v) {
					src.typedConsts[typ] = append(src.typedConsts[typ], v)
				}
			case vs.Type == nil && isStr:
				v, err := strconv.Unquote(lit.Value)
				if err != nil {
					return err
				}
				src.untypedNames = append(src.untypedNames, name.Name)
				src.untyped[name.Name] = v
			}
		}
	}
	return nil
}

type dashspecLowering struct {
	src     *dashspecSource
	structs map[string]*DashspecStruct
	enums   map[string]*DashspecEnum
}

func (l *dashspecLowering) needStruct(name string) error {
	if _, done := l.structs[name]; done {
		return nil
	}
	ts, ok := l.src.types[name]
	if !ok {
		return fmt.Errorf("type %s not found in %s", name, DashspecPackageDir)
	}
	st, ok := ts.Type.(*ast.StructType)
	if !ok {
		return fmt.Errorf("%s is not a struct", name)
	}
	out := &DashspecStruct{Name: name, Doc: l.src.typeDocs[name]}
	l.structs[name] = out // before the fields, so a recursive reference terminates
	for _, f := range st.Fields.List {
		if len(f.Names) == 0 {
			return fmt.Errorf("%s embeds a field; the KCL mirror lowers named fields only", name)
		}
		if f.Tag == nil {
			continue
		}
		tag, err := strconv.Unquote(f.Tag.Value)
		if err != nil {
			return err
		}
		jsonTag, hasTag := reflect.StructTag(tag).Lookup("json")
		jsonName, opts, _ := strings.Cut(jsonTag, ",")
		if !hasTag || jsonName == "-" {
			continue
		}
		if len(f.Names) > 1 {
			return fmt.Errorf("%s: fields %v share one json tag", name, f.Names)
		}
		if jsonName == "" {
			jsonName = f.Names[0].Name
		}
		if kclKeywords[jsonName] {
			return fmt.Errorf("%s.%s: the JSON name is a KCL keyword; teach dashspec_kcl_gen.go to escape it ($%s)", name, jsonName, jsonName)
		}
		typ, err := l.lower(f.Type)
		if err != nil {
			return fmt.Errorf("%s.%s: %w", name, jsonName, err)
		}
		if prefix, ok := fieldEnumDirective(f.Doc); ok {
			if err := l.applyDirective(&typ, prefix); err != nil {
				return fmt.Errorf("%s.%s: %w", name, jsonName, err)
			}
		}
		out.Fields = append(out.Fields, DashspecField{
			JSON:     jsonName,
			Doc:      firstParagraphLines(f.Doc.Text()),
			Optional: containsString(strings.Split(opts, ","), "omitempty"),
			Type:     typ,
		})
	}
	return nil
}

func (l *dashspecLowering) lower(e ast.Expr) (DashspecType, error) {
	switch t := e.(type) {
	case *ast.StarExpr:
		return l.lower(t.X)
	case *ast.ArrayType:
		if t.Len != nil {
			return DashspecType{}, fmt.Errorf("fixed-size arrays have no JSON list form here")
		}
		elem, err := l.lower(t.Elt)
		if err != nil {
			return DashspecType{}, err
		}
		return DashspecType{Kind: "list", Elem: &elem}, nil
	case *ast.MapType:
		if k, ok := t.Key.(*ast.Ident); !ok || (k.Name != "string" && !l.src.isStringType(k.Name)) {
			return DashspecType{}, fmt.Errorf("a map key must be a string")
		}
		elem, err := l.lower(t.Value)
		if err != nil {
			return DashspecType{}, err
		}
		return DashspecType{Kind: "map", Elem: &elem}, nil
	case *ast.Ident:
		switch t.Name {
		case "string":
			return DashspecType{Kind: "scalar", Scalar: "str"}, nil
		case "bool":
			return DashspecType{Kind: "scalar", Scalar: "bool"}, nil
		case "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64":
			return DashspecType{Kind: "scalar", Scalar: "int"}, nil
		case "float32", "float64":
			return DashspecType{Kind: "scalar", Scalar: "float"}, nil
		}
		ts, ok := l.src.types[t.Name]
		if !ok {
			return DashspecType{}, fmt.Errorf("type %s is not declared in %s", t.Name, DashspecPackageDir)
		}
		switch ts.Type.(type) {
		case *ast.StructType:
			if err := l.needStruct(t.Name); err != nil {
				return DashspecType{}, err
			}
			return DashspecType{Kind: "struct", Struct: t.Name}, nil
		}
		under, err := l.lower(ts.Type)
		if err != nil {
			return DashspecType{}, fmt.Errorf("%s: %w", t.Name, err)
		}
		if under.Kind == "scalar" && under.Scalar == "str" {
			if values := l.src.typedConsts[t.Name]; len(values) > 0 {
				l.enums[t.Name] = &DashspecEnum{Name: t.Name, Doc: l.src.typeDocs[t.Name], Values: values}
				under.Enum = t.Name
			}
		}
		return under, nil
	}
	return DashspecType{}, fmt.Errorf("unsupported type expression %T: the vocabulary is scalars, enums, lists, maps and its own structs", e)
}

// applyDirective gives a plain string field (or the strings of a list or
// map) the untyped constants named by a //dashspec:enum directive.
func (l *dashspecLowering) applyDirective(typ *DashspecType, prefix string) error {
	target := typ
	if target.Kind == "list" || target.Kind == "map" {
		target = target.Elem
	}
	if target.Kind != "scalar" || target.Scalar != "str" {
		return fmt.Errorf("//dashspec:enum names string values; this field is not a string")
	}
	if target.Enum != "" {
		return fmt.Errorf("//dashspec:enum %s on a field already typed with enum %s", prefix, target.Enum)
	}
	var values []string
	for _, n := range l.src.untypedNames {
		if strings.HasPrefix(n, prefix) && len(n) > len(prefix) && ast.IsExported(n) && !containsString(values, l.src.untyped[n]) {
			values = append(values, l.src.untyped[n])
		}
	}
	if len(values) == 0 {
		return fmt.Errorf("//dashspec:enum %s: no untyped string constant is named %s*", prefix, prefix)
	}
	if _, clash := l.src.types[prefix]; clash {
		return fmt.Errorf("//dashspec:enum %s names a type; type the field with it instead", prefix)
	}
	l.enums[prefix] = &DashspecEnum{Name: prefix, Values: values}
	target.Enum = prefix
	return nil
}

func fieldEnumDirective(doc *ast.CommentGroup) (string, bool) {
	if doc == nil {
		return "", false
	}
	for _, c := range doc.List {
		if rest, ok := strings.CutPrefix(c.Text, enumDirective); ok {
			return strings.TrimSpace(rest), true
		}
	}
	return "", false
}

// firstParagraphLines is a doc's text up to its first blank line, one entry
// per source line.
func firstParagraphLines(doc string) []string {
	para, _, _ := strings.Cut(strings.TrimSpace(doc), "\n\n")
	if para == "" {
		return nil
	}
	lines := strings.Split(para, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	return lines
}

// kclKeywords are the words KCL reserves. A schema attribute named one must be
// written $name, which the generator does not do yet: it refuses instead.
var kclKeywords = map[string]bool{
	"True": true, "False": true, "None": true, "Undefined": true,
	"import": true, "as": true, "rule": true, "schema": true, "mixin": true, "protocol": true,
	"check": true, "for": true, "assert": true, "if": true, "elif": true, "else": true,
	"or": true, "and": true, "not": true, "in": true, "is": true, "lambda": true,
	"all": true, "any": true, "filter": true, "map": true, "type": true,
}

// enumConst is the KCL name of an enum's value list: PANEL_KIND_VALUES.
func enumConst(name string) string { return strings.ToUpper(toSnake(name)) + "_VALUES" }

// GenerateDashspecKCL renders the KCL module text. It is pure: the same model
// always produces byte-identical output.
func GenerateDashspecKCL(m *DashspecModel) (string, error) {
	var b strings.Builder
	b.WriteString(`"""
GENERATED by forge from pkg/dashspec — DO NOT EDIT.

Regenerate: FORGE_UPDATE_GENERATED=1 go test ./internal/codegen -run TestDashspecKCLIsCurrent

forge's dashboard vocabulary (forge.dashboards/v1) as KCL schemas. The Go
types in pkg/dashspec are the source of truth; field names are the JSON names
a dashboard file carries, so a declaration here serializes directly to one.

The schemas are CLOSED: a field that is not declared is refused at author
time. The checks are the per-field enums. Cross-field rules (a quantile needs
a histogram, which panel kinds take which fields, ids, bounds) are enforced by
dashspec.ParseDashboard, which every reader of a dashboard runs.

Each schema also has a <name>_json lambda that drops unset optional fields,
so the output is exactly the JSON a dashboard file carries:
dashboard_json(Dashboard {...}).
"""

`)
	fmt.Fprintf(&b, "# The serialized shape a dashboard is written in (dashspec.SpecVersion).\nSPEC_VERSION = %s\n\n", quoteKCLString(m.SpecVersion))
	for _, e := range m.Enums {
		if e.Doc != "" {
			fmt.Fprintf(&b, "# %s\n", e.Doc)
		} else {
			fmt.Fprintf(&b, "# %s: the %s* constants.\n", e.Name, e.Name)
		}
		vals := make([]string, len(e.Values))
		for i, v := range e.Values {
			vals[i] = quoteKCLString(v)
		}
		fmt.Fprintf(&b, "%s: [str] = [%s]\n\n", enumConst(e.Name), strings.Join(vals, ", "))
	}
	enums := map[string]DashspecEnum{}
	for _, e := range m.Enums {
		enums[e.Name] = e
	}
	for _, s := range m.Structs {
		if err := writeDashspecSchema(&b, s, enums); err != nil {
			return "", fmt.Errorf("%s: %w", s.Name, err)
		}
	}
	return strings.TrimRight(b.String(), "\n") + "\n", nil
}

func dashspecKCLType(t DashspecType) (string, error) {
	switch t.Kind {
	case "scalar":
		return t.Scalar, nil
	case "struct":
		return t.Struct, nil
	case "list", "map":
		inner, err := dashspecKCLType(*t.Elem)
		if err != nil {
			return "", err
		}
		if t.Kind == "list" {
			return "[" + inner + "]", nil
		}
		return "{str:" + inner + "}", nil
	}
	return "", fmt.Errorf("unknown kind %q", t.Kind)
}

func writeDashspecSchema(b *strings.Builder, s DashspecStruct, enums map[string]DashspecEnum) error {
	fmt.Fprintf(b, "schema %s:\n", s.Name)
	if s.Doc != "" {
		fmt.Fprintf(b, "    %s\n", quoteKCLDoc(s.Doc))
	}
	var checks []string
	for _, f := range s.Fields {
		typ, err := dashspecKCLType(f.Type)
		if err != nil {
			return fmt.Errorf("field %s: %w", f.JSON, err)
		}
		for _, line := range f.Doc {
			if line == "" {
				b.WriteString("    #\n")
				continue
			}
			fmt.Fprintf(b, "    # %s\n", line)
		}
		opt := ""
		if f.Optional {
			opt = "?"
		}
		fmt.Fprintf(b, "    %s%s: %s\n", f.JSON, opt, typ)
		c, err := dashspecEnumCheck(s.Name, f, enums)
		if err != nil {
			return err
		}
		if c != "" {
			checks = append(checks, c)
		}
	}
	if len(checks) > 0 {
		b.WriteString("\n    check:\n")
		for _, c := range checks {
			b.WriteString("        " + c + "\n")
		}
	}
	b.WriteString("\n")
	fmt.Fprintf(b, "%s_json = lambda v: %s -> {str:} {\n", toSnake(s.Name), s.Name)
	b.WriteString("    _d = {\n")
	for _, f := range s.Fields {
		fmt.Fprintf(b, "        %q: %s\n", f.JSON, dashspecJSONExpr("v."+f.JSON, f.Type))
	}
	b.WriteString("    }\n")
	b.WriteString("    {k: _v for k, _v in _d if _v != Undefined and _v != None}\n")
	b.WriteString("}\n\n")
	return nil
}

// dashspecEnumCheck is the check line holding a field to its enum, or "".
func dashspecEnumCheck(schema string, f DashspecField, enums map[string]DashspecEnum) (string, error) {
	var enum, cond string
	switch t := f.Type; {
	case t.Kind == "scalar" && t.Enum != "":
		enum, cond = t.Enum, fmt.Sprintf("%s in %s", f.JSON, enumConst(t.Enum))
	case t.Kind == "list" && t.Elem.Kind == "scalar" && t.Elem.Enum != "":
		enum, cond = t.Elem.Enum, fmt.Sprintf("all _x in %s { _x in %s }", f.JSON, enumConst(t.Elem.Enum))
	case t.Kind == "map" && t.Elem.Kind == "scalar" && t.Elem.Enum != "":
		enum, cond = t.Elem.Enum, fmt.Sprintf("all _k, _x in %s { _x in %s }", f.JSON, enumConst(t.Elem.Enum))
	case t.Kind == "list" || t.Kind == "map":
		if hasEnum(t) {
			return "", fmt.Errorf("field %s: an enum nested deeper than one list or map has no check lowering", f.JSON)
		}
		return "", nil
	default:
		return "", nil
	}
	if f.Optional {
		cond = fmt.Sprintf("not %s or %s", f.JSON, cond)
	}
	vals := make([]string, len(enums[enum].Values))
	for i, v := range enums[enum].Values {
		vals[i] = strconv.Quote(v)
	}
	what := "must be one of"
	if f.Type.Kind != "scalar" {
		what = "values must each be one of"
	}
	return cond + ", " + quoteKCLString(fmt.Sprintf("%s.%s %s: %s", schema, f.JSON, what, strings.Join(vals, ", "))), nil
}

func hasEnum(t DashspecType) bool {
	if t.Enum != "" {
		return true
	}
	return t.Elem != nil && hasEnum(*t.Elem)
}

func dashspecJSONExpr(access string, t DashspecType) string {
	switch {
	case t.Kind == "struct":
		return fmt.Sprintf("%s_json(%s) if %s else Undefined", toSnake(t.Struct), access, access)
	case t.Kind == "list" && t.Elem.Kind == "struct":
		return fmt.Sprintf("[%s_json(_i) for _i in %s] if %s else Undefined", toSnake(t.Elem.Struct), access, access)
	case t.Kind == "map" && t.Elem.Kind == "struct":
		return fmt.Sprintf("{_k: %s_json(_i) for _k, _i in %s} if %s else Undefined", toSnake(t.Elem.Struct), access, access)
	}
	return access
}

// WriteDashspecKCL regenerates kcl/dashspec/dashspec_gen.k in forgeRoot.
func WriteDashspecKCL(forgeRoot string) (string, error) {
	m, err := LoadDashspecModel(forgeRoot)
	if err != nil {
		return "", err
	}
	body, err := GenerateDashspecKCL(m)
	if err != nil {
		return "", err
	}
	path := filepath.Join(forgeRoot, DashspecKCLPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	return body, atomicfile.WriteFile(path, []byte(body), 0o644)
}
