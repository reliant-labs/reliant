// Copyright (c) 2025 Reliant Labs
package message

import (
	"encoding/json"
	"fmt"
)

// ContentPartWrapper is used for unmarshaling ContentPart from JSON
type ContentPartWrapper struct {
	ContentPart
}

// UnmarshalJSON implements custom unmarshaling for ContentPart interface
func (c *ContentPartWrapper) UnmarshalJSON(data []byte) error {
	// First, try to determine the type
	var typeDetector struct {
		Type   string `json:"type"`
		Reason string `json:"Reason"` // For Finish type
		ID     string `json:"ID"`     // For ToolCall
		Name   string `json:"Name"`   // For ToolCall
	}

	if err := json.Unmarshal(data, &typeDetector); err != nil {
		return err
	}

	// Check for Finish type (has Reason field)
	if typeDetector.Reason != "" {
		var finish Finish
		if err := json.Unmarshal(data, &finish); err != nil {
			return err
		}
		c.ContentPart = finish
		return nil
	}

	// Check for ToolCall (has ID and Name)
	if typeDetector.ID != "" && typeDetector.Name != "" {
		var toolCall ToolCall
		if err := json.Unmarshal(data, &toolCall); err != nil {
			return err
		}
		c.ContentPart = toolCall
		return nil
	}

	// Check for ToolResult
	if typeDetector.Type == "tool_result" {
		var toolResult ToolResult
		if err := json.Unmarshal(data, &toolResult); err != nil {
			return err
		}
		c.ContentPart = toolResult
		return nil
	}

	// Check for specific content types
	switch typeDetector.Type {
	case "text", "":
		// Try TextContent first (most common)
		var textContent TextContent
		if err := json.Unmarshal(data, &textContent); err == nil && textContent.Text != "" {
			c.ContentPart = textContent
			return nil
		}

		// If it's a simple string, wrap it
		var simpleText string
		if err := json.Unmarshal(data, &simpleText); err == nil {
			c.ContentPart = TextContent{Text: simpleText}
			return nil
		}
	case "reasoning":
		var reasoning ReasoningContent
		if err := json.Unmarshal(data, &reasoning); err != nil {
			return err
		}
		c.ContentPart = reasoning
		return nil
	case "binary":
		var binary BinaryContent
		if err := json.Unmarshal(data, &binary); err != nil {
			return err
		}
		c.ContentPart = binary
		return nil
	}

	// Default to TextContent if we can't determine the type
	var textContent TextContent
	if err := json.Unmarshal(data, &textContent); err != nil {
		return fmt.Errorf("unable to unmarshal ContentPart: %w", err)
	}
	c.ContentPart = textContent
	return nil
}

// UnmarshalContentParts unmarshals an array of ContentPart from JSON
func UnmarshalContentParts(data []byte) ([]ContentPart, error) {
	// First try to unmarshal as array of raw JSON
	var rawParts []json.RawMessage
	if err := json.Unmarshal(data, &rawParts); err != nil {
		// Maybe it's a single string?
		var singleString string
		if err := json.Unmarshal(data, &singleString); err == nil {
			return []ContentPart{TextContent{Text: singleString}}, nil
		}
		return nil, err
	}

	parts := make([]ContentPart, 0, len(rawParts))
	for _, raw := range rawParts {
		var wrapper ContentPartWrapper
		if err := wrapper.UnmarshalJSON(raw); err != nil {
			// Skip parts that fail to unmarshal
			continue
		}
		if wrapper.ContentPart != nil {
			parts = append(parts, wrapper.ContentPart)
		}
	}

	return parts, nil
}
