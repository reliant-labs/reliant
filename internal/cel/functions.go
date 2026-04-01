// Copyright (c) 2025 Reliant Labs
package cel

import (
	"regexp"

	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
)

// toolRequiresApproval checks if tool needs approval
// Usage: toolRequiresApproval(tool.name)
func toolRequiresApprovalImpl(arg ref.Val) ref.Val {
	toolName, ok := arg.Value().(string)
	if !ok {
		return types.NewErr("toolRequiresApproval requires string argument")
	}

	// Dangerous tools list
	dangerousTools := map[string]bool{
		"bash":       true,
		"powershell": true,
		"edit":       true,
		"write":      true,
		"delete":     true,
	}

	return types.Bool(dangerousTools[toolName])
}

// matchesPattern performs regex matching
// Usage: matchesPattern(message.content, "^/.*")
func matchesPatternImpl(text, pattern ref.Val) ref.Val {
	textStr, ok1 := text.Value().(string)
	patternStr, ok2 := pattern.Value().(string)

	if !ok1 || !ok2 {
		return types.NewErr("matchesPattern requires string arguments")
	}

	// Use Go regex
	matched, err := regexp.MatchString(patternStr, textStr)
	if err != nil {
		return types.NewErr("invalid regex pattern: %v", err)
	}

	return types.Bool(matched)
}
