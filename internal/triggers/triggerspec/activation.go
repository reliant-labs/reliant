// Copyright (c) 2025 Reliant Labs
package triggerspec

import (
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// A trigger's filter, inputs mapping and prompt read the event's `trigger` root,
// and under it `trigger.activation`: the activation the event fires for.
//
//	trigger.activation.id                  the activation's id
//	trigger.activation.owner.user_id       the user its runs execute as
//	trigger.activation.owner.github_id     the GitHub user id of the connection
//	                                       it listens through; "" when there is
//	                                       none, or it is not a GitHub one
//	trigger.activation.params.<input>      what it sets for the workflow's inputs
//
// It is data for CEL and nothing more: a filter decides who may start a run by
// comparing trigger.sender with it, and no table of trusted people stands
// behind it. It exists only here, before a run does. A run's nodes read the
// same `trigger` rebuilt from the event row, which has no activation to rebuild,
// so the key is deliberately not part of that root.
//
// activation.params are the activation's params as the run will receive them:
// the workflow's input defaults applied and each value coerced to its input's
// declared type. They let one declaration serve many activations, each with
// its own repository list or trusted ids, without pasting either into the filter:
//
//	trigger.sender.verified && (
//	    trigger.sender.id == trigger.activation.owner.github_id ||
//	    trigger.sender.id in trigger.activation.params.trusted_ids)
//
// Ids are strings on both sides (trigger.sender.id and github_id are strings),
// so a trusted_ids input is an array of strings: "583231", quoted. CEL does not
// match the number 583231 against the string "583231".

// Scope is everything a trigger expression reads.
type Scope struct {
	Trigger    map[string]any
	Activation map[string]any
}

// Owner is who an activation's runs are for.
type Owner struct {
	UserID string
	// GitHubID is the owner's GitHub user id, as a GitHub event names its
	// sender: the id of the connection the activation listens through, or
	// else the owner's linked GitHub identity (triggers.OwnerOf resolves
	// it). Empty when the owner has none.
	GitHubID string
}

const ownerGitHubIDPath = "activation.owner.github_id"

// ReadsOwnerGitHubID reports whether expr reads trigger.activation.owner.github_id:
// a filter that gates on who the owner is on GitHub.
func ReadsOwnerGitHubID(expr string) bool { return strings.Contains(expr, ownerGitHubIDPath) }

// OwnerGitHubID is the owner's GitHub id this scope carries; "" when unknown.
func (s Scope) OwnerGitHubID() string {
	owner, _ := s.Activation["owner"].(map[string]any)
	id, _ := owner["github_id"].(string)
	return id
}

// ActivationRoot is `trigger.activation`. Every key is always present, so an
// expression never fails with "no such key" on the root itself.
func ActivationRoot(id string, owner Owner, params map[string]any) map[string]any {
	if params == nil {
		params = map[string]any{}
	}
	return map[string]any{
		"id":     id,
		"owner":  map[string]any{"user_id": owner.UserID, "github_id": owner.GitHubID},
		"params": params,
	}
}

// Vars are the variables a program is evaluated with: the event's root with the
// activation under it. The event's own map is not modified.
func (s Scope) Vars() map[string]any {
	activation := s.Activation
	if activation == nil {
		activation = ActivationRoot("", Owner{}, nil)
	}
	root := make(map[string]any, len(s.Trigger)+1)
	for k, v := range s.Trigger {
		root[k] = v
	}
	root["activation"] = activation
	return map[string]any{string(wfcel.CELTrigger): root}
}

// ParamsInputs are the workflow inputs an activation sets through its params:
// every declared input except the ones the trigger's `inputs` mapping sets
// from the event, which an activation may not set (the event's value always
// wins, so a param of that name would be a setting that does nothing).
func ParamsInputs(inputs map[string]*reliantv1.Input, mapped map[string]string) map[string]*reliantv1.Input {
	out := make(map[string]*reliantv1.Input, len(inputs))
	for name, input := range inputs {
		if _, fromEvent := mapped[name]; !fromEvent && input != nil {
			out[name] = input
		}
	}
	return out
}

// ParamsSchema is the JSON Schema of activation.params for these inputs
// (ParamsInputs). It is closed: a param no input declares is a misspelling,
// and a read of one is rejected when the workflow is checked rather than
// failing on every firing.
func ParamsSchema(inputs map[string]*reliantv1.Input, types ValueTypes) map[string]any {
	props := make(map[string]any, len(inputs))
	for name, input := range inputs {
		if input != nil {
			props[name] = InputSchema(input, types)
		}
	}
	return map[string]any{
		"type":                 "object",
		"description":          "The activation's params: the workflow's declared inputs that the trigger does not set from the event.",
		"additionalProperties": false,
		"properties":           props,
	}
}

// ValueTypes resolves the value schema of an input type an integration defines
// (`github.repository`). It is declared here, by its consumer, so this package
// never learns what an integration or its catalog is.
type ValueTypes interface {
	// ValueSchema is the JSON Schema of ONE value of the type; ok is false when
	// no integration declares ref.
	ValueSchema(ref string) (schema map[string]any, ok bool)
}

// stringValueSchema is what a value of an integration type is taken to be when
// the type cannot be resolved: a plain string, which is what nearly every type
// is. The unknown type is reported where the workflow is validated; typing the
// reads of it as strings here avoids a second, misleading error on each.
var stringValueSchema = map[string]any{"type": "string"}

// InputSchema is the JSON Schema of the value an input holds: a scalar's own
// type, and for an integration type the type's value schema, wrapped in an
// array when the input is multi.
func InputSchema(input *reliantv1.Input, types ValueTypes) map[string]any {
	if nested := model.GetGroupInputs(input); nested != nil {
		return ParamsSchema(nested, types)
	}
	if cfg := input.GetIntegrationInput(); cfg != nil {
		item := stringValueSchema
		if types != nil {
			if schema, ok := types.ValueSchema(input.GetType()); ok && schema != nil {
				item = schema
			}
		}
		if cfg.GetMulti() {
			return map[string]any{"type": "array", "items": item}
		}
		return item
	}
	switch input.GetType() {
	case "string", "message":
		return map[string]any{"type": "string"}
	case "integer":
		return map[string]any{"type": "integer"}
	case "number":
		return map[string]any{"type": "number"}
	case "boolean":
		return map[string]any{"type": "boolean"}
	case "enum":
		if input.GetEnumInput().GetMulti() {
			return map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
		}
		return map[string]any{"type": "string"}
	case "array", "tools", "attachments":
		return map[string]any{"type": "array"}
	case "object":
		return map[string]any{"type": "object"}
	default:
		return map[string]any{}
	}
}
