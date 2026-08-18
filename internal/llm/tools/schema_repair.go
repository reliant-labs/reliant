// Copyright (c) 2025 Reliant Labs
package tools

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"

	gojsonschema "github.com/google/jsonschema-go/jsonschema"
	"github.com/reliant-labs/reliant/internal/logging"
)

// This file implements schema-guided repair of "stringified JSON" tool inputs.
//
// Models (across providers) sometimes emit an array- or object-typed tool
// parameter as a JSON-encoded STRING containing the value, e.g.:
//
//	{"slides": "[{\"title\": \"Intro\"}]"}   instead of   {"slides": [{"title": "Intro"}]}
//
// Prior to this repair layer, JSON Schema validation hard-failed with
// `... has type "string", want "array"`, the model retried with the same
// encoding, and agent loops spun for dozens of round-trips.
//
// The repair is deliberately narrow:
//   - It only fires AFTER validation has already failed.
//   - It only rewrites a string value when the schema at that exact position
//     expects an array or object (never when the schema accepts a string).
//   - It walks the schema recursively (properties, items,
//     additionalProperties), so nested stringified values are repaired too
//     (e.g. a stringified array at $.deck.slides).
//   - Double-encoded values (a JSON string whose content is itself a JSON
//     string) are unwrapped by a single bounded repair (see unwrapToType /
//     maxUnwrapDepth) — one repair per path, never a repair-of-a-repair.
//   - The repaired input is re-validated; if it still fails, the caller gets
//     the original input back along with the post-repair validation error.
//
// Both JSON-schema validation sites share this helper:
//   - regular tools:  ToolWrapper.Run (internal/llm/tools/tool_wrapper.go)
//   - response tools: executeResponseToolInline
//     (internal/workflow/runtime/activities/handlers/execute_tools.go)

// ValidateJSONAgainstSchema validates a JSON string against a JSON Schema
// document (raw JSON bytes) without attempting any repair.
func ValidateJSONAgainstSchema(jsonStr string, schemaJSON []byte) error {
	resolved, err := resolveSchemaJSON(schemaJSON)
	if err != nil {
		return err
	}

	var inputData interface{}
	if err := json.Unmarshal([]byte(jsonStr), &inputData); err != nil {
		return fmt.Errorf("failed to unmarshal input JSON: %w", err)
	}

	if err := resolved.Validate(inputData); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}
	return nil
}

// ValidateJSONWithRepair validates jsonStr against schemaJSON (a JSON Schema
// document). When validation fails, it attempts a schema-guided repair of
// stringified array/object values and re-validates.
//
// Returns the (possibly repaired) JSON string. A nil error means the returned
// JSON validates against the schema. On error, the original jsonStr is
// returned unchanged.
//
// When a repair fires, one INFO log is emitted with the tool name and the
// repaired property paths — telemetry for how often models emit stringified
// JSON, and from which tools.
func ValidateJSONWithRepair(toolName, jsonStr string, schemaJSON []byte) (string, error) {
	resolved, err := resolveSchemaJSON(schemaJSON)
	if err != nil {
		return jsonStr, err
	}

	var inputData interface{}
	if err := json.Unmarshal([]byte(jsonStr), &inputData); err != nil {
		return jsonStr, fmt.Errorf("failed to unmarshal input JSON: %w", err)
	}

	valErr := resolved.Validate(inputData)
	if valErr == nil {
		return jsonStr, nil
	}

	// Validation failed — attempt schema-guided repair.
	var schemaMap map[string]interface{}
	if err := json.Unmarshal(schemaJSON, &schemaMap); err != nil {
		return jsonStr, fmt.Errorf("validation failed: %w", valErr)
	}

	repairedData, repairedPaths := repairStringifiedJSON(inputData, schemaMap)

	// Second repair class: the model spelled a property name differently than
	// the schema (file vs file_path, filePath vs file_path). Under a closed
	// schema that is a hard rejection, so the call costs a full turn to retry.
	repairedData, renamedKeys := repairAliasedKeys(repairedData, schemaMap)

	if len(repairedPaths) == 0 && len(renamedKeys) == 0 {
		// Nothing repairable (e.g. the string isn't valid JSON of the expected
		// type, or the schema expects a string) — fail with the original error.
		return jsonStr, fmt.Errorf("validation failed: %w", valErr)
	}

	// Telemetry: log every repair, even if re-validation still fails — the
	// signal we want is "how often do models stringify values".
	if len(repairedPaths) > 0 {
		logging.Info("Repaired stringified JSON in tool input",
			"tool", toolName,
			"properties", strings.Join(repairedPaths, ", "),
		)
	}
	if len(renamedKeys) > 0 {
		logging.Info("Repaired aliased property name in tool input",
			"tool", toolName,
			"renames", strings.Join(renamedKeys, ", "),
		)
	}

	if err := resolved.Validate(repairedData); err != nil {
		// Repair fired but the input is still invalid for other reasons —
		// report the post-repair error (it's the more actionable one) and
		// hand back the original input.
		return jsonStr, fmt.Errorf("validation failed: %w", err)
	}

	repairedJSON, err := json.Marshal(repairedData)
	if err != nil {
		return jsonStr, fmt.Errorf("validation failed: %w", valErr)
	}
	return string(repairedJSON), nil
}

// resolveSchemaJSON parses and resolves a JSON Schema document for validation.
func resolveSchemaJSON(schemaJSON []byte) (*gojsonschema.Resolved, error) {
	var goSchema gojsonschema.Schema
	if err := json.Unmarshal(schemaJSON, &goSchema); err != nil {
		return nil, fmt.Errorf("failed to unmarshal schema: %w", err)
	}
	resolved, err := goSchema.Resolve(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve schema: %w", err)
	}
	return resolved, nil
}

// repairStringifiedJSON walks value against schema (both as generic decoded
// JSON) and replaces string values with their parsed JSON when the schema
// expects an array or object at that position — including the root value.
// Returns the (possibly replaced) value and the list of repaired paths
// ("$", "$.slides", "$.deck.slides", "$.items[2]", ...).
func repairStringifiedJSON(value interface{}, schema map[string]interface{}) (interface{}, []string) {
	var repairedPaths []string
	out := repairValueAgainstSchema(value, schema, "$", &repairedPaths)
	return out, repairedPaths
}

func repairValueAgainstSchema(value interface{}, schema map[string]interface{}, path string, repairedPaths *[]string) interface{} {
	if schema == nil {
		return value
	}

	if s, ok := value.(string); ok {
		expected := schemaExpectedTypes(schema)
		// Never repair when the schema accepts a string here — a string that
		// happens to contain JSON is a legitimate value.
		if !expected["string"] {
			for _, want := range []string{"array", "object"} {
				if !expected[want] {
					continue
				}
				// unwrapToType peels string-encoding layers (bounded by
				// maxUnwrapDepth) until the decoded value matches the wanted
				// type, so a double-encoded value is fixed by this single
				// repair. Each path is repaired at most once.
				if parsed, ok := unwrapToType(s, want); ok {
					*repairedPaths = append(*repairedPaths, path)
					value = parsed
					break
				}
			}
		}
		if _, still := value.(string); still {
			// Not repairable — leave it for validation to reject cleanly.
			return value
		}
	}

	// Recurse into containers per the schema so nested stringified values
	// (including ones inside a value we just repaired) are handled too.
	switch v := value.(type) {
	case map[string]interface{}:
		props, _ := schema["properties"].(map[string]interface{})
		additional, _ := schema["additionalProperties"].(map[string]interface{})
		for key, child := range v {
			childSchema, _ := props[key].(map[string]interface{})
			if childSchema == nil {
				childSchema = additional
			}
			if childSchema != nil {
				v[key] = repairValueAgainstSchema(child, childSchema, path+"."+key, repairedPaths)
			}
		}
	case []interface{}:
		// Only the single-schema "items" form is supported (tuple-form
		// prefixItems is not used by our tool schemas).
		if items, ok := schema["items"].(map[string]interface{}); ok {
			for i, item := range v {
				v[i] = repairValueAgainstSchema(item, items, fmt.Sprintf("%s[%d]", path, i), repairedPaths)
			}
		}
	}
	return value
}

// repairAliasedKeys renames a top-level property the model spelled differently
// than the schema does — the observed case being a write tool call carrying
// {"file": ...} instead of {"file_path": ...}, which a closed schema rejects
// with `unexpected additional properties ["file"]`.
//
// The model cannot see why it was wrong from that message alone, so it burns a
// full generation retrying. Renaming a key we can identify UNAMBIGUOUSLY costs
// nothing and saves that turn.
//
// The bar for renaming is deliberately high, because a wrong rename writes the
// caller's content somewhere they did not ask for — worse than a clean
// rejection. All of the following must hold:
//
//   - The schema is CLOSED (additionalProperties: false). If unknown keys are
//     legal the key is a real value, not a misspelling, and renaming it would
//     destroy data.
//   - The key is genuinely unknown (not itself a declared property).
//   - The target property is ABSENT or empty in the input, so nothing the
//     model actually supplied is overwritten.
//   - Exactly ONE candidate property matches lexically. Two candidates means we
//     would be guessing.
//   - The supplied value FITS the target property's declared type.
//
// Matching is lexical only (normalize away case and separators, then require
// one name to be a prefix/suffix-delimited component of the other). It never
// infers meaning, so an unrelated key like "destination" is left alone for
// validation to reject.
//
// Only top-level properties are considered: that is where the failure occurs,
// and every extra layer of inference widens the blast radius.
func repairAliasedKeys(value interface{}, schema map[string]interface{}) (interface{}, []string) {
	obj, ok := value.(map[string]interface{})
	if !ok || schema == nil {
		return value, nil
	}

	// Only repair closed schemas. Under an open schema an unknown key is legal.
	if additional, ok := schema["additionalProperties"].(bool); !ok || additional {
		return value, nil
	}

	props, ok := schema["properties"].(map[string]interface{})
	if !ok || len(props) == 0 {
		return value, nil
	}

	var renamed []string
	for key, val := range obj {
		if _, declared := props[key]; declared {
			continue
		}

		target, ok := uniqueAliasTarget(key, val, props, obj)
		if !ok {
			continue
		}

		obj[target] = val
		delete(obj, key)
		renamed = append(renamed, fmt.Sprintf("%s->%s", key, target))
	}

	// Deterministic order so the telemetry line is stable across map iteration.
	sort.Strings(renamed)
	return obj, renamed
}

// uniqueAliasTarget returns the single schema property that unknown key `key`
// unambiguously refers to, or ok=false when there is not exactly one safe
// candidate. See repairAliasedKeys for the full set of conditions.
func uniqueAliasTarget(key string, val interface{}, props, obj map[string]interface{}) (string, bool) {
	var found string
	for candidate, rawSchema := range props {
		// Never overwrite something the model actually supplied.
		if existing, present := obj[candidate]; present && !isEmptyValue(existing) {
			continue
		}
		if !namesAlias(key, candidate) {
			continue
		}
		candidateSchema, _ := rawSchema.(map[string]interface{})
		if !valueFitsSchemaType(val, candidateSchema) {
			continue
		}
		if found != "" {
			// Ambiguous — two properties could be meant. Refuse to guess.
			return "", false
		}
		found = candidate
	}
	return found, found != ""
}

// namesAlias reports whether two property names are plausibly the same name
// spelled differently. After normalizing case and separators away, one must
// either equal the other or appear as a leading/trailing WORD component of it
// (on the original separator/case boundaries).
//
// So file ~ file_path and path ~ file_path, but destination !~ file_path, and
// "at" does not match "path" — component boundaries are respected rather than
// matching bare substrings.
func namesAlias(a, b string) bool {
	na, nb := normalizeKeyName(a), normalizeKeyName(b)
	if na == "" || nb == "" {
		return false
	}
	if na == nb {
		return true
	}
	return hasWordComponent(splitKeyWords(a), splitKeyWords(b))
}

// hasWordComponent reports whether the shorter word sequence appears at the
// start or end of the longer one, e.g. ["file"] within ["file","path"].
func hasWordComponent(a, b []string) bool {
	short, long := a, b
	if len(short) > len(long) {
		short, long = long, short
	}
	if len(short) == 0 || len(short) >= len(long) {
		return false
	}
	matchesAt := func(offset int) bool {
		for i, w := range short {
			if long[offset+i] != w {
				return false
			}
		}
		return true
	}
	return matchesAt(0) || matchesAt(len(long)-len(short))
}

// splitKeyWords splits a property name into lowercase words on underscores,
// dashes, spaces and camelCase humps: "filePath" and "file_path" both yield
// ["file","path"].
func splitKeyWords(s string) []string {
	var words []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			words = append(words, strings.ToLower(cur.String()))
			cur.Reset()
		}
	}
	for i, r := range s {
		switch {
		case r == '_' || r == '-' || r == ' ' || r == '.':
			flush()
		case unicode.IsUpper(r):
			// A hump starts a new word, except across a run of capitals.
			if i > 0 && !unicode.IsUpper(rune(s[i-1])) {
				flush()
			}
			cur.WriteRune(r)
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return words
}

// normalizeKeyName lowercases a name and strips separators, so file_path,
// filePath and FilePath all collapse to "filepath".
func normalizeKeyName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// isEmptyValue reports whether a present value carries no information, so
// renaming onto it cannot destroy anything the model meant to send.
func isEmptyValue(v interface{}) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case []interface{}:
		return len(t) == 0
	case map[string]interface{}:
		return len(t) == 0
	default:
		return false
	}
}

// valueFitsSchemaType reports whether a decoded JSON value is acceptable for a
// schema position. A schema with no declared type accepts anything.
func valueFitsSchemaType(v interface{}, schema map[string]interface{}) bool {
	if schema == nil {
		return false
	}
	expected := schemaExpectedTypes(schema)
	if len(expected) == 0 {
		return true
	}
	switch v.(type) {
	case string:
		return expected["string"]
	case bool:
		return expected["boolean"]
	case float64:
		return expected["number"] || expected["integer"]
	case []interface{}:
		return expected["array"]
	case map[string]interface{}:
		return expected["object"]
	case nil:
		return expected["null"]
	default:
		return false
	}
}

// schemaExpectedTypes returns the set of type names a schema position accepts.
// Handles both the string form (`"type": "array"`) and the list form
// (`"type": ["array", "null"]`).
func schemaExpectedTypes(schema map[string]interface{}) map[string]bool {
	types := map[string]bool{}
	switch t := schema["type"].(type) {
	case string:
		types[t] = true
	case []interface{}:
		for _, item := range t {
			if s, ok := item.(string); ok {
				types[s] = true
			}
		}
	}
	return types
}
