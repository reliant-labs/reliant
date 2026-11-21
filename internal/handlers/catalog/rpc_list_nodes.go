package catalog

import (
	"context"
	"fmt"
	"sort"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
)

// categoryDisplayNames maps category IDs to human-readable names
var categoryDisplayNames = map[string]string{
	"agentic":             "Agentic",
	"git":                 "Git",
	"worktree":            "Worktree",
	"message_processing":  "Message Processing",
	"tool_execution":      "Tool Execution",
	"context_management":  "Context Management",
	"approval":            "Approval",
	"run_step":            "Run Step",
	"workflow_management": "Workflow Management",
	"utility":             "Utility",
}

// outputFieldsToProto converts a node's output fields, with their nested
// children and visibility contexts, for the builder's Outputs tab.
func outputFieldsToProto(fields []schema.InputFieldInfo) []*reliantv1.NodeInputField {
	out := make([]*reliantv1.NodeInputField, 0, len(fields))
	for _, field := range fields {
		protoField := &reliantv1.NodeInputField{
			Name:               field.Name,
			Type:               field.Type,
			Description:        field.Description,
			Label:              field.Label,
			VisibilityContexts: field.VisibilityContexts,
			Children:           outputFieldsToProto(field.Children),
		}
		if field.Default != nil {
			protoField.DefaultValue = fmt.Sprintf("%v", field.Default)
		}
		out = append(out, protoField)
	}
	return out
}

// uiHintNodeTool marks a field that names a tool an invoke_tool node may run
// ((reliant).ui_hint in workflow_v2.proto). ListNodes fills its options.
const uiHintNodeTool = "node_tool"

// nodeToolOptions are the tools an invoke_tool node may name — the ones that
// opted in to node exposure — with the first sentence of each tool's own
// description, for the editor's tool picker.
func (s *Service) nodeToolOptions() []*reliantv1.NodeFieldOption {
	registry := map[string]tools.ToolDefinition{}
	for _, def := range tools.GetToolRegistry() {
		registry[def.Name] = def
	}
	builder := s.toolBuilder()
	exposed := tools.NodeExposedTools()
	options := make([]*reliantv1.NodeFieldOption, 0, len(exposed))
	for _, exposure := range exposed {
		option := &reliantv1.NodeFieldOption{Value: exposure.Tool}
		if def, ok := registry[exposure.Tool]; ok {
			if tool := builder.ToolFor(def); tool != nil {
				option.Description = firstSentence(tool.Description())
			}
		}
		options = append(options, option)
	}
	return options
}

// ListNodes returns all workflow nodes available for the builder
//
// forge:auth-unscoped-ok: the workflow node registry is built into the binary and the same for every caller
func (s *Service) ListNodes(
	ctx context.Context,
	req *connect.Request[reliantv1.ListNodesRequest],
) (*connect.Response[reliantv1.ListNodesResponse], error) {
	category := req.Msg.GetCategory()

	// Get only nodes visible in the workflow builder
	// Uses reflection-based schema - activities must implement ActivityWithMetadata
	metadataList := schema.ListVisibleActivities()

	// Filter by category if specified
	if category != "" {
		filtered := make([]schema.ActivityMetadata, 0)
		for _, meta := range metadataList {
			if string(meta.Category) == category {
				filtered = append(filtered, meta)
			}
		}
		metadataList = filtered
	}

	// Convert to response format
	nodeResponses := make([]*reliantv1.NodeInfo, 0, len(metadataList))
	categoryCounts := make(map[string]int)

	for _, meta := range metadataList {
		// Convert input fields with enhanced metadata
		inputFields := make([]*reliantv1.NodeInputField, 0, len(meta.InputFields))
		for _, field := range meta.InputFields {
			protoField := &reliantv1.NodeInputField{
				Name:               field.Name,
				Type:               field.Type,
				Description:        field.Description,
				Required:           field.Required,
				EnumValues:         field.EnumValues,
				UiHint:             field.UIHint,
				Label:              field.Label,
				VisibilityContexts: field.VisibilityContexts,
				IsCel:              field.IsCEL,
				Category:           field.Category,
				Example:            field.Example,
				TypeHint:           field.TypeHint,
			}

			// Set default value as string
			if field.Default != nil {
				protoField.DefaultValue = fmt.Sprintf("%v", field.Default)
			}

			// Set min/max for numeric fields
			if field.Min != nil {
				protoField.MinValue = field.Min
			}
			if field.Max != nil {
				protoField.MaxValue = field.Max
			}
			if field.CleanupSemantics != nil {
				protoField.CleanupSemantics = field.CleanupSemantics
			}
			if field.UIHint == uiHintNodeTool {
				protoField.Options = s.nodeToolOptions()
			}

			inputFields = append(inputFields, protoField)
		}

		outputFields := outputFieldsToProto(meta.OutputFields)

		nodeResponses = append(nodeResponses, &reliantv1.NodeInfo{
			Id:           meta.ID,
			DisplayName:  meta.DisplayName,
			Description:  meta.Description,
			Category:     string(meta.Category),
			InputFields:  inputFields,
			IconHint:     meta.IconHint,
			OutputFields: outputFields,
		})

		categoryCounts[string(meta.Category)]++
	}

	// Sort nodes by display name
	sort.Slice(nodeResponses, func(i, j int) bool {
		return nodeResponses[i].DisplayName < nodeResponses[j].DisplayName
	})

	// Build category info
	categoryInfos := make([]*reliantv1.NodeCategory, 0, len(categoryCounts))
	for catID, count := range categoryCounts {
		displayName := categoryDisplayNames[catID]
		if displayName == "" {
			displayName = catID
		}
		categoryInfos = append(categoryInfos, &reliantv1.NodeCategory{
			Id:          catID,
			DisplayName: displayName,
			Count:       int32(count),
		})
	}

	// Sort categories by display name
	sort.Slice(categoryInfos, func(i, j int) bool {
		return categoryInfos[i].DisplayName < categoryInfos[j].DisplayName
	})

	resp := &reliantv1.ListNodesResponse{
		Nodes:      nodeResponses,
		Categories: categoryInfos,
	}
	return connect.NewResponse(resp), nil
}
