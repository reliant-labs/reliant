// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	configv1 "github.com/reliant-labs/reliant/gen/config/v1"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
)

// llmSettings is a server role's LLMConfig in the shape the LLM driver layer
// takes. A stream deadline that is not positive is refused here: zero would
// silently take the default and a negative one would never fire, so either
// misreads what the operator asked for.
func llmSettings(cfg *configv1.LLMConfig) (drivers.Settings, error) {
	var errs []error
	positive := func(name string, d *durationpb.Duration) time.Duration {
		if d == nil {
			return 0 // unset: the LLM layer's default (mid-block: derived)
		}
		v := d.AsDuration()
		if v <= 0 {
			errs = append(errs, fmt.Errorf("config: %s = %s is not a positive duration", name, v))
		}
		return v
	}
	timeouts := llm.StreamTimeouts{
		Idle:         positive("RELIANT_LLM_STREAM_IDLE_TIMEOUT", cfg.GetStreamIdleTimeout()),
		ContentStall: positive("RELIANT_LLM_STREAM_CONTENT_STALL_TIMEOUT", cfg.GetStreamContentStallTimeout()),
		Progress:     positive("RELIANT_LLM_STREAM_PROGRESS_TIMEOUT", cfg.GetStreamProgressTimeout()),
		MidBlock:     positive("RELIANT_LLM_STREAM_MID_BLOCK_STALL_TIMEOUT", cfg.GetStreamMidBlockStallTimeout()),
	}
	if err := errors.Join(errs...); err != nil {
		return drivers.Settings{}, err
	}
	return drivers.Settings{
		StreamTimeouts:            timeouts,
		ReliantBaseURL:            cfg.GetReliantApiBaseUrl(),
		VertexProject:             cfg.GetVertexaiProject(),
		VertexLocation:            cfg.GetVertexaiLocation(),
		DebugOpenAIResponsesInput: cfg.GetDebugOpenaiResponsesInput(),
		DebugOpenAIToolSchemas:    cfg.GetDebugOpenaiToolSchemas(),
	}, nil
}
