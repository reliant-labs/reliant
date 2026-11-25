package insights

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/orgs"
)

const (
	defaultAuditPageSize = 100
	maxAuditPageSize     = 500
)

// ListDrillDownAudit is the record of member drill-downs in the org, newest
// first. An owner or admin of the org, by a fresh read of their role and with
// a person's credential (authz.RequireOrgAdmin), reads every record; any
// other member reads the records of drill-downs into themselves, so they can
// see who looked at their numbers.
func (s *Service) ListDrillDownAudit(
	ctx context.Context,
	req *connect.Request[pb.ListDrillDownAuditRequest],
) (*connect.Response[pb.ListDrillDownAuditResponse], error) {
	msg := req.Msg
	userID, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	orgID := msg.GetOrgId()
	if err := requireMember(ctx, userID, orgID); err != nil {
		return nil, err
	}
	scope, subject := pb.ListDrillDownAuditScope_LIST_DRILL_DOWN_AUDIT_SCOPE_SELF, userID
	switch err := authz.RequireOrgAdmin(ctx, userID, orgID); {
	case err == nil:
		scope, subject = pb.ListDrillDownAuditScope_LIST_DRILL_DOWN_AUDIT_SCOPE_ORG, ""
	case errors.Is(err, authz.ErrMembershipUnavailable):
		return nil, svcerr.Wrap(orgs.LookupFailed(ctx, "could not confirm your role in this organization; try again", err))
	}

	size := int(msg.GetPageSize())
	switch {
	case size < 0 || size > maxAuditPageSize:
		return nil, svcerr.Wrap(svcerr.InvalidArgument("page_size must be between 0 and 500"))
	case size == 0:
		size = defaultAuditPageSize
	}
	after, err := decodeCursor(msg.GetPageToken())
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	rows, err := s.deps.Members.ListInsightsDrilldowns(ctx, orgID, subject, after, size+1)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	resp := &pb.ListDrillDownAuditResponse{Scope: scope}
	if len(rows) > size {
		rows = rows[:size]
		last := rows[size-1]
		resp.NextPageToken = encodeCursor(db.InsightsDrilldownCursor{CreatedAt: last.CreatedAt, ID: last.ID})
	}
	for _, r := range rows {
		resp.Events = append(resp.Events, &pb.InsightsDrillDownEvent{
			Id:            r.ID,
			ActorUserId:   r.ActorUserID,
			SubjectUserId: r.SubjectUserID,
			StartDate:     r.StartDate.UTC().Format(dateLayout),
			EndDate:       r.EndDate.UTC().Format(dateLayout),
			CreatedAt:     r.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return connect.NewResponse(resp), nil
}

// A page token is the last row's (created_at, id), opaque to the client.
func encodeCursor(c db.InsightsDrilldownCursor) string {
	return base64.RawURLEncoding.EncodeToString([]byte(c.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + c.ID))
}

func decodeCursor(token string) (*db.InsightsDrilldownCursor, error) {
	if token == "" {
		return nil, nil
	}
	invalid := svcerr.InvalidArgument("page_token is not one this service issued")
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, invalid
	}
	at, id, ok := strings.Cut(string(raw), "|")
	if !ok || id == "" {
		return nil, invalid
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return nil, invalid
	}
	return &db.InsightsDrilldownCursor{CreatedAt: t, ID: id}, nil
}
