package catalog

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	gojsonschema "github.com/google/jsonschema-go/jsonschema"
)

// reservedParams are params the editor never renders as a field: a node's
// connection has its own picker.
var reservedParams = map[string]bool{"connection": true}

// TestEveryActionParamIsDocumented is the bar for what an action's form shows.
// The editor labels a field with the param's title, prints its description
// under the input and uses examples[0] as the placeholder; an agent reads the
// same schema as a tool definition. A bare `sid` with no words and no example
// is how Twilio's Message SID came to read as a second Account SID.
//
// So every param an action takes needs a title, a description and an example
// that is itself a valid value for the param. A boolean (a toggle) or an enum
// (a select listing every value) already shows its value space, so it needs
// no example.
func TestEveryActionParamIsDocumented(t *testing.T) {
	var problems []string
	for _, m := range MustBuiltin().Manifests() {
		for _, a := range m.GetActions() {
			params := a.GetParams().AsMap()
			props, _ := params["properties"].(map[string]any)
			names := make([]string, 0, len(props))
			for name := range props {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				if reservedParams[name] {
					continue
				}
				prop, _ := props[name].(map[string]any)
				where := fmt.Sprintf("%s/%s@%d param %q", m.GetId(), a.GetId(), m.GetVersion(), name)
				for _, problem := range paramDocProblems(prop) {
					problems = append(problems, where+": "+problem)
				}
			}
		}
	}
	if len(problems) > 0 {
		t.Errorf("%d undocumented action params:\n  %s", len(problems), strings.Join(problems, "\n  "))
	}
}

func paramDocProblems(prop map[string]any) []string {
	var problems []string
	if title, _ := prop["title"].(string); strings.TrimSpace(title) == "" {
		problems = append(problems, "no title")
	}
	if desc, _ := prop["description"].(string); strings.TrimSpace(desc) == "" {
		problems = append(problems, "no description")
	}
	examples, _ := prop["examples"].([]any)
	if len(examples) == 0 {
		if prop["type"] == "boolean" || prop["enum"] != nil {
			return problems
		}
		return append(problems, "no examples")
	}
	schema, err := resolveProperty(prop)
	if err != nil {
		return append(problems, err.Error())
	}
	for i, example := range examples {
		if err := schema.Validate(example); err != nil {
			problems = append(problems, fmt.Sprintf("examples[%d] %v is not a valid value: %v", i, example, err))
		}
	}
	return problems
}

func resolveProperty(prop map[string]any) (*gojsonschema.Resolved, error) {
	raw, err := json.Marshal(prop)
	if err != nil {
		return nil, err
	}
	var schema gojsonschema.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, fmt.Errorf("schema: %w", err)
	}
	return schema.Resolve(nil)
}

// TestParamDocProblems pins the bar itself, so a regression in the check
// cannot pass silently against a fully documented catalog.
func TestParamDocProblems(t *testing.T) {
	for _, tc := range []struct {
		name string
		prop map[string]any
		want []string
	}{
		{"bare", map[string]any{"type": "string"}, []string{"no title", "no description", "no examples"}},
		{"documented", map[string]any{"type": "string", "title": "Message SID", "description": "The SM… id.", "examples": []any{"SM123"}}, nil},
		{"boolean needs no example", map[string]any{"type": "boolean", "title": "Draft", "description": "Open as a draft."}, nil},
		{"enum needs no example", map[string]any{"type": "string", "enum": []any{"a", "b"}, "title": "Mode", "description": "Which mode."}, nil},
		{"example must validate", map[string]any{"type": "string", "pattern": "^SM", "title": "Message SID", "description": "The SM… id.", "examples": []any{"AC123"}}, []string{"examples[0]"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := paramDocProblems(tc.prop)
			if len(got) != len(tc.want) {
				t.Fatalf("got %q, want %d problems like %q", got, len(tc.want), tc.want)
			}
			for i := range got {
				if !strings.HasPrefix(got[i], tc.want[i]) {
					t.Errorf("problem %d = %q, want prefix %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}
