// Copyright (c) 2026 Reliant Labs

// Package featuregate answers one question for every surface of a gated
// feature: may this person use feature f in this org? Nav, the feature's
// RPCs and the agent's tools all ask the same Gate, so they cannot disagree.
//
// A Gate fails closed: an empty id, an unset or empty allowlist, a missing
// membership, or a membership outage is "no". A refusal never says why, so a
// surface can answer it as if the feature did not exist.
//
// The package's surface is constructors (NewStatic, DenyAll) that take their
// collaborators per call; this contract declares the Gate they return and the
// Membership they ask, and forge generates their mocks. internal/app builds
// the Gate and hands it to the components that ask it.
//
//forge:not-a-component: a Gate is built by NewStatic from GtmConfig and the org resolver (or DenyAll), not from Deps; the components that ask it take it as a dependency
package featuregate

import "context"

// Feature names a gated feature.
type Feature string

// GTM is the go-to-market workspace: its nav item, GtmService and the agent's
// GTM tools.
const GTM Feature = "gtm"

// Gate decides whether a person may use a feature.
type Gate interface {
	// Allowed reports whether userID is a live member of orgID
	// (authz.OrgMemberDenial is nil) AND orgID is entitled to f. Any error
	// comes with false. Callers should set a deadline on ctx; a check that
	// runs out of time is "no".
	Allowed(ctx context.Context, f Feature, userID, orgID string) (bool, error)
	// AllowedForUser reports whether userID is a live member of SOME org
	// entitled to f, by the same check as Allowed. Only for callers with no
	// org (CatalogService.ListTools). Callers should set a deadline on ctx; a
	// check that runs out of time is "no".
	AllowedForUser(ctx context.Context, f Feature, userID string) (bool, error)
}

// Membership answers whether userID is a live member of orgID: the check
// behind authz.OrgMemberDenial (authz.Membership; *orgs.Resolver is one). ok
// is false when the deployment has no answer (no orgs); err is an outage.
type Membership interface {
	IsMember(ctx context.Context, userID, orgID string) (member, ok bool, err error)
}
