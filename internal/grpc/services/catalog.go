// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"connectrpc.com/connect"

	"github.com/reliant-labs/reliant/internal/auth"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/reference"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
)

// CatalogService implements the CatalogService RPC handlers
type CatalogService struct {
	reliantv1connect.UnimplementedCatalogServiceHandler
	toolsFactory *tools.ToolsFactory

	celCompletionsOnce sync.Once
	celCompletionsResp *reliantv1.GetCELCompletionsResponse
}

// NewCatalogService creates a new CatalogService
func NewCatalogService(toolsFactory *tools.ToolsFactory) *CatalogService {
	return &CatalogService{
		toolsFactory: toolsFactory,
	}
}

// ListModels returns all available models filtered by user's configured API keys
// Each model is returned once per available driver, allowing users to choose which API to use
func (s *CatalogService) ListModels(
	ctx context.Context,
	req *connect.Request[reliantv1.ListModelsRequest],
) (*connect.Response[reliantv1.ListModelsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	// Get user's available drivers (configured API keys)
	availableDrivers := drivers.GetAvailableDrivers(ctx, userID)

	// Get all user-visible models from the registry
	registry := models.MustGetRegistry()
	allModels := registry.GetUserVisibleModels()

	logging.Info("[ListModels] Building model list for user", "userID", userID, "totalModels", len(allModels), "availableDrivers", len(availableDrivers.Drivers))

	// Build list of all model+driver combinations
	// Key: "modelID@driverID" to ensure uniqueness
	var modelList []*reliantv1.ModelInfo

	for _, model := range allModels {
		// For each provider the model supports, check if user has that provider configured
		for _, provider := range model.Providers {
			// Local models don't need API keys - they're available if they exist in the registry
			isLocalDriver := provider.Driver == "local"
			if isLocalDriver {
				logging.Info("[ListModels] Found local model", "modelID", model.ID, "driver", provider.Driver)
			}
			if !isLocalDriver {
				driverConfig, exists := availableDrivers.Drivers[models.DriverID(provider.Driver)]
				if !exists || !driverConfig.Enabled || driverConfig.APIKey == "" {
					continue
				}
			}

			driverID := provider.Driver

			// Create a unique ID that includes the driver
			// Format: "claude-4.5-sonnet@openrouter"
			uniqueID := model.ID + "@" + driverID

			// Get display name for the driver (used for grouping in UI)
			driverDisplayName := getDriverDisplayName(driverID)

			modelList = append(modelList, &reliantv1.ModelInfo{
				Id:                      uniqueID,
				Name:                    model.Name,
				Provider:                driverDisplayName, // For UI grouping
				DriverId:                driverID,          // For routing
				Capabilities:            capabilitiesToStrings(model.Capabilities),
				ContextWindow:           int64(model.Capabilities.MaxContextWindow),
				DefaultMaxTokens:        int64(model.Capabilities.MaxOutputTokens),
				CostPer_1MIn:            model.Cost.InputPer1M,
				CostPer_1MOut:           model.Cost.OutputPer1M,
				CanReason:               model.Capabilities.CanReason,
				SupportsAttachments:     model.Capabilities.SupportsAttachments,
				Tags:                    model.Tags,
				SupportsTools:           model.Capabilities.SupportsTools,
				SupportsCaching:         model.Capabilities.SupportsCaching,
				SupportedThinkingLevels: models.SupportedThinkingLevels(model.Capabilities),
			})
		}
	}

	// Sort models: by provider, then by model priority within each provider
	sortModelsByProvider(modelList)

	logging.Info("[ListModels] Returning models", "count", len(modelList))

	resp := &reliantv1.ListModelsResponse{
		Models: modelList,
		Total:  int32(len(modelList)),
	}
	return connect.NewResponse(resp), nil
}

// capabilitiesToStrings converts model capabilities to a list of strings for display
func capabilitiesToStrings(caps models.ModelCapabilities) []string {
	var result []string
	if caps.SupportsTools {
		result = append(result, "tools")
	}
	if caps.SupportsCaching {
		result = append(result, "caching")
	}
	if caps.SupportsAttachments {
		result = append(result, "attachments")
	}
	if caps.CanReason {
		result = append(result, "reasoning")
	}
	return result
}

// getDriverDisplayName returns a human-readable name for a driver
func getDriverDisplayName(driverID string) string {
	displayNames := map[string]string{
		"anthropic":  "Anthropic",
		"openai":     "OpenAI",
		"codex":      "Codex",
		"gemini":     "Google AI",
		"openrouter": "OpenRouter",
		"local":      "Local",
	}
	if name, ok := displayNames[driverID]; ok {
		return name
	}
	return driverID
}

// sortModelsByProvider sorts models grouped by provider, with model priority within each group
// Uses centralized priority from models.yaml order
func sortModelsByProvider(modelList []*reliantv1.ModelInfo) {
	// Provider display order
	providerOrder := map[string]int{
		"Anthropic":  1,
		"OpenAI":     2,
		"Codex":      3,
		"Google AI":  4,
		"OpenRouter": 5,
		"Local":      6,
	}

	reg := models.MustGetRegistry()
	sort.Slice(modelList, func(i, j int) bool {
		mi, mj := modelList[i], modelList[j]

		// First sort by provider
		pi := providerOrder[mi.Provider]
		pj := providerOrder[mj.Provider]
		if pi == 0 {
			pi = 99
		}
		if pj == 0 {
			pj = 99
		}
		if pi != pj {
			return pi < pj
		}

		// Within same provider, sort by model family then model priority
		// Extract base model ID (before @)
		miBase := extractBaseModelID(mi.Id)
		mjBase := extractBaseModelID(mj.Id)

		// Sort by model family
		fi := models.FamilyPriority[models.GetModelFamily(miBase)]
		fj := models.FamilyPriority[models.GetModelFamily(mjBase)]
		if fi == 0 {
			fi = 99
		}
		if fj == 0 {
			fj = 99
		}
		if fi != fj {
			return fi < fj
		}

		// Within same family, sort by model priority (from YAML order)
		miPriority := reg.GetModelPriority(miBase)
		mjPriority := reg.GetModelPriority(mjBase)
		if miPriority != mjPriority {
			return miPriority < mjPriority
		}

		// Fall back to alphabetical
		return mi.Name < mj.Name
	})
}

// extractBaseModelID extracts the model ID without the driver suffix
// e.g., "claude-4.5-sonnet@openrouter" -> "claude-4.5-sonnet"
func extractBaseModelID(id string) string {
	if idx := strings.Index(id, "@"); idx != -1 {
		return id[:idx]
	}
	return id
}

// ListModelsByProvider returns all models for a specific provider
func (s *CatalogService) ListModelsByProvider(
	ctx context.Context,
	req *connect.Request[reliantv1.ListModelsByProviderRequest],
) (*connect.Response[reliantv1.ListModelsByProviderResponse], error) {
	provider := req.Msg.Provider

	// Get all models for the provider from the registry
	registry := models.MustGetRegistry()
	providerModels := registry.ListModelsByProvider(provider)

	// Convert to response format
	modelInfos := make([]*reliantv1.ModelInfo, 0, len(providerModels))
	for _, def := range providerModels {
		modelInfos = append(modelInfos, &reliantv1.ModelInfo{
			Id:                      def.ID,
			Name:                    def.Name,
			Provider:                provider,
			DriverId:                provider,
			ContextWindow:           int64(def.Capabilities.MaxContextWindow),
			DefaultMaxTokens:        int64(def.Capabilities.MaxOutputTokens),
			CostPer_1MIn:            def.Cost.InputPer1M,
			CostPer_1MOut:           def.Cost.OutputPer1M,
			CanReason:               def.Capabilities.CanReason,
			SupportedThinkingLevels: models.SupportedThinkingLevels(def.Capabilities),
		})
	}

	resp := &reliantv1.ListModelsByProviderResponse{
		Models: modelInfos,
	}
	return connect.NewResponse(resp), nil
}

// ListTools returns all available tools
func (s *CatalogService) ListTools(
	ctx context.Context,
	req *connect.Request[reliantv1.ListToolsRequest],
) (*connect.Response[reliantv1.ListToolsResponse], error) {
	// Get all available tools from the tools registry
	toolRegistry := tools.GetToolRegistry()

	var toolList []*reliantv1.ToolInfo
	for _, toolDef := range toolRegistry {
		// Create tool instance to get its description
		tool := toolDef.Factory(s.toolsFactory)
		description := ""
		if tool != nil {
			description = tool.Description()
		}

		// Categorize tools based on their names
		category := "General"
		switch toolDef.Name {
		case "view", "write", "edit", "find_replace":
			category = "File Operations"
		case "grep", "glob", "ls":
			category = "Search & Discovery"
		case "bash", "powershell", "bash_list", "bash_output", "bash_kill":
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

// ListNodes returns all workflow nodes available for the builder
func (s *CatalogService) ListNodes(
	ctx context.Context,
	req *connect.Request[reliantv1.ListNodesRequest],
) (*connect.Response[reliantv1.ListNodesResponse], error) {
	category := req.Msg.Category

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
			if field.Placeholder != nil {
				protoField.Placeholder = field.Placeholder
			}
			if field.CleanupSemantics != nil {
				protoField.CleanupSemantics = field.CleanupSemantics
			}

			inputFields = append(inputFields, protoField)
		}

		// Convert output fields
		outputFields := make([]*reliantv1.NodeInputField, 0, len(meta.OutputFields))
		for _, field := range meta.OutputFields {
			protoField := &reliantv1.NodeInputField{
				Name:        field.Name,
				Type:        field.Type,
				Description: field.Description,
			}
			if field.Default != nil {
				protoField.DefaultValue = fmt.Sprintf("%v", field.Default)
			}
			outputFields = append(outputFields, protoField)
		}

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

// GetCELCompletions returns CEL expression completion data for the workflow builder.
// The response is computed once and cached since the data is static.
func (s *CatalogService) GetCELCompletions(
	ctx context.Context,
	req *connect.Request[reliantv1.GetCELCompletionsRequest],
) (*connect.Response[reliantv1.GetCELCompletionsResponse], error) {
	s.celCompletionsOnce.Do(func() {
		s.celCompletionsResp = s.buildCELCompletionsResponse()
	})
	return connect.NewResponse(s.celCompletionsResp), nil
}

// buildCELCompletionsResponse assembles CEL completion data from reference and type registry.
func (s *CatalogService) buildCELCompletionsResponse() *reliantv1.GetCELCompletionsResponse {
	resp := &reliantv1.GetCELCompletionsResponse{}

	// 1. Map reference.CELNamespaces → CELNamespaceInfo protos
	for _, ns := range reference.CELNamespaces {
		fields := make([]*reliantv1.CELFieldInfo, 0, len(ns.Fields))
		for _, f := range ns.Fields {
			fields = append(fields, &reliantv1.CELFieldInfo{
				Name:        f.Name,
				Type:        f.Type,
				Description: f.Description,
			})
		}
		resp.Namespaces = append(resp.Namespaces, &reliantv1.CELNamespaceInfo{
			Name:        ns.Name,
			Description: ns.Description,
			IsDynamic:   ns.IsDynamic,
			Fields:      fields,
		})
	}

	// 2. Map reference.CELFunctions → CELFunctionInfo protos
	for _, fn := range reference.CELFunctions {
		// Determine if this is a member function from the signature pattern.
		// Member functions have signatures like "string.func(...)" or "list.func(...)"
		isMember := strings.Contains(fn.Signature, ".")
		resp.Functions = append(resp.Functions, &reliantv1.CELFunctionInfo{
			Name:        fn.Name,
			Signature:   fn.Signature,
			Description: fn.Description,
			Example:     fn.Example,
			IsMember:    isMember,
		})
	}

	// 3. Map reference.CELHelperTypes → CELHelperTypeInfo protos
	for _, ht := range reference.CELHelperTypes {
		fields := make([]*reliantv1.CELFieldInfo, 0, len(ht.Fields))
		for _, f := range ht.Fields {
			fields = append(fields, &reliantv1.CELFieldInfo{
				Name:        f.Name,
				Type:        f.Type,
				Description: f.Description,
			})
		}
		resp.HelperTypes = append(resp.HelperTypes, &reliantv1.CELHelperTypeInfo{
			Name:        ht.Name,
			Description: ht.Description,
			AccessPath:  ht.AccessPath,
			Fields:      fields,
		})
	}

	// 4. Build node output schemas from the type registry
	registry := wfcel.NewTypeRegistry()
	nodeTypes := registry.NodeTypes()
	sort.Strings(nodeTypes)

	for _, nodeType := range nodeTypes {
		outputFields := registry.OutputFieldsForNodeType(nodeType)
		if len(outputFields) == 0 {
			continue
		}

		fields := make([]*reliantv1.CELFieldInfo, 0, len(outputFields))
		for _, f := range outputFields {
			fields = append(fields, &reliantv1.CELFieldInfo{
				Name:        f.Name,
				Type:        f.Type,
				Description: f.Description,
			})
		}

		// Get display name from reference.GetNodeType if available
		displayName := nodeType
		if info, ok := reference.GetNodeType(nodeType); ok && info.Name != "" {
			displayName = info.Name
		}

		resp.NodeOutputSchemas = append(resp.NodeOutputSchemas, &reliantv1.CELNodeOutputSchema{
			NodeType:    nodeType,
			DisplayName: displayName,
			Fields:      fields,
		})
	}

	return resp
}
