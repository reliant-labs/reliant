// Copyright (c) 2025 Reliant Labs
package connection

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/connectionrpc"
	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/db/core"
)

func connectionStatusToProto(s string) reliantv1.ConnectionStatus {
	switch s {
	case core.ConnectionStatusActive:
		return reliantv1.ConnectionStatus_CONNECTION_STATUS_ACTIVE
	case core.ConnectionStatusNeedsReauth:
		return reliantv1.ConnectionStatus_CONNECTION_STATUS_NEEDS_REAUTH
	case core.ConnectionStatusRevoked:
		return reliantv1.ConnectionStatus_CONNECTION_STATUS_REVOKED
	}
	return reliantv1.ConnectionStatus_CONNECTION_STATUS_UNSPECIFIED
}

func rfc3339Ptr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func connectionOwnerKindToProto(c *core.Connection) reliantv1.ConnectionOwnerKind {
	if c.OwnerKind == core.ConnectionOwnerOrg {
		return reliantv1.ConnectionOwnerKind_CONNECTION_OWNER_KIND_ORG
	}
	return reliantv1.ConnectionOwnerKind_CONNECTION_OWNER_KIND_USER
}

func connectionToProto(c *core.Connection) *reliantv1.Connection {
	scopes := c.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	return &reliantv1.Connection{
		OwnerKind: connectionOwnerKindToProto(c), OrgId: derefStr(c.OrgID),
		Id: c.ID, IntegrationId: c.IntegrationID, AuthKind: connectionrpc.AuthKind(c.AuthKind),
		Name: c.Name, AccountLabel: derefStr(c.AccountLabel), ExternalAccountId: derefStr(c.ExternalAccountID),
		Scopes: scopes, Status: connectionStatusToProto(c.Status), StatusReason: derefStr(c.StatusReason),
		IsDefault: c.IsDefault, AccessExpiresAt: rfc3339Ptr(c.AccessExpiresAt), LastUsedAt: rfc3339Ptr(c.LastUsedAt),
		CreatedAt: c.CreatedAt.UTC().Format(time.RFC3339), UpdatedAt: c.UpdatedAt.UTC().Format(time.RFC3339),
		Params: c.Params, SenderId: derefStr(c.SenderID),
	}
}

// orgAdminCaller is the caller of an rpc that administers an org's
// connection: a person signed in to the app, never a credential an agent can
// hold (connections.HumanOnly, reason human_only).
func orgAdminCaller(ctx context.Context) (string, error) {
	userID, err := connectionrpc.Caller(ctx)
	if err != nil {
		return "", err
	}
	if auth.RequirePerson(ctx, "manage an organization's connections") != nil {
		return "", svcerr.Wrap(connections.HumanOnly())
	}
	return userID, nil
}
