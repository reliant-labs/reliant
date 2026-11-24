// Copyright (c) 2026 Reliant Labs
package llm

import (
	"context"
	"errors"

	"github.com/reliant-labs/reliant/internal/llm/drivererrors"
	"github.com/reliant-labs/reliant/internal/telemetry"
)

// ErrorType is the class of a failed provider call, as the error.type of its
// chat span and of the agent turn that made it: one value of telemetry's
// closed vocabulary (telemetry/ai_error_type.go), never the error's text. The
// checks run from the most specific to the least, so a context overflow
// reported with a 400 is context_overflow, not http_400. "" for nil.
func ErrorType(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.Canceled):
		return telemetry.ErrorTypeCanceled
	case drivererrors.IsContextOverflow(err):
		return telemetry.ErrorTypeContextOverflow
	case drivererrors.IsProviderCreditExhaustion(err):
		return telemetry.ErrorTypeCreditExhausted
	case drivererrors.IsProviderPromptRefusal(err):
		return telemetry.ErrorTypePromptRefused
	case errors.Is(err, ErrStreamContentStalled):
		return telemetry.ErrorTypeStreamStalled
	case errors.Is(err, drivererrors.ErrStreamEndedEarly):
		return telemetry.ErrorTypeStreamEndedEarly
	case errors.Is(err, drivererrors.ErrMalformedModelOutput):
		return telemetry.ErrorTypeMalformedOutput
	case errors.Is(err, drivererrors.ErrEmptyInput):
		return telemetry.ErrorTypeEmptyInput
	case errors.Is(err, drivererrors.ErrNoServableProvider):
		return telemetry.ErrorTypeNoProvider
	case errors.Is(err, context.DeadlineExceeded):
		return telemetry.ErrorTypeTimeout
	}
	// The status a driver printed into the error: a number, so the class is
	// one of a bounded set whatever else the text says.
	if status := drivererrors.StatusFromText(err.Error()); status != 0 {
		return telemetry.ErrorTypeHTTP(status)
	}
	return telemetry.ErrorTypeError
}
