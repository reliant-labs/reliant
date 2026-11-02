// Copyright (c) 2025 Reliant Labs
package validation

import (
	"fmt"
	"sort"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// ParamIssue is one input an activation's params do not satisfy.
type ParamIssue struct {
	// Input is the workflow input the issue is about.
	Input string
	// Message is a sentence naming the input: "repos is required".
	Message string
}

// ValidateActivationParams checks the params an activation of a declared
// trigger stores against the workflow's inputs, as setup is finished:
//
//   - every param names a declared input and fits its type and constraints
//     (min_items / max_items included);
//   - every required input the trigger does not map from the event has a
//     value. mapped is the trigger's `inputs:` (input name -> template): those
//     inputs are set by each event, and an activation's param of that name is
//     ignored, exactly as at fire time.
//
// A required input is one the author marked required or gave no default
// (model.IsInputRequired). Only plain value inputs are considered: a model,
// message, preset, group or tools input is supplied by presets or the launch
// itself. A required multi-valued integration input needs at least one value,
// because an empty selection would watch nothing.
//
// It returns one issue per input, in input-name order; none means the params
// are complete. The same check runs wherever an activation is written or
// resumed and, on read, to decide NEEDS_SETUP.
func ValidateActivationParams(declared map[string]*reliantv1.Input, mapped map[string]string, params map[string]any) []ParamIssue {
	return ValidateActivationParamsWith(declared, mapped, params, nil)
}

// ValidateActivationParamsWith is ValidateActivationParams with the resolver
// for integration input types; nil means the embedded catalog's.
func ValidateActivationParamsWith(declared map[string]*reliantv1.Input, mapped map[string]string, params map[string]any, types ValueTypes) []ParamIssue {
	issues := map[string]string{}

	supplied := make(map[string]any, len(params))
	for name, value := range params {
		if _, fromEvent := mapped[name]; !fromEvent {
			supplied[name] = value
		}
	}
	for name := range supplied {
		for _, problem := range ValidateParamsWith(declared, map[string]any{name: supplied[name]}, types) {
			issues[name] = problem
		}
	}

	for name, input := range declared {
		if _, fromEvent := mapped[name]; fromEvent || input == nil || issues[name] != "" {
			continue
		}
		if workflow.RuntimeInjectedInputs[name] || !isSuppliedByParams(input) || !model.IsInputRequired(input) {
			continue
		}
		value, ok := supplied[name]
		switch {
		case !ok || value == nil:
			issues[name] = fmt.Sprintf("%s is required", name)
		case isEmptyMultiIntegration(input, value):
			issues[name] = fmt.Sprintf("%s needs at least 1 value", name)
		}
	}

	names := make([]string, 0, len(issues))
	for name := range issues {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]ParamIssue, 0, len(names))
	for _, name := range names {
		out = append(out, ParamIssue{Input: name, Message: issues[name]})
	}
	return out
}

// ValidateTriggerInputValues checks the values one event produced through a
// trigger's inputs mapping against the workflow's inputs, by the rule every
// launch applies (InputValue and validateProtoInputType): what fails here is
// what a firing of that event would be refused for. An undeclared name is
// left to the static check, and only plain value inputs are checked, as
// validateTriggerInputType does. It returns one message per problem, in name
// order.
func ValidateTriggerInputValues(declared map[string]*reliantv1.Input, values map[string]any, types ValueTypes) []string {
	vt := typesOrDefault(types)
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	var problems []string
	for _, name := range names {
		input := declared[name]
		if input == nil || values[name] == nil || !isSuppliedByParams(input) {
			continue
		}
		if err := validateProtoInputType(name, values[name], input, vt); err != nil {
			problems = append(problems, err.Error())
		}
	}
	return problems
}

// isSuppliedByParams reports whether an activation's params are where input's
// value comes from: a plain value, not a model, message, preset, group or
// tools input.
func isSuppliedByParams(input *reliantv1.Input) bool {
	switch input.GetConfig().(type) {
	case *reliantv1.Input_StringInput, *reliantv1.Input_IntegerInput, *reliantv1.Input_NumberInput,
		*reliantv1.Input_BooleanInput, *reliantv1.Input_EnumInput, *reliantv1.Input_ArrayInput,
		*reliantv1.Input_ObjectInput, *reliantv1.Input_IntegrationInput:
		return true
	}
	return false
}

func isEmptyMultiIntegration(input *reliantv1.Input, value any) bool {
	if !isIntegrationInput(input) || !input.GetIntegrationInput().GetMulti() {
		return false
	}
	items, ok := toAnySlice(value)
	return ok && len(items) == 0
}
