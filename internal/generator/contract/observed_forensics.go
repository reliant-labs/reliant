// File: internal/generator/contract/observed_forensics.go
//
// The per-method constants middleware_gen.go carries beyond the wrapper
// itself (docs/proposals/runtime-context.md, section B):
//
//   - an *observe.Op per wrapped method: forge.op, code.function.name
//     ("<import path>.<Iface>.<Method>") and code.file.path (the
//     project-relative file of the IMPLEMENTING method when it can be found
//     in the package, else of the interface). No line numbers: they would
//     churn the generated file on every edit of the implementation.
//   - a forensics capture type per method, over only the parameters this file
//     classifies as capturable. Redaction is decided HERE, from each
//     parameter's type expression and name, deny by default; the runtime
//     (pkg/observe/component_forensics.go) only bounds what it is handed and
//     walks proto messages under the same field rules.
package contract

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/pkg/observe"
)

// NoCaptureMarker excludes parameters of one method from forensics:
// `// forge:no-capture <param> [<param>…]` on the interface method or its
// implementation. Bare, it excludes every parameter of the method.
const NoCaptureMarker = "forge:no-capture"

// captureKind is how one parameter is recorded when its call fails or runs
// slow — decided at generation time.
type captureKind int

const (
	captureNone     captureKind = iota // context, or excluded by forge:no-capture
	captureInt                         // value, as int64
	captureUint                        // value, as uint64
	captureFloat                       // value, as float64
	captureBool                        // value
	captureDuration                    // time.Duration: "[type=Duration]", never the value
	captureTime                        // time.Time: "[type=Time]", never the value
	captureID                          // ID-shaped string: the value, bounded
	captureLen                         // other strings, []byte: the length
	captureCount                       // slices, arrays, maps, variadics: the element count
	captureProto                       // pointer to a generated proto message: walked
	captureNamed                       // a named generated type (a proto enum): its name
	captureType                        // anything else: the type only
	captureDigits                      // a number with a secret name (observe.ForensicSecretNumberName): its digit count
)

func (k captureKind) String() string {
	return [...]string{"none", "int", "uint", "float", "bool", "duration", "time", "id", "len", "count", "proto", "named", "type", "digits"}[k]
}

// capturePrimitives maps a predeclared type to its capture kind; string is
// decided by the parameter's name.
var capturePrimitives = map[string]captureKind{
	"int": captureInt, "int8": captureInt, "int16": captureInt, "int32": captureInt, "int64": captureInt, "rune": captureInt,
	"uint": captureUint, "uint8": captureUint, "uint16": captureUint, "uint32": captureUint, "uint64": captureUint, "uintptr": captureUint, "byte": captureUint,
	"float32": captureFloat, "float64": captureFloat,
	"bool": captureBool,
}

// classifyParam decides how a parameter named name of type typeExpr is
// captured. imports maps a qualifier to its import path (contract.go's import
// block); aliases maps a package-local named type to its primitive underlying
// type (File.PrimitiveAliases).
func classifyParam(name, typeExpr string, imports, aliases map[string]string) captureKind {
	t := strings.TrimSpace(typeExpr)
	if strings.HasPrefix(t, "...") {
		return captureCount
	}
	if kind, ok := capturePrimitives[t]; ok {
		// A secret-named number (`pin`, `cvv`, `sessionID`) records its digit
		// count, never its value, unless its name is a quantity
		// (`max_tokens`); a bool keeps its value, being one of two.
		if kind != captureBool && observe.ForensicSecretNumberName(name) {
			return captureDigits
		}
		return kind
	}
	if t == "string" {
		// ForensicIDValueName is false for a secret name: its length only.
		if observe.ForensicIDValueName(name) {
			return captureID
		}
		return captureLen
	}
	switch {
	case t == "[]byte":
		return captureLen
	case strings.HasPrefix(t, "[]"), strings.HasPrefix(t, "map["):
		return captureCount
	case strings.HasPrefix(t, "["):
		// [N]T: an array's length is its type's, but it is still all the
		// value-free information there is.
		return captureCount
	case strings.HasPrefix(t, "*"):
		if q, _, ok := selector(t[1:]); ok && isProtoImport(imports[q]) {
			return captureProto
		}
		return captureType
	}
	if q, sel, ok := selector(t); ok {
		switch path := imports[q]; {
		case path == "time" && sel == "Duration":
			return captureDuration
		case path == "time" && sel == "Time":
			return captureTime
		case path == "context" && sel == "Context":
			return captureNone
		case isProtoImport(path):
			return captureNamed
		}
		return captureType
	}
	// A named type fails CLOSED: its value is recorded only when it resolves
	// to a predeclared primitive declared in this package (`type OTPCode
	// int64`), and then under the same name rules as that primitive — a
	// secret name still hides it (digits only). Anything not
	// resolved — another package's named type, a type over another named
	// type, a struct — records its type and nothing else.
	if isIdent(t) {
		if under, ok := aliases[t]; ok && under != t && isPredeclaredValue(under) {
			return classifyParam(name, under, nil, nil)
		}
	}
	return captureType
}

// isPredeclaredValue reports whether t is a predeclared type classifyParam
// records by value (a number or a bool) or by name rule (a string).
func isPredeclaredValue(t string) bool {
	_, ok := capturePrimitives[t]
	return ok || t == "string"
}

// selector splits "pkg.Name" (no further structure) into its parts.
func selector(t string) (qualifier, name string, ok bool) {
	q, n, found := strings.Cut(t, ".")
	if !found || !isIdent(q) || !isIdent(n) {
		return "", "", false
	}
	return q, n, true
}

func isIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

// isProtoImport reports whether an import path holds generated protobuf
// types: a project's gen/ tree (buf.build/gen included) or the protobuf
// well-known types.
func isProtoImport(path string) bool {
	if path == "" {
		return false
	}
	return strings.Contains("/"+path+"/", "/gen/") ||
		strings.HasPrefix(path, "google.golang.org/protobuf/types/known/")
}

// captureField is one parameter a capture type stores and records.
type captureField struct {
	Field string // the struct field, named after the wrapper's parameter
	Attr  string // the attribute suffix: the contract's parameter name
	Type  string // the parameter's type, as the field's type
	Kind  captureKind
	// Base is the numeric kind (int, uint, float) a captureDigits field
	// counts the digits of.
	Base  captureKind
	Label string // the type named in a Type / Named record
}

// recordStmt renders the recorder call for f against receiver a.
func (f captureField) recordStmt() string {
	v := "a." + f.Field
	// conv converts v to the recorder's parameter type unless it already is.
	conv := func(to string) string {
		if f.Type == to {
			return v
		}
		return to + "(" + v + ")"
	}
	switch f.Kind {
	case captureInt:
		return fmt.Sprintf("r.Int(%q, %s)", f.Attr, conv("int64"))
	case captureUint:
		return fmt.Sprintf("r.Uint(%q, %s)", f.Attr, conv("uint64"))
	case captureFloat:
		return fmt.Sprintf("r.Float(%q, %s)", f.Attr, conv("float64"))
	case captureDigits:
		switch f.Base {
		case captureUint:
			return fmt.Sprintf("r.UintDigits(%q, %s)", f.Attr, conv("uint64"))
		case captureFloat:
			return fmt.Sprintf("r.FloatDigits(%q, %s)", f.Attr, conv("float64"))
		}
		return fmt.Sprintf("r.IntDigits(%q, %s)", f.Attr, conv("int64"))
	case captureBool:
		return fmt.Sprintf("r.Bool(%q, %s)", f.Attr, conv("bool"))
	case captureDuration:
		// An exact duration or timestamp in a request is user content (when
		// someone booked, how long they asked for): its type only.
		return fmt.Sprintf("r.Type(%q, %q)", f.Attr, "Duration")
	case captureTime:
		return fmt.Sprintf("r.Type(%q, %q)", f.Attr, "Time")
	case captureID:
		return fmt.Sprintf("r.ID(%q, %s)", f.Attr, conv("string"))
	case captureLen:
		return fmt.Sprintf("r.Len(%q, len(%s))", f.Attr, v)
	case captureCount:
		return fmt.Sprintf("r.Count(%q, len(%s))", f.Attr, v)
	case captureProto:
		return fmt.Sprintf("r.Proto(%q, %s)", f.Attr, v)
	case captureNamed:
		return fmt.Sprintf("r.Named(%q, %q, %s)", f.Attr, f.Label, v)
	}
	return fmt.Sprintf("r.Type(%q, %q)", f.Attr, f.Label)
}

// stored reports whether the capture keeps the parameter's value: a
// type-only record is a constant.
func (f captureField) stored() bool {
	switch f.Kind {
	case captureType, captureNone, captureDuration, captureTime:
		return false
	}
	return true
}

// methodCapture is one method's capture type: its stored fields and its
// recorder statements, in parameter order.
type methodCapture struct {
	TypeName string
	Fields   []captureField
}

// buildCapture classifies m's parameters. orig is the method as contract.go
// declares it (attribute names); scoped is the same method after
// withBodyScopedNames (field names, matching the wrapper's parameters).
// Returns nil when nothing is recorded.
func buildCapture(orig, scoped MethodDef, typeName string, cf *File, excluded noCapture) *methodCapture {
	mc := &methodCapture{TypeName: typeName}
	for i, p := range orig.Params {
		attr := concreteParamName(p, i)
		if excluded.excludes(p.Name) {
			continue
		}
		kind := classifyParam(p.Name, p.TypeExpr, cf.Imports, cf.PrimitiveAliases)
		if kind == captureNone {
			continue
		}
		typ := scoped.Params[i].TypeExpr
		label := typ
		if p.Variadic || strings.HasPrefix(typ, "...") {
			typ = "[]" + strings.TrimPrefix(typ, "...")
			label = typ
		}
		var base captureKind
		if kind == captureDigits {
			// The same type under a name that is not secret: its numeric kind.
			base = classifyParam("", p.TypeExpr, cf.Imports, cf.PrimitiveAliases)
		}
		mc.Fields = append(mc.Fields, captureField{
			Field: concreteParamName(scoped.Params[i], i),
			Attr:  attr,
			Type:  typ,
			Kind:  kind,
			Base:  base,
			Label: label,
		})
	}
	if len(mc.Fields) == 0 {
		return nil
	}
	return mc
}

// literal is the capture value the wrapper body builds: the type with every
// stored field, positionally.
func (mc *methodCapture) literal() string {
	if mc == nil {
		return "observe.NoArgs{}"
	}
	var vals []string
	for _, f := range mc.Fields {
		if f.stored() {
			vals = append(vals, f.Field)
		}
	}
	return mc.TypeName + "{" + strings.Join(vals, ", ") + "}"
}

// decl renders the capture type and its RecordForensics method.
func (mc *methodCapture) decl(method string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "// %s holds what %s's span records if the call fails or runs\n", mc.TypeName, method)
	b.WriteString("// slow, classified at generation time (forge:no-capture excludes a parameter).\n")
	var fields strings.Builder
	for _, f := range mc.Fields {
		if f.stored() {
			fmt.Fprintf(&fields, "\t%s %s\n", f.Field, f.Type)
		}
	}
	if fields.Len() == 0 {
		fmt.Fprintf(&b, "type %s struct{}\n\n", mc.TypeName)
	} else {
		fmt.Fprintf(&b, "type %s struct {\n%s}\n\n", mc.TypeName, fields.String())
	}
	fmt.Fprintf(&b, "func (a %s) RecordForensics(r *observe.ForensicRecorder) {\n", mc.TypeName)
	for _, f := range mc.Fields {
		b.WriteString("\t" + f.recordStmt() + "\n")
	}
	b.WriteString("}\n")
	return b.String()
}

// noCapture is one method's forge:no-capture exclusions.
type noCapture struct {
	all    bool
	params map[string]bool
}

func (n noCapture) excludes(param string) bool { return n.all || n.params[param] }

func (n *noCapture) merge(o noCapture) {
	n.all = n.all || o.all
	for p := range o.params {
		if n.params == nil {
			n.params = map[string]bool{}
		}
		n.params[p] = true
	}
}

// ParseNoCaptureComment reads one comment as a forge:no-capture marker:
// ok when it is one, with the parameters it names (none: every parameter).
// The name may be followed by nothing, `:` or whitespace; the parameters are
// separated by spaces or commas.
func ParseNoCaptureComment(raw string) (params []string, ok bool) {
	text := strings.TrimSpace(strings.TrimPrefix(raw, "//"))
	rest, found := strings.CutPrefix(text, NoCaptureMarker)
	if !found || rest != "" && rest[0] != ':' && rest[0] != ' ' && rest[0] != '\t' {
		return nil, false
	}
	rest = strings.TrimPrefix(strings.TrimSpace(rest), ":")
	return strings.FieldsFunc(rest, func(r rune) bool { return r == ' ' || r == ',' || r == '\t' }), true
}

// parseNoCapture reads forge:no-capture markers out of a method's comments.
func parseNoCapture(groups ...*ast.CommentGroup) noCapture {
	var out noCapture
	for _, g := range groups {
		if g == nil {
			continue
		}
		for _, c := range g.List {
			names, ok := ParseNoCaptureComment(c.Text)
			if !ok {
				continue
			}
			if len(names) == 0 {
				out.all = true
				continue
			}
			if out.params == nil {
				out.params = map[string]bool{}
			}
			for _, n := range names {
				out.params[n] = true
			}
		}
	}
	return out
}

// implScan is what the package's own source says about an interface: the
// file of each method's implementation and each method's forge:no-capture
// exclusions.
type implScan struct {
	files     map[string]string // method → absolute file
	noCapture map[string]noCapture
}

type methodDecl struct {
	file string
	doc  *ast.CommentGroup
}

// scanImplementation parses dir's non-test Go files. The implementing type is
// the one that declares every method of iface; when several do, the one a
// constructor returns (`return &T{…}`) wins. A method no single type can be
// tied to falls back to the one type that declares it, then to the interface.
// Generated files (*_gen.go) never count as an implementation — a mock
// implements every method too — but are read for the interface itself (a
// derived contract lives in contract_gen.go).
func scanImplementation(dir string, iface InterfaceDef) implScan {
	out := implScan{files: map[string]string{}, noCapture: map[string]noCapture{}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	byType := map[string]map[string]methodDecl{} // receiver type → method → decl
	byMethod := map[string][]string{}            // method → receiver types
	returned := map[string]bool{}                // types a constructor returns
	fset := token.NewFileSet()
	want := map[string]bool{}
	for _, m := range iface.Methods {
		want[m.Name] = true
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") && !strings.HasSuffix(e.Name(), "_test.go") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	for _, name := range files {
		path := filepath.Join(dir, name)
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		generated := strings.HasSuffix(name, "_gen.go")
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.GenDecl:
				out.addInterfaceDecl(d, iface.Name, path)
			case *ast.FuncDecl:
				if generated {
					continue
				}
				if d.Recv == nil {
					if d.Body != nil && d.Type.Results != nil {
						collectReturnedTypes(d.Body, returned)
					}
					continue
				}
				recv := receiverType(d.Recv)
				if recv == "" || !want[d.Name.Name] {
					continue
				}
				if byType[recv] == nil {
					byType[recv] = map[string]methodDecl{}
				}
				byType[recv][d.Name.Name] = methodDecl{file: path, doc: d.Doc}
				byMethod[d.Name.Name] = append(byMethod[d.Name.Name], recv)
			}
		}
	}

	impl := pickImplementation(byType, len(want), returned)
	for _, m := range iface.Methods {
		decl, ok := byType[impl][m.Name]
		if !ok && len(byMethod[m.Name]) == 1 {
			decl, ok = byType[byMethod[m.Name][0]][m.Name]
		}
		if !ok {
			continue
		}
		out.files[m.Name] = decl.file
		nc := out.noCapture[m.Name]
		nc.merge(parseNoCapture(decl.doc))
		out.noCapture[m.Name] = nc
	}
	return out
}

// addInterfaceDecl records, from a type declaration of the interface named
// name in path, each method's file (the first seen) and its forge:no-capture
// exclusions.
func (out implScan) addInterfaceDecl(d *ast.GenDecl, name, path string) {
	for _, s := range d.Specs {
		ts, ok := s.(*ast.TypeSpec)
		if !ok || ts.Name.Name != name {
			continue
		}
		it, ok := ts.Type.(*ast.InterfaceType)
		if !ok {
			continue
		}
		for _, fld := range it.Methods.List {
			for _, n := range fld.Names {
				if _, seen := out.files[n.Name]; !seen {
					out.files[n.Name] = path
				}
				nc := out.noCapture[n.Name]
				nc.merge(parseNoCapture(fld.Doc, fld.Comment))
				out.noCapture[n.Name] = nc
			}
		}
	}
}

// pickImplementation is the one type declaring all want methods, or, when
// several do, the one a constructor returns; "" when neither is unique.
func pickImplementation(byType map[string]map[string]methodDecl, want int, returned map[string]bool) string {
	var full []string
	for typ, methods := range byType {
		if len(methods) == want && want > 0 {
			full = append(full, typ)
		}
	}
	sort.Strings(full)
	switch {
	case len(full) == 1:
		return full[0]
	case len(full) > 1:
		var picked []string
		for _, typ := range full {
			if returned[typ] {
				picked = append(picked, typ)
			}
		}
		if len(picked) == 1 {
			return picked[0]
		}
	}
	return ""
}

// receiverType is the base type name of a method receiver: T for (s T),
// (s *T) and (s *T[K]).
func receiverType(fl *ast.FieldList) string {
	if fl == nil || len(fl.List) == 0 {
		return ""
	}
	t := fl.List[0].Type
	if s, ok := t.(*ast.StarExpr); ok {
		t = s.X
	}
	switch x := t.(type) {
	case *ast.IndexExpr:
		t = x.X
	case *ast.IndexListExpr:
		t = x.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// collectReturnedTypes records T for every `return &T{…}` / `return T{…}`.
func collectReturnedTypes(body *ast.BlockStmt, into map[string]bool) {
	ast.Inspect(body, func(n ast.Node) bool {
		ret, ok := n.(*ast.ReturnStmt)
		if !ok {
			return true
		}
		for _, r := range ret.Results {
			if u, ok := r.(*ast.UnaryExpr); ok && u.Op == token.AND {
				r = u.X
			}
			if cl, ok := r.(*ast.CompositeLit); ok {
				if id, ok := cl.Type.(*ast.Ident); ok {
					into[id.Name] = true
				}
			}
		}
		return true
	})
}

// codeFile renders path relative to the module root, with forward slashes;
// the base name when it cannot be made relative.
func codeFile(root, path string) string {
	if path == "" {
		return ""
	}
	if root != "" {
		if abs, err := filepath.Abs(path); err == nil {
			if rel, err := filepath.Rel(root, abs); err == nil && !strings.HasPrefix(rel, "..") {
				return filepath.ToSlash(rel)
			}
		}
	}
	return filepath.Base(path)
}
