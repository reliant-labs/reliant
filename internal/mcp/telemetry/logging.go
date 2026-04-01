package telemetry

import (
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/mcp/compat"
)

func LogCallAttempt(serverName, toolName string, attemptIndex int, total int, attempt compat.Attempt, err error, kind compat.ErrorKind, next compat.EnvelopeName) {
	fields := []interface{}{
		"server", serverName,
		"tool", toolName,
		"attempt", attemptIndex + 1,
		"total_attempts", total,
		"envelope", attempt.Name,
		"error_kind", kind,
	}
	if err != nil {
		fields = append(fields, "error", err.Error())
	}
	if next != "" {
		fields = append(fields, "next_envelope", next)
	}
	logging.Warn("MCP tool call attempt failed", fields...)
}

func LogCallSuccess(serverName, toolName string, attemptIndex int, total int, envelope compat.EnvelopeName) {
	logging.Info("MCP tool call succeeded",
		"server", serverName,
		"tool", toolName,
		"attempt", attemptIndex+1,
		"total_attempts", total,
		"envelope", envelope)
}
