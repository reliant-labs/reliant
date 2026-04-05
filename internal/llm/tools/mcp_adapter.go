package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/invopop/jsonschema"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/mcp"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// MCPToolAdapter adapts an MCP tool to the internal Tool interface.
type MCPToolAdapter struct {
	projectPath string
	serverName  string
	tool        mcp.Tool

	// Cached schema
	schema *jsonschema.Schema
}

// NewMCPToolAdapter creates a new adapter for an MCP tool.
func NewMCPToolAdapter(serverName string, tool mcp.Tool) (*MCPToolAdapter, error) {
	if err := validateMCPLogicalServerName(serverName); err != nil {
		return nil, err
	}

	adapter := &MCPToolAdapter{
		serverName: strings.TrimSpace(serverName),
		tool:       tool,
	}

	adapter.parseSchema()
	return adapter, nil
}

// NewProjectMCPToolAdapter creates a project-scoped adapter for an MCP tool.
func NewProjectMCPToolAdapter(projectPath, serverName string, tool mcp.Tool) (*MCPToolAdapter, error) {
	if err := validateMCPLogicalServerName(serverName); err != nil {
		return nil, err
	}

	adapter := &MCPToolAdapter{
		projectPath: projectPath,
		serverName:  strings.TrimSpace(serverName),
		tool:        tool,
	}

	adapter.parseSchema()
	return adapter, nil
}

// Name returns the tool name with server prefix to avoid conflicts.
func (a *MCPToolAdapter) Name() string {
	return fmt.Sprintf("mcp__%s__%s", a.serverName, a.tool.Name)
}

// Description returns the tool description.
func (a *MCPToolAdapter) Description() string {
	desc := a.tool.Description
	if desc == "" {
		desc = fmt.Sprintf("MCP tool from %s", a.serverName)
	}
	return fmt.Sprintf("%s [via MCP:%s]", desc, a.serverName)
}

// RequiresPermission returns permission requirements for the MCP tool.
func (a *MCPToolAdapter) RequiresPermission(_ *rctx.ToolContext, _ ToolCall) (bool, error) {
	return true, nil
}

// ParamSchema returns the JSON schema for the tool parameters.
func (a *MCPToolAdapter) ParamSchema() *jsonschema.Schema {
	return a.schema
}

// Run executes the MCP tool and returns the result.
func (a *MCPToolAdapter) Run(toolCtx *rctx.ToolContext, params ToolCall) (ToolResponse, error) {
	logging.Info("Executing MCP tool",
		"server", a.serverName,
		"tool", a.tool.Name,
		"id", params.ID)

	var arguments map[string]interface{}
	if params.Input != "" {
		if err := json.Unmarshal([]byte(params.Input), &arguments); err != nil {
			logging.Error("Failed to parse tool arguments", "error", err, "input", params.Input)
			return NewTextErrorResponse(fmt.Sprintf("Failed to parse arguments: %v", err)), nil
		}
	}

	runtime := runtimeFromToolContext(toolCtx)
	if runtime == nil {
		return NewTextErrorResponse("MCP runtime is not configured for this execution"), nil
	}

	var (
		result *mcp.ToolResult
		err    error
	)
	if strings.TrimSpace(a.projectPath) != "" {
		result, err = runtime.ProjectCallTool(a.projectPath, a.serverName, a.tool.Name, arguments)
	} else {
		result, err = runtime.CallTool(a.serverName, a.tool.Name, arguments)
	}
	if err != nil {
		logging.Error("MCP tool execution failed",
			"server", a.serverName,
			"tool", a.tool.Name,
			"error", err)
		return NewTextErrorResponse(fmt.Sprintf("MCP tool error: %v", err)), nil
	}

	return a.convertResult(result), nil
}

func (a *MCPToolAdapter) parseSchema() {
	if a.tool.InputSchema == nil {
		a.schema = &jsonschema.Schema{
			Type:        "object",
			Description: a.Description(),
		}
		return
	}

	normalizedSchema := normalizeSchemaForInvopop(a.tool.InputSchema)
	schemaData, err := json.Marshal(normalizedSchema)
	if err != nil {
		logging.Warn("Failed to marshal MCP tool schema",
			"server", a.serverName,
			"tool", a.tool.Name,
			"error", err)
		a.schema = a.buildLooseFallbackSchema()
		return
	}

	var schema jsonschema.Schema
	if err := json.Unmarshal(schemaData, &schema); err != nil {
		logging.Warn("Failed to parse MCP tool schema",
			"server", a.serverName,
			"tool", a.tool.Name,
			"error", err)
		a.schema = a.buildLooseFallbackSchema()
		return
	}

	if schema.Description == "" {
		schema.Description = a.Description()
	}

	a.schema = &schema
}

func (a *MCPToolAdapter) buildLooseFallbackSchema() *jsonschema.Schema {
	description := a.Description()

	propertyNames, requiredNames := extractSchemaHints(a.tool.InputSchema)
	if len(propertyNames) > 0 {
		description += "\n\nPossible argument keys: " + strings.Join(propertyNames, ", ")
	}
	if len(requiredNames) > 0 {
		description += "\nRequired keys: " + strings.Join(requiredNames, ", ")
	}

	return &jsonschema.Schema{
		Type:        "object",
		Description: description,
	}
}

func normalizeSchemaForInvopop(value interface{}) interface{} {
	switch v := value.(type) {
	case map[string]interface{}:
		normalized := make(map[string]interface{}, len(v))
		for key, raw := range v {
			if key == "type" {
				normalized[key] = normalizeSchemaType(raw)
				continue
			}
			normalized[key] = normalizeSchemaForInvopop(raw)
		}
		return normalized
	case []interface{}:
		normalized := make([]interface{}, len(v))
		for i, item := range v {
			normalized[i] = normalizeSchemaForInvopop(item)
		}
		return normalized
	default:
		return value
	}
}

func validateMCPLogicalServerName(serverName string) error {
	trimmed := strings.TrimSpace(serverName)
	if trimmed == "" {
		return errors.New("MCP server name is required")
	}
	if strings.Contains(trimmed, "::") || strings.Contains(trimmed, "/") {
		return fmt.Errorf("MCP server name invariant violated: %q must be logical-only (no '::' or '/')", trimmed)
	}
	return nil
}

func normalizeSchemaType(raw interface{}) interface{} {
	if arr, ok := raw.([]interface{}); ok {
		stringTypes := make([]string, 0, len(arr))
		for _, item := range arr {
			if s, ok := item.(string); ok && s != "" {
				stringTypes = append(stringTypes, s)
			}
		}
		if len(stringTypes) == 0 {
			return "object"
		}

		for _, preferred := range []string{"object", "array", "string", "number", "integer", "boolean"} {
			for _, current := range stringTypes {
				if current == preferred {
					return preferred
				}
			}
		}

		for _, current := range stringTypes {
			if current != "null" {
				return current
			}
		}
		return stringTypes[0]
	}

	if s, ok := raw.(string); ok && s != "" {
		return s
	}

	return "object"
}

func extractSchemaHints(input map[string]interface{}) ([]string, []string) {
	if input == nil {
		return nil, nil
	}

	propertyNames := make([]string, 0)
	if props, ok := input["properties"].(map[string]interface{}); ok {
		for name := range props {
			propertyNames = append(propertyNames, name)
		}
		sort.Strings(propertyNames)
	}

	requiredNames := make([]string, 0)
	if required, ok := input["required"].([]interface{}); ok {
		for _, item := range required {
			if s, ok := item.(string); ok && s != "" {
				requiredNames = append(requiredNames, s)
			}
		}
		sort.Strings(requiredNames)
	}

	return propertyNames, requiredNames
}

// convertResult converts MCP tool result to internal ToolResponse.
func (a *MCPToolAdapter) convertResult(result *mcp.ToolResult) ToolResponse {
	if result == nil || len(result.Content) == 0 {
		return NewTextResponse("Tool executed successfully with no output")
	}

	var contentParts []string
	for _, content := range result.Content {
		switch content.Type {
		case "text":
			if content.Text != "" {
				contentParts = append(contentParts, content.Text)
			}
		case "image":
			if content.Data != "" && content.MimeType != "" {
				contentParts = append(contentParts, fmt.Sprintf("[Image: %s]", content.MimeType))
			}
		case "resource":
			if content.Text != "" {
				contentParts = append(contentParts, content.Text)
			}
		default:
			if content.Text != "" {
				contentParts = append(contentParts, content.Text)
			}
		}
	}

	responseText := strings.Join(contentParts, "\n")
	if result.IsError {
		return NewTextErrorResponse(responseText)
	}
	return NewTextResponse(responseText)
}

// MCPToolRegistry manages MCP tools from multiple servers.
type MCPToolRegistry struct {
	runtime     MCPRuntime
	projectPath string
	tools       map[string]Tool
	mu          sync.RWMutex
}

// NewMCPToolRegistry creates a new MCP tool registry.
func NewMCPToolRegistry(runtime MCPRuntime, projectPath string) *MCPToolRegistry {
	return &MCPToolRegistry{
		runtime:     runtime,
		projectPath: projectPath,
		tools:       make(map[string]Tool),
	}
}

// RefreshTools updates the registry with current MCP tools.
func (r *MCPToolRegistry) RefreshTools() error {
	logging.Debug("Refreshing MCP tool registry")

	var (
		serverTools map[string][]mcp.Tool
		err         error
	)
	if strings.TrimSpace(r.projectPath) != "" {
		serverTools, err = r.runtime.ListProjectTools(r.projectPath)
	} else {
		serverTools, err = r.runtime.ListAllTools()
	}
	if err != nil {
		return fmt.Errorf("failed to list MCP tools: %w", err)
	}

	newTools, err := r.buildAdaptersFromServerTools(serverTools)
	if err != nil {
		return err
	}

	r.mu.Lock()
	r.tools = newTools
	r.mu.Unlock()

	if len(newTools) > 0 {
		logging.Info("MCP tool registry refreshed", "count", len(newTools))
	}
	return nil
}

func (r *MCPToolRegistry) buildAdaptersFromServerTools(serverTools map[string][]mcp.Tool) (map[string]Tool, error) {
	newTools := make(map[string]Tool)

	serverNames := make([]string, 0, len(serverTools))
	for serverName := range serverTools {
		serverNames = append(serverNames, serverName)
	}
	sort.Strings(serverNames)

	loggedDuplicateNames := make(map[string]bool)
	for _, serverName := range serverNames {
		toolsForServer := append([]mcp.Tool(nil), serverTools[serverName]...)
		sort.Slice(toolsForServer, func(i, j int) bool {
			if toolsForServer[i].Name == toolsForServer[j].Name {
				return toolsForServer[i].Description < toolsForServer[j].Description
			}
			return toolsForServer[i].Name < toolsForServer[j].Name
		})
		if err := validateMCPLogicalServerName(serverName); err != nil {
			return nil, fmt.Errorf("invalid MCP server identifier from manager list (project=%q): %w", r.projectPath, err)
		}
		for _, tool := range toolsForServer {
			var (
				adapter *MCPToolAdapter
				err     error
			)
			if strings.TrimSpace(r.projectPath) != "" {
				adapter, err = NewProjectMCPToolAdapter(r.projectPath, serverName, tool)
			} else {
				adapter, err = NewMCPToolAdapter(serverName, tool)
			}
			if err != nil {
				return nil, fmt.Errorf("failed to build MCP adapter (server=%q tool=%q): %w", serverName, tool.Name, err)
			}
			toolName := adapter.Name()
			if _, exists := newTools[toolName]; exists {
				if !loggedDuplicateNames[toolName] {
					logging.Warn("Pruned duplicate MCP tool name deterministically",
						"name", toolName,
						"keptFromOrder", "lexicographically-first-server")
					loggedDuplicateNames[toolName] = true
				}
				continue
			}

			newTools[toolName] = adapter
			logging.Debug("Registered MCP tool",
				"name", toolName,
				"server", serverName,
				"original", tool.Name)
		}
	}

	return newTools, nil
}

// GetTools returns all MCP tools as Tool interfaces.
func (r *MCPToolRegistry) GetTools() []Tool {
	if err := r.RefreshTools(); err != nil {
		logging.Warn("Failed to refresh MCP tools", "error", err)
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	tools := make([]Tool, 0, len(r.tools))
	for _, tool := range r.tools {
		tools = append(tools, tool)
	}

	sort.Slice(tools, func(i, j int) bool {
		return tools[i].Name() < tools[j].Name()
	})

	return tools
}

// GetTool returns a specific MCP tool by name.
func (r *MCPToolRegistry) GetTool(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tool, exists := r.tools[name]
	return tool, exists
}
