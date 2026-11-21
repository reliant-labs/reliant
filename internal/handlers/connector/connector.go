// Copyright (c) 2025 Reliant Labs
package connector

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/connectorgrant"
	"github.com/reliant-labs/reliant/internal/mcpserver"
	"github.com/reliant-labs/reliant/internal/ospath"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
)

// connectorResource is the binding a grant's credential carries.
func connectorResource(grantID string) fat.Resource {
	return fat.Resource{Kind: fat.ResourceConnector, ID: grantID}
}

// createGrantWithCredential stores the grant, then mints its credential bound
// to it. The grant must exist first (the credential names it); if minting
// fails, the grant is revoked so no credential-less grant looks usable.
//
// A forge grant gets no credential: the Reliant Forge connector acts only as
// its signed-in person (OAuth), which a connector credential cannot do.
func (s *Service) createGrantWithCredential(ctx context.Context, grant *connectorgrant.Grant) (string, error) {
	if err := s.deps.Grants.CreateGrant(ctx, grant); err != nil {
		return "", fmt.Errorf("create connector: %w", err)
	}
	if grant.KindOrDefault() == connectorgrant.KindForge {
		return "", nil
	}
	res := connectorResource(grant.ID)
	minted, err := s.deps.Tokens.MintForUser(ctx, tokenauthority.MintRequest{
		UserID:    grant.UserID,
		Name:      "connector: " + grant.Name,
		Scopes:    []fat.Scope{fat.ScopeMCPConnector},
		Resource:  &res,
		ExpiresAt: grant.ExpiresAt,
	})
	if err != nil {
		_, _ = s.deps.Grants.RevokeGrant(ctx, grant.UserID, grant.ID)
		return "", fmt.Errorf("mint connector credential: %w", err)
	}
	grant.TokenPrefix = minted.DisplayPrefix
	if err := s.deps.Grants.SetTokenPrefix(ctx, grant.ID, minted.DisplayPrefix); err != nil {
		return "", err
	}
	return minted.Plaintext, nil
}

// buildGrant validates a create request and assembles the grant.
//
// Validation lives here, above the store, so the user gets a specific message
// instead of a constraint violation. The store and the DB re-check the same
// rules; this is the layer that explains them.
//
// Every refusal is an InvalidArgument whose text, written for the person
// creating the connector, is what they read.
func (s *Service) buildGrant(userID string, msg *reliantv1.CreateConnectorRequest) (*connectorgrant.Grant, error) {
	grant, err := grantFromRequest(userID, msg)
	if err != nil {
		return nil, svcerr.InvalidArgument(err.Error())
	}
	return grant, nil
}

func grantFromRequest(userID string, msg *reliantv1.CreateConnectorRequest) (*connectorgrant.Grant, error) {
	switch msg.GetKind() {
	case reliantv1.ConnectorKind_CONNECTOR_KIND_FORGE:
		return forgeGrantFromRequest(userID, msg)
	case reliantv1.ConnectorKind_CONNECTOR_KIND_UNSPECIFIED, reliantv1.ConnectorKind_CONNECTOR_KIND_WORKSPACE:
	default:
		return nil, fmt.Errorf("unknown connector kind %v", msg.GetKind())
	}
	if strings.TrimSpace(msg.GetOrgId()) != "" || len(msg.GetProjectNames()) > 0 {
		return nil, errors.New("a workspace connector names a workspace, not an organization or projects")
	}
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

// forgeGrantFromRequest builds a Reliant Forge grant: one organization,
// optionally some of its projects, and which forge tools.
//
// The org is not checked for membership here: the grant only ever NARROWS
// what the person's own credential may do at the control plane, so a grant
// naming an org they do not belong to permits nothing.
func forgeGrantFromRequest(userID string, msg *reliantv1.CreateConnectorRequest) (*connectorgrant.Grant, error) {
	if strings.TrimSpace(msg.GetDaemonId()) != "" || strings.TrimSpace(msg.GetPathRoot()) != "" ||
		len(msg.GetExecAllowlist()) > 0 ||
		(msg.GetExecMode() != reliantv1.ConnectorExecMode_CONNECTOR_EXEC_MODE_UNSPECIFIED &&
			msg.GetExecMode() != reliantv1.ConnectorExecMode_CONNECTOR_EXEC_MODE_DENY) {
		return nil, errors.New("a Reliant Forge connector reaches an organization, not a workspace: it takes no workspace, directory or shell access")
	}
	orgID := strings.TrimSpace(msg.GetOrgId())
	if orgID == "" {
		return nil, errors.New("choose the organization this connector may read")
	}
	projects, err := validateProjectNames(msg.GetProjectNames())
	if err != nil {
		return nil, err
	}
	tools, err := validateForgeTools(msg.GetAllowedTools())
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(msg.GetName())
	if name == "" {
		name = "Reliant Forge"
	}
	grant := &connectorgrant.Grant{
		ID: uuid.New().String(), UserID: userID, Name: name, Kind: connectorgrant.KindForge,
		OrgID: orgID, ProjectNames: projects, AllowedTools: tools, ExecMode: connectorgrant.ExecDeny,
	}
	if grant.ExpiresAt, err = parseExpiry(msg.GetExpiresAt()); err != nil {
		return nil, err
	}
	return grant, nil
}

// parseExpiry reads an optional RFC 3339 expiry that must lie ahead.
func parseExpiry(raw string) (*time.Time, error) {
	if raw == "" {
		return nil, nil
	}
	ts, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, fmt.Errorf("expires_at must be an RFC3339 timestamp: %w", err)
	}
	if ts.Before(time.Now()) {
		return nil, errors.New("expires_at is in the past")
	}
	return &ts, nil
}

// projectNameRE is a forge project name: forge.yaml `name`, the console's
// /forge/p/<project>.
var projectNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// validateProjectNames cleans a forge grant's project list; empty is every
// project of the org.
func validateProjectNames(names []string) ([]string, error) {
	out := make([]string, 0, len(names))
	seen := map[string]bool{}
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" || seen[n] {
			continue
		}
		if !projectNameRE.MatchString(n) {
			return nil, fmt.Errorf("%q is not a forge project name (lowercase letters, digits and dashes)", n)
		}
		seen[n] = true
		out = append(out, n)
	}
	return out, nil
}

// validateForgeTools rejects names outside the Reliant Forge catalog.
func validateForgeTools(requested []string) ([]string, error) {
	tools := make([]string, 0, len(requested))
	seen := map[string]bool{}
	for _, name := range requested {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		if _, ok := mcpserver.ForgeToolByName(name); !ok {
			return nil, fmt.Errorf("unknown Reliant Forge tool %q", name)
		}
		seen[name] = true
		tools = append(tools, name)
	}
	if len(tools) == 0 {
		return nil, errors.New("a connector must be granted at least one tool")
	}
	return tools, nil
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
		if _, ok := mcpserver.ToolByName(name); !ok {
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
			if tool, _ := mcpserver.ToolByName(name); tool.NeedsExec {
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
		Kind:          kindToProto(g.KindOrDefault()),
		OrgId:         g.OrgID,
		ProjectNames:  g.ProjectNames,
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

// kindToProto is a grant kind on the wire.
func kindToProto(k connectorgrant.Kind) reliantv1.ConnectorKind {
	if k == connectorgrant.KindForge {
		return reliantv1.ConnectorKind_CONNECTOR_KIND_FORGE
	}
	return reliantv1.ConnectorKind_CONNECTOR_KIND_WORKSPACE
}
