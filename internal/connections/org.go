// Copyright (c) 2025 Reliant Labs

package connections

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// Org-owned connections (control-plane docs/design/org-sharing.md §7): the
// org's own accounts, its Slack bot or API key, which any live member may
// name in that org's runs (resolver.go) and only the org's owners and admins
// manage. Who may do what:
//
//   - create and reconnect: an owner or admin, read fresh (authz.RequireOrgAdmin,
//     so a just-demoted admin cannot act on a cached role), and only while the
//     deployment enables org connections (WithOrgConnections; D13);
//   - rename, set the default, test, delete, read the audit trail: an owner
//     or admin, read fresh, whatever the switch says (an admin must always be
//     able to remove the org's credential);
//   - read one or list them: any live member.
//
// That the caller is a person, not a token an agent can hold, is the
// handler's to check (auth.RequirePerson): this package has no credential.

// errOrgConnectionsDisabled is the refusal of a create or reconnect while the
// deployment has not enabled org connections.
func errOrgConnectionsDisabled() error {
	return newError(CodeFailedPrecondition, "org connections are not enabled on this deployment")
}

// WithOrgConnections turns on creating and reconnecting org-owned
// connections (ApiServerConfig orgs.connections_enabled). Off, the default,
// refuses both: an older replica, or the release a rollback would return
// to, may not route events through an org connection.
func (s *Service) WithOrgConnections(enabled bool) *Service {
	s.orgConnections = enabled
	if s.broker != nil {
		s.broker.orgConnections = enabled
	}
	return s
}

// requireOrgAdmin is authz.RequireOrgAdmin as this package's typed error: an
// outage is retryable, anything else is PermissionDenied.
func requireOrgAdmin(ctx context.Context, userID, orgID string) error {
	err := authz.RequireOrgAdmin(ctx, userID, orgID)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, authz.ErrMembershipUnavailable):
		return &Error{Code: CodeUnavailable, Message: "could not check your role in the organization; try again", Err: err}
	case errors.Is(err, authz.ErrPersonRequired):
		return &Error{Code: CodePermissionDenied, Message: "only a person signed in to the app can manage an organization's connections", Err: err}
	default:
		return &Error{Code: CodePermissionDenied, Message: "only an owner or admin of the organization can manage its connections", Err: err}
	}
}

// requireOrgMember is authz.OrgMemberDenial as this package's typed error.
func requireOrgMember(ctx context.Context, userID, orgID string) error {
	err := authz.OrgMemberDenial(ctx, userID, orgID)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, authz.ErrMembershipUnavailable):
		return &Error{Code: CodeUnavailable, Message: "could not check organization membership; try again", Err: err}
	default:
		return &Error{Code: CodePermissionDenied, Message: "you are not a member of that organization", Err: err}
	}
}

// GetOrg returns orgID's connection id to a live member of orgID.
func (s *Service) GetOrg(ctx context.Context, userID, orgID, id string) (*core.Connection, error) {
	if err := requireOrgMember(ctx, userID, orgID); err != nil {
		return nil, err
	}
	c, err := s.store.GetOwnedConnection(ctx, core.OrgConnectionOwner(orgID), id)
	return c, mapStoreErr(err)
}

// RenameOrg renames one of orgID's connections; an owner or admin only.
func (s *Service) RenameOrg(ctx context.Context, userID, orgID, id, name string) (*core.Connection, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > maxNameLen {
		return nil, newError(CodeInvalidArgument, "name is required and at most %d characters", maxNameLen)
	}
	if err := requireOrgAdmin(ctx, userID, orgID); err != nil {
		return nil, err
	}
	owner := core.OrgConnectionOwner(orgID)
	if err := s.store.RenameOwnedConnection(ctx, owner, id, name, userEvent(core.ConnectionEventRenamed, userID)); err != nil {
		return nil, mapStoreErr(err)
	}
	c, err := s.store.GetOwnedConnection(ctx, owner, id)
	return c, mapStoreErr(err)
}

// SetDefaultOrg makes one of orgID's connections the org's default for its
// integration: what a picker offers first. An unnamed reference still never
// falls back to it (D10). An owner or admin only.
func (s *Service) SetDefaultOrg(ctx context.Context, userID, orgID, id string) (*core.Connection, error) {
	if err := requireOrgAdmin(ctx, userID, orgID); err != nil {
		return nil, err
	}
	owner := core.OrgConnectionOwner(orgID)
	if err := s.store.SetDefaultOwnedConnection(ctx, owner, id, userEvent(core.ConnectionEventDefaultChanged, userID)); err != nil {
		return nil, mapStoreErr(err)
	}
	c, err := s.store.GetOwnedConnection(ctx, owner, id)
	return c, mapStoreErr(err)
}

// DeleteOrg revokes one of orgID's connections at the provider where an API
// exists (best effort, as Delete), then soft-deletes it and destroys its
// ciphertext. An owner or admin only; never gated by the rollout switch.
func (s *Service) DeleteOrg(ctx context.Context, userID, orgID, id string) error {
	if err := requireOrgAdmin(ctx, userID, orgID); err != nil {
		return err
	}
	owner := core.OrgConnectionOwner(orgID)
	conn, err := s.store.GetOwnedConnection(ctx, owner, id)
	if err != nil {
		return mapStoreErr(err)
	}
	if prov, ok := s.providers.Get(conn.IntegrationID); ok && conn.AuthKind == core.ConnectionAuthOAuth2 {
		if tok, _, err := s.tokens.token(ctx, owner, conn.ID); err == nil {
			prov.revoke(ctx, s.doer, conn.Params, tok)
		}
	}
	if err := s.store.SoftDeleteOwnedConnection(ctx, owner, id, userEvent(core.ConnectionEventDeleted, userID)); err != nil {
		return mapStoreErr(err)
	}
	s.tokens.Forget(id)
	return nil
}

// TestOrg probes one of orgID's connections, as Test does a person's. An
// owner or admin only.
func (s *Service) TestOrg(ctx context.Context, userID, orgID, id string) (*TestResult, error) {
	if err := requireOrgAdmin(ctx, userID, orgID); err != nil {
		return nil, err
	}
	return s.test(ctx, core.OrgConnectionOwner(orgID), id)
}

// EventsOrg pages the audit trail of one of orgID's connections: every
// member's use of it, each attributed to the member who acted. An owner or
// admin only (who posted as the org's account is theirs to answer).
func (s *Service) EventsOrg(ctx context.Context, userID, orgID, id string, limit int, beforeID int64) ([]core.ConnectionEvent, error) {
	if err := requireOrgAdmin(ctx, userID, orgID); err != nil {
		return nil, err
	}
	if _, err := s.store.GetOwnedConnection(ctx, core.OrgConnectionOwner(orgID), id); err != nil {
		return nil, mapStoreErr(err)
	}
	evs, err := s.store.ListAllConnectionEvents(ctx, id, limit, beforeID)
	return evs, mapStoreErr(err)
}
