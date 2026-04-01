// Copyright (c) 2025 Reliant Labs
package validation

import (
	"reflect"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// This file registers all config types that have runtime type expectations.
// When adding a new config type with CEL template fields that need type validation,
// register it here using RegisterFieldExpectations.

func init() {
	// Register V2SaveMessageConfig field expectations.
	// These fields are string templates that contain CEL expressions,
	// but we validate what type the CEL expression should produce at runtime.
	RegisterFieldExpectations(reliantv1.SaveMessageConfig{}, map[string]*FieldInfo{
		"tool_results": {
			Name:     "tool_results",
			Kind:     reflect.Slice,
			IsSlice:  true,
			ElemType: reflect.TypeOf(message.ToolResult{}),
		},
		"tool_calls": {
			Name:     "tool_calls",
			Kind:     reflect.Slice,
			IsSlice:  true,
			ElemType: reflect.TypeOf(message.ToolCall{}),
		},
		"attachments": {
			Name:     "attachments",
			Kind:     reflect.Slice,
			IsSlice:  true,
			ElemType: reflect.TypeOf(""), // []string
		},
		"role": {
			Name: "role",
			Kind: reflect.String,
		},
		"content": {
			Name: "content",
			Kind: reflect.String,
		},
	})
}
