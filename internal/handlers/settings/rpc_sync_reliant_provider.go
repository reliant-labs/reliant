package settings

import (
	"context"
	"strings"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/controlplane"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/drivers"

	"github.com/reliant-labs/reliant/internal/logging"
)

// SyncReliantProvider mints the user's LLM gateway key via control-plane and
// persists it locally as the "reliant" provider credential.
//
// The key is an `rlat_` access token (llm:invoke, acting as the user) for the
// device controlplane.ReliantProviderKeyName. Control-plane returns a NEW
// plaintext on every call and revokes this device's previous key in the same
// transaction — it never re-serves a stored plaintext — so created/rotated
// come from whether a previous key was replaced, not from comparing strings.
func (s *Service) SyncReliantProvider(ctx context.Context, req *connect.Request[reliantv1.SyncReliantProviderRequest]) (*connect.Response[reliantv1.SyncReliantProviderResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if s.deps.ControlPlane == nil {
		return nil, svcerr.Internal("control-plane client not configured")
	}

	jwt, ok := auth.GetUserJWT(userID)
	if !ok || strings.TrimSpace(jwt) == "" {
		return nil, svcerr.Unauthenticated("missing user JWT for control-plane call")
	}

	key, err := s.deps.ControlPlane.MintLLMKey(ctx, jwt, controlplane.ReliantProviderKeyName)
	if err != nil {
		// A remote Connect client's error describes control-plane's failure,
		// not this rpc's: it is the logged cause, never the verdict.
		return nil, svcerr.WithCause(svcerr.Internal("failed to mint Reliant LLM key"), err)
	}
	plaintext := strings.TrimSpace(key.Plaintext)
	if plaintext == "" {
		return nil, svcerr.Internal("control-plane returned an empty Reliant LLM key")
	}

	_, _, err = s.deps.Repo.UpsertProviderAccount(ctx, core.AccountUpsert{
		UserID: userID, Provider: "reliant", AuthKind: core.AuthKindManaged, IdentityKey: core.ManagedIdentityKey,
		KeyHint: core.KeyHint(plaintext), Secrets: core.AccountSecrets{Secret: plaintext},
	})
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to persist Reliant API key"))
	}

	// A re-sync rotates the managed keys minted in each org too (per-org keys,
	// ControlPlaneConfig.managed_keys_per_org). One that fails keeps its
	// current key, which still serves; the org-less key above is the sync.
	if err := drivers.RotateOrgReliantKeys(ctx, userID); err != nil {
		logging.WarnContext(ctx, "Could not rotate every org's managed Reliant key after SyncReliantProvider", "user_id", userID, "error", err)
	}

	if err := s.deps.Repo.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{}); err != nil {
		logging.WarnContext(ctx, "Failed to emit config_health refetch after SyncReliantProvider", "error", err)
	}
	s.wakeChats(ctx, userID, "the Reliant provider key was synced")

	created := !key.Rotated
	rotated := key.Rotated

	return connect.NewResponse(&reliantv1.SyncReliantProviderResponse{
		Success:    true,
		Message:    "Reliant provider synced",
		Synced:     true,
		CreatedKey: created,
		RotatedKey: rotated,
		Provider: &reliantv1.ProviderStatus{
			Provider:     "reliant",
			DisplayName:  "Reliant",
			Configured:   true,
			AccountCount: 1,
		},
	}), nil
}
