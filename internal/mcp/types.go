// Copyright (c) 2025 Reliant Labs
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

// Client defines the interface for MCP client operations
type Client interface {
	// Initialize performs the MCP handshake and initialization
	// The context is used for timeout/cancellation during connection.
	Initialize(ctx context.Context) error

	// ListTools returns all available tools from the MCP server
	ListTools() ([]Tool, error)

	// CallTool executes a tool and returns the result
	CallTool(name string, arguments map[string]interface{}) (*ToolResult, error)

	// ListResources returns available resources (optional MCP feature)
	ListResources() ([]Resource, error)

	// ReadResource reads a specific resource (optional MCP feature)
	ReadResource(uri string) (*ResourceContent, error)

	// ListPrompts returns available prompts (optional MCP feature)
	ListPrompts() ([]Prompt, error)

	// GetPrompt retrieves a specific prompt (optional MCP feature)
	GetPrompt(name string, arguments map[string]interface{}) (*PromptResult, error)

	// Close cleanly shuts down the client
	Close() error

	// IsConnected returns true if the client is connected
	IsConnected() bool

	// ServerInfo returns information about the connected server
	ServerInfo() *ServerInfo
}

// Tool represents an MCP tool definition
type Tool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

// ToolResult represents the result of a tool call per MCP protocol spec.
// This wire format uses []ToolContent for multi-part responses (text, images).
// For the domain model, see message.ToolResult which has a single Content string.
// Conversion: mcp.ToolResult -> message.ToolResult happens in mcp_adapter.go.
type ToolResult struct {
	Content []ToolContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

// ToolContent represents content returned by a tool
type ToolContent struct {
	Type     string                 `json:"type"`
	Text     string                 `json:"text,omitempty"`
	Data     string                 `json:"data,omitempty"`
	MimeType string                 `json:"mimeType,omitempty"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// Resource represents an MCP resource
type Resource struct {
	URI         string                 `json:"uri"`
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	MimeType    string                 `json:"mimeType,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

// ResourceContent represents the content of a resource
type ResourceContent struct {
	URI      string        `json:"uri"`
	MimeType string        `json:"mimeType,omitempty"`
	Content  []ToolContent `json:"contents"`
}

// Prompt represents an MCP prompt template
type Prompt struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Arguments   []PromptArgument       `json:"arguments,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

// PromptArgument represents an argument for a prompt
type PromptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// PromptResult represents the result of getting a prompt
type PromptResult struct {
	Description string                 `json:"description,omitempty"`
	Messages    []PromptMessage        `json:"messages"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

// PromptMessage represents a message in a prompt result
type PromptMessage struct {
	Role    string        `json:"role"`
	Content []ToolContent `json:"content"`
}

// ServerInfo contains information about the MCP server
type ServerInfo struct {
	Name         string                 `json:"name"`
	Version      string                 `json:"version"`
	Capabilities ServerCapabilities     `json:"capabilities,omitempty"`
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
}

// ServerCapabilities describes what features the server supports
type ServerCapabilities struct {
	Tools     *ToolsCapability     `json:"tools,omitempty"`
	Resources *ResourcesCapability `json:"resources,omitempty"`
	Prompts   *PromptsCapability   `json:"prompts,omitempty"`
	Logging   *LoggingCapability   `json:"logging,omitempty"`
}

// ToolsCapability indicates tool support
type ToolsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// ResourcesCapability indicates resource support
type ResourcesCapability struct {
	Subscribe   bool `json:"subscribe,omitempty"`
	ListChanged bool `json:"listChanged,omitempty"`
}

// PromptsCapability indicates prompt support
type PromptsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// LoggingCapability indicates logging support
type LoggingCapability struct {
	Level string `json:"level,omitempty"`
}

// ClientCapabilities describes what features the client supports
type ClientCapabilities struct {
	Experimental map[string]interface{} `json:"experimental,omitempty"`
	Sampling     *SamplingCapability    `json:"sampling,omitempty"`
	Roots        *RootsCapability       `json:"roots,omitempty"`
}

// SamplingCapability indicates sampling support
type SamplingCapability struct{}

// RootsCapability indicates roots support
type RootsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// InitializeParams contains parameters for initialization
type InitializeParams struct {
	ProtocolVersion string             `json:"protocolVersion"`
	ClientInfo      ClientInfo         `json:"clientInfo"`
	Capabilities    ClientCapabilities `json:"capabilities"`
}

// ClientInfo contains information about the client
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// InitializeResult contains the result of initialization
type InitializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	ServerInfo      ServerInfo         `json:"serverInfo"`
	Capabilities    ServerCapabilities `json:"capabilities,omitempty"`
}

// Error represents an MCP error
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string {
	if e.Data != nil {
		return fmt.Sprintf("MCP error %d: %s (data: %s)", e.Code, e.Message, string(e.Data))
	}
	return fmt.Sprintf("MCP error %d: %s", e.Code, e.Message)
}

// Standard MCP error codes
const (
	ErrorCodeParse          = -32700
	ErrorCodeInvalidRequest = -32600
	ErrorCodeMethodNotFound = -32601
	ErrorCodeInvalidParams  = -32602
	ErrorCodeInternalError  = -32603
)
