// Copyright (c) 2025 Reliant Labs
package geminiwire

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/invopop/jsonschema"
	"github.com/reliant-labs/reliant/internal/logging"
	"google.golang.org/genai"
)

// Tool schemas: JSON Schema in, Gemini's Schema subset out.
//
// Gemini's FunctionDeclaration.parameters accepts only a subset of OpenAPI 3.0
// (genai.Schema): type, format, title, description, nullable, enum, items,
// properties, required, anyOf, the numeric/length/count bounds, pattern,
// default and example. Reliant's tools describe their parameters in JSON
// Schema, and MCP servers hand over whatever their framework emits (pydantic
// and zod both use $ref/$defs, type arrays and oneOf). Every Gemini transport
// used to copy only type/description/enum/items/properties/required, so:
//
//   - an optional field typed ["string","null"] or anyOf [X, {type:null}]
//     reached Gemini with no type at all, and lost the fact that it is
//     nullable;
//   - anyOf/oneOf unions vanished, leaving an untyped node;
//   - a $ref left an empty node where the referenced object should be;
//   - required named properties that were never declared, which Gemini
//     rejects with a 400 for the ENTIRE request (hermes-agent
//     agent/gemini_schema.py: "one bad tool schema fails the ENTIRE request.
//     MCP servers routinely emit `required` without `properties`").
//
// NormalizeJSONSchema rewrites a JSON Schema into the shape that subset can
// represent, still spelled as JSON Schema, so the Reliant gateway (which
// forwards JSON Schema to LiteLLM) can share it. ToolParameters goes the last
// step to a genai.Schema for the transports that speak Gemini natively. The
// rules follow hermes-agent's legacy-`parameters` translator
// (agent/gemini_schema.py sanitize_gemini_schema: allowlist, type arrays to
// nullable/anyOf with structural keys moved onto the typed branch, enum
// entries stringified, required pruned) and its $ref inliner
// (_inline_refs: same-document pointers, sibling keys win, circular refs and
// an expansion budget cut), plus opencode's draft-04 exclusive-bound handling
// (packages/ai/src/protocols/utils/gemini-json-schema.ts).
//
// opencode and hermes both prefer `parametersJsonSchema`, which takes full
// JSON Schema, on the Gemini API. It is not used here: the Antigravity
// (cloudcode) backend's support for it is unverified, and one shared
// converter for all three transports is what keeps them from drifting.

// maxRefExpansions bounds $ref inlining. Real tool schemas hold a handful of
// references; the cap stops a model graph that references itself through
// many paths from expanding exponentially (hermes-agent uses the same 256).
const maxRefExpansions = 256

// maxSchemaDepth bounds nesting, so a pathological schema cannot recurse
// without end even when no single $ref repeats on the path.
const maxSchemaDepth = 64

// stringFormats, integerFormats and numberFormats are the only formats the
// Gemini API documents for each type (ai.google.dev Schema.format: "for
// NUMBER type: float, double; for INTEGER type: int32, int64; for STRING
// type: enum, date-time"). Any other format (uri, email, uuid, …) is dropped
// rather than sent.
var (
	stringFormats  = map[string]bool{"enum": true, "date-time": true}
	integerFormats = map[string]bool{"int32": true, "int64": true}
	numberFormats  = map[string]bool{"float": true, "double": true}
)

// structuralKeys are the keys that belong to one type. When a type array
// (["array","null"] or ["string","object"]) becomes an anyOf, they move onto
// the branch of their type: Gemini requires items/properties on the typed
// node itself, not a typeless parent, and a bound or pattern left on that
// parent would constrain nothing. A key several types share (minimum on
// integer and number, format, enum) is copied onto each of their branches.
var structuralKeys = map[string][]string{
	"array":   {"items", "minItems", "maxItems"},
	"object":  {"properties", "required", "minProperties", "maxProperties"},
	"string":  {"minLength", "maxLength", "pattern", "format", "enum"},
	"integer": {"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "format", "enum"},
	"number":  {"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "format", "enum"},
}

var geminiTypes = map[string]genai.Type{
	"string":  genai.TypeString,
	"number":  genai.TypeNumber,
	"integer": genai.TypeInteger,
	"boolean": genai.TypeBoolean,
	"array":   genai.TypeArray,
	"object":  genai.TypeObject,
}

// paramSchemaTool is what ToolParameters needs from a tool. Declared here, at
// the consumer, so this leaf package does not import internal/llm/tools.
type paramSchemaTool interface {
	ParamSchema() *jsonschema.Schema
}

// schemaMapTool is implemented by tools that hold their raw JSON Schema (MCP
// and workflow-native tools). The raw map is preferred: the invopop model
// cannot represent a type array, so a round trip through it loses exactly
// the shapes this converter exists to translate.
type schemaMapTool interface {
	ParamSchemaMap() map[string]any
}

// ToolSchemaMap returns a tool's parameter schema as a JSON Schema map.
func ToolSchemaMap(tool paramSchemaTool) map[string]any {
	if provider, ok := tool.(schemaMapTool); ok {
		if raw := provider.ParamSchemaMap(); raw != nil {
			return raw
		}
	}
	return JSONSchemaMap(tool.ParamSchema())
}

// JSONSchemaMap converts a typed schema to a JSON Schema map, or nil.
func JSONSchemaMap(schema *jsonschema.Schema) map[string]any {
	if schema == nil {
		return nil
	}
	data, err := json.Marshal(schema)
	if err != nil {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil
	}
	return out
}

// ToolParameters is a tool's FunctionDeclaration.parameters.
//
// The root is always an OBJECT, as a function's parameters must be: a schema
// with no type gets one, and a root of any other type (which no tool should
// have) is replaced with an empty object rather than sent to fail the whole
// request.
func ToolParameters(tool paramSchemaTool) *genai.Schema {
	return ParametersSchema(ToolSchemaMap(tool))
}

// ParametersSchema converts a JSON Schema map to a FunctionDeclaration's
// parameters. See ToolParameters.
func ParametersSchema(raw map[string]any) *genai.Schema {
	normalized := NormalizeJSONSchema(raw)
	if t, _ := normalized["type"].(string); t != "object" {
		if t != "" || normalized["anyOf"] != nil {
			logging.Warn("Tool parameters are not an object schema; declaring the tool with no parameters",
				"type", t, "hasAnyOf", normalized["anyOf"] != nil)
			return &genai.Schema{Type: genai.TypeObject}
		}
		normalized["type"] = "object"
	}
	return GenAISchema(normalized)
}

// NormalizeJSONSchema returns a copy of schema rewritten into the JSON Schema
// shapes Gemini's Schema subset can represent. The input is not modified.
//
//   - $ref to the same document ("#/$defs/X", "#/definitions/X", any "#/…"
//     pointer) is inlined, sibling keys winning; a reference back into its own
//     expansion is cut to {"type":"object"}, and an unresolvable one leaves an
//     untyped node.
//   - allOf is merged into its parent; oneOf becomes anyOf.
//   - A "null" type, in a type array or as an anyOf branch, becomes
//     nullable: true. One remaining type becomes the type; several become an
//     anyOf with each type's structural keywords on its branch. An anyOf left
//     with a single branch is merged into its parent.
//   - format survives only where Gemini documents it (see stringFormats).
//   - enum entries become strings (Gemini requires string enums even on
//     INTEGER/NUMBER/BOOLEAN; the type stays as declared), null entries
//     become nullable, and const becomes a one-entry enum.
//   - Tuple items / prefixItems become an items union; draft-04 boolean
//     exclusiveMinimum/Maximum keep their bound, numeric ones become it.
//   - required keeps only names present in properties, and is dropped when
//     empty.
//   - Every other keyword ($schema, $defs, additionalProperties, not, if,
//     examples, …) is dropped.
func NormalizeJSONSchema(schema map[string]any) map[string]any {
	if schema == nil {
		return map[string]any{}
	}
	n := &normalizer{root: schema, budget: maxRefExpansions}
	return n.node(schema, nil, 0)
}

type normalizer struct {
	root   map[string]any
	budget int
}

// node normalizes one schema node. refs is the chain of $refs being
// expanded on the path to it.
func (n *normalizer) node(raw any, refs []string, depth int) map[string]any {
	in, ok := raw.(map[string]any)
	if !ok {
		// JSON Schema's boolean schemas: true accepts anything, false
		// nothing. Neither has a Gemini spelling; an untyped node is the
		// nearest for true, and callers drop properties whose schema is false.
		return map[string]any{}
	}
	if depth > maxSchemaDepth {
		return map[string]any{"type": "object"}
	}

	s := make(map[string]any, len(in))
	for k, v := range in {
		s[k] = v
	}

	if ref, ok := s["$ref"].(string); ok {
		delete(s, "$ref")
		expanded, nextRefs, ok := n.resolveRef(ref, refs)
		if !ok {
			cut := map[string]any{"type": "object"}
			if expanded == nil {
				// Unresolvable: nothing to inline, so no type to claim.
				cut = map[string]any{}
			}
			if d, ok := s["description"].(string); ok && d != "" {
				cut["description"] = d
			}
			return cut
		}
		// Siblings of $ref apply alongside the referenced schema and win.
		merged := make(map[string]any, len(expanded)+len(s))
		for k, v := range expanded {
			merged[k] = v
		}
		for k, v := range s {
			merged[k] = v
		}
		return n.node(merged, nextRefs, depth+1)
	}

	if all, ok := s["allOf"].([]any); ok {
		delete(s, "allOf")
		for _, branch := range all {
			if _, ok := branch.(map[string]any); !ok {
				continue
			}
			// Each branch goes through the whole node path first, so a $ref
			// in it is expanded with its own reference chain (cycles cut),
			// and its nested $refs, allOfs and type arrays are normalized
			// like any other node's. A normalized node normalizes to itself,
			// so merging it and normalizing the parent changes nothing more.
			mergeAllOfBranch(s, n.node(branch, refs, depth+1))
		}
	}

	out := map[string]any{}
	nullable := false
	if v, ok := s["nullable"].(bool); ok && v {
		nullable = true
	}

	// Types, with "null" folded into nullable.
	var types []string
	switch t := s["type"].(type) {
	case string:
		types = []string{t}
	case []any:
		for _, item := range t {
			if name, ok := item.(string); ok {
				types = append(types, name)
			}
		}
	}
	var concrete []string
	for _, t := range types {
		switch {
		case t == "null":
			nullable = true
		case geminiTypes[t] != "" && !contains(concrete, t):
			concrete = append(concrete, t)
		}
	}

	// anyOf (oneOf joins it: Gemini has no oneOf, and anyOf accepts a
	// superset of what oneOf does, which is all a generator needs).
	var branches []any
	if v, ok := s["anyOf"].([]any); ok {
		branches = append(branches, v...)
	}
	if v, ok := s["oneOf"].([]any); ok {
		branches = append(branches, v...)
	}
	var anyOf []map[string]any
	for _, b := range branches {
		nb := n.node(b, refs, depth+1)
		if isNullOnly(nb) {
			nullable = true
			continue
		}
		anyOf = append(anyOf, nb)
	}

	if len(concrete) > 1 {
		// Several concrete types: one anyOf branch per type, each carrying
		// the keywords that belong to it.
		for _, t := range concrete {
			branch := map[string]any{"type": t}
			for _, key := range structuralKeys[t] {
				if v, ok := s[key]; ok {
					branch[key] = v
				}
			}
			anyOf = append(anyOf, n.node(branch, refs, depth+1))
		}
		for _, t := range concrete {
			for _, key := range structuralKeys[t] {
				delete(s, key)
			}
		}
		concrete = nil
	}

	if len(anyOf) == 1 && len(concrete) == 0 {
		// A union of one (typically [X, null] once null became nullable) is
		// X itself: hoist it so the node has a real type.
		for k, v := range anyOf[0] {
			if k == "description" || k == "title" {
				if _, own := s[k]; own {
					continue
				}
			}
			if k == "nullable" {
				if b, _ := v.(bool); b {
					nullable = true
				}
				continue
			}
			out[k] = v
		}
		anyOf = nil
	}

	typ := ""
	if len(concrete) == 1 {
		typ = concrete[0]
	} else if t, ok := out["type"].(string); ok {
		typ = t
	}
	if typ == "" && len(anyOf) == 0 {
		// Infer from the keywords present.
		switch {
		case s["properties"] != nil:
			typ = "object"
		case s["items"] != nil || s["prefixItems"] != nil:
			typ = "array"
		}
	}
	if typ != "" {
		out["type"] = typ
	}
	if len(anyOf) > 0 {
		list := make([]any, len(anyOf))
		for i, b := range anyOf {
			list[i] = b
		}
		out["anyOf"] = list
	}

	for _, key := range []string{"description", "title", "pattern"} {
		if v, ok := s[key].(string); ok && v != "" {
			out[key] = v
		}
	}

	if f, ok := s["format"].(string); ok && formatAllowed(typ, f) {
		out["format"] = f
	}

	// enum / const.
	var enumIn []any
	if v, ok := s["enum"].([]any); ok {
		enumIn = v
	} else if v, ok := s["const"]; ok {
		enumIn = []any{v}
	}
	if enumIn != nil {
		var values []any
		seen := map[string]bool{}
		for _, v := range enumIn {
			if v == nil {
				nullable = true
				continue
			}
			str, ok := enumString(v)
			if !ok || seen[str] {
				continue
			}
			seen[str] = true
			values = append(values, str)
		}
		if len(values) > 0 {
			out["enum"] = values
			if typ == "" && len(anyOf) == 0 && allStringEnum(enumIn) {
				out["type"] = "string"
			}
		}
	}

	// items: a schema, or a tuple (draft-07 items[] / 2020-12 prefixItems).
	if items := n.items(s, refs, depth); items != nil {
		out["items"] = items
	}

	// properties.
	if props, ok := s["properties"].(map[string]any); ok {
		normalized := make(map[string]any, len(props))
		for name, sub := range props {
			if b, ok := sub.(bool); ok && !b {
				continue // a property that can never be valid
			}
			normalized[name] = n.node(sub, refs, depth+1)
		}
		out["properties"] = normalized
	}
	if req, ok := s["required"].([]any); ok {
		props, _ := out["properties"].(map[string]any)
		var kept []any
		seen := map[string]bool{}
		for _, r := range req {
			name, ok := r.(string)
			if !ok || seen[name] {
				continue
			}
			if _, declared := props[name]; declared {
				seen[name] = true
				kept = append(kept, name)
			}
		}
		if len(kept) > 0 {
			out["required"] = kept
		}
	}

	// Bounds.
	for _, key := range []string{"minItems", "maxItems", "minLength", "maxLength", "minProperties", "maxProperties"} {
		if v, ok := nonNegativeInt(s[key]); ok {
			out[key] = v
		}
	}
	for _, pair := range [][3]string{{"minimum", "exclusiveMinimum"}, {"maximum", "exclusiveMaximum"}} {
		bound, exclusive := pair[0], pair[1]
		if v, ok := finiteNumber(s[bound]); ok {
			out[bound] = v
		}
		// Draft 6+: exclusiveMinimum is itself the (exclusive) bound. Gemini
		// has only inclusive bounds; the exclusive one is the closest.
		// Draft 4's boolean form only qualifies the bound kept above.
		if v, ok := finiteNumber(s[exclusive]); ok {
			if _, has := out[bound]; !has {
				out[bound] = v
			}
		}
	}

	for _, key := range []string{"default", "example"} {
		if v, ok := s[key]; ok && isScalar(v) {
			out[key] = v
		}
	}

	if nullable {
		out["nullable"] = true
	}
	return out
}

// items normalizes a node's items: a single schema, or a tuple folded into a
// union of its positions (Gemini has no tuple form).
func (n *normalizer) items(s map[string]any, refs []string, depth int) map[string]any {
	var tuple []any
	switch v := s["items"].(type) {
	case map[string]any:
		return n.node(v, refs, depth+1)
	case bool:
		if v {
			return map[string]any{}
		}
		return nil
	case []any:
		tuple = v
	}
	if v, ok := s["prefixItems"].([]any); ok {
		tuple = v
	}
	if len(tuple) == 0 {
		if v, ok := s["additionalItems"].(map[string]any); ok {
			return n.node(v, refs, depth+1)
		}
		return nil
	}
	if len(tuple) == 1 {
		return n.node(tuple[0], refs, depth+1)
	}
	return n.node(map[string]any{"anyOf": tuple}, refs, depth+1)
}

// resolveRef looks up a same-document JSON pointer. ok is false when the ref
// is circular (target non-nil, the caller cuts it to an object), or
// unresolvable or over budget (target nil).
func (n *normalizer) resolveRef(ref string, refs []string) (target map[string]any, nextRefs []string, ok bool) {
	if contains(refs, ref) {
		return map[string]any{}, nil, false
	}
	if n.budget <= 0 {
		return map[string]any{}, nil, false
	}
	n.budget--
	if !strings.HasPrefix(ref, "#") {
		return nil, nil, false
	}
	var node any = n.root
	pointer := strings.TrimPrefix(ref, "#")
	if pointer != "" {
		for _, part := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
			part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
			m, isMap := node.(map[string]any)
			if !isMap {
				return nil, nil, false
			}
			node, isMap = m[part]
			if !isMap {
				return nil, nil, false
			}
		}
	}
	m, isMap := node.(map[string]any)
	if !isMap {
		return nil, nil, false
	}
	next := make([]string, len(refs), len(refs)+1)
	copy(next, refs)
	return m, append(next, ref), true
}

// mergeAllOfBranch folds one allOf branch into its parent: properties and
// required are unioned, any other keyword is taken only if the parent lacks
// it.
func mergeAllOfBranch(parent, branch map[string]any) {
	for k, v := range branch {
		switch k {
		case "properties":
			bp, ok := v.(map[string]any)
			if !ok {
				continue
			}
			pp, _ := parent["properties"].(map[string]any)
			merged := make(map[string]any, len(pp)+len(bp))
			for name, sub := range pp {
				merged[name] = sub
			}
			for name, sub := range bp {
				if _, exists := merged[name]; !exists {
					merged[name] = sub
				}
			}
			parent["properties"] = merged
		case "required":
			br, ok := v.([]any)
			if !ok {
				continue
			}
			pr, _ := parent["required"].([]any)
			parent["required"] = append(append([]any{}, pr...), br...)
		default:
			if _, exists := parent[k]; !exists {
				parent[k] = v
			}
		}
	}
}

// GenAISchema converts a NormalizeJSONSchema result to a genai.Schema.
// Unknown keys are ignored: the normalizer has already reduced the map to the
// subset, so anything else here is not representable anyway.
func GenAISchema(m map[string]any) *genai.Schema {
	out := &genai.Schema{}
	if t, ok := m["type"].(string); ok {
		out.Type = geminiTypes[t]
	}
	out.Description, _ = m["description"].(string)
	out.Title, _ = m["title"].(string)
	out.Pattern, _ = m["pattern"].(string)
	out.Format, _ = m["format"].(string)
	if v, ok := m["nullable"].(bool); ok && v {
		out.Nullable = genai.Ptr(true)
	}
	if enum, ok := m["enum"].([]any); ok {
		for _, v := range enum {
			if s, ok := v.(string); ok {
				out.Enum = append(out.Enum, s)
			}
		}
	}
	if items, ok := m["items"].(map[string]any); ok {
		out.Items = GenAISchema(items)
	}
	if anyOf, ok := m["anyOf"].([]any); ok {
		for _, b := range anyOf {
			if bm, ok := b.(map[string]any); ok {
				out.AnyOf = append(out.AnyOf, GenAISchema(bm))
			}
		}
	}
	if props, ok := m["properties"].(map[string]any); ok && len(props) > 0 {
		out.Properties = make(map[string]*genai.Schema, len(props))
		names := make([]string, 0, len(props))
		for name := range props {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if pm, ok := props[name].(map[string]any); ok {
				out.Properties[name] = GenAISchema(pm)
			}
		}
	}
	if req, ok := m["required"].([]any); ok {
		for _, r := range req {
			if s, ok := r.(string); ok {
				out.Required = append(out.Required, s)
			}
		}
	}
	out.MinItems = int64Ptr(m["minItems"])
	out.MaxItems = int64Ptr(m["maxItems"])
	out.MinLength = int64Ptr(m["minLength"])
	out.MaxLength = int64Ptr(m["maxLength"])
	out.MinProperties = int64Ptr(m["minProperties"])
	out.MaxProperties = int64Ptr(m["maxProperties"])
	out.Minimum = float64Ptr(m["minimum"])
	out.Maximum = float64Ptr(m["maximum"])
	if v, ok := m["default"]; ok {
		out.Default = v
	}
	if v, ok := m["example"]; ok {
		out.Example = v
	}
	return out
}

func formatAllowed(typ, format string) bool {
	switch typ {
	case "string":
		return stringFormats[format]
	case "integer":
		return integerFormats[format]
	case "number":
		return numberFormats[format]
	}
	return false
}

// enumString is Gemini's spelling of one enum entry: strings as they are,
// finite numbers and booleans in their JSON text. Objects and arrays have no
// spelling and are dropped.
func enumString(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case bool:
		if x {
			return "true", true
		}
		return "false", true
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return "", false
		}
		return fmt.Sprint(x), true
	case int, int32, int64, float32, json.Number:
		return fmt.Sprint(x), true
	}
	return "", false
}

func allStringEnum(values []any) bool {
	for _, v := range values {
		if _, ok := v.(string); !ok && v != nil {
			return false
		}
	}
	return true
}

func isNullOnly(m map[string]any) bool {
	if len(m) == 0 {
		return false
	}
	for k, v := range m {
		switch k {
		case "nullable":
			if b, _ := v.(bool); !b {
				return false
			}
		case "description", "title":
		default:
			return false
		}
	}
	_, hasNullable := m["nullable"]
	return hasNullable
}

func isScalar(v any) bool {
	switch v.(type) {
	case string, bool, float64, float32, int, int32, int64, json.Number:
		return true
	}
	return false
}

func finiteNumber(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, !math.IsNaN(x) && !math.IsInf(x, 0)
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	}
	return 0, false
}

func nonNegativeInt(v any) (int64, bool) {
	f, ok := finiteNumber(v)
	if !ok || f < 0 || f != math.Trunc(f) {
		return 0, false
	}
	return int64(f), true
}

func int64Ptr(v any) *int64 {
	if i, ok := nonNegativeInt(v); ok {
		return &i
	}
	return nil
}

func float64Ptr(v any) *float64 {
	if f, ok := finiteNumber(v); ok {
		return &f
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}
