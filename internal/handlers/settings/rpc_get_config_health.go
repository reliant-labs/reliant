package settings

import (
	"context"
	"encoding/json"

	"connectrpc.com/connect"

	"github.com/reliant-labs/reliant/internal/auth"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/drivers"

	"github.com/reliant-labs/reliant/internal/validation"
)

// ============================================================================
// Configuration Health
// ============================================================================

// GetConfigHealth retrieves configuration errors and warnings.
// Collects errors from preset, workflow, and MCP loading.
func (s *Service) GetConfigHealth(
	ctx context.Context,
	req *connect.Request[reliantv1.GetConfigHealthRequest],
) (*connect.Response[reliantv1.GetConfigHealthResponse], error) {
	collector := validation.Global()

	// Get all errors from the collector
	errors := collector.Errors()

	// Credential health is per-user, so it is computed here rather than held in
	// the process-global collector.
	if userID, ok := auth.GetUserIDFromContext(ctx); ok {
		if stale, err := drivers.HasStaleReliantKey(ctx, s.deps.Repo, userID); err == nil && stale {
			// Heal first: the message is only true if the replacement failed.
			if _, configured, healErr := drivers.HealReliantKey(ctx, userID); healErr != nil {
				errors = append(errors, validation.NewError(validation.CategoryConfig,
					"Your Reliant LLM key is in a legacy format the gateway rejects, so Reliant models are unavailable, and automatic replacement failed: "+healErr.Error()+". Reconnect your Reliant account (re-sync) to replace the key.").
					Source("provider:reliant").
					Build())
			} else if !configured {
				errors = append(errors, validation.NewError(validation.CategoryConfig,
					"Your Reliant LLM key is in a legacy format the gateway rejects, so Reliant models are unavailable, and this deployment cannot replace it automatically. Reconnect your Reliant account (re-sync) to replace the key.").
					Source("provider:reliant").
					Build())
			}
		}
	}

	// Apply type filter if specified
	if req.Msg.TypeFilter != nil && *req.Msg.TypeFilter != "" {
		filtered := make([]*validation.Error, 0)
		for _, err := range errors {
			if string(err.Category) == *req.Msg.TypeFilter {
				filtered = append(filtered, err)
			}
		}
		errors = filtered
	}

	// Convert to proto
	protoErrors := make([]*reliantv1.ConfigError, 0, len(errors))
	var errorCount, warningCount int32

	for _, err := range errors {
		protoErr := &reliantv1.ConfigError{
			Type:     string(err.Category),
			Source:   err.Source,
			Message:  err.Message,
			Severity: configSeverityFromString(string(err.Severity)),
		}

		// Convert details map
		if err.Details != nil {
			protoErr.Details = make(map[string]string)
			for k, v := range err.Details {
				if strVal, ok := v.(string); ok {
					protoErr.Details[k] = strVal
				} else {
					// Convert non-string values to JSON
					if jsonBytes, jsonErr := json.Marshal(v); jsonErr == nil {
						protoErr.Details[k] = string(jsonBytes)
					}
				}
			}
		}

		protoErrors = append(protoErrors, protoErr)

		if err.Severity == validation.SeverityError {
			errorCount++
		} else {
			warningCount++
		}
	}

	return connect.NewResponse(&reliantv1.GetConfigHealthResponse{
		Errors:       protoErrors,
		ErrorCount:   errorCount,
		WarningCount: warningCount,
	}), nil
}
