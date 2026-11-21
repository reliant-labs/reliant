package catalog

import (
	"context"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/tools"
)

// ListTools returns all available tools
//
// forge:auth-unscoped-ok: the tool registry is built into the binary and the same for every caller
func (s *Service) ListTools(
	ctx context.Context,
	req *connect.Request[reliantv1.ListToolsRequest],
) (*connect.Response[reliantv1.ListToolsResponse], error) {
	// Get all available tools from the tools registry
	toolRegistry := tools.GetToolRegistry()

	builder := s.toolBuilder()
	var toolList []*reliantv1.ToolInfo
	for _, toolDef := range toolRegistry {
		// Create tool instance to get its description
		tool := builder.ToolFor(toolDef)
		description := ""
		if tool != nil {
			description = tool.Description()
		}

		// Categorize tools based on their names
		category := "General"
		switch toolDef.Name {
		case "view", "write", "edit", "find_replace":
			category = "File Operations"
		case "shell", "shell_list", "shell_output", "shell_wait", "shell_kill", "wait_for_machine":
			category = "Execution"
		case "fetch", "websearch":
			category = "Network"
		case "create_plan", "update_plan", "get_plan":
			category = "Planning"
		case "list_tasks", "add_task", "update_task", "create_subtask":
			category = "Task Management"
		case "project_analyzer", "sourcegraph":
			category = "Analysis"
		case "build":
			category = "Build"
		case "state_transition":
			category = "State Management"
		case "agent":
			category = "Agent Management"
		case "notes":
			category = "Documentation"
		case "save_recommendations":
			category = "Recommendations"
		case "metadata_writer":
			category = "Metadata"
		}

		toolList = append(toolList, &reliantv1.ToolInfo{
			Name:        toolDef.Name,
			Description: description,
			Category:    category,
		})
	}

	resp := &reliantv1.ListToolsResponse{
		Tools: toolList,
		Total: int32(len(toolList)),
	}
	return connect.NewResponse(resp), nil
}
