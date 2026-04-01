package model

import (
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
)

// GetEntryNodes returns the entry node IDs for a workflow.
// If Entry is explicitly set, returns those IDs.
// Otherwise returns nil (the engine determines entry nodes from the graph).
func GetEntryNodes(wf *reliantv1.Workflow) []string {
	if wf == nil {
		return nil
	}
	return wf.GetEntry()
}

// FindEdgesFrom returns all edges originating from a given node ID.
func FindEdgesFrom(wf *reliantv1.Workflow, nodeID string) []*reliantv1.Edge {
	if wf == nil {
		return nil
	}
	var result []*reliantv1.Edge
	for _, e := range wf.GetEdges() {
		if e.GetFrom() == nodeID {
			result = append(result, e)
		}
	}
	return result
}

// GetInputType returns the type string of an input.
func GetInputType(input *reliantv1.Input) string {
	if input == nil {
		return ""
	}
	return input.GetType()
}

// IsInputRequired returns true if the input has no default value.
func IsInputRequired(input *reliantv1.Input) bool {
	return GetInputDefault(input) == nil
}

// GetInputDefault returns the default value for an input, or nil if required (no default).
// The return type varies by input config type.
func GetInputDefault(input *reliantv1.Input) interface{} {
	if input == nil {
		return nil
	}
	switch cfg := input.GetConfig().(type) {
	case *reliantv1.Input_StringInput:
		if cfg.StringInput != nil && cfg.StringInput.Default != nil {
			return *cfg.StringInput.Default
		}
	case *reliantv1.Input_NumberInput:
		if cfg.NumberInput != nil && cfg.NumberInput.Default != nil {
			return *cfg.NumberInput.Default
		}
	case *reliantv1.Input_IntegerInput:
		if cfg.IntegerInput != nil && cfg.IntegerInput.Default != nil {
			return *cfg.IntegerInput.Default
		}
	case *reliantv1.Input_BooleanInput:
		if cfg.BooleanInput != nil && cfg.BooleanInput.Default != nil {
			return *cfg.BooleanInput.Default
		}
	case *reliantv1.Input_EnumInput:
		if cfg.EnumInput != nil && cfg.EnumInput.Default != nil {
			return cfg.EnumInput.Default.AsInterface()
		}
	case *reliantv1.Input_ModelInput:
		if cfg.ModelInput != nil && cfg.ModelInput.Default != nil {
			selector := cfg.ModelInput.Default
			if selector.GetId() == "" && len(selector.GetTags()) == 0 && len(selector.GetProviders()) == 0 {
				return nil
			}
			selectorValue := make(map[string]interface{})
			if selector.GetId() != "" {
				selectorValue["id"] = selector.GetId()
			}
			if len(selector.GetTags()) > 0 {
				tags := make([]interface{}, len(selector.GetTags()))
				for index, tag := range selector.GetTags() {
					tags[index] = tag
				}
				selectorValue["tags"] = tags
			}
			if len(selector.GetProviders()) > 0 {
				providers := make([]interface{}, len(selector.GetProviders()))
				for index, provider := range selector.GetProviders() {
					providers[index] = provider
				}
				selectorValue["providers"] = providers
			}
			return selectorValue
		}
	case *reliantv1.Input_AnyInput:
		if cfg.AnyInput != nil && cfg.AnyInput.Default != nil {
			return cfg.AnyInput.Default.AsInterface()
		}
	case *reliantv1.Input_MessageInput:
		if cfg.MessageInput != nil && cfg.MessageInput.Default != nil {
			return *cfg.MessageInput.Default
		}
	case *reliantv1.Input_AttachmentsInput:
		if cfg.AttachmentsInput != nil && cfg.AttachmentsInput.Default != nil {
			return cfg.AttachmentsInput.Default.AsInterface()
		}
	case *reliantv1.Input_ToolsInput:
		if cfg.ToolsInput != nil && cfg.ToolsInput.Default != nil {
			return cfg.ToolsInput.Default.AsInterface()
		}
	case *reliantv1.Input_ArrayInput:
		if cfg.ArrayInput != nil && cfg.ArrayInput.Default != nil {
			return cfg.ArrayInput.Default.AsInterface()
		}
	case *reliantv1.Input_ObjectInput:
		if cfg.ObjectInput != nil && cfg.ObjectInput.Default != nil {
			return cfg.ObjectInput.Default.AsInterface()
		}
	case *reliantv1.Input_PresetInput:
		if cfg.PresetInput != nil && cfg.PresetInput.Default != nil {
			return cfg.PresetInput.Default.AsInterface()
		}
	}
	return nil
}

// IsGroupInput returns true if the input is a group type.
func IsGroupInput(input *reliantv1.Input) bool {
	if input == nil {
		return false
	}
	_, ok := input.GetConfig().(*reliantv1.Input_GroupInput)
	return ok && input.GetType() == "group"
}

// GetGroupInputs returns the nested inputs for a group input, or nil if not a group.
func GetGroupInputs(input *reliantv1.Input) map[string]*reliantv1.Input {
	if input == nil {
		return nil
	}
	if cfg, ok := input.GetConfig().(*reliantv1.Input_GroupInput); ok && cfg.GroupInput != nil {
		return cfg.GroupInput.GetInputs()
	}
	return nil
}

// AllInputs returns a flattened map of all inputs from a workflow.
// GroupInput entries are expanded with "group.param" keys; the group itself is not included.
// Non-group inputs keep their original key.
func AllInputs(inputs map[string]*reliantv1.Input) map[string]*reliantv1.Input {
	if inputs == nil {
		return nil
	}
	result := make(map[string]*reliantv1.Input)
	for name, input := range inputs {
		if input == nil {
			continue
		}
		if nested := GetGroupInputs(input); nested != nil {
			for paramName, sub := range nested {
				result[name+"."+paramName] = sub
			}
		} else {
			result[name] = input
		}
	}
	return result
}
