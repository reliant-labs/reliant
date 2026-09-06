// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/connectorgrant"
	"github.com/reliant-labs/reliant/internal/mcpserver"
	"github.com/reliant-labs/reliant/internal/ospath"
)

// ConnectorService is the JWT-authed surface for managing connector grants.
//
// Connector credentials themselves authenticate the MCP endpoint, never this
// service: a connector cannot mint or widen itself, only a logged-in user can.
type ConnectorService struct {
	reliantv1connect.UnimplementedConnectorServiceHandler

	store connectorgrant.Store

	// publicURL is the externally reachable base URL, used to tell the user
	// where to point their MCP client.
	publicURL string
}

// NewConnectorService constructs the service.
func NewConnectorService(store connectorgrant.Store, publicURL string) *ConnectorService {
	return &ConnectorService{store: store, publicURL: publicURL}
}

// CreateConnector mints a credential bound to one daemon.
func (s *ConnectorService) CreateConnector(
	ctx context.Context,
	req *connect.Request[reliantv1.CreateConnectorRequest],
) (*connect.Response[reliantv1.CreateConnectorResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, nil)
	}

	msg := req.Msg
	grant, err := s.buildGrant(userID, msg)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	raw, hash, prefix, err := connectorgrant.GenerateCredential()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("mint connector credential: %w", err))
	}
	grant.TokenHash = hash
	grant.TokenPrefix = prefix

	if err := s.store.CreateGrant(ctx, grant); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create connector: %w", err))
	}

	return connect.NewResponse(&reliantv1.CreateConnectorResponse{
		Connector: toProtoConnector(grant),
		// Returned exactly once. Everything after this point holds only the hash.
		Credential: raw,
		McpUrl:     s.mcpURL(),
	}), nil
}

// buildGrant validates a create request and assembles the grant.
//
// Validation lives here, above the store, so the user gets a specific message
// instead of a constraint violation. The store and the DB re-check the same
// rules; this is the layer that explains them.
func (s *ConnectorService) buildGrant(userID string, msg *reliantv1.CreateConnectorRequest) (*connectorgrant.Grant, error) {
	daemonID := strings.TrimSpace(msg.GetDaemonId())
	if daemonID == "" {
		return nil, errors.New("a connector must be bound to a specific workspace")
	}

	name := strings.TrimSpace(msg.GetName())
	if name == "" {
		name = "connector"
	}

	tools, err := validateTools(msg.GetAllowedTools())
	if err != nil {
		return nil, err
	}

	pathRoot := strings.TrimSpace(msg.GetPathRoot())
	if pathRoot == "" {
		return nil, errors.New("a connector must specify a directory it may access")
	}
	// ospath, not filepath: the grant is bound to a specific daemon
	// (daemonID above is required) and PathRoot names a directory on THAT
	// machine, which may be Windows. The enforcement side is correct to use
	// filepath — it runs on the daemon, where the host OS is the right
	// authority — but this validation runs on the server.
	if !ospath.IsAbs(pathRoot) {
		// A relative root would be resolved against whatever the daemon's
		// working directory happens to be, which is not a decision the user
		// can reason about when granting access.
		//
		// Glob syntax gets a specific message: the root is a prefix, not a
		// pattern, so "*" is a natural guess that can never work. Saying so
		// beats letting someone conclude the field is broken.
		if strings.ContainsAny(pathRoot, "*?[") {
			return nil, fmt.Errorf(
				"the allowed directory is a path prefix, not a pattern, so %q cannot be used — "+
					"grant a directory such as /Users/you/code, or \"/\" for the whole machine",
				pathRoot)
		}
		return nil, fmt.Errorf("the allowed directory must be an absolute path, got %q", pathRoot)
	}

	execMode, execAllowlist, err := validateExec(msg.GetExecMode(), msg.GetExecAllowlist(), tools)
	if err != nil {
		return nil, err
	}

	grant := &connectorgrant.Grant{
		ID:           uuid.New().String(),
		UserID:       userID,
		DaemonID:     daemonID,
		Name:         name,
		AllowedTools: tools,
		// ospath.Clean for the same reason: filepath.Clean on a Linux server
		// leaves a Windows root's backslashes in place, and the confinement
		// check downstream compares this stored prefix against paths the
		// daemon builds.
		PathRoot:      ospath.Clean(pathRoot),
		ExecMode:      execMode,
		ExecAllowlist: execAllowlist,
	}

	if raw := msg.GetExpiresAt(); raw != "" {
		ts, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return nil, fmt.Errorf("expires_at must be an RFC3339 timestamp: %w", err)
		}
		if ts.Before(time.Now()) {
			return nil, errors.New("expires_at is in the past")
		}
		grant.ExpiresAt = &ts
	}

	return grant, nil
}

// validateTools rejects unknown tool names.
//
// Silently dropping an unrecognized name would produce a connector that is
// quietly narrower than the user asked for, and the failure would surface much
// later as a puzzling refusal.
func validateTools(requested []string) ([]string, error) {
	if len(requested) == 0 {
		return nil, errors.New("a connector must be granted at least one tool")
	}

	seen := make(map[string]bool, len(requested))
	tools := make([]string, 0, len(requested))
	for _, name := range requested {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := mcpserver.CatalogByName[name]; !ok {
			return nil, fmt.Errorf("unknown tool %q", name)
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		tools = append(tools, name)
	}

	if len(tools) == 0 {
		return nil, errors.New("a connector must be granted at least one tool")
	}
	return tools, nil
}

// validateExec maps the proto exec mode onto the stored one and checks it
// against the granted tools.
func validateExec(
	mode reliantv1.ConnectorExecMode,
	allowlist []string,
	tools []string,
) (connectorgrant.ExecMode, []string, error) {
	var execMode connectorgrant.ExecMode
	switch mode {
	case reliantv1.ConnectorExecMode_CONNECTOR_EXEC_MODE_ALLOWLIST:
		execMode = connectorgrant.ExecAllowlist
	case reliantv1.ConnectorExecMode_CONNECTOR_EXEC_MODE_UNRESTRICTED:
		execMode = connectorgrant.ExecUnrestricted
	default:
		// Includes UNSPECIFIED: an unset mode grants no shell access.
		execMode = connectorgrant.ExecDeny
	}

	cleaned := make([]string, 0, len(allowlist))
	seen := make(map[string]bool, len(allowlist))
	for _, cmd := range allowlist {
		cmd = strings.TrimSpace(cmd)
		if cmd == "" || seen[cmd] {
			continue
		}
		seen[cmd] = true
		cleaned = append(cleaned, cmd)
	}

	if execMode == connectorgrant.ExecAllowlist && len(cleaned) == 0 {
		return "", nil, errors.New("allowlist mode requires at least one allowed command")
	}

	// A grant that includes a shell tool but denies exec would refuse every
	// call to that tool. That is safe but confusing, and the user would have
	// no way to tell why their connector cannot run anything.
	if execMode == connectorgrant.ExecDeny {
		for _, name := range tools {
			if mcpserver.CatalogByName[name].NeedsExec {
				return "", nil, fmt.Errorf(
					"tool %q runs shell commands, so the connector needs an exec mode of allowlist or unrestricted", name)
			}
		}
	}

	if execMode != connectorgrant.ExecAllowlist {
		cleaned = nil
	}
	return execMode, cleaned, nil
}

// ListConnectors returns the caller's connectors.
func (s *ConnectorService) ListConnectors(
	ctx context.Context,
	_ *connect.Request[reliantv1.ListConnectorsRequest],
) (*connect.Response[reliantv1.ListConnectorsResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, nil)
	}

	grants, err := s.store.ListGrantsByUser(ctx, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list connectors: %w", err))
	}

	out := make([]*reliantv1.Connector, 0, len(grants))
	for _, g := range grants {
		out = append(out, toProtoConnector(g))
	}
	return connect.NewResponse(&reliantv1.ListConnectorsResponse{Connectors: out}), nil
}

// RevokeConnector revokes one of the caller's connectors.
func (s *ConnectorService) RevokeConnector(
	ctx context.Context,
	req *connect.Request[reliantv1.RevokeConnectorRequest],
) (*connect.Response[reliantv1.RevokeConnectorResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, nil)
	}

	id := strings.TrimSpace(req.Msg.GetId())
	if id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("connector id is required"))
	}

	// Scoped by user in the store, so a guessed id cannot revoke someone
	// else's connector.
	revoked, err := s.store.RevokeGrant(ctx, userID, id)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("revoke connector: %w", err))
	}
	return connect.NewResponse(&reliantv1.RevokeConnectorResponse{Revoked: revoked}), nil
}

// ListConnectorActivity returns the audit log.
func (s *ConnectorService) ListConnectorActivity(
	ctx context.Context,
	req *connect.Request[reliantv1.ListConnectorActivityRequest],
) (*connect.Response[reliantv1.ListConnectorActivityResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, nil)
	}

	limit := int(req.Msg.GetLimit())

	var (
		records []*connectorgrant.AuditRecord
		err     error
	)
	if grantID := strings.TrimSpace(req.Msg.GetGrantId()); grantID != "" {
		records, err = s.store.ListAuditByGrant(ctx, userID, grantID, limit)
	} else {
		records, err = s.store.ListAuditByUser(ctx, userID, limit)
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list connector activity: %w", err))
	}

	out := make([]*reliantv1.ConnectorActivity, 0, len(records))
	for _, r := range records {
		out = append(out, &reliantv1.ConnectorActivity{
			Id:           r.ID,
			GrantId:      r.GrantID,
			DaemonId:     r.DaemonID,
			ToolName:     r.ToolName,
			CommandType:  r.CommandType,
			Arguments:    string(r.Arguments),
			Denied:       r.Denied,
			ErrorMessage: r.ErrorMsg,
			DurationMs:   int32(r.DurationMS),
			Status:       r.Status,
			CreatedAt:    r.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return connect.NewResponse(&reliantv1.ListConnectorActivityResponse{Activity: out}), nil
}

// ListAvailableTools returns the grantable catalog, so the UI never hardcodes
// a list that drifts from the server's.
func (s *ConnectorService) ListAvailableTools(
	ctx context.Context,
	_ *connect.Request[reliantv1.ListAvailableToolsRequest],
) (*connect.Response[reliantv1.ListAvailableToolsResponse], error) {
	if _, ok := auth.GetUserIDFromContext(ctx); !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, nil)
	}

	tools := make([]*reliantv1.ConnectorTool, 0, len(mcpserver.Catalog))
	for _, t := range mcpserver.Catalog {
		tools = append(tools, &reliantv1.ConnectorTool{
			Name:        t.Name,
			Description: t.Description,
			Mutating:    t.Mutating,
			NeedsExec:   t.NeedsExec,
		})
	}
	return connect.NewResponse(&reliantv1.ListAvailableToolsResponse{Tools: tools}), nil
}

// mcpURL is the endpoint the user pastes into their MCP client.
func (s *ConnectorService) mcpURL() string {
	base := strings.TrimSuffix(strings.TrimSpace(s.publicURL), "/")
	if base == "" {
		// Without a configured public URL, return the path alone rather than
		// inventing a hostname that would silently be wrong.
		return mcpserver.MountPath
	}
	return base + mcpserver.MountPath
}

// toProtoConnector converts a grant for display. It never carries credential
// material beyond the visible prefix.
func toProtoConnector(g *connectorgrant.Grant) *reliantv1.Connector {
	if g == nil {
		return nil
	}

	out := &reliantv1.Connector{
		Id:            g.ID,
		DaemonId:      g.DaemonID,
		Name:          g.Name,
		TokenPrefix:   g.TokenPrefix,
		AllowedTools:  g.AllowedTools,
		PathRoot:      g.PathRoot,
		ExecMode:      execModeToProto(g.ExecMode),
		ExecAllowlist: g.ExecAllowlist,
		CreatedAt:     g.CreatedAt.UTC().Format(time.RFC3339),
	}

	if g.ExpiresAt != nil {
		out.ExpiresAt = optionalTime(g.ExpiresAt.UTC().Format(time.RFC3339))
	}
	if g.LastUsedAt != nil {
		out.LastUsedAt = optionalTime(g.LastUsedAt.UTC().Format(time.RFC3339))
	}
	if g.RevokedAt != nil {
		out.RevokedAt = optionalTime(g.RevokedAt.UTC().Format(time.RFC3339))
	}
	return out
}

func execModeToProto(m connectorgrant.ExecMode) reliantv1.ConnectorExecMode {
	switch m {
	case connectorgrant.ExecAllowlist:
		return reliantv1.ConnectorExecMode_CONNECTOR_EXEC_MODE_ALLOWLIST
	case connectorgrant.ExecUnrestricted:
		return reliantv1.ConnectorExecMode_CONNECTOR_EXEC_MODE_UNRESTRICTED
	default:
		return reliantv1.ConnectorExecMode_CONNECTOR_EXEC_MODE_DENY
	}
}

// optionalTime formats a timestamp for an optional proto string field.
func optionalTime(s string) *string { return &s }
