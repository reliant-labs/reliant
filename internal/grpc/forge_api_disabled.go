// Copyright (c) 2025 Reliant Labs
package grpc

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/mountkit"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// forgeAPIDisabledReason is the X-Reliant-Reason a forgeAPIDisabled refusal
// carries. The web app reads it (isForgeAPIDisabled, web/src/api/forge-grpc.ts)
// to say "turned off on this server" rather than blame the user's daemon:
// FailedPrecondition alone is also what a forge rpc answers when forge ran and
// failed, so the code cannot tell the two apart.
const forgeAPIDisabledReason = "forge_api_disabled"

// ForgeAPIDisabled is ForgeService's mount option when the operator turned
// the forge API off (ApiServerConfig.disable_forge_api,
// RELIANT_DISABLE_FORGE_API): forgeAPIDisabled innermost in its chain — a
// mount's own handler options run inside the listener's — so every rpc
// answers FailedPrecondition, after auth, before the handler runs.
func ForgeAPIDisabled() mountkit.Option {
	return mountkit.WithHandlerOptions(connect.WithInterceptors(forgeAPIDisabled{}))
}

// forgeAPIDisabled answers every ForgeService rpc with FailedPrecondition: the
// operator's kill switch (RELIANT_DISABLE_FORGE_API).
//
// The service stays mounted, so a disabled surface is an honest answer the web
// shows as such. An unmounted one answered 404, which the web files as version
// skew. Innermost in the chain, so a caller is authenticated before learning
// how this deployment is configured, and no handler — StartDeploy applies to a
// live cluster — ever runs.
type forgeAPIDisabled struct{}

func (forgeAPIDisabled) refusal() error {
	err := svcerr.ToConnect(svcerr.FailedPrecondition("the forge API is disabled in this deployment (RELIANT_DISABLE_FORGE_API)"))
	err.Meta().Set("X-Reliant-Reason", forgeAPIDisabledReason)
	return err
}

func (d forgeAPIDisabled) WrapUnary(connect.UnaryFunc) connect.UnaryFunc {
	return func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, d.refusal()
	}
}

// WrapStreamingClient is the identity: this interceptor only ever wraps
// handlers.
func (forgeAPIDisabled) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (d forgeAPIDisabled) WrapStreamingHandler(connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(context.Context, connect.StreamingHandlerConn) error {
		return d.refusal()
	}
}
