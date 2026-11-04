// Copyright (c) 2025 Reliant Labs
package validation

import (
	"sort"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// ListCoercion is one input a launch received as a single value where the
// workflow declares a list of values of that type, and that was therefore
// wrapped into a one-item list.
type ListCoercion struct {
	// Input is the input's path: "repos", or "<group>.<input>".
	Input string
	// Type is the declared item type: "github.repository", "enum", "preset"
	// or "tools".
	Type string
}

// CoerceSingleToList wraps, in place, every value that a launch supplied as
// ONE value for an input declared as a list of a typed item, when that value
// is itself a valid item: `repos: acme/web` for `repos: {type:
// github.repository, multi: true}` becomes `repos: [acme/web]`. It returns
// one ListCoercion per wrapped input, in path order, for the caller to log.
//
// This is the only reading of a scalar as a list, and only where it is
// unambiguous: the item type is declared (a multi integration type, a multi
// enum, a multi preset, tools) and the value passes that item type's check. A
// `type: array` input declares no item type, so its value is never guessed at;
// nor is a value that is not a valid item, which validation then refuses
// naming the input.
//
// It belongs at the launch boundary (StartChat, Run now, start_run, a trigger
// firing, `reliant workflow run`), where values come from people and agents,
// and at the runtime's own root input check, so a launch path that bypassed
// the boundary still does not fail a run for it. It is deliberately NOT
// applied to a workflow's YAML: a parent passing a string to a child's list
// input is an authoring error, and `reliant workflow validate` reports it.
func CoerceSingleToList(inputs map[string]any, declared map[string]*reliantv1.Input, types ValueTypes) []ListCoercion {
	if len(inputs) == 0 || len(declared) == 0 {
		return nil
	}
	vt := typesOrDefault(types)
	var out []ListCoercion
	for name, input := range declared {
		if input == nil {
			continue
		}
		if model.IsGroupInput(input) {
			group, ok := inputs[name].(map[string]any)
			if !ok {
				continue
			}
			for _, c := range CoerceSingleToList(group, model.GetGroupInputs(input), vt) {
				c.Input = name + "." + c.Input
				out = append(out, c)
			}
			continue
		}
		value, present := inputs[name]
		if !present || value == nil {
			continue
		}
		if _, isList := toAnySlice(value); isList {
			continue
		}
		itemType, ok := singleListItem(value, input, vt)
		if !ok {
			continue
		}
		inputs[name] = []any{value}
		out = append(out, ListCoercion{Input: name, Type: itemType})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Input < out[j].Input })
	return out
}

// singleListItem reports whether value is one valid item of input's declared
// list type, and names that item type.
func singleListItem(value any, input *reliantv1.Input, vt ValueTypes) (string, bool) {
	if isIntegrationInput(input) {
		if !input.GetIntegrationInput().GetMulti() {
			return "", false
		}
		ref := input.GetType()
		if validateAgainstSchema(valueSchemaFor(vt, ref), value) != nil {
			return "", false
		}
		return ref, true
	}
	text, isText := value.(string)
	if !isText || strings.TrimSpace(text) == "" {
		return "", false
	}
	if cfg := input.GetEnumInput(); cfg != nil {
		if !cfg.GetMulti() {
			return "", false
		}
		allowed := cfg.GetEnumValues()
		if len(allowed) == 0 {
			return "enum", true
		}
		for _, v := range allowed {
			if v == text {
				return "enum", true
			}
		}
		return "", false
	}
	if cfg := input.GetPresetInput(); cfg != nil {
		return "preset", cfg.GetMulti()
	}
	if model.GetInputType(input) == "tools" {
		return "tools", true
	}
	return "", false
}
