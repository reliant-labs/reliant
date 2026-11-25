package auth

import (
	"sort"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// Role extraction: which claim of a verified token becomes [Claims.Roles].
//
// Generic issuers put roles in a flat `roles` array. Zitadel does not: it
// asserts them as a project-namespaced MAP, role key -> {orgId: orgDomain}:
//
//	"urn:zitadel:iam:org:project:<projectId>:roles": {"admin": {"<orgId>": "acme.example"}}
//	"urn:zitadel:iam:org:project:roles":              {"admin": {"<orgId>": "acme.example"}}
//
// The first form carries one project's roles; the second aggregates every
// project the token was issued for. A validator that reads only `roles`
// silently hands a Zitadel app no roles at all.

const (
	zitadelProjectPrefix = "urn:zitadel:iam:org:project:"
	zitadelRolesSuffix   = ":roles"
	zitadelAggregateKey  = "urn:zitadel:iam:org:project:roles"

	// RolesClaimAuto is the default for [JWTConfig.RolesClaim]: read the flat
	// `roles` array when the token has one, otherwise a Zitadel roles claim.
	RolesClaimAuto = "auto"
)

// claimOpts carries the per-validator knobs that shape [Claims] extraction.
// The zero value is the default: auto-detected roles, no audience scoping.
type claimOpts struct {
	// rolesClaim is the claim to read roles from. "" and "auto" detect it.
	rolesClaim string
	// audience is the validator's configured audience. For Zitadel it is the
	// project id, which scopes auto-detection to that project's roles.
	audience string
}

func (o claimOpts) auto() bool {
	return o.rolesClaim == "" || strings.EqualFold(o.rolesClaim, RolesClaimAuto)
}

// extractRoles returns the roles of a verified token and whether they came
// from somewhere other than the flat `roles` array (which decides whether a
// single role may also populate [Claims.Role]).
//
// An explicit claim is read exclusively: it is the operator saying exactly
// where roles live, so nothing else is consulted.
//
// Auto keeps every generic issuer exactly as it was: a non-empty flat `roles`
// array wins. Only when it is absent does auto look for Zitadel's claims. With
// an audience configured it reads that project's claim ONLY. The aggregate
// claim also carries roles from other projects the token was issued for, so
// falling back to it would let a role in project Q pass as a role in project
// P. Without an audience the project is unknown and the roles of every
// project in the token are merged; set jwt_audience so that cannot happen.
func extractRoles(mc map[string]any, o claimOpts) (roles []string, derived bool) {
	if !o.auto() {
		return rolesFromValue(mc[o.rolesClaim]), o.rolesClaim != "roles"
	}
	if flat := getStringSliceClaim(jwt.MapClaims(mc), "roles"); len(flat) > 0 {
		return flat, false
	}
	if o.audience != "" {
		return rolesFromValue(mc[zitadelProjectPrefix+o.audience+zitadelRolesSuffix]), true
	}
	set := map[string]struct{}{}
	for k, v := range mc {
		if k != zitadelAggregateKey &&
			!(strings.HasPrefix(k, zitadelProjectPrefix) && strings.HasSuffix(k, zitadelRolesSuffix)) {
			continue
		}
		for _, r := range rolesFromValue(v) {
			set[r] = struct{}{}
		}
	}
	return sortedKeys(set), true
}

// rolesFromValue accepts the shapes a roles claim takes: an array of strings,
// Zitadel's role -> {org: domain} object (the keys are the roles; the same
// role in several organizations appears once), or a space or comma separated
// string. Anything else yields no roles.
func rolesFromValue(v any) []string {
	switch t := v.(type) {
	case []any:
		var out []string
		for _, item := range t {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return t
	case map[string]any:
		set := make(map[string]struct{}, len(t))
		for role := range t {
			if role != "" {
				set[role] = struct{}{}
			}
		}
		return sortedKeys(set)
	case string:
		return strings.FieldsFunc(t, func(r rune) bool { return r == ' ' || r == ',' })
	}
	return nil
}

func sortedKeys(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
