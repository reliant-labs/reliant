// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"fmt"
	"strings"
	"unicode"
)

// ForgeOrgEnv is the environment variable that tells reliant's forge credential
// helper (`reliant auth forge-credential`) which org the token it mints must act
// in. The daemon sets it on the forge child it runs for ONE request, because the
// org is the web's active org for that request, not a property of the daemon: a
// person in two orgs deploys to either from the same machine. Unset, the helper
// asks for none and the token lands in the session's own org.
const ForgeOrgEnv = "RELIANT_FORGE_ORG"

type forgeOrgKey struct{}

// withForgeOrg records the org a forge invocation acts in. "" records nothing.
func withForgeOrg(ctx context.Context, orgID string) context.Context {
	if orgID = strings.TrimSpace(orgID); orgID == "" {
		return ctx
	}
	return context.WithValue(ctx, forgeOrgKey{}, orgID)
}

// forgeOrgOf is the org recorded on ctx, or "".
func forgeOrgOf(ctx context.Context) string {
	orgID, _ := ctx.Value(forgeOrgKey{}).(string)
	return orgID
}

// forgeOrgEnv is the environment entry for ctx's org, or none.
func forgeOrgEnv(ctx context.Context) []string {
	if orgID := forgeOrgOf(ctx); orgID != "" {
		return []string{ForgeOrgEnv + "=" + orgID}
	}
	return nil
}

// validateForgeOrg rejects an org id that could not be one. It lands in a
// child's environment, so the check is for shape only: membership is the
// control plane's to judge when the helper asks for the token.
func validateForgeOrg(orgID string) error {
	if len(orgID) > 128 || strings.ContainsFunc(orgID, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return fmt.Errorf("org_id is not an organization id")
	}
	return nil
}

// bindForgeOrg is the step every forge handler takes with its payload's
// org_id: check it and put it on the context the forge child runs under.
func bindForgeOrg(ctx context.Context, orgID string) (context.Context, error) {
	orgID = strings.TrimSpace(orgID)
	if err := validateForgeOrg(orgID); err != nil {
		return ctx, err
	}
	return withForgeOrg(ctx, orgID), nil
}

// ForgeProjectEnv names the Reliant project a forge invocation runs for, set
// beside ForgeOrgEnv. forge records it as the source of a release cut, deploy
// attempt or release record (control-plane
// docs/design/unified-shell-and-projects.md §8.1), so an app links back to the
// project its code is in. A chat's shell commands carry the same variable
// (tools.ProjectIDEnv). Nothing requires it.
const ForgeProjectEnv = "RELIANT_PROJECT_ID"

type forgeProjectKey struct{}

// withForgeProject records the project a forge invocation runs for. ""
// records nothing.
func withForgeProject(ctx context.Context, projectID string) context.Context {
	if projectID = strings.TrimSpace(projectID); projectID == "" {
		return ctx
	}
	return context.WithValue(ctx, forgeProjectKey{}, projectID)
}

// forgeProjectOf is the project recorded on ctx, or "".
func forgeProjectOf(ctx context.Context) string {
	projectID, _ := ctx.Value(forgeProjectKey{}).(string)
	return projectID
}

// bindForgeScope is the one step every forge handler takes with its payload's
// org_id and project_id: check both and put them on the context the forge
// child runs under. A project id has the shape check an org id has, for the
// same reason: it lands in a child's environment. "" binds nothing.
func bindForgeScope(ctx context.Context, orgID, projectID string) (context.Context, error) {
	ctx, err := bindForgeOrg(ctx, orgID)
	if err != nil {
		return ctx, err
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return ctx, nil
	}
	if len(projectID) > 128 || strings.ContainsFunc(projectID, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return ctx, fmt.Errorf("project_id is not a project id")
	}
	return withForgeProject(ctx, projectID), nil
}

// forgeScopeEnv is the environment entries for ctx's org and project, or
// none.
func forgeScopeEnv(ctx context.Context) []string {
	env := forgeOrgEnv(ctx)
	if projectID := forgeProjectOf(ctx); projectID != "" {
		env = append(env, ForgeProjectEnv+"="+projectID)
	}
	return env
}
