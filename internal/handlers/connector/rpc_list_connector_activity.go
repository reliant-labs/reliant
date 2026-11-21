package connector

import (
	"context"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/connectorgrant"
)

// ListConnectorActivity returns the audit log.
func (s *Service) ListConnectorActivity(
	ctx context.Context,
	req *connect.Request[reliantv1.ListConnectorActivityRequest],
) (*connect.Response[reliantv1.ListConnectorActivityResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, svcerr.ErrUnauthenticated
	}

	limit := int(req.Msg.GetLimit())

	var (
		records []*connectorgrant.AuditRecord
		err     error
	)
	if grantID := strings.TrimSpace(req.Msg.GetGrantId()); grantID != "" {
		records, err = s.deps.Grants.ListAuditByGrant(ctx, userID, grantID, limit)
	} else {
		records, err = s.deps.Grants.ListAuditByUser(ctx, userID, limit)
	}
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list connector activity"))
	}

	out := make([]*reliantv1.ConnectorActivity, 0, len(records))
	for _, r := range records {
		out = append(out, &reliantv1.ConnectorActivity{
			Id:           r.ID,
			GrantId:      r.GrantID,
			DaemonId:     r.DaemonID,
			ToolName:     r.ToolName,
			CommandType:  r.CommandType,
			Arguments:    string(r.Arguments),
			Denied:       r.Denied,
			ErrorMessage: r.ErrorMsg,
			DurationMs:   int32(r.DurationMS),
			Status:       r.Status,
			CreatedAt:    r.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return connect.NewResponse(&reliantv1.ListConnectorActivityResponse{Activity: out}), nil
}
