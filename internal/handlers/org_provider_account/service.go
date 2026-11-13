// Package org_provider_account is the OrgProviderAccountService: an org's owners and admins
// manage the AI provider accounts the org owns (its API keys, who in the org may use each) and
// the provider workspaces whose members' seats it pays for (control-plane
// docs/design/org-provider-accounts.md §3, §6, §7). Members list the org's accounts without
// their key hints. Routing them (which account serves a chat) is not here.
package org_provider_account

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Store is the slice of the repository this service uses; *db.Repo satisfies it. Every method
// scopes to the org (or person) it is given, so an id of another org's row is not found.
type Store interface {
	ListOwnedProviderAccounts(ctx context.Context, owner core.AccountOwner) ([]core.ProviderAccount, error)
	ListOwnedAccountLimits(ctx context.Context, owner core.AccountOwner) (map[string]core.LimitState, error)
	UpsertProviderAccount(ctx context.Context, u core.AccountUpsert) (core.ProviderAccount, bool, error)
	PatchOwnedProviderAccount(ctx context.Context, owner core.AccountOwner, accountID string, p core.AccountPatch) (core.ProviderAccount, error)
	SetOrgAccountAccess(ctx context.Context, orgID, accountID, access string) (core.ProviderAccount, error)
	ReorderOwnedProviderAccounts(ctx context.Context, owner core.AccountOwner, provider string, orderedIDs []string) error
	DeleteOwnedProviderAccount(ctx context.Context, owner core.AccountOwner, accountID string) error
	RotateOrgAPIKey(ctx context.Context, orgID, accountID string, k core.OrgAPIKey) (core.ProviderAccount, error)
	ListOrgAccountGrants(ctx context.Context, orgID string) ([]core.OrgAccountGrant, error)
	SetOrgAccountGrants(ctx context.Context, orgID, accountID string, userIDs []string, grantedBy string) error

	// ListProviderAccounts is the caller's own accounts: a workspace is bound from one of them.
	ListProviderAccounts(ctx context.Context, userID string) ([]core.ProviderAccount, error)
	ListOrgProviderWorkspaces(ctx context.Context, orgID string) ([]core.OrgProviderWorkspace, error)
	BindOrgProviderWorkspace(ctx context.Context, w core.OrgProviderWorkspace) (core.OrgProviderWorkspace, bool, error)
	UnbindOrgProviderWorkspace(ctx context.Context, orgID, provider, externalOrgID string) error
	ListOrgSeats(ctx context.Context, orgID string) ([]core.ProviderAccount, error)
	ReleaseOrgSeat(ctx context.Context, orgID, accountID string) error
}

// Deps holds dependencies for the org_provider_account service. Composition
// (internal/app/compose.go) supplies Logger/Config and resolves added fields
// by type. The rpc methods on *Service are where this service's logic lives;
// see the forge `api` / `services` skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config
	// Store holds the org's accounts, their grants and limit state, and its workspace bindings.
	Store Store
	// ValidateKey checks a pasted API key against its provider with one cheap call before it is
	// stored (keyvalidate.Validate in production). ok=false carries a message for the admin.
	ValidateKey func(ctx context.Context, provider models.Family, apiKey string) (ok bool, message string)
	// AddingEnabled lets an org's owners and admins add accounts and bind workspaces
	// (ApiServerConfig orgs.provider_accounts_enabled, app.APIOrgProviderAccounts). Off, the
	// default, refuses both with FailedPrecondition; nothing else is gated.
	AddingEnabled bool
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("org_provider_account: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("org_provider_account: Deps.Config is required")
	}
	if d.Store == nil {
		return fmt.Errorf("org_provider_account: Deps.Store is required")
	}
	if d.ValidateKey == nil {
		return fmt.Errorf("org_provider_account: Deps.ValidateKey is required")
	}
	return nil
}

// Service implements the org_provider_account Connect RPC service.
type Service struct {
	reliantv1connect.UnimplementedOrgProviderAccountServiceHandler
	deps Deps
}

// New creates a new org_provider_account service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return &Service{deps: deps}, nil
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "org_provider_account"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewOrgProviderAccountServiceHandler(s, opts...)
	mux.Handle(path, handler)
}

// RegisterHTTP registers plain HTTP handlers (callbacks, REST endpoints)
// outside Connect RPC. This service has none.
func (s *Service) RegisterHTTP(_ *http.ServeMux, _ func(http.Handler) http.Handler) {}

// Ensure the generated protobuf package import remains referenced even before methods exist.
var _ = pb.File_reliant_v1_org_provider_account_proto
