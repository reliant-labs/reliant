package catalog

import (
	"context"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/featuregate"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
)

// ListTools returns all available tools
//
// forge:auth-unscoped-ok: the tool registry is built into the binary; the only caller-dependent part is that a feature-gated tool is listed only to a member of an org entitled to its feature (Gate.AllowedForUser)
func (s *Service) ListTools(
	ctx context.Context,
	req *connect.Request[reliantv1.ListToolsRequest],
) (*connect.Response[reliantv1.ListToolsResponse], error) {
	// Get all available tools from the tools registry
	toolRegistry := tools.GetToolRegistry()

	builder := s.toolBuilder()
	entitled := s.entitledFeatures(ctx)
	var toolList []*reliantv1.ToolInfo
	for _, toolDef := range toolRegistry {
		// A feature-gated tool does not exist for a caller entitled to no
		// org with its feature, so it is not listed.
		if f, gated := tools.FeatureGate(toolDef.Name); gated && !entitled(f) {
			continue
		}
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

// entitledFeatures answers, per feature and at most once each, whether the
// caller is a member of some org entitled to it. The request names no org, so
// this is Gate.AllowedForUser. It fails closed: no gate, no caller, a refusal
// or an error is "no".
func (s *Service) entitledFeatures(ctx context.Context) func(featuregate.Feature) bool {
	answers := map[featuregate.Feature]bool{}
	return func(f featuregate.Feature) bool {
		if allowed, asked := answers[f]; asked {
			return allowed
		}
		allowed := false
		if s.deps.Gate != nil {
			if userID, ok := auth.GetUserIDFromContext(ctx); ok && userID != "" {
				var err error
				allowed, err = s.deps.Gate.AllowedForUser(ctx, f, userID)
				if err != nil {
					logging.WarnContext(ctx, "[ListTools] Could not establish whether the caller is entitled to a feature; leaving its tools out",
						"feature", string(f), "error", err)
					allowed = false
				}
			}
		}
		answers[f] = allowed
		return allowed
	}
}
