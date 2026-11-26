// Copyright (c) 2026 Reliant Labs

package gtm

import (
	"context"
	"slices"
	"strings"

	"github.com/reliant-labs/reliant/internal/gtm/claimcheck"
	"github.com/reliant-labs/reliant/internal/store"
)

var (
	claimCategories = []ClaimCategory{ClaimPricing, ClaimCapability, ClaimLimit, ClaimLicense, ClaimTiming, ClaimComparison}
	claimStatuses   = []ClaimStatus{ClaimVerified, ClaimUnverified, ClaimRetired}
	claimSources    = []ClaimSourceKind{ClaimSourceNone, ClaimSourceDocs, ClaimSourcePricingCatalog, ClaimSourceCode, ClaimSourcePrimaryThirdParty}
)

const maxClaimSearchLen = 200

// ListClaims lists the org's claims, newest first.
func (s *service) ListClaims(ctx context.Context, actor Actor, in ListClaimsInput) (ListClaimsResult, error) {
	if err := s.admit(ctx, actor); err != nil {
		return ListClaimsResult{}, err
	}
	limit, offset, err := page(in.PageSize, in.PageToken, DefaultClaimPageSize, MaxClaimPageSize)
	if err != nil {
		return ListClaimsResult{}, err
	}
	if in.Status != "" && !slices.Contains(claimStatuses, in.Status) {
		return ListClaimsResult{}, invalid("status", "unknown status")
	}
	if in.Category != "" && !slices.Contains(claimCategories, in.Category) {
		return ListClaimsResult{}, invalid("category", "unknown category")
	}
	if err := checkLen("search", in.Search, 0, maxClaimSearchLen); err != nil {
		return ListClaimsResult{}, err
	}
	rows, err := s.store.ListClaims(ctx, ClaimFilter{
		OrgID: actor.OrgID, Status: in.Status, Category: in.Category, Search: in.Search,
		Limit: limit, Offset: offset,
	})
	if err != nil {
		return ListClaimsResult{}, err
	}
	rows, next := nextToken(rows, limit, offset)
	return ListClaimsResult{Claims: claimsFromRows(rows), NextPageToken: next}, nil
}

// UpsertClaim creates or changes a claim. Only a person verifies, and a
// verified claim carries its source and verifier; text and category are fixed
// at creation.
func (s *service) UpsertClaim(ctx context.Context, actor Actor, in UpsertClaimInput) (UpsertClaimResult, error) {
	if err := s.admit(ctx, actor); err != nil {
		return UpsertClaimResult{}, err
	}
	if err := checkClaimInput(in); err != nil {
		return UpsertClaimResult{}, err
	}
	// An agent drafts the registry: it creates and edits UNVERIFIED claims,
	// and never verifies, retires or touches a verified or retired one.
	if in.Status != nil && *in.Status != ClaimUnverified && !actor.Person() {
		if *in.Status == ClaimVerified {
			return UpsertClaimResult{}, personOnly("verify a claim")
		}
		return UpsertClaimResult{}, personOnly("retire a claim")
	}
	if in.ID == "" {
		return s.createClaim(ctx, actor, in)
	}
	if err := checkRevision(in.ExpectedRevision); err != nil {
		return UpsertClaimResult{}, err
	}
	var out Claim
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		row, err := s.store.GetClaim(ctx, actor.OrgID, in.ID)
		if err != nil {
			return s.notFound(ctx, err, "claim", in.ID)
		}
		if row.Revision != in.ExpectedRevision {
			return conflict(in.ID, in.ExpectedRevision, row.Revision)
		}
		if in.Text != nil && *in.Text != row.Text {
			return invalid("text", "a claim's text never changes; create a new claim")
		}
		if in.Category != nil && string(*in.Category) != row.Category {
			return invalid("category", "a claim's category never changes; create a new claim")
		}
		evidence := in.SourceURL != nil || in.SourceKind != nil || in.ExpiresOrRecheckAt != nil || in.ClearExpiresOrRecheckAt
		if !actor.Person() && ClaimStatus(row.Status) != ClaimUnverified {
			return personOnly("change a claim that is " + strings.ToLower(strings.TrimPrefix(row.Status, "GTM_CLAIM_STATUS_")))
		}
		becomesVerified := in.Status != nil && *in.Status == ClaimVerified && ClaimStatus(row.Status) != ClaimVerified
		applyClaimUpdate(row, in)
		// A person who verifies, or re-sources a verified claim, vouches for
		// it now.
		if ClaimStatus(row.Status) == ClaimVerified && (becomesVerified || evidence) {
			now := s.now()
			row.VerifiedBy = actor.UserID
			row.VerifiedAt = &now
		}
		if err := checkVerified(row); err != nil {
			return err
		}
		row.UpdatedAt = s.now()
		if err := s.store.UpdateClaim(ctx, row, in.ExpectedRevision); err != nil {
			return s.writeErr(err, in.ID, in.ExpectedRevision, func() (int32, error) {
				cur, err := s.store.GetClaim(ctx, actor.OrgID, in.ID)
				if err != nil {
					return 0, err
				}
				return cur.Revision, nil
			})
		}
		stored, err := s.store.GetClaim(ctx, actor.OrgID, in.ID)
		if err != nil {
			return s.notFound(ctx, err, "claim", in.ID)
		}
		out = claimFromRow(stored)
		return nil
	})
	if err != nil {
		return UpsertClaimResult{}, err
	}
	return UpsertClaimResult{Claim: out}, nil
}

func (s *service) createClaim(ctx context.Context, actor Actor, in UpsertClaimInput) (UpsertClaimResult, error) {
	if in.Text == nil {
		return UpsertClaimResult{}, invalid("text", "required")
	}
	if in.Category == nil {
		return UpsertClaimResult{}, invalid("category", "required")
	}
	now := s.now()
	row := &store.GtmClaim{
		OrgID:      actor.OrgID,
		Text:       *in.Text,
		Category:   string(*in.Category),
		Status:     string(ClaimUnverified),
		SourceKind: string(ClaimSourceNone),
		Revision:   1,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	applyClaimUpdate(row, in)
	if ClaimStatus(row.Status) == ClaimVerified {
		row.VerifiedBy = actor.UserID
		row.VerifiedAt = &now
	}
	if err := checkVerified(row); err != nil {
		return UpsertClaimResult{}, err
	}
	var out Claim
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		r := *row
		if err := s.store.CreateClaim(ctx, &r); err != nil {
			return err
		}
		stored, err := s.store.GetClaim(ctx, actor.OrgID, r.ID)
		if err != nil {
			return s.notFound(ctx, err, "claim", r.ID)
		}
		out = claimFromRow(stored)
		return nil
	})
	if err != nil {
		return UpsertClaimResult{}, err
	}
	return UpsertClaimResult{Claim: out, Created: true}, nil
}

func checkClaimInput(in UpsertClaimInput) error {
	if in.Text != nil {
		if err := checkLen("text", *in.Text, 1, MaxClaimTextLen); err != nil {
			return err
		}
		if claimcheck.Normalize(*in.Text) == "" {
			return invalid("text", "has no words")
		}
	}
	if in.Category != nil && !slices.Contains(claimCategories, *in.Category) {
		return invalid("category", "unknown category")
	}
	if in.Status != nil && !slices.Contains(claimStatuses, *in.Status) {
		return invalid("status", "unknown status")
	}
	if in.SourceKind != nil && !slices.Contains(claimSources, *in.SourceKind) {
		return invalid("source_kind", "unknown source kind")
	}
	if in.SourceURL != nil {
		if err := checkURL("source_url", *in.SourceURL); err != nil {
			return err
		}
	}
	if in.Notes != nil {
		if err := checkLen("notes", *in.Notes, 0, MaxShortTextLen); err != nil {
			return err
		}
	}
	return nil
}

func applyClaimUpdate(row *store.GtmClaim, in UpsertClaimInput) {
	if in.Status != nil {
		row.Status = string(*in.Status)
	}
	if in.SourceURL != nil {
		row.SourceURL = *in.SourceURL
	}
	if in.SourceKind != nil {
		row.SourceKind = string(*in.SourceKind)
	}
	switch {
	case in.ClearExpiresOrRecheckAt:
		row.ExpiresOrRecheckAt = nil
	case in.ExpiresOrRecheckAt != nil:
		row.ExpiresOrRecheckAt = utcPtr(in.ExpiresOrRecheckAt)
	}
	if in.Notes != nil {
		row.Notes = *in.Notes
	}
}

// checkVerified is the table's evidence CHECKs, as named errors: a VERIFIED
// claim has a source and a verifier, and a comparison's source is a primary
// third party.
func checkVerified(row *store.GtmClaim) error {
	if ClaimStatus(row.Status) != ClaimVerified {
		return nil
	}
	switch {
	case row.SourceURL == "":
		return invalid("source_url", "a verified claim needs its source")
	case ClaimSourceKind(row.SourceKind) == ClaimSourceNone:
		return invalid("source_kind", "a verified claim needs its source kind")
	case row.VerifiedBy == "" || row.VerifiedAt == nil:
		return invalid("verified_by", "a verified claim needs its verifier")
	case ClaimCategory(row.Category) == ClaimComparison && ClaimSourceKind(row.SourceKind) != ClaimSourcePrimaryThirdParty:
		return invalid("source_kind", "a verified comparison needs a primary third-party source")
	}
	return nil
}

// CheckClaims runs claimcheck over text against the org's registry.
func (s *service) CheckClaims(ctx context.Context, actor Actor, in CheckClaimsInput) (CheckClaimsResult, error) {
	if err := s.admit(ctx, actor); err != nil {
		return CheckClaimsResult{}, err
	}
	if err := checkLen("text", in.Text, 0, MaxCheckTextLen); err != nil {
		return CheckClaimsResult{}, err
	}
	var ex claimcheck.Rules
	mentions := ex.Extract(in.Text)
	rows, err := s.store.ListClaims(ctx, ClaimFilter{OrgID: actor.OrgID, Limit: MaxCheckedClaims})
	if err != nil {
		return CheckClaimsResult{}, err
	}
	registry := make([]claimcheck.Claim, 0, len(rows))
	for _, r := range rows {
		registry = append(registry, claimcheck.Claim{
			ID:        r.ID,
			Text:      r.Text,
			Status:    checkStatus(ClaimStatus(r.Status)),
			RecheckAt: r.ExpiresOrRecheckAt,
		})
	}
	found := claimcheck.Match(ex, mentions, registry, s.now())
	out := CheckClaimsResult{Findings: make([]ClaimFinding, 0, len(found)), Blocking: claimcheck.Blocking(found)}
	for _, f := range found {
		cf := ClaimFinding{
			Kind:        findingKind(f.Kind),
			MentionKind: f.Mention.Kind,
			Mention:     f.Mention.Text,
			Context:     f.Mention.Context,
			Reason:      f.Reason,
			Blocking:    f.Blocking(),
		}
		if f.Claim != nil {
			cf.ClaimID, cf.ClaimText = f.Claim.ID, f.Claim.Text
		}
		out.Findings = append(out.Findings, cf)
	}
	return out, nil
}

// checkStatus maps a stored status to claimcheck's; anything unknown is
// unverified, which never vouches.
func checkStatus(st ClaimStatus) claimcheck.Status {
	switch st {
	case ClaimVerified:
		return claimcheck.StatusVerified
	case ClaimRetired:
		return claimcheck.StatusRetired
	}
	return claimcheck.StatusUnverified
}

func findingKind(k claimcheck.FindingKind) ClaimFindingKind {
	switch k {
	case claimcheck.MatchedVerified:
		return FindingMatchedVerified
	case claimcheck.MatchedUnverified:
		return FindingMatchedUnverified
	}
	return FindingUnmatchedRisky
}
