package gtm

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
)

// UpsertClaim creates a claim (no id) or changes one; VERIFIED and RETIRED
// are a person's call. A set source_kind of UNSPECIFIED is the stored "no
// source" (gtmsvc.ClaimSourceNone), not "leave alone".
func (s *Service) UpsertClaim(
	ctx context.Context,
	req *connect.Request[pb.UpsertClaimRequest],
) (*connect.Response[pb.UpsertClaimResponse], error) {
	m := req.Msg
	a, err := s.begin(ctx, m.GetOrgId(), m)
	if err != nil {
		return nil, err
	}
	recheck, err := timeIn("expires_or_recheck_at", m.GetExpiresOrRecheckAt())
	if err != nil {
		return nil, s.invalid(ctx, a, err)
	}
	in := gtmsvc.UpsertClaimInput{
		ID:                      m.GetId(),
		ExpectedRevision:        m.GetExpectedRevision(),
		Text:                    m.Text,
		Category:                optEnumIn[gtmsvc.ClaimCategory](m.Category),
		Status:                  optEnumIn[gtmsvc.ClaimStatus](m.Status),
		SourceURL:               m.SourceUrl,
		ExpiresOrRecheckAt:      recheck,
		ClearExpiresOrRecheckAt: m.GetClearExpiresOrRecheckAt(),
		Notes:                   m.Notes,
	}
	if m.SourceKind != nil {
		kind := gtmsvc.ClaimSourceKind(m.SourceKind.String())
		in.SourceKind = &kind
	}
	res, err := s.deps.Service.UpsertClaim(ctx, a, in)
	if err != nil {
		return nil, wireErr(err)
	}
	return connect.NewResponse(&pb.UpsertClaimResponse{
		Claim:   claimOut(res.Claim),
		Created: res.Created,
	}), nil
}
