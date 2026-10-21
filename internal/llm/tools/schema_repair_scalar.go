// Copyright (c) 2025 Reliant Labs
package tools

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Scalar repair: a model sometimes sends a number or boolean as a quoted
// string ({"defects": "2"}). The schema says integer, validation rejects it,
// and because the rejection prints the value as `2` the model cannot see that
// the quotes are the problem and repeats the call.
//
// Coercion is strict. The string must be exactly a JSON number (or exactly
// true/false) — no whitespace, sign prefix, leading zeros, or trailing text —
// and must fit the wanted type. It never fires when the schema also accepts a
// string at that position.

// maxExactInt is the largest integer a float64 (what JSON decodes into)
// represents exactly; beyond it a coerced integer would silently change value.
const maxExactInt = 1 << 53

var jsonNumberPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

// coerceScalarString converts s to the scalar type the schema expects. The
// caller guarantees the schema does not accept a string here.
func coerceScalarString(s string, expected map[string]bool) (interface{}, bool) {
	if expected["integer"] || expected["number"] {
		if jsonNumberPattern.MatchString(s) {
			f, err := strconv.ParseFloat(s, 64)
			if err == nil && !math.IsInf(f, 0) && !math.IsNaN(f) {
				if expected["number"] {
					return f, true
				}
				if f == math.Trunc(f) && math.Abs(f) <= maxExactInt {
					return f, true
				}
			}
		}
	}
	if expected["boolean"] {
		switch s {
		case "true":
			return true, true
		case "false":
			return false, true
		}
	}
	return nil, false
}

// maxTypeHints bounds how many mismatches one error message lists.
const maxTypeHints = 5

// typeMismatchHints walks value against schema and describes, for the model,
// every position whose JSON type is not one the schema allows: the path, the
// expected type, what was actually sent (quoted when it is a string) and how
// to fix it. Go's validator prints a string "2" as `2`, which hides the
// problem; this does not.
func typeMismatchHints(value interface{}, schema map[string]interface{}) []string {
	var hints []string
	collectTypeHints(value, schema, "$", &hints)
	if len(hints) > maxTypeHints {
		hints = append(hints[:maxTypeHints], fmt.Sprintf("(%d more type mismatches not shown)", len(hints)-maxTypeHints))
	}
	return hints
}

func collectTypeHints(value interface{}, schema map[string]interface{}, path string, hints *[]string) {
	if schema == nil {
		return
	}
	expected := schemaExpectedTypes(schema)
	if len(expected) > 0 && !valueMatchesTypes(value, expected) {
		*hints = append(*hints, describeTypeMismatch(value, expected, path))
		return
	}

	switch v := value.(type) {
	case map[string]interface{}:
		props, _ := schema["properties"].(map[string]interface{})
		additional, _ := schema["additionalProperties"].(map[string]interface{})
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			childSchema, _ := props[key].(map[string]interface{})
			if childSchema == nil {
				childSchema = additional
			}
			collectTypeHints(v[key], childSchema, path+"."+key, hints)
		}
	case []interface{}:
		if items, ok := schema["items"].(map[string]interface{}); ok {
			for i, item := range v {
				collectTypeHints(item, items, fmt.Sprintf("%s[%d]", path, i), hints)
			}
		}
	}
}

func valueMatchesTypes(value interface{}, expected map[string]bool) bool {
	switch v := value.(type) {
	case nil:
		return expected["null"]
	case string:
		return expected["string"]
	case bool:
		return expected["boolean"]
	case float64:
		return expected["number"] || (expected["integer"] && v == math.Trunc(v))
	case []interface{}:
		return expected["array"]
	case map[string]interface{}:
		return expected["object"]
	}
	return true
}

func jsonTypeName(value interface{}) string {
	switch v := value.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64:
		if v == math.Trunc(v) {
			return "integer"
		}
		return "number"
	case []interface{}:
		return "array"
	case map[string]interface{}:
		return "object"
	}
	return fmt.Sprintf("%T", value)
}

func describeTypeMismatch(value interface{}, expected map[string]bool, path string) string {
	names := make([]string, 0, len(expected))
	for name := range expected {
		names = append(names, name)
	}
	sort.Strings(names)

	field := strings.TrimPrefix(strings.TrimPrefix(path, "$"), ".")
	if field == "" {
		field = "the input"
	}

	sent := jsonTypeName(value)
	switch value.(type) {
	case string, float64, bool, nil:
		encoded, _ := json.Marshal(value)
		sent = fmt.Sprintf("%s %s", sent, truncateForHint(string(encoded), 40))
	}

	msg := fmt.Sprintf("%s must be of type %s but you sent %s", field, strings.Join(names, " or "), sent)
	if _, isString := value.(string); isString && (expected["integer"] || expected["number"] || expected["boolean"]) {
		msg += "; send it as a bare JSON value without quotes"
	}
	return msg
}

func truncateForHint(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}

// withTypeHints appends the type-mismatch hints for data to err's message.
func withTypeHints(err error, data interface{}, schema map[string]interface{}) error {
	hints := typeMismatchHints(data, schema)
	if len(hints) == 0 {
		return err
	}
	return fmt.Errorf("%w. Fix: %s", err, strings.Join(hints, "; "))
}
