// Copyright (c) 2025 Reliant Labs
package grpc

import (
	"context"

	"connectrpc.com/connect/v2"
	"github.com/reliant-labs/forge/pkg/mountkit"
	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/errreason"
)

// forgeAPIDisabledReason is the reason a forgeAPIDisabled refusal carries, in
// x-forge-error-reason. The web app reads it (isForgeAPIDisabled) to say "turned off on this server"
// rather than blame the user's daemon: FailedPrecondition alone is also what a
// forge rpc answers when forge ran and failed, so the code cannot tell the two
// apart.
const forgeAPIDisabledReason = errreason.ForgeAPIDisabled

// ForgeAPIDisabled is ForgeService's mount option when the operator turned
// the forge API off (ApiServerConfig.disable_forge_api,
// RELIANT_DISABLE_FORGE_API): forgeAPIDisabled innermost in its chain — a
// mount's own interceptors run inside the listener's — so every rpc
// answers FailedPrecondition, after auth, before the handler runs.
func ForgeAPIDisabled() mountkit.Option {
	return mountkit.WithInterceptorsV2(forgeAPIDisabled{}.intercept)
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
	return svcerr.ToConnectV2(svcerr.WithReason(
		svcerr.FailedPrecondition("the forge API is disabled in this deployment (RELIANT_DISABLE_FORGE_API)"),
		forgeAPIDisabledReason))
}

// intercept answers every call, unary or streaming, with the refusal; the
// listener's svcerr layer, outside it, delivers the refusal's reason.
func (d forgeAPIDisabled) intercept(connect.ServerFunc) connect.ServerFunc {
	return func(context.Context, connect.Spec, connect.ServerStream) error {
		return d.refusal()
	}
}
