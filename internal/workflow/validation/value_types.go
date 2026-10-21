// Copyright (c) 2025 Reliant Labs
package validation

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/google/cel-go/cel"
	gojsonschema "github.com/google/jsonschema-go/jsonschema"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// An input whose type an integration defines (`type: github.repository`) is
// checked against that integration's declaration of the type, never against
// anything this package knows about GitHub. Every site that would otherwise
// switch on an input's type asks a ValueTypes instead, so adding a type or an
// integration changes no code here.

// ValueTypes is what validation needs to know about the input types
// integrations define. The embedded catalog is the production source; tests
// supply a fake. It is declared here, by its consumer.
type ValueTypes interface {
	// ValueSchema is the JSON Schema of ONE value of the type `ref`
	// (`<integration>.<type>`). ok is false when no integration declares ref.
	ValueSchema(ref string) (schema map[string]any, ok bool)

	// TypeRefs lists every type ref the integrations declare. It is what an
	// unknown type is matched against to suggest the one that was meant.
	TypeRefs() []string

	// ScopeTypes names the other typed values the type's options depend on:
	// scope key -> the type ref the input bound to that key must have. A type
	// with no scope returns nil.
	ScopeTypes(ref string) map[string]string
}

// catalogValueTypes is the ValueTypes over the embedded integration catalog.
//
// SEAM: the catalog does not declare input types yet (manifest `types:`), so
// until it does every integration type is reported as unknown. When manifests
// carry TypeSpec, implement these three methods over catalog.Builtin() the way
// catalogIntegrations does for triggers; nothing else changes, because every
// production caller reaches ValueTypes through ValidationOptions.valueTypes
// (or the default of the entry points that take no options).
type catalogValueTypes struct{}

func (catalogValueTypes) ValueSchema(string) (map[string]any, bool) { return nil, false }
func (catalogValueTypes) TypeRefs() []string                        { return nil }
func (catalogValueTypes) ScopeTypes(string) map[string]string       { return nil }

// valueTypes is the resolver to use: the one the caller set, else the catalog.
// Safe on a nil receiver.
func (o *ValidationOptions) valueTypes() ValueTypes {
	if o == nil {
		return typesOrDefault(nil)
	}
	return typesOrDefault(o.ValueTypes)
}

func typesOrDefault(t ValueTypes) ValueTypes {
	if t == nil {
		return catalogValueTypes{}
	}
	return t
}

// stringValueSchema is what a value of a type that cannot be resolved is taken
// to be: a string, which is what nearly every integration type is. The unknown
// type itself is reported where the workflow is validated; reading its values
// as strings avoids a second, misleading error at every use.
var stringValueSchema = map[string]any{"type": "string"}

// valueSchemaFor is the value schema of ref, or a plain string's when the type
// is unknown.
func valueSchemaFor(vt ValueTypes, ref string) map[string]any {
	if vt != nil {
		if schema, ok := vt.ValueSchema(ref); ok && schema != nil {
			return schema
		}
	}
	return stringValueSchema
}

// --- values -----------------------------------------------------------------

// validateIntegrationInputValue checks a supplied value against the type's
// value schema: each item of a multi input, or the value itself, plus the
// item-count bounds of a multi input.
func validateIntegrationInputValue(name string, value any, input *reliantv1.Input, vt ValueTypes) error {
	cfg := input.GetIntegrationInput()
	ref := input.GetType()
	item := valueSchemaFor(vt, ref)

	if !cfg.GetMulti() {
		if err := validateAgainstSchema(item, value); err != nil {
			return fmt.Errorf("input '%s' is not a valid %s: %s", name, ref, err)
		}
		return nil
	}

	items, ok := toAnySlice(value)
	if !ok {
		return fmt.Errorf("input '%s' expects an array of %s values, got %T", name, ref, value)
	}
	if cfg.MinItems != nil && len(items) < int(cfg.GetMinItems()) {
		return fmt.Errorf("input '%s' requires at least %d item(s)", name, cfg.GetMinItems())
	}
	if cfg.MaxItems != nil && len(items) > int(cfg.GetMaxItems()) {
		return fmt.Errorf("input '%s' allows at most %d item(s)", name, cfg.GetMaxItems())
	}
	for i, it := range items {
		if err := validateAgainstSchema(item, it); err != nil {
			return fmt.Errorf("input '%s' item %d is not a valid %s: %s", name, i, ref, err)
		}
	}
	return nil
}

// resolvedSchemas caches compiled value schemas by their JSON, so a launch
// that validates a hundred repositories compiles the pattern once.
var resolvedSchemas sync.Map // string -> *gojsonschema.Resolved

// validateAgainstSchema checks one JSON value against a JSON Schema. The error
// is the validator's reason, cut down to what a person needs to fix the value.
func validateAgainstSchema(schema map[string]any, value any) error {
	raw, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("value schema: %w", err)
	}
	var resolved *gojsonschema.Resolved
	if cached, ok := resolvedSchemas.Load(string(raw)); ok {
		resolved = cached.(*gojsonschema.Resolved)
	} else {
		var s gojsonschema.Schema
		if err := json.Unmarshal(raw, &s); err != nil {
			return fmt.Errorf("value schema: %w", err)
		}
		resolved, err = s.Resolve(nil)
		if err != nil {
			return fmt.Errorf("value schema: %w", err)
		}
		resolvedSchemas.Store(string(raw), resolved)
	}
	if err := resolved.Validate(value); err != nil {
		return fmt.Errorf("%s", trimSchemaError(err))
	}
	return nil
}

// --- CEL typing ---------------------------------------------------------------

// integrationFieldInfo is the CEL-facing shape of an integration input: the
// value schema's scalar type, or a list of it when the input is multi. For the
// usual string-valued type that is `string`, or `list<string>`.
func integrationFieldInfo(name string, input *reliantv1.Input, vt ValueTypes) *FieldInfo {
	kind, elem, celItem := scalarShape(valueSchemaFor(vt, input.GetType()))
	info := &FieldInfo{Name: name, Description: model.GetInputBase(input).GetDescription()}
	if input.GetIntegrationInput().GetMulti() {
		info.Kind = reflect.Slice
		info.IsSlice = true
		info.ElemType = elem
		info.CELType = cel.ListType(celItem)
		return info
	}
	info.Kind = kind
	if kind == reflect.Interface {
		info.IsDynamic = true
	}
	return info
}

// scalarShape maps a value schema's JSON type to the Go kind, element type and
// CEL type a value of it has. A schema that is not a scalar (an object, or one
// with no type) is dynamic: its fields are not typed here.
func scalarShape(schema map[string]any) (reflect.Kind, reflect.Type, *cel.Type) {
	switch t, _ := schema["type"].(string); t {
	case "string":
		return reflect.String, reflect.TypeOf(""), cel.StringType
	case "integer":
		return reflect.Int64, reflect.TypeOf(int64(0)), cel.IntType
	case "number":
		return reflect.Float64, reflect.TypeOf(float64(0)), cel.DoubleType
	case "boolean":
		return reflect.Bool, reflect.TypeOf(false), cel.BoolType
	}
	return reflect.Interface, nil, cel.DynType
}

// integrationCELType is the CEL type an integration input is declared as when
// it is passed as an argument: its value's type, or a list of it when multi.
func integrationCELType(input *reliantv1.Input, vt ValueTypes) *cel.Type {
	_, _, item := scalarShape(valueSchemaFor(vt, input.GetType()))
	if input.GetIntegrationInput().GetMulti() {
		return cel.ListType(item)
	}
	return item
}

// integrationTypeName is how an error names an integration input's type:
// `github.repository`, or `list<github.repository>` when multi.
func integrationTypeName(input *reliantv1.Input) string {
	if input.GetIntegrationInput().GetMulti() {
		return "list<" + input.GetType() + ">"
	}
	return input.GetType()
}

// --- misc -----------------------------------------------------------------------

// isIntegrationInput is model.IsIntegrationInput, named for the call sites in
// this package.
func isIntegrationInput(input *reliantv1.Input) bool { return model.IsIntegrationInput(input) }

// --- declarations ---------------------------------------------------------------

// validateIntegrationInputDecl checks an integration input as written: the
// type exists, its bounds and default make sense, and its scope is bound to
// inputs that can supply it. A type that does not resolve is reported once,
// here, and the rest of the declaration is left alone.
func validateIntegrationInputDecl(path []string, input *reliantv1.Input, all map[string]*reliantv1.Input, vt ValueTypes, result *Result) {
	cfg := input.GetIntegrationInput()
	ref := input.GetType()

	if _, _, ok := model.SplitIntegrationType(ref); !ok {
		result.AddErrorWithSuggestion(CategoryInput, path, "type",
			fmt.Sprintf("%q is not a valid input type", ref),
			"an integration's type is written <integration>.<type>, for example github.repository")
		return
	}

	schema, known := vt.ValueSchema(ref)
	if !known {
		msg, hint := unknownTypeMessage(ref, vt.TypeRefs())
		result.AddErrorWithSuggestion(CategoryInput, path, "type", msg, hint)
		return
	}

	if !cfg.GetMulti() {
		for _, b := range []struct {
			key string
			set bool
		}{{"min_items", cfg.MinItems != nil}, {"max_items", cfg.MaxItems != nil}} {
			if b.set {
				result.AddErrorWithSuggestion(CategoryInput, path, b.key,
					fmt.Sprintf("%s counts the values of a list, but this input holds one %s", b.key, ref),
					"set multi: true to take a list, or remove "+b.key)
			}
		}
	}
	if cfg.MinItems != nil && cfg.GetMinItems() < 0 {
		result.AddError(CategoryInput, path, "min_items", "min_items cannot be negative")
	}
	if cfg.MaxItems != nil && cfg.GetMaxItems() < 0 {
		result.AddError(CategoryInput, path, "max_items", "max_items cannot be negative")
	}
	if cfg.MinItems != nil && cfg.MaxItems != nil && cfg.GetMinItems() > cfg.GetMaxItems() {
		result.AddError(CategoryInput, path, "min_items",
			fmt.Sprintf("min_items (%d) is greater than max_items (%d)", cfg.GetMinItems(), cfg.GetMaxItems()))
	}

	if cfg.Default != nil {
		if err := validateIntegrationInputValue("default", cfg.Default.AsInterface(), input, vt); err != nil {
			result.AddError(CategoryInput, path, "default", strings.TrimPrefix(err.Error(), "input 'default' "))
		}
	}
	_ = schema

	validateIntegrationScope(path, input, all, vt, result)
}

// validateIntegrationScope checks the scope: bindings of an integration input.
// Each key must be one the type declares, and the input it names must exist and
// itself be the type that key needs.
func validateIntegrationScope(path []string, input *reliantv1.Input, all map[string]*reliantv1.Input, vt ValueTypes, result *Result) {
	scope := input.GetIntegrationInput().GetScope()
	if len(scope) == 0 {
		return
	}
	ref := input.GetType()
	declared := vt.ScopeTypes(ref)

	keys := make([]string, 0, len(scope))
	for k := range scope {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	declaredKeys := make([]string, 0, len(declared))
	for k := range declared {
		declaredKeys = append(declaredKeys, k)
	}
	sort.Strings(declaredKeys)

	for _, key := range keys {
		field := "scope." + key
		want, ok := declared[key]
		if !ok {
			hint := ref + " takes no scope"
			if len(declaredKeys) > 0 {
				hint = ref + " declares scope: " + strings.Join(declaredKeys, ", ")
				if near := didYouMean(key, declaredKeys); near != "" {
					hint = fmt.Sprintf("did you mean %q? ", near) + hint
				}
			}
			result.AddErrorWithSuggestion(CategoryInput, path, field,
				fmt.Sprintf("%s has no scope %q", ref, key), hint)
			continue
		}
		source := scope[key]
		src, exists := all[source]
		if !exists || src == nil {
			names := make([]string, 0, len(all))
			for n := range all {
				names = append(names, n)
			}
			sort.Strings(names)
			hint := "declare it under inputs:"
			if near := didYouMean(source, names); near != "" {
				hint = fmt.Sprintf("did you mean %q?", near)
			}
			result.AddErrorWithSuggestion(CategoryInput, path, field,
				fmt.Sprintf("input %q is not declared", source), hint)
			continue
		}
		if src.GetType() != want {
			result.AddError(CategoryInput, path, field,
				fmt.Sprintf("input %q is a %s, but scope %q of %s needs a %s", source, src.GetType(), key, ref, want))
		}
	}
}

// unknownTypeMessage words the error for a type ref no integration declares,
// and the hint toward the one that was probably meant. refs is every declared
// type ref.
func unknownTypeMessage(ref string, refs []string) (message, hint string) {
	integration, typeID, _ := model.SplitIntegrationType(ref)

	byIntegration := map[string][]string{}
	for _, r := range refs {
		if i, t, ok := model.SplitIntegrationType(r); ok {
			byIntegration[i] = append(byIntegration[i], t)
		}
	}
	integrations := make([]string, 0, len(byIntegration))
	for i := range byIntegration {
		integrations = append(integrations, i)
	}
	sort.Strings(integrations)

	if ids, ok := byIntegration[integration]; ok {
		sort.Strings(ids)
		hint = fmt.Sprintf("%s declares: %s", integration, strings.Join(ids, ", "))
		if near := didYouMean(typeID, ids); near != "" {
			hint = fmt.Sprintf("did you mean %q? ", integration+"."+near) + hint
		}
		return fmt.Sprintf("%s has no input type %q", integration, typeID), hint
	}
	if len(integrations) == 0 {
		return fmt.Sprintf("unknown input type %q", ref),
			"no integration declares input types yet; the built-in types are " + strings.Join(model.BuiltinInputTypes, ", ")
	}
	hint = "integrations with input types: " + strings.Join(integrations, ", ")
	if near := didYouMean(integration, integrations); near != "" {
		hint = fmt.Sprintf("did you mean %q? ", near) + hint
	}
	return fmt.Sprintf("unknown input type %q: no integration named %q declares input types", ref, integration), hint
}

// didYouMean names the candidate a mistyped target most likely meant: the one
// it is a prefix of (or that is a prefix of it) when there is exactly one, else
// the nearest by edit distance. Empty when nothing is close.
func didYouMean(target string, candidates []string) string {
	var prefixed []string
	for _, c := range candidates {
		if target != "" && (strings.HasPrefix(c, target) || strings.HasPrefix(target, c)) {
			prefixed = append(prefixed, c)
		}
	}
	if len(prefixed) == 1 {
		return prefixed[0]
	}
	best, bestDist := "", len(target)+1
	for _, c := range candidates {
		if d := levenshtein(target, c); d < bestDist && d <= 3 {
			best, bestDist = c, d
		}
	}
	return best
}

// trimSchemaError drops the validator's "validating <schema path>:" prefix,
// which names a location inside a schema the author never wrote.
func trimSchemaError(err error) string {
	msg := err.Error()
	if strings.HasPrefix(msg, "validating ") {
		if _, rest, ok := strings.Cut(msg, ": "); ok {
			return rest
		}
	}
	return msg
}
