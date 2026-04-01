// Copyright (c) 2025 Reliant Labs
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

// MCPToolAdapter adapts an MCP tool to the internal Tool interface
type MCPToolAdapter struct {
	projectPath string
	serverName  string
	tool        mcp.Tool
	manager     *mcp.Manager

	// Cached schema
	schema *jsonschema.Schema
}

// NewMCPToolAdapter creates a new adapter for an MCP tool
func NewMCPToolAdapter(serverName string, tool mcp.Tool, manager *mcp.Manager) (*MCPToolAdapter, error) {
	if err := validateMCPLogicalServerName(serverName); err != nil {
		return nil, err
	}

	adapter := &MCPToolAdapter{
		serverName: strings.TrimSpace(serverName),
		tool:       tool,
		manager:    manager,
	}

	// Parse and cache the schema
	adapter.parseSchema()

	return adapter, nil
}

// NewProjectMCPToolAdapter creates a project-scoped adapter for an MCP tool.
func NewProjectMCPToolAdapter(projectPath, serverName string, tool mcp.Tool, manager *mcp.Manager) (*MCPToolAdapter, error) {
	if err := validateMCPLogicalServerName(serverName); err != nil {
		return nil, err
	}

	adapter := &MCPToolAdapter{
		projectPath: projectPath,
		serverName:  strings.TrimSpace(serverName),
		tool:        tool,
		manager:     manager,
	}

	adapter.parseSchema()
	return adapter, nil
}

// Name returns the tool name with server prefix to avoid conflicts
func (a *MCPToolAdapter) Name() string {
	// Prefix with server name to avoid conflicts
	return fmt.Sprintf("mcp__%s__%s", a.serverName, a.tool.Name)
}

// Description returns the tool description
func (a *MCPToolAdapter) Description() string {
	desc := a.tool.Description
	if desc == "" {
		desc = fmt.Sprintf("MCP tool from %s", a.serverName)
	}
	// Add server info to description
	return fmt.Sprintf("%s [via MCP:%s]", desc, a.serverName)
}

// RequiresPermission returns permission requirements for the MCP tool
func (a *MCPToolAdapter) RequiresPermission(rctx *rctx.ToolContext, params ToolCall) (bool, error) {
	// Get working directory for permission context
	return true, nil
}

// ParamSchema returns the JSON schema for the tool parameters
func (a *MCPToolAdapter) ParamSchema() *jsonschema.Schema {
	return a.schema
}

// Run executes the MCP tool and returns the result
func (a *MCPToolAdapter) Run(rctx *rctx.ToolContext, params ToolCall) (ToolResponse, error) {
	logging.Info("Executing MCP tool",
		"server", a.serverName,
		"tool", a.tool.Name,
		"id", params.ID)

	// Parse the input parameters
	var arguments map[string]interface{}
	if params.Input != "" {
		if err := json.Unmarshal([]byte(params.Input), &arguments); err != nil {
			logging.Error("Failed to parse tool arguments", "error", err, "input", params.Input)
			return NewTextErrorResponse(fmt.Sprintf("Failed to parse arguments: %v", err)), nil
		}
	}

	// Call the MCP tool
	var (
		result *mcp.ToolResult
		err    error
	)
	if strings.TrimSpace(a.projectPath) != "" {
		result, err = a.manager.ProjectCallTool(a.projectPath, a.serverName, a.tool.Name, arguments)
	} else {
		result, err = a.manager.CallTool(a.serverName, a.tool.Name, arguments)
	}
	if err != nil {
		logging.Error("MCP tool execution failed",
			"server", a.serverName,
			"tool", a.tool.Name,
			"error", err)
		return NewTextErrorResponse(fmt.Sprintf("MCP tool error: %v", err)), nil
	}

	// Convert MCP result to internal format
	return a.convertResult(result), nil
}

// parseSchema converts the MCP tool schema to jsonschema.Schema
func (a *MCPToolAdapter) parseSchema() {
	// If no schema provided, create a minimal one
	if a.tool.InputSchema == nil {
		a.schema = &jsonschema.Schema{
			Type:        "object",
			Description: a.Description(),
		}
		return
	}

	normalizedSchema := normalizeSchemaForInvopop(a.tool.InputSchema)

	// Convert the map to jsonschema.Schema
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

	// Ensure description is set
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

// normalizeSchemaForInvopop converts common JSON Schema variants to forms
// accepted by invopop/jsonschema's Schema struct decoder.
// In particular, many MCP servers emit union types like: "type": ["object", "null"].
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

// convertResult converts MCP tool result to internal ToolResponse
func (a *MCPToolAdapter) convertResult(result *mcp.ToolResult) ToolResponse {
	if result == nil || len(result.Content) == 0 {
		return NewTextResponse("Tool executed successfully with no output")
	}

	// Combine all content into a single response
	var contentParts []string

	for _, content := range result.Content {
		switch content.Type {
		case "text":
			if content.Text != "" {
				contentParts = append(contentParts, content.Text)
			}
		case "image":
			// Handle image content
			if content.Data != "" && content.MimeType != "" {
				contentParts = append(contentParts,
					fmt.Sprintf("[Image: %s]", content.MimeType))
			}
		case "resource":
			// Handle resource content
			if content.Text != "" {
				contentParts = append(contentParts, content.Text)
			}
		default:
			// Handle unknown content types
			if content.Text != "" {
				contentParts = append(contentParts, content.Text)
			}
		}
	}

	// Join all content parts
	responseText := strings.Join(contentParts, "\n")

	if result.IsError {
		return NewTextErrorResponse(responseText)
	}

	return NewTextResponse(responseText)
}

// MCPToolRegistry manages MCP tools from multiple servers
type MCPToolRegistry struct {
	manager     *mcp.Manager
	projectPath string
	tools       map[string]Tool
	mu          sync.RWMutex
}

// NewMCPToolRegistry creates a new MCP tool registry
func NewMCPToolRegistry(manager *mcp.Manager) *MCPToolRegistry {
	return &MCPToolRegistry{
		manager: manager,
		tools:   make(map[string]Tool),
	}
}

// NewProjectMCPToolRegistry creates a project-scoped MCP tool registry.
func NewProjectMCPToolRegistry(manager *mcp.Manager, projectPath string) *MCPToolRegistry {
	return &MCPToolRegistry{
		manager:     manager,
		projectPath: projectPath,
		tools:       make(map[string]Tool),
	}
}

// RefreshTools updates the registry with current MCP tools
func (r *MCPToolRegistry) RefreshTools() error {
	logging.Debug("Refreshing MCP tool registry")

	// Get all tools from all servers
	var (
		serverTools map[string][]mcp.Tool
		err         error
	)
	if strings.TrimSpace(r.projectPath) != "" {
		serverTools, err = r.manager.ListProjectTools(r.projectPath)
	} else {
		serverTools, err = r.manager.ListAllTools()
	}
	if err != nil {
		return fmt.Errorf("failed to list MCP tools: %w", err)
	}

	newTools, err := r.buildAdaptersFromServerTools(serverTools)
	if err != nil {
		return err
	}

	// Update the registry
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
				adapter, err = NewProjectMCPToolAdapter(r.projectPath, serverName, tool, r.manager)
			} else {
				adapter, err = NewMCPToolAdapter(serverName, tool, r.manager)
			}
			if err != nil {
				return nil, fmt.Errorf("failed to build MCP adapter (server=%q tool=%q): %w", serverName, tool.Name, err)
			}
			toolName := adapter.Name()

			// Deterministically keep first occurrence by sorted server iteration.
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

// GetTools returns all MCP tools as Tool interfaces
// Tools are refreshed from the manager each time to pick up newly loaded servers
// Tools are sorted by name to ensure consistent ordering for LLM cache hits
func (r *MCPToolRegistry) GetTools() []Tool {
	// Refresh tools from manager to pick up any newly loaded servers
	if err := r.RefreshTools(); err != nil {
		logging.Warn("Failed to refresh MCP tools", "error", err)
		// Continue with existing tools
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	// Collect tools into slice
	tools := make([]Tool, 0, len(r.tools))
	for _, tool := range r.tools {
		tools = append(tools, tool)
	}

	// Sort tools by name to ensure consistent ordering for cache hits
	// This is critical because map iteration order is random in Go
	sort.Slice(tools, func(i, j int) bool {
		return tools[i].Name() < tools[j].Name()
	})

	return tools
}

// GetTool returns a specific MCP tool by name
func (r *MCPToolRegistry) GetTool(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tool, exists := r.tools[name]
	return tool, exists
}
