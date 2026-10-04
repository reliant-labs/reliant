// Copyright (c) 2025 Reliant Labs
package validation

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	gojsonschema "github.com/google/jsonschema-go/jsonschema"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// validateActionNode checks an action node against the integration catalog:
// `uses` must name a real action@major, and literal params must satisfy that
// action's manifest schema. A param written as a {{expr}} template cannot be
// typed statically, so it satisfies "present" and is otherwise left to run
// time; the same goes for a `uses` that is itself an expression.
func validateActionNode(node *reliantv1.Node, nodePath []string, result *Result) {
	args := node.GetAction()
	if args == nil {
		result.AddError(CategoryStructure, nodePath, "args", "action node missing args")
		return
	}
	if !model.CelStringIsSet(args.GetUses()) {
		result.AddError(CategoryStructure, nodePath, "uses", "required")
		return
	}
	if model.CelStringIsExpr(args.GetUses()) {
		return
	}
	uses := strings.TrimSpace(model.CelStringRaw(args.GetUses()))
	resolved, err := catalog.MustBuiltin().Resolve(uses)
	if err != nil {
		result.AddError(CategoryStructure, nodePath, "uses", err.Error())
		return
	}
	if resolved.Spec.GetParams() == nil {
		return
	}
	schema := resolved.Spec.GetParams().AsMap()
	props, _ := schema["properties"].(map[string]any)
	required, _ := schema["required"].([]any)
	closed := schema["additionalProperties"] == false

	for _, name := range required {
		if _, ok := args.GetWith()[fmt.Sprint(name)]; !ok {
			result.AddError(CategoryStructure, nodePath, "with."+fmt.Sprint(name),
				fmt.Sprintf("required parameter %q of %s is missing", name, uses))
		}
	}
	names := make([]string, 0, len(args.GetWith()))
	for name := range args.GetWith() {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, known := props[name]; !known {
			if closed {
				result.AddError(CategoryStructure, nodePath, "with."+name,
					fmt.Sprintf("unknown parameter %q for %s", name, uses))
			}
			continue
		}
		value := args.GetWith()[name].AsInterface()
		if s, ok := value.(string); ok && strings.Contains(s, "{{") {
			continue
		}
		if msg := checkParamValue(props[name], value); msg != "" {
			result.AddError(CategoryStructure, nodePath, "with."+name, msg)
		}
	}
}

// checkParamValue validates one literal value against its property schema.
func checkParamValue(propSchema any, value any) string {
	raw, err := json.Marshal(propSchema)
	if err != nil {
		return ""
	}
	var s gojsonschema.Schema
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	resolved, err := s.Resolve(nil)
	if err != nil {
		return ""
	}
	if err := resolved.Validate(value); err != nil {
		return err.Error()
	}
	return ""
}
