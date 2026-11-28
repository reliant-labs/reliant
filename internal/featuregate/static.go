// Copyright (c) 2026 Reliant Labs

package featuregate

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

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

// checkTimeout bounds one membership check, and allowedForUserTimeout every
// check AllowedForUser makes. The membership is a control-plane round trip on
// a hosted deployment; during an outage a slow one must cost the caller its
// gated feature, not its request. A check that runs out of time is "no".
// Variables only so a test can shorten them.
var (
	checkTimeout          = 2 * time.Second
	allowedForUserTimeout = 5 * time.Second
)

// member is authz.OrgMemberDenial over the gate's membership, bounded by
// checkTimeout: so a credential bound to another org is refused here exactly
// as it is at every org resource. err is set only for an outage (or a check
// that ran out of time); any other refusal is (false, nil).
func (g *staticGate) member(ctx context.Context, userID, orgID string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	switch err := authz.OrgMemberDenial(authz.WithMembership(ctx, g.membership), userID, orgID); {
	case err == nil:
		return true, nil
	case errors.Is(err, authz.ErrMembershipUnavailable):
		return false, err
	default:
		return false, nil
	}
}

// Allowed checks membership BEFORE the allowlist, so a refusal looks the same
// whether or not orgID is listed: a credential bound to another org is
// refused (and recorded as refused) for a listed and an unlisted org alike,
// and nothing about a refusal reveals which orgs have the feature. Callers
// should bound ctx; each check is bounded by checkTimeout regardless.
func (g *staticGate) Allowed(ctx context.Context, f Feature, userID, orgID string) (bool, error) {
	if userID == "" || orgID == "" {
		return false, nil
	}
	member, err := g.member(ctx, userID, orgID)
	if err != nil || !member {
		return false, err
	}
	return slices.Contains(g.features[f], orgID), nil
}

// AllowedForUser asks, one entitled org at a time, the same check Allowed
// asks (so a credential bound to an unlisted org gets "no"), and stops at the
// first yes. The checks run on a fork of the credential binding: a refusal of
// a listed org the caller never named is not recorded on the caller's ctx,
// where the edge would report it, naming that org, on a later refusal of the
// same request. An outage on one org does not hide a yes from another; with no
// yes, the first outage is returned. Every check together is bounded by
// allowedForUserTimeout: once it passes, the orgs not yet asked are skipped
// and the answer is "no". Callers should bound ctx as well.
func (g *staticGate) AllowedForUser(ctx context.Context, f Feature, userID string) (bool, error) {
	if userID == "" {
		return false, nil
	}
	if bound, _, _ := authz.CredentialRefusal(ctx); bound != "" {
		ctx = authz.WithCredentialOrg(ctx, bound)
	}
	ctx, cancel := context.WithTimeout(ctx, allowedForUserTimeout)
	defer cancel()
	var firstErr error
	for _, orgID := range g.features[f] {
		if err := ctx.Err(); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			break
		}
		member, err := g.member(ctx, userID, orgID)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if member {
			return true, nil
		}
	}
	return false, firstErr
}
