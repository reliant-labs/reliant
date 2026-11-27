// Copyright (c) 2025 Reliant Labs
package core

import (
	"context"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// MessageOrigin attributes a user-role message to the agent that sent it.
// This trusted attribution travels only on an in-process context, not an RPC.
type MessageOrigin struct {
	ThreadID string
	Kind     reliantv1.AgentMessageKind
	Subject  string
}

type messageOriginKey struct{}
type llmOnlyDeliveryKey struct{}

// WithMessageOrigin attributes messages to origin, defaulting its kind.
func WithMessageOrigin(ctx context.Context, origin MessageOrigin) context.Context {
	if origin.ThreadID == "" {
		return ctx
	}
	if origin.Kind == reliantv1.AgentMessageKind_AGENT_MESSAGE_KIND_UNSPECIFIED {
		origin.Kind = reliantv1.AgentMessageKind_AGENT_MESSAGE_KIND_MESSAGE
	}
	return context.WithValue(ctx, messageOriginKey{}, origin)
}

// MessageOriginFrom reports the trusted origin carried by ctx.
func MessageOriginFrom(ctx context.Context) (MessageOrigin, bool) {
	origin, ok := ctx.Value(messageOriginKey{}).(MessageOrigin)
	return origin, ok && origin.ThreadID != ""
}

// WithLLMOnlyDelivery marks a user message as hidden from the transcript.
func WithLLMOnlyDelivery(ctx context.Context) context.Context {
	return context.WithValue(ctx, llmOnlyDeliveryKey{}, true)
}

// LLMOnlyDelivery reports whether ctx marks a model-only delivery.
func LLMOnlyDelivery(ctx context.Context) bool {
	v, _ := ctx.Value(llmOnlyDeliveryKey{}).(bool)
	return v
}

// QueuedDisplayStyle returns the hidden display style for a model-only delivery.
func QueuedDisplayStyle(ctx context.Context) *int32 {
	if !LLMOnlyDelivery(ctx) {
		return nil
	}
	hidden := int32(reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN)
	return &hidden
}
