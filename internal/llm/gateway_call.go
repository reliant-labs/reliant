// Copyright (c) 2025 Reliant Labs
package llm

import (
	"context"
	"sync"
)

// GatewayCall receives the LLM gateway's id for one model call: what
// RecordGatewayCallID also puts on the call's chat span
// (telemetry.AttrGatewayCallID). The usage ledger reads it into the call's row
// (llm_call_usage.gateway_call_id), which then joins exactly to
// control-plane's wallet ledger, as the span does.
//
// It is carried on the ctx the caller hands the driver, the way the span is,
// because the id arrives in a response header only the gateway's driver
// reads, on failed attempts too, where no DriverResponse exists to carry it.
// The driver records it from its own goroutine, so it is safe for concurrent
// use. The zero value is ready; a nil *GatewayCall reads as no id.
type GatewayCall struct {
	mu sync.Mutex
	id string
}

type gatewayCallKey struct{}

// WithGatewayCall returns ctx carrying g: the gateway call id of the call made
// with the returned ctx is recorded into g. One GatewayCall per call; a call
// made under another's ctx would overwrite its id.
func WithGatewayCall(ctx context.Context, g *GatewayCall) context.Context {
	return context.WithValue(ctx, gatewayCallKey{}, g)
}

// ID is the gateway's id for the call's last attempt; "" when the call did
// not go through the gateway, the last attempt received no response, or the
// gateway sent no id.
func (g *GatewayCall) ID() string {
	if g == nil {
		return ""
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.id
}

func (g *GatewayCall) set(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.id = id
}
