// Copyright (c) 2025 Reliant Labs
package triggerspec

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common"
	celast "github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/operators"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"

	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
)

// CompileFilter and friends check an expression against a DYNAMIC `trigger`:
// every field read type-checks, so `l.name` over a list of label names
// compiles and then fails on every event that has a label. When the source's
// payload is declared — an integration's manifest gives the JSON Schema of
// trigger.payload for each event (manifest.TriggerPayloadSchema) — a Shape
// types `trigger` with it, and the CEL checker rejects reads the payload can
// never satisfy.
//
// The JSON Schema maps onto CEL types as follows:
//   - an object with properties is a CEL object type: its declared fields
//     are typed, and an undeclared field is dyn — JSON Schema objects are
//     open unless additionalProperties is false, which closes it;
//   - an object with no properties, or whose properties all share the type
//     additionalProperties gives (event attributes), is a map, so index
//     syntax keeps working;
//   - an array is a list of its items' type;
//   - a string or boolean is NULLABLE: a provider's JSON can carry null in
//     any field, and `x != null` must not be a type error;
//   - integer and number are dyn: a payload number is an int64 when the
//     filter runs on the event as received, and a double once the event has
//     round-tripped through storage, so neither static type is true.
//
// A Shape is for checking only. Evaluation always uses the untyped program
// CompileFilter builds, so a Shape can only add findings, never change what
// runs.

// TypeError is an expression that reads `trigger` in a way its declared shape
// rules out.
type TypeError struct {
	// Expr is the expression checked.
	Expr string
	// Reason names the offending read, the field it reads and that field's
	// declared type.
	Reason string
	// Suggestion is how to write it instead, when there is a likely fix.
	Suggestion string
}

func (e *TypeError) Error() string { return e.Reason }

// Shape is the declared type of the `trigger` root for a trigger whose
// payload has a schema.
type Shape struct {
	env  *cel.Env
	root *shapeNode
}

// NewShape types `trigger` for a source whose events deliver payloads with
// these JSON Schemas (trigger.payload, envelope included). Several schemas —
// a trigger on more than one event — are their union: a field any event
// declares may be read, and a field the events declare with different types
// is dyn. With no schema the payload is dyn; the rest of the root is still
// typed.
func NewShape(payloadSchemas ...map[string]any) (*Shape, error) {
	var payload map[string]any
	for _, s := range payloadSchemas {
		payload = mergeSchemas(payload, s)
	}
	if payload == nil {
		payload = map[string]any{}
	}
	b := &shapeBuilder{objects: map[string]*shapeNode{}}
	root := b.build(string(wfcel.CELTrigger), rootSchema(payload))

	// The filter environment without its dynamic `trigger`, which is
	// declared below with the shape's type.
	base, err := wfcel.NewEnv(triggerEnvConfig())
	if err != nil {
		return nil, fmt.Errorf("build trigger shape environment: %w", err)
	}
	provider := &shapeProvider{base: base.CELTypeProvider(), objects: b.objects}
	env, err := base.Extend(
		cel.CustomTypeProvider(provider),
		cel.Variable(string(wfcel.CELTrigger), root.celType),
	)
	if err != nil {
		return nil, fmt.Errorf("build trigger shape environment: %w", err)
	}
	return &Shape{env: env, root: root}, nil
}

// rootSchema is `trigger` as runtime.TriggerInfo.CELValue builds it, with
// payload's schema in place. It is closed: CELValue sets exactly these keys,
// so any other is a misspelling. TestFilterRootMatchesTheDeclaredShape (in
// package triggers) keeps the two in step.
func rootSchema(payload map[string]any) map[string]any {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"kind":          str("What fired the run: integration, webhook, schedule, workflow_event or manual."),
			"trigger_id":    str("The activated trigger's id."),
			"event_id":      str("The recorded event's id."),
			"occurred_at":   str("When the event happened, RFC 3339."),
			"scheduled_for": str("The slot a schedule fired for; otherwise occurred_at."),
			"name":          str("The trigger's name."),
			"payload":       payload,
			"sender": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"kind":         str("How the sender was identified."),
					"id":           str("The sender's stable id at the source."),
					"display_name": str("The sender's display name."),
					"verified":     map[string]any{"type": "boolean"},
				},
			},
		},
	}
}

// CheckFilter type-checks a filter: every read must fit the shape, and the
// result must be a bool. Syntax and unknown-root errors are CompileFilter's
// to report, so an expression that does not parse returns nothing here.
func (s *Shape) CheckFilter(expr string) []*TypeError {
	checked, errs := s.check(expr)
	if len(errs) > 0 || checked == nil {
		return errs
	}
	switch out := checked.OutputType(); {
	case out.IsExactType(cel.BoolType), out.IsExactType(cel.DynType), out.IsExactType(types.NewNullableType(types.BoolType)):
		return nil
	default:
		return []*TypeError{{Expr: expr, Reason: fmt.Sprintf("must evaluate to a bool, not %s", s.formatCELType(out))}}
	}
}

// CheckExpr type-checks one expression of a template ({{ }} contents).
func (s *Shape) CheckExpr(expr string) []*TypeError {
	_, errs := s.check(expr)
	return errs
}

func (s *Shape) check(expr string) (*cel.Ast, []*TypeError) {
	parsed, iss := s.env.Parse(expr)
	if iss != nil && iss.Err() != nil {
		return nil, nil
	}
	checked, iss := s.env.Check(parsed)
	if iss == nil || iss.Err() == nil {
		return checked, nil
	}
	reads := map[int64]shapeRead{}
	s.resolve(parsed.NativeRep().Expr(), nil, reads)
	var out []*TypeError
	seen := map[string]bool{}
	for _, e := range iss.Errors() {
		te := s.explain(expr, e, reads)
		if !seen[te.Reason] {
			seen[te.Reason] = true
			out = append(out, te)
		}
	}
	return nil, out
}

// --- explaining a checker error ------------------------------------------

// shapeRead is one field read in an expression: `operand.field`.
type shapeRead struct {
	text    string     // the read as written, e.g. "l.name"; "" when not expressible
	field   string     // the field read
	operand *shapeNode // what it is read from; nil when not resolvable
	// operandText is the operand as written, e.g. "l".
	operandText string
}

func (s *Shape) explain(expr string, e *common.Error, reads map[int64]shapeRead) *TypeError {
	read, ok := reads[e.ExprID]
	if ok && read.operand != nil {
		switch {
		case strings.Contains(e.Message, "does not support field selection"):
			return readOfNonObject(expr, read)
		case strings.Contains(e.Message, "undefined field"):
			return readOfUndeclaredField(expr, read)
		}
	}
	reason := s.rewriteTypeNames(e.Message)
	if ok {
		// A read whose operand the walk could not place (the result of a
		// function or a macro): the checker's own words, said of the read.
		what := read.text
		if what == "" {
			what = "field '" + read.field + "'"
		}
		reason = what + ": " + reason
	}
	return &TypeError{Expr: expr, Reason: reason}
}

func readOfNonObject(expr string, r shapeRead) *TypeError {
	op := r.operand
	what := r.text
	if what == "" {
		what = "a read"
	}
	if list := op.elemOf; list != nil {
		subject := "an element of " + list.path
		if r.operandText != "" && r.operandText != list.path && !strings.HasPrefix(r.operandText, list.path+"[") {
			subject = r.operandText + ", an element of " + list.path
		}
		return &TypeError{
			Expr: expr,
			Reason: fmt.Sprintf("%s reads field '%s' of %s; %s is %s%s, so each element is a %s, which has no field '%s'",
				what, r.field, subject, list.path, list.format(), list.describe(), op.format(), r.field),
			Suggestion: fmt.Sprintf("compare the element itself, e.g. %s.exists(x, x == '...') or '...' in %s", list.path, list.path),
		}
	}
	suggestion := fmt.Sprintf("compare %s itself", op.path)
	if op.kind == "list" {
		suggestion = fmt.Sprintf("test its elements, e.g. '...' in %s", op.path)
	}
	return &TypeError{
		Expr: expr,
		Reason: fmt.Sprintf("%s reads field '%s' of %s, which is %s%s and has no field '%s'",
			what, r.field, op.path, op.format(), op.describe(), r.field),
		Suggestion: suggestion,
	}
}

func readOfUndeclaredField(expr string, r shapeRead) *TypeError {
	op := r.operand
	return &TypeError{
		Expr:       expr,
		Reason:     fmt.Sprintf("%s has no field '%s' (its fields: %s)", op.path, r.field, strings.Join(op.fieldNames(), ", ")),
		Suggestion: "read one of the fields listed",
	}
}

// rewriteTypeNames turns the checker's internal type names back into the
// payload's: a nullable string is a string, an object is named by its path.
func (s *Shape) rewriteTypeNames(msg string) string {
	msg = strings.NewReplacer("wrapper(string)", "string", "wrapper(bool)", "bool").Replace(msg)
	return strings.ReplaceAll(msg, shapeTypePrefix, "object ")
}

func (s *Shape) formatCELType(t *cel.Type) string { return s.rewriteTypeNames(t.String()) }

// resolve walks expr, recording every field read with what it reads from.
// scope binds the comprehension variables in effect (exists, all, map, ...)
// to the shape of the element they range over.
func (s *Shape) resolve(e celast.Expr, scope map[string]*shapeNode, reads map[int64]shapeRead) *shapeNode {
	switch e.Kind() {
	case celast.IdentKind:
		name := e.AsIdent()
		if n, ok := scope[name]; ok {
			return n
		}
		if name == string(wfcel.CELTrigger) {
			return s.root
		}
		return nil
	case celast.SelectKind:
		sel := e.AsSelect()
		op := s.resolve(sel.Operand(), scope, reads)
		reads[e.ID()] = shapeRead{text: exprText(e), field: sel.FieldName(), operand: op, operandText: exprText(sel.Operand())}
		if sel.IsTestOnly() {
			return nil
		}
		return op.child(sel.FieldName())
	case celast.CallKind:
		call := e.AsCall()
		if call.IsMemberFunction() {
			s.resolve(call.Target(), scope, reads)
		}
		args := make([]*shapeNode, len(call.Args()))
		for i, a := range call.Args() {
			args[i] = s.resolve(a, scope, reads)
		}
		switch call.FunctionName() {
		case operators.OptSelect:
			field, ok := stringLiteral(call.Args()[1])
			if !ok {
				return nil
			}
			reads[e.ID()] = shapeRead{text: exprText(e), field: field, operand: args[0], operandText: exprText(call.Args()[0])}
			return args[0].child(field)
		case operators.Index, operators.OptIndex:
			return args[0].item()
		}
		return nil
	case celast.ComprehensionKind:
		c := e.AsComprehension()
		rng := s.resolve(c.IterRange(), scope, reads)
		s.resolve(c.AccuInit(), scope, reads)
		inner := make(map[string]*shapeNode, len(scope)+3)
		for k, v := range scope {
			inner[k] = v
		}
		inner[c.AccuVar()] = nil
		if c.HasIterVar2() {
			// Two-variable forms bind an index or key and a value.
			inner[c.IterVar()] = nil
			inner[c.IterVar2()] = rng.item()
		} else {
			inner[c.IterVar()] = rng.iterated()
		}
		s.resolve(c.LoopCondition(), inner, reads)
		s.resolve(c.LoopStep(), inner, reads)
		s.resolve(c.Result(), inner, reads)
		return nil
	case celast.ListKind:
		for _, el := range e.AsList().Elements() {
			s.resolve(el, scope, reads)
		}
	case celast.MapKind:
		for _, ent := range e.AsMap().Entries() {
			me := ent.AsMapEntry()
			s.resolve(me.Key(), scope, reads)
			s.resolve(me.Value(), scope, reads)
		}
	case celast.StructKind:
		for _, f := range e.AsStruct().Fields() {
			s.resolve(f.AsStructField().Value(), scope, reads)
		}
	}
	return nil
}

// exprText renders a field-read chain as written: idents, selections
// (has(x.y) renders as x.y) and literal indexes. Anything else renders as "".
func exprText(e celast.Expr) string {
	switch e.Kind() {
	case celast.IdentKind:
		return e.AsIdent()
	case celast.SelectKind:
		sel := e.AsSelect()
		if op := exprText(sel.Operand()); op != "" {
			return op + "." + sel.FieldName()
		}
	case celast.CallKind:
		call := e.AsCall()
		if len(call.Args()) != 2 {
			return ""
		}
		op := exprText(call.Args()[0])
		if op == "" {
			return ""
		}
		switch call.FunctionName() {
		case operators.OptSelect:
			if f, ok := stringLiteral(call.Args()[1]); ok {
				return op + ".?" + f
			}
		case operators.Index, operators.OptIndex:
			if call.Args()[1].Kind() == celast.LiteralKind {
				q := ""
				if call.FunctionName() == operators.OptIndex {
					q = "?"
				}
				return op + "[" + q + literalText(call.Args()[1].AsLiteral()) + "]"
			}
		}
	}
	return ""
}

func stringLiteral(e celast.Expr) (string, bool) {
	if e.Kind() != celast.LiteralKind {
		return "", false
	}
	s, ok := e.AsLiteral().Value().(string)
	return s, ok
}

func literalText(v ref.Val) string {
	if s, ok := v.Value().(string); ok {
		return strconv.Quote(s)
	}
	return fmt.Sprint(v.Value())
}

// --- the shape tree ------------------------------------------------------

// shapeTypePrefix names the CEL object types a Shape declares. The colon
// cannot appear in a CEL identifier, so no expression can resolve one of
// these names as a qualified type (`trigger.payload` must stay a field read).
const shapeTypePrefix = "trigger_shape:"

// shapeNode is one position in the declared `trigger` tree.
type shapeNode struct {
	path    string // as an expression reads it; a list element's is its list's + "[]"
	kind    string // object, map, list, string, boolean, integer, number or any
	desc    string
	celType *types.Type
	fields  map[string]*shapeNode // declared properties (object, map)
	extra   *shapeNode            // undeclared properties (object) or values (map); nil when closed
	elem    *shapeNode            // list items
	elemOf  *shapeNode            // set on a list's element: the list
}

// child is the shape of field name; nil when unknown (dyn).
func (n *shapeNode) child(name string) *shapeNode {
	if n == nil {
		return nil
	}
	if f, ok := n.fields[name]; ok {
		return f
	}
	if n.kind == "object" || n.kind == "map" {
		return n.extra
	}
	return nil
}

// item is the shape of n[i]: a list's element or a map's value.
func (n *shapeNode) item() *shapeNode {
	if n == nil {
		return nil
	}
	switch n.kind {
	case "list":
		return n.elem
	case "map":
		return n.extra
	}
	return nil
}

// iterated is what a one-variable comprehension over n binds: a list's
// element or a map's key.
func (n *shapeNode) iterated() *shapeNode {
	if n == nil {
		return nil
	}
	if n.kind == "list" {
		return n.elem
	}
	return nil
}

func (n *shapeNode) fieldNames() []string {
	names := make([]string, 0, len(n.fields))
	for k := range n.fields {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// format is the node's type as a message names it: string, list(string),
// object, map(string, string).
func (n *shapeNode) format() string {
	if n == nil {
		return "any"
	}
	switch n.kind {
	case "list":
		return "list(" + n.elem.format() + ")"
	case "map":
		return "map(string, " + n.extra.format() + ")"
	}
	return n.kind
}

// describe is the schema's description, parenthesized, when it has one.
func (n *shapeNode) describe() string {
	d := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(n.desc), "."))
	if d == "" {
		return ""
	}
	return " (" + d + ")"
}

type shapeBuilder struct {
	objects map[string]*shapeNode
}

func (b *shapeBuilder) build(path string, schema map[string]any) *shapeNode {
	n := &shapeNode{path: path, kind: schemaType(schema)}
	n.desc, _ = schema["description"].(string)
	switch n.kind {
	case "string":
		n.celType = types.NewNullableType(types.StringType)
	case "boolean":
		n.celType = types.NewNullableType(types.BoolType)
	case "integer", "number":
		n.celType = types.DynType
	case "array":
		n.kind = "list"
		items, _ := schema["items"].(map[string]any)
		n.elem = b.build(path+"[]", items)
		n.elem.elemOf = n
		n.celType = types.NewListType(n.elem.celType)
	case "object":
		b.buildObject(n, schema)
	default:
		n.kind = "any"
		n.celType = types.DynType
	}
	return n
}

func (b *shapeBuilder) buildObject(n *shapeNode, schema map[string]any) {
	props, _ := schema["properties"].(map[string]any)
	n.fields = make(map[string]*shapeNode, len(props))
	for name, raw := range props {
		prop, _ := raw.(map[string]any)
		n.fields[name] = b.build(n.path+"."+name, prop)
	}
	switch ap := schema["additionalProperties"].(type) {
	case bool:
		if ap {
			n.extra = b.build(n.path+".*", nil)
		}
	case map[string]any:
		n.extra = b.build(n.path+".*", ap)
	default:
		n.extra = b.build(n.path+".*", nil)
	}
	if len(props) == 0 || (n.extra != nil && uniform(n.fields, n.extra)) {
		n.kind = "map"
		if n.extra == nil {
			n.extra = b.build(n.path+".*", nil)
		}
		n.celType = types.NewMapType(types.StringType, n.extra.celType)
		return
	}
	n.kind = "object"
	name := shapeTypePrefix + n.path
	b.objects[name] = n
	n.celType = types.NewObjectType(name)
}

// uniform reports whether every declared field is a scalar of extra's type,
// so the object is a map with documented keys (an event's attributes).
func uniform(fields map[string]*shapeNode, extra *shapeNode) bool {
	switch extra.kind {
	case "string", "boolean":
	default:
		return false
	}
	for _, f := range fields {
		if f.kind != extra.kind {
			return false
		}
	}
	return true
}

// schemaType is a schema's single non-null type, inferred from properties
// or items when "type" is absent; "" when it has none or several.
func schemaType(schema map[string]any) string {
	var named []string
	switch t := schema["type"].(type) {
	case string:
		named = []string{t}
	case []any:
		for _, v := range t {
			if s, ok := v.(string); ok {
				named = append(named, s)
			}
		}
	case []string:
		named = t
	}
	var nonNull []string
	for _, t := range named {
		if t != "null" {
			nonNull = append(nonNull, t)
		}
	}
	switch {
	case len(nonNull) == 1:
		return nonNull[0]
	case len(nonNull) > 1:
		return ""
	}
	if _, ok := schema["properties"]; ok {
		return "object"
	}
	if _, ok := schema["items"]; ok {
		return "array"
	}
	return ""
}

// mergeSchemas is the union of two payload schemas: what an expression may
// read when the event is either one. Properties are unioned; a property the
// two type differently is unconstrained.
func mergeSchemas(a, b map[string]any) map[string]any {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	}
	ta, tb := schemaType(a), schemaType(b)
	if ta != tb {
		if isNumeric(ta) && isNumeric(tb) {
			return map[string]any{"type": "number"}
		}
		return map[string]any{}
	}
	out := map[string]any{"type": ta}
	if da, _ := a["description"].(string); da != "" && da == b["description"] {
		out["description"] = da
	}
	switch ta {
	case "object":
		pa, _ := a["properties"].(map[string]any)
		pb, _ := b["properties"].(map[string]any)
		props := make(map[string]any, len(pa)+len(pb))
		for k, v := range pa {
			props[k] = v
		}
		for k, v := range pb {
			va, _ := props[k].(map[string]any)
			vb, _ := v.(map[string]any)
			props[k] = mergeSchemas(va, vb)
		}
		out["properties"] = props
		if closed(a) && closed(b) {
			out["additionalProperties"] = false
		} else if aa, ok := a["additionalProperties"].(map[string]any); ok {
			if ab, ok := b["additionalProperties"].(map[string]any); ok {
				out["additionalProperties"] = mergeSchemas(aa, ab)
			}
		}
	case "array":
		ia, _ := a["items"].(map[string]any)
		ib, _ := b["items"].(map[string]any)
		if ia != nil && ib != nil {
			out["items"] = mergeSchemas(ia, ib)
		}
	}
	return out
}

func closed(schema map[string]any) bool {
	ap, ok := schema["additionalProperties"].(bool)
	return ok && !ap
}

func isNumeric(t string) bool { return t == "integer" || t == "number" }

// --- the CEL type provider ----------------------------------------------

// shapeProvider resolves the object types a Shape declares and defers every
// other name to the environment's own provider.
type shapeProvider struct {
	base    types.Provider
	objects map[string]*shapeNode
}

func (p *shapeProvider) EnumValue(name string) ref.Val { return p.base.EnumValue(name) }

func (p *shapeProvider) FindIdent(name string) (ref.Val, bool) { return p.base.FindIdent(name) }

func (p *shapeProvider) NewValue(name string, fields map[string]ref.Val) ref.Val {
	return p.base.NewValue(name, fields)
}

func (p *shapeProvider) FindStructType(name string) (*types.Type, bool) {
	if _, ok := p.objects[name]; ok {
		return types.NewTypeTypeWithParam(types.NewObjectType(name)), true
	}
	return p.base.FindStructType(name)
}

func (p *shapeProvider) FindStructFieldNames(name string) ([]string, bool) {
	if n, ok := p.objects[name]; ok {
		return n.fieldNames(), true
	}
	return p.base.FindStructFieldNames(name)
}

func (p *shapeProvider) FindStructFieldType(name, field string) (*types.FieldType, bool) {
	n, ok := p.objects[name]
	if !ok {
		return p.base.FindStructFieldType(name, field)
	}
	f := n.child(field)
	if f == nil {
		return nil, false
	}
	// Checking only: a Shape never evaluates, so nothing reads a value
	// through these.
	return &types.FieldType{
		Type:    f.celType,
		IsSet:   func(any) bool { return false },
		GetFrom: func(any) (any, error) { return nil, fmt.Errorf("trigger shape %s is for type-checking only", name) },
	}, true
}
