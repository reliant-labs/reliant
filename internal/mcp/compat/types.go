package compat

import "github.com/modelcontextprotocol/go-sdk/mcp"

// EnvelopeName identifies the compatibility payload shape used for a tool call.
type EnvelopeName string

const (
	EnvelopeDirect                     EnvelopeName = "direct"
	EnvelopeParams                     EnvelopeName = "params"
	EnvelopeParamsApplicationJSON      EnvelopeName = "params.application/json"
	EnvelopeParamsArguments            EnvelopeName = "params.arguments"
	EnvelopeParamsArgumentsApplication EnvelopeName = "params.arguments.application/json"
)

// Attempt represents one wire payload try for a tool call.
type Attempt struct {
	Name    EnvelopeName
	Payload map[string]interface{}
}

// CallRequest is the normalized input for compatibility planning.
type CallRequest struct {
	ServerName string
	ToolName   string
	Arguments  map[string]interface{}
}

// ErrorKind is a normalized classification of call failure.
type ErrorKind string

const (
	ErrorKindNone           ErrorKind = "none"
	ErrorKindInvalidParams  ErrorKind = "invalid_params"
	ErrorKindSchemaMismatch ErrorKind = "schema_mismatch"
	ErrorKindNonRetryable   ErrorKind = "non_retryable"
)

// AttemptResult captures the result of one attempt.
type AttemptResult struct {
	Attempt Attempt
	Error   error
	Kind    ErrorKind
}

// CallOutcome represents final call result and attempt history.
type CallOutcome struct {
	Result         *mcp.CallToolResult
	WinningAttempt Attempt
	Attempts       []AttemptResult
	FinalError     error
}
