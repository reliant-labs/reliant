package service

import (
	"fmt"

	"github.com/reliant-labs/reliant/internal/mcp/compat"
)

func buildCallRequest(serverName, toolName string, arguments map[string]interface{}) compat.CallRequest {
	return compat.CallRequest{
		ServerName: serverName,
		ToolName:   toolName,
		Arguments:  arguments,
	}
}

// ToPromptArguments converts generic argument values to the string map expected by SDK GetPrompt.
func ToPromptArguments(arguments map[string]interface{}) map[string]string {
	if arguments == nil {
		return map[string]string{}
	}
	args := make(map[string]string, len(arguments))
	for k, v := range arguments {
		args[k] = fmt.Sprintf("%v", v)
	}
	return args
}
