package manifest

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/operators"
	gojsonschema "github.com/google/jsonschema-go/jsonschema"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/tmpl"
)

// A type is a value an integration defines, such as `github.repository`: a JSON
// Schema for ONE value, and optionally where a picker gets the values the
// caller can choose from. The options source is a read-only list action of the
// same manifest plus CEL projections from each item of its output, so naming a
// new type or a new list is data. Nothing here knows what GitHub is.
//
// The projections are checked at load against the action's declared output
// schema: a field the projection reads must be one the action says it returns.
// Without that a typo would be an empty picker found by a user.

var (
	// TypeRefPattern is a type reference: `<id>` within a manifest, or
	// `<integration>.<id>`.
	TypeRefPattern = regexp.MustCompile(`^([a-z][a-z0-9_]*\.)?[a-z][a-z0-9_]*$`)
	// TypeSearchPattern is options.search: "client" or "param:<name>".
	TypeSearchPattern = regexp.MustCompile(`^(client|param:[a-z][a-z0-9_]*)$`)
)

const (
	MaxTypes           = 100
	MaxTypeAnnotations = 20
	MaxTypeWarnings    = 10
	MaxTypeScope       = 5

	// SearchClient and SearchParamPrefix are the forms of options.search.
	SearchClient      = "client"
	SearchParamPrefix = "param:"

	// ItemsOutputField is where a list action returns its items.
	// options.items defaults to reading it.
	ItemsOutputField = "items"
)

// The variables the CEL of a type reads.
const (
	VarItem      = "item"
	VarOption    = "option"
	VarContext   = "context"
	VarOutput    = "output"
	VarScope     = "scope"
	VarQuery     = "query"
	VarPageToken = "page_token"
	VarValue     = "value"
)

// ContextActingIdentity is the one fact `context` carries: who the options are
// listed for, "owner" or "app".
const ContextActingIdentity = "acting_identity"

// TypeRef is the qualified reference of t: `<integration>.<id>`.
func TypeRef(m *reliantv1.IntegrationManifest, t *reliantv1.TypeSpec) string {
	return m.GetId() + "." + t.GetId()
}

// QualifyTypeRef is ref as `<integration>.<id>`: a bare id belongs to
// integration.
func QualifyTypeRef(integration, ref string) string {
	if ref == "" || strings.Contains(ref, ".") {
		return ref
	}
	return integration + "." + ref
}

// SplitTypeRef splits a qualified `<integration>.<id>`.
func SplitTypeRef(ref string) (integration, id string, ok bool) {
	integration, id, ok = strings.Cut(ref, ".")
	if !ok || !IDPattern.MatchString(integration) || !IDPattern.MatchString(id) {
		return "", "", false
	}
	return integration, id, true
}

// Type returns the manifest's type with id.
func Type(m *reliantv1.IntegrationManifest, id string) (*reliantv1.TypeSpec, bool) {
	for _, t := range m.GetTypes() {
		if t.GetId() == id {
			return t, true
		}
	}
	return nil, false
}

// Action returns the manifest's action with id.
func Action(m *reliantv1.IntegrationManifest, id string) (*reliantv1.ActionSpec, bool) {
	for _, a := range m.GetActions() {
		if a.GetId() == id {
			return a, true
		}
	}
	return nil, false
}

// ValueSchema is the JSON Schema of one value of t, a fresh map.
func ValueSchema(t *reliantv1.TypeSpec) map[string]any {
	if t.GetValue() == nil {
		return map[string]any{"type": "string"}
	}
	return t.GetValue().AsMap()
}

// validateTypes applies the load-time rules to every type of m and to the
// types its trigger attributes name.
func validateTypes(m *reliantv1.IntegrationManifest) error {
	if len(m.GetTypes()) > MaxTypes {
		return fmt.Errorf("types: at most %d", MaxTypes)
	}
	ids := map[string]bool{}
	for _, t := range m.GetTypes() {
		if ids[t.GetId()] {
			return fmt.Errorf("duplicate type id %q", t.GetId())
		}
		ids[t.GetId()] = true
	}
	for _, t := range m.GetTypes() {
		if err := validateType(m, t); err != nil {
			return fmt.Errorf("type %q: %w", t.GetId(), err)
		}
	}
	if err := checkScopeCycles(m); err != nil {
		return err
	}
	for _, trig := range m.GetTriggers() {
		for _, a := range trig.GetAttributes() {
			if a.GetType() == "" {
				continue
			}
			if err := checkLocalTypeRef(m, a.GetType()); err != nil {
				return fmt.Errorf("trigger %q: attribute %q: type: %w", trig.GetId(), a.GetName(), err)
			}
		}
	}
	return nil
}

// checkLocalTypeRef checks a type reference as far as m alone can: a bare id,
// or one qualified with m's own id, must name a type of m. A reference to
// another integration is CheckTypeRefs's, which sees every manifest.
func checkLocalTypeRef(m *reliantv1.IntegrationManifest, ref string) error {
	if !TypeRefPattern.MatchString(ref) {
		return fmt.Errorf("%q must match %s", ref, TypeRefPattern)
	}
	integration, id, qualified := strings.Cut(ref, ".")
	if !qualified {
		id = integration
	} else if integration != m.GetId() {
		return nil
	}
	if _, ok := Type(m, id); !ok {
		return fmt.Errorf("%q: %s declares no type %q (it declares: %s)", ref, m.GetId(), id, typeIDs(m))
	}
	return nil
}

func typeIDs(m *reliantv1.IntegrationManifest) string {
	ids := make([]string, 0, len(m.GetTypes()))
	for _, t := range m.GetTypes() {
		ids = append(ids, t.GetId())
	}
	if len(ids) == 0 {
		return "none"
	}
	return strings.Join(ids, ", ")
}

func validateType(m *reliantv1.IntegrationManifest, t *reliantv1.TypeSpec) error {
	if !IDPattern.MatchString(t.GetId()) {
		return fmt.Errorf("id must match %s", IDPattern)
	}
	if strings.ContainsAny(t.GetSummary(), "\r\n") || len(t.GetSummary()) > 200 {
		return fmt.Errorf("summary must be one line of at most 200 characters")
	}
	if err := validateKeywords("", t.GetKeywords()); err != nil {
		return err
	}
	if t.GetValue() == nil {
		return fmt.Errorf("value is required: the JSON Schema of one value of the type")
	}
	schema := t.GetValue().AsMap()
	if _, ok := schema["type"]; !ok {
		return fmt.Errorf("value must be a JSON Schema with a type")
	}
	if err := validateSchema("value", schema); err != nil {
		return err
	}
	if err := checkExamples(schema); err != nil {
		return fmt.Errorf("value: %w", err)
	}
	if t.GetOptions() == nil {
		return nil
	}
	if err := validateOptions(m, t.GetOptions()); err != nil {
		return fmt.Errorf("options: %w", err)
	}
	return nil
}

// checkExamples holds a value schema's examples to the schema: an example that
// is not a valid value misleads the placeholder an editor shows from it.
func checkExamples(schema map[string]any) error {
	examples, _ := schema["examples"].([]any)
	if len(examples) == 0 {
		return nil
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return err
	}
	var s gojsonschema.Schema
	if err := json.Unmarshal(raw, &s); err != nil {
		return err
	}
	resolved, err := s.Resolve(nil)
	if err != nil {
		return err
	}
	for i, ex := range examples {
		if err := resolved.Validate(ex); err != nil {
			return fmt.Errorf("examples[%d] %v is not a valid value: %w", i, ex, err)
		}
	}
	return nil
}

// optionsAction is the list action of a type's options with the facts the
// rules need.
type optionsAction struct {
	spec   *reliantv1.ActionSpec
	params map[string]any // the action's params schema
	props  map[string]any // params.properties
	output map[string]any // output.schema
}

func lookupListAction(m *reliantv1.IntegrationManifest, what, id string) (*optionsAction, error) {
	if id == "" {
		return nil, fmt.Errorf("%s.action is required: the read-only action that lists the values", what)
	}
	a, ok := Action(m, id)
	if !ok {
		return nil, fmt.Errorf("%s.action %q is not an action of %s", what, id, m.GetId())
	}
	if a.GetMutates() {
		return nil, fmt.Errorf("%s.action %q changes external state; a picker only reads, so it must be mutates: false", what, id)
	}
	if a.GetPlacement() == PlacementDaemon {
		return nil, fmt.Errorf("%s.action %q runs on a daemon; a picker is answered by the server, so it must be placement: server or any", what, id)
	}
	out := &optionsAction{spec: a, params: map[string]any{}}
	if a.GetParams() != nil {
		out.params = a.GetParams().AsMap()
	}
	out.props, _ = out.params["properties"].(map[string]any)
	if a.GetOutput().GetSchema() == nil {
		return nil, fmt.Errorf("%s.action %q declares no output.schema, so what its projections read cannot be checked", what, id)
	}
	out.output = a.GetOutput().GetSchema().AsMap()
	return out, nil
}

// checkParamsProvided holds the templates for an action's params to the
// action's schema: every key is a param it has, and every param it requires is
// given a value (by a template or by name in provided).
func (a *optionsAction) checkParamsProvided(what string, templates map[string]string, provided ...string) error {
	for k := range templates {
		if _, ok := a.props[k]; !ok {
			return fmt.Errorf("%s.params.%s: %s has no param %q (it has: %s)", what, k, a.spec.GetId(), k, sortedKeys(a.props))
		}
	}
	required, _ := a.params["required"].([]any)
	for _, r := range required {
		name, _ := r.(string)
		if _, ok := templates[name]; ok || slices.Contains(provided, name) {
			continue
		}
		return fmt.Errorf("%s: %s requires param %q and nothing supplies it; add it to %s.params", what, a.spec.GetId(), name, what)
	}
	return nil
}

func validateOptions(m *reliantv1.IntegrationManifest, o *reliantv1.TypeOptionsSpec) error {
	act, err := lookupListAction(m, "options", o.GetAction())
	if err != nil {
		return err
	}

	// scope
	if len(o.GetScope()) > MaxTypeScope {
		return fmt.Errorf("scope: at most %d", MaxTypeScope)
	}
	scopeKeys := make([]string, 0, len(o.GetScope()))
	for key, sc := range o.GetScope() {
		if !ParamNamePattern.MatchString(key) {
			return fmt.Errorf("scope key %q must match %s", key, ParamNamePattern)
		}
		if sc.GetType() == "" {
			return fmt.Errorf("scope.%s.type is required: the type of the value that narrows the list", key)
		}
		if err := checkLocalTypeRef(m, sc.GetType()); err != nil {
			return fmt.Errorf("scope.%s.type: %w", key, err)
		}
		scopeKeys = append(scopeKeys, key)
	}
	sort.Strings(scopeKeys)

	// search and paging name params the action must have
	var provided []string
	if s := o.GetSearch(); s != "" {
		if !TypeSearchPattern.MatchString(s) {
			return fmt.Errorf("search %q must be %q or %q<param>", s, SearchClient, SearchParamPrefix)
		}
		if name, ok := strings.CutPrefix(s, SearchParamPrefix); ok {
			if _, has := act.props[name]; !has {
				return fmt.Errorf("search: %s has no param %q to receive the query (it has: %s)", act.spec.GetId(), name, sortedKeys(act.props))
			}
			if _, set := o.GetParams()[name]; set {
				return fmt.Errorf("search: param %q receives the query, so params must not also set it", name)
			}
			provided = append(provided, name)
		}
	}
	if p := o.GetPaging(); p != nil {
		if p.GetNextTokenPath() == "" || p.GetTokenParam() == "" {
			return fmt.Errorf("paging needs next_token_path and token_param")
		}
		if _, has := act.props[p.GetTokenParam()]; !has {
			return fmt.Errorf("paging.token_param: %s has no param %q (it has: %s)", act.spec.GetId(), p.GetTokenParam(), sortedKeys(act.props))
		}
		if _, ok := resolvePath(act.output, strings.Split(p.GetNextTokenPath(), ".")); !ok {
			return fmt.Errorf("paging.next_token_path %q is not a field of %s's output", p.GetNextTokenPath(), act.spec.GetId())
		}
		provided = append(provided, p.GetTokenParam())
	}

	// params are templates over scope, query and page_token
	if err := act.checkParamsProvided("options", o.GetParams(), provided...); err != nil {
		return err
	}
	paramRoots := map[string]map[string]any{
		VarScope:     objectSchema(scopeKeys...),
		VarQuery:     {"type": "string"},
		VarPageToken: {"type": "string"},
	}
	for _, k := range sortedMapKeys(o.GetParams()) {
		exprs, err := tmpl.Exprs(o.GetParams()[k])
		if err != nil {
			return fmt.Errorf("params.%s: %w", k, err)
		}
		for _, e := range exprs {
			if err := checkCEL("params."+k, e, paramRoots); err != nil {
				return err
			}
		}
	}

	// items: where the list is, and what one item looks like
	itemSchema, err := itemSchemaOf(act, o.GetItems())
	if err != nil {
		return err
	}

	// the projections
	if o.GetValue() == "" {
		return fmt.Errorf("value is required: the CEL that yields the stored value from an item")
	}
	annotationNames := sortedMapKeys(o.GetAnnotations())
	if len(annotationNames) > MaxTypeAnnotations {
		return fmt.Errorf("annotations: at most %d", MaxTypeAnnotations)
	}
	for _, p := range []struct{ name, expr string }{
		{"value", o.GetValue()}, {"label", o.GetLabel()}, {"description", o.GetDescription()}, {"group", o.GetGroup()},
	} {
		if p.expr == "" {
			continue
		}
		if err := checkCEL(p.name, p.expr, map[string]map[string]any{VarItem: itemSchema}); err != nil {
			return err
		}
	}
	for _, name := range annotationNames {
		if !ParamNamePattern.MatchString(name) {
			return fmt.Errorf("annotations: name %q must match %s", name, ParamNamePattern)
		}
		if err := checkCEL("annotations."+name, o.GetAnnotations()[name], map[string]map[string]any{VarItem: itemSchema}); err != nil {
			return err
		}
	}
	if len(o.GetWarnings()) > MaxTypeWarnings {
		return fmt.Errorf("warnings: at most %d", MaxTypeWarnings)
	}
	warnRoots := map[string]map[string]any{
		VarItem: itemSchema, VarContext: contextSchema(), VarOption: optionSchema(annotationNames),
	}
	for i, w := range o.GetWarnings() {
		if w.GetWhen() == "" || w.GetMessage() == "" {
			return fmt.Errorf("warnings[%d] needs when and message", i)
		}
		if strings.ContainsAny(w.GetMessage(), "\r\n") || len(w.GetMessage()) > 200 {
			return fmt.Errorf("warnings[%d].message must be one line of at most 200 characters", i)
		}
		if err := checkCEL(fmt.Sprintf("warnings[%d].when", i), w.GetWhen(), warnRoots); err != nil {
			return err
		}
	}

	// resolve
	if r := o.GetResolve(); r != nil {
		if err := validateResolve(m, r); err != nil {
			return fmt.Errorf("resolve: %w", err)
		}
	}
	return nil
}

func validateResolve(m *reliantv1.IntegrationManifest, r *reliantv1.TypeResolveSpec) error {
	act, err := lookupListAction(m, "resolve", r.GetAction())
	if err != nil {
		return err
	}
	if err := act.checkParamsProvided("resolve", r.GetParams()); err != nil {
		return err
	}
	for _, k := range sortedMapKeys(r.GetParams()) {
		exprs, err := tmpl.Exprs(r.GetParams()[k])
		if err != nil {
			return fmt.Errorf("params.%s: %w", k, err)
		}
		for _, e := range exprs {
			if err := checkCEL("params."+k, e, map[string]map[string]any{VarValue: {"type": "string"}}); err != nil {
				return err
			}
		}
	}
	if r.GetLabel() == "" {
		return fmt.Errorf("label is required: the CEL that names the looked-up value")
	}
	roots := map[string]map[string]any{VarItem: act.output}
	for _, p := range []struct{ name, expr string }{{"label", r.GetLabel()}, {"description", r.GetDescription()}} {
		if p.expr == "" {
			continue
		}
		if err := checkCEL(p.name, p.expr, roots); err != nil {
			return err
		}
	}
	return nil
}

// itemSchemaOf checks options.items against the action's output and returns
// the schema of one item, nil when it cannot be known (an items expression
// that computes the list rather than reading it).
func itemSchemaOf(act *optionsAction, items string) (map[string]any, error) {
	expr := items
	if expr == "" {
		expr = VarOutput + "." + ItemsOutputField
		if _, ok := properties(act.output); !ok {
			return nil, nil
		}
	}
	roots := map[string]map[string]any{VarOutput: act.output}
	if err := checkCEL("items", expr, roots); err != nil {
		return nil, err
	}
	a, err := tmpl.Compile(expr, VarOutput)
	if err != nil {
		return nil, fmt.Errorf("items: %w", err)
	}
	root, path, ok := selectChain(a.NativeRep().Expr())
	if !ok || root != VarOutput {
		return nil, nil
	}
	list, ok := resolvePath(act.output, path)
	if !ok {
		return nil, nil
	}
	if !schemaIsType(list, "array") {
		return nil, fmt.Errorf("items: %s is not a list in %s's output.schema", expr, act.spec.GetId())
	}
	item, _ := list["items"].(map[string]any)
	return item, nil
}

// --- CEL field checking -------------------------------------------------------

// checkCEL compiles expr over the variables in roots (each dynamic) and
// checks that every field it reads off a variable whose schema is given is one
// the schema declares. A nil schema leaves that variable unchecked.
func checkCEL(what, expr string, roots map[string]map[string]any) error {
	names := make([]string, 0, len(roots))
	for n := range roots {
		names = append(names, n)
	}
	compiled, err := tmpl.Compile(expr, names...)
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	var problem error
	ast.PreOrderVisit(compiled.NativeRep().Expr(), ast.NewExprVisitor(func(e ast.Expr) {
		if problem != nil {
			return
		}
		root, path, ok := selectChain(e)
		if !ok || len(path) == 0 {
			return
		}
		schema := roots[root]
		if schema == nil {
			return
		}
		parent, ok := resolvePath(schema, path[:len(path)-1])
		if !ok {
			return
		}
		props, ok := properties(parent)
		if !ok {
			return
		}
		if field := path[len(path)-1]; props[field] == nil {
			problem = fmt.Errorf("%s: %s.%s: %s has no field %q (it has: %s)",
				what, root, strings.Join(path, "."), describeRoot(root, path[:len(path)-1]), field, sortedKeys(props))
		}
	}))
	return problem
}

func describeRoot(root string, path []string) string {
	switch root {
	case VarItem:
		if len(path) == 0 {
			return "an item of the action's output"
		}
	case VarOutput:
		if len(path) == 0 {
			return "the action's output"
		}
	case VarScope:
		return "the scope"
	case VarContext:
		return "the context"
	case VarOption:
		if len(path) == 0 {
			return "the option"
		}
	}
	return root + "." + strings.Join(path, ".")
}

// selectChain reads `root.a.b` and `root.?a.b` (and has(root.a)): the variable
// at the bottom of a chain of field selections and the fields, in order. ok is
// false for anything else.
func selectChain(e ast.Expr) (root string, path []string, ok bool) {
	switch e.Kind() {
	case ast.IdentKind:
		return e.AsIdent(), nil, true
	case ast.SelectKind:
		sel := e.AsSelect()
		root, path, ok = selectChain(sel.Operand())
		if !ok {
			return "", nil, false
		}
		return root, append(path, sel.FieldName()), true
	case ast.CallKind:
		call := e.AsCall()
		if call.FunctionName() != operators.OptSelect || len(call.Args()) != 2 || call.Args()[1].Kind() != ast.LiteralKind {
			return "", nil, false
		}
		field, isString := call.Args()[1].AsLiteral().Value().(string)
		if !isString {
			return "", nil, false
		}
		root, path, ok = selectChain(call.Args()[0])
		if !ok {
			return "", nil, false
		}
		return root, append(path, field), true
	}
	return "", nil, false
}

// properties is the declared fields of an object schema. ok is false for a
// schema that does not enumerate its fields, or that allows others.
func properties(schema map[string]any) (map[string]any, bool) {
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		return nil, false
	}
	if ap, set := schema["additionalProperties"]; set && ap != false {
		return nil, false
	}
	return props, true
}

// resolvePath follows fields from schema. ok is false when a step is not a
// declared field of a schema that enumerates its fields.
func resolvePath(schema map[string]any, path []string) (map[string]any, bool) {
	cur := schema
	for _, field := range path {
		props, ok := properties(cur)
		if !ok {
			return nil, false
		}
		next, ok := props[field].(map[string]any)
		if !ok {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

func schemaIsType(schema map[string]any, want string) bool {
	switch t := schema["type"].(type) {
	case string:
		return t == want
	case []any:
		for _, v := range t {
			if v == want {
				return true
			}
		}
	}
	return false
}

func objectSchema(fields ...string) map[string]any {
	props := make(map[string]any, len(fields))
	for _, f := range fields {
		props[f] = map[string]any{}
	}
	return map[string]any{"type": "object", "properties": props}
}

func contextSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{ContextActingIdentity: map[string]any{"type": "string"}}}
}

// optionSchema is the projection so far, as a warning's `option` sees it.
func optionSchema(annotations []string) map[string]any {
	str := map[string]any{"type": "string"}
	return map[string]any{"type": "object", "properties": map[string]any{
		"value": str, "label": str, "description": str, "group": str,
		"annotations": objectSchema(annotations...),
	}}
}

func sortedKeys(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return "none"
	}
	return strings.Join(keys, ", ")
}

func sortedMapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// checkScopeCycles refuses types whose scopes lead back to themselves: no
// value could be chosen first.
func checkScopeCycles(m *reliantv1.IntegrationManifest) error {
	edges := map[string][]string{}
	for _, t := range m.GetTypes() {
		for _, sc := range t.GetOptions().GetScope() {
			integration, id, qualified := strings.Cut(sc.GetType(), ".")
			if !qualified {
				id = integration
			} else if integration != m.GetId() {
				continue
			}
			edges[t.GetId()] = append(edges[t.GetId()], id)
		}
	}
	const (
		visiting = 1
		done     = 2
	)
	state := map[string]int{}
	var visit func(id string, trail []string) error
	visit = func(id string, trail []string) error {
		switch state[id] {
		case visiting:
			return fmt.Errorf("type scopes form a cycle: %s", strings.Join(append(trail, id), " -> "))
		case done:
			return nil
		}
		state[id] = visiting
		for _, next := range edges[id] {
			if err := visit(next, append(trail, id)); err != nil {
				return err
			}
		}
		state[id] = done
		return nil
	}
	ids := make([]string, 0, len(edges))
	for id := range edges {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := visit(id, nil); err != nil {
			return err
		}
	}
	return nil
}

// CheckTypeRefs checks what no single manifest can: every type reference to
// another integration (a scope's type, a trigger attribute's type) names a
// type some loaded manifest declares.
func CheckTypeRefs(ms []*reliantv1.IntegrationManifest) error {
	declared := map[string]bool{}
	for _, m := range ms {
		for _, t := range m.GetTypes() {
			declared[TypeRef(m, t)] = true
		}
	}
	check := func(m *reliantv1.IntegrationManifest, where, ref string) error {
		qualified := QualifyTypeRef(m.GetId(), ref)
		if !declared[qualified] {
			return fmt.Errorf("manifest %q: %s: no integration declares the type %q", m.GetId(), where, ref)
		}
		return nil
	}
	for _, m := range ms {
		for _, t := range m.GetTypes() {
			for key, sc := range t.GetOptions().GetScope() {
				if err := check(m, fmt.Sprintf("type %q: scope.%s.type", t.GetId(), key), sc.GetType()); err != nil {
					return err
				}
			}
		}
		for _, trig := range m.GetTriggers() {
			for _, a := range trig.GetAttributes() {
				if a.GetType() == "" {
					continue
				}
				if err := check(m, fmt.Sprintf("trigger %q: attribute %q: type", trig.GetId(), a.GetName()), a.GetType()); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
