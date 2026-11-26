// Copyright (c) 2026 Reliant Labs

package featuregate

import (
	"context"
	"errors"
	"slices"
	"strings"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"

	"github.com/reliant-labs/reliant/internal/authz"
)

// NewStatic returns the Gate of a deployment-time allowlist: GTM is on for
// the orgs in cfg.allowed_org_ids (GTM_ALLOWED_ORG_IDS), for their live
// members only. A nil cfg, an empty list or a nil membership gives a Gate
// that allows nothing. The list is read once; a change takes a restart.
func NewStatic(cfg *configv1.GtmConfig, membership Membership) Gate {
	orgs := ParseOrgIDs(cfg.GetAllowedOrgIds())
	if len(orgs) == 0 || membership == nil {
		return DenyAll()
	}
	return &staticGate{
		features:   map[Feature][]string{GTM: orgs},
		membership: membership,
	}
}

// ParseOrgIDs trims each id and drops blank and repeated ones, in order. The
// config loader already trims and drops blanks; this keeps a Gate built from
// any other source to the same rule.
func ParseOrgIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || slices.Contains(out, id) {
			continue
		}
		out = append(out, id)
	}
	return out
}

// DenyAll returns a Gate that allows nothing: the Gate of a deployment that
// enables no gated feature, and the fail-closed default wherever a Gate is
// missing.
func DenyAll() Gate { return denyAll{} }

type denyAll struct{}

func (denyAll) Allowed(context.Context, Feature, string, string) (bool, error) { return false, nil }

func (denyAll) AllowedForUser(context.Context, Feature, string) (bool, error) { return false, nil }

type staticGate struct {
	// features maps each enabled feature to its orgs, in config order.
	features   map[Feature][]string
	membership Membership
}

// Allowed is authz.OrgMemberDenial over the gate's membership, after the
// allowlist: so a credential bound to another org is refused here exactly as
// it is at every org resource.
func (g *staticGate) Allowed(ctx context.Context, f Feature, userID, orgID string) (bool, error) {
	if userID == "" || orgID == "" || !slices.Contains(g.features[f], orgID) {
		return false, nil
	}
	switch err := authz.OrgMemberDenial(authz.WithMembership(ctx, g.membership), userID, orgID); {
	case err == nil:
		return true, nil
	case errors.Is(err, authz.ErrMembershipUnavailable):
		return false, err
	default:
		return false, nil
	}
}

// AllowedForUser asks the membership directly, one entitled org at a time,
// and stops at the first it answers yes for. An outage on one org does not
// hide a yes from another; with no yes, the first outage is returned.
func (g *staticGate) AllowedForUser(ctx context.Context, f Feature, userID string) (bool, error) {
	if userID == "" {
		return false, nil
	}
	var firstErr error
	for _, orgID := range g.features[f] {
		member, ok, err := g.membership.IsMember(ctx, userID, orgID)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if ok && member {
			return true, nil
		}
	}
	return false, firstErr
}
