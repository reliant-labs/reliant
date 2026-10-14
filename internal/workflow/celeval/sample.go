package celeval

import (
	"fmt"
	"sort"
	"strings"

	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
)

// EventSchema returns the declared trigger.payload JSON Schema for one event of
// an integration (manifest.TriggerPayloadSchema, the schema workflow validation
// type-checks against). The error lists the events the integration does
// declare, so a misspelt one is one call from the fix.
func EventSchema(integration, event string) (map[string]any, error) {
	c, err := catalog.Builtin()
	if err != nil {
		return nil, fmt.Errorf("integration catalog unavailable: %w", err)
	}
	var known []string
	integrationFound := false
	for _, m := range c.Manifests() {
		if m.GetId() != integration {
			continue
		}
		integrationFound = true
		for _, ts := range m.GetTriggers() {
			for _, ev := range ts.GetEvents() {
				known = append(known, ev)
				if ev == event {
					return manifest.TriggerPayloadSchema(m, ts), nil
				}
			}
		}
	}
	if !integrationFound {
		return nil, fmt.Errorf("no integration %q in the catalog", integration)
	}
	sort.Strings(known)
	if len(known) == 0 {
		return nil, fmt.Errorf("integration %q declares no trigger events", integration)
	}
	return nil, fmt.Errorf("integration %q declares no event %q; it declares: %s", integration, event, strings.Join(known, ", "))
}

// maxSampleDepth bounds a generated sample: schemas can be recursive.
const maxSampleDepth = 8

// SampleFromSchema builds a representative value for a JSON Schema: the first
// of `const`, `examples`, `default`, `enum`, else a placeholder for the type
// (a string is its property name, an integer 1, a boolean false, an array one
// element, an object every declared property). It is deterministic, so a
// sample is stable across calls and diffs; the caller overrides what matters
// to the expression under test.
func SampleFromSchema(schema map[string]any) any {
	return sampleValue("value", schema, 0)
}

func sampleValue(name string, schema map[string]any, depth int) any {
	if schema == nil || depth > maxSampleDepth {
		return nil
	}
	if v, ok := schema["const"]; ok {
		return v
	}
	if ex, ok := schema["examples"].([]any); ok && len(ex) > 0 {
		return ex[0]
	}
	if v, ok := schema["default"]; ok {
		return v
	}
	if en, ok := schema["enum"].([]any); ok && len(en) > 0 {
		return en[0]
	}
	switch schemaType(schema) {
	case "string":
		return name
	case "integer", "number":
		return int64(1)
	case "boolean":
		return false
	case "null":
		return nil
	case "array":
		item, _ := schema["items"].(map[string]any)
		return []any{sampleValue(singular(name), item, depth+1)}
	case "object":
		out := map[string]any{}
		props, _ := schema["properties"].(map[string]any)
		for key, raw := range props {
			sub, _ := raw.(map[string]any)
			out[key] = sampleValue(key, sub, depth+1)
		}
		return out
	}
	// No type: a schema that only has properties is an object.
	if props, ok := schema["properties"].(map[string]any); ok && len(props) > 0 {
		return sampleValue(name, map[string]any{"type": "object", "properties": props}, depth)
	}
	return nil
}

// schemaType is the schema's type, ignoring a "null" alternative.
func schemaType(schema map[string]any) string {
	switch t := schema["type"].(type) {
	case string:
		return t
	case []any:
		for _, x := range t {
			if s, ok := x.(string); ok && s != "null" {
				return s
			}
		}
	}
	return ""
}

func singular(name string) string { return strings.TrimSuffix(name, "s") }
