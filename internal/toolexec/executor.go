// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// DefaultToolTimeout bounds a single tool call. It is the hard ceiling every
// blocking tool must stay under: the execution context cancels the call when
// it elapses, which surfaces to the model as an error rather than as a result.
//
// Sized for the blocking waiters (bash_wait) rather than for a typical
// command, because those are what press against it. bash_wait deliberately
// returns a minute early so it can answer "still running, call again" instead
// of being cancelled mid-flight, so this is defined in terms of that budget —
// the two cannot drift apart.
const DefaultToolTimeout = tools.MaxBlockingToolWait + time.Minute

// ToolExecutor defines the interface for executing tools
// Implementation: RemoteExecutor (DB-backed queue + daemon gRPC stream delivery)
type ToolExecutor interface {
	// ExecuteTool executes a tool and returns the result
	ExecuteTool(ctx context.Context, req *ToolRequest) (*ToolResult, error)

	// Close cleans up resources
	Close() error
}

// ToolRequest represents a tool execution request
// All fields are primitives - ToolContext is reconstructed as needed
type ToolRequest struct {
	// Tool identification
	ToolName       string // Name of the tool to execute
	ToolInput      string // JSON-encoded tool parameters
	ToolCallID     string // ID from LLM tool call
	ContentBlockID string // Database content block ID

	// Required identifiers
	UserID     string // User who owns this execution (REQUIRED for routing)
	ChatID     string // Chat this execution belongs to (REQUIRED)
	ProjectID  string // Project ID (REQUIRED)
	WorktreeID string // Worktree ID (optional, empty if none)

	// Context for tool execution (primitives only)
	Thread       string // Thread path (UUID matching workflow ID)
	MessageID    string // Message that triggered this tool call
	ProjectPath  string // Absolute path to project root
	ProjectName  string // Project display name
	WorktreePath string // Absolute path to worktree (empty if using project path)

	// Execution options
	WorkingDir  string            // Override working directory (optional)
	Timeout     time.Duration     // Execution timeout
	Environment map[string]string // Additional environment variables

	// Daemon targeting - specifies which daemon should execute this request.
	// nil means use default daemon resolution (local → cloud → wake).
	DaemonSelector *DaemonSelector // Target daemon selector (optional)

	// Repos lists every repo in the project. Tools with a `repo` param resolve
	// it to workspaceRoot/repo.relative_path. Empty for legacy single-repo /
	// projects.
	Repos []*core.Repo
}

// ToolResult represents the result of tool execution.
// This is an internal type for the toolexec package with execution-specific
// fields (timing, error codes). For the domain model, see message.ToolResult.
// Conversion: toolexec.ToolResult -> message.ToolResult happens in execute_tools.go.
type ToolResult struct {
	// Execution status
	Success      bool   // Whether execution succeeded
	IsError      bool   // Whether the content represents an error
	Backgrounded bool   // Whether the tool was converted to background execution
	Content      string // Tool output/result

	// Execution-specific metadata (not in domain model)
	Metadata     string    // JSON-encoded metadata
	StartTime    time.Time // Execution start time
	EndTime      time.Time // Execution end time
	ErrorMessage string    // Error message if success=false
	ErrorCode    string    // Error code for categorization

	// Binary content (images, PDFs) to pass to the LLM
	BinaryParts []message.BinaryContent
}

// ExecutionMetrics provides telemetry for tool execution
type ExecutionMetrics struct {
	ToolName   string
	Success    bool
	Duration   time.Duration
	IsRemote   bool // True if executed remotely, false if local
	ErrorCode  string
	RetryCount int
}
