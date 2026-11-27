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
	"github.com/reliant-labs/reliant/internal/serveraction"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// validateActionNode checks an action node against the integration catalog,
// or, for a `uses: reliant/...`, the server-action catalog: `uses` must name a
// real action@major, and literal params must satisfy that action's schema. A param written as a {{expr}} template cannot be
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
	if args.GetHostedUse() != nil {
		if !model.CelStringIsSet(args.GetHostedUse()) {
			result.AddError(CategoryStructure, nodePath, "hosted_use", "hosted use grant must not be empty")
		}
		if model.CelStringIsExpr(args.GetUses()) || strings.TrimSpace(model.CelStringRaw(args.GetUses())) != "http/request@1" {
			result.AddError(CategoryStructure, nodePath, "hosted_use", "hosted use requires a fixed http/request@1 action")
		}
		if model.CelStringIsSet(args.GetConnection()) {
			result.AddError(CategoryStructure, nodePath, "hosted_use", "hosted_use and connection are mutually exclusive")
		}
		for name := range args.GetWith() {
			switch name {
			case "url", "method", "body":
			default:
				result.AddError(CategoryStructure, nodePath, "with."+name, "hosted HTTP actions accept only url, method and body")
			}
		}
	}
	if model.CelStringIsExpr(args.GetUses()) {
		return
	}
	uses := strings.TrimSpace(model.CelStringRaw(args.GetUses()))
	if serveraction.IsRef(uses) {
		validateServerActionNode(args, uses, nodePath, result)
		return
	}
	resolved, err := catalog.MustBuiltin().Resolve(uses)
	if err != nil {
		result.AddError(CategoryStructure, nodePath, "uses", err.Error())
		return
	}
	if resolved.Spec.GetParams() == nil {
		return
	}
	checkActionParams(resolved.Spec.GetParams().AsMap(), args, uses, nodePath, result)
}

// serverActions is the server-action catalog a `uses: reliant/...` resolves
// against; a variable so tests can declare actions of their own.
var serverActions = serveraction.Builtin

// validateServerActionNode checks a `uses: reliant/...` node against the
// server-action catalog, not the integration catalog: the name must be
// declared, and its literal params must satisfy its schema. A server action
// runs on Reliant's own state as the run's principal, so it takes none of an
// integration action's ways to authenticate.
func validateServerActionNode(args *reliantv1.ActionArgs, uses string, nodePath []string, result *Result) {
	spec, err := serverActions().Resolve(uses)
	if err != nil {
		result.AddError(CategoryStructure, nodePath, "uses", err.Error())
		return
	}
	if model.CelStringIsSet(args.GetConnection()) {
		result.AddError(CategoryStructure, nodePath, "connection", fmt.Sprintf("%s is a Reliant server action: it runs as the run itself and takes no connection", uses))
	}
	if args.GetCredential() != nil {
		result.AddError(CategoryStructure, nodePath, "credential", fmt.Sprintf("%s is a Reliant server action: it runs as the run itself and takes no credential", uses))
	}
	if spec.Params == nil {
		return
	}
	checkActionParams(spec.Params, args, uses, nodePath, result)
}

// checkActionParams checks a node's `with:` against an action's params
// schema: every required param present, no unknown one when the schema is
// closed, and each literal value valid. A {{expr}} value is left to run time.
func checkActionParams(schema map[string]any, args *reliantv1.ActionArgs, uses string, nodePath []string, result *Result) {
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
