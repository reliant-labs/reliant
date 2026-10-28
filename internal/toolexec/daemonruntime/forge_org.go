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

// bindForgeOrg is the one step every forge handler takes with its payload's
// org_id: check it and put it on the context the forge child runs under.
func bindForgeOrg(ctx context.Context, orgID string) (context.Context, error) {
	orgID = strings.TrimSpace(orgID)
	if err := validateForgeOrg(orgID); err != nil {
		return ctx, err
	}
	return withForgeOrg(ctx, orgID), nil
}
