// Copyright (c) 2025 Reliant Labs
package streaming

import (
	"context"
	"encoding/json"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/orgs"
)

// defaultOrgFeedPollInterval is Service.orgFeedPollInterval in production:
// how long a change to a shared chat may take to reach the org's lists.
const defaultOrgFeedPollInterval = 2 * time.Second

// orgFeedBatchSize bounds the rows one read of an org feed returns and one
// frame carries.
const orgFeedBatchSize = 200

// StreamOrgUpdates follows an org's feed of what its members shared
// (control-plane docs/design/org-sharing.md §5.2): a replay after the
// client's cursor, one caught_up, then each new row as it is read. The feed
// is written by the database (org_feed_* triggers), one row per coarse event
// about a shared chat or project, so it never carries a transcript and is
// O(events), not O(events x members). The caller's own rows are left out.
//
// Only a person may follow it, only for an org they are a live member of,
// and membership is asked again every reauthorizeInterval (orgFeedMember):
// anything but a definite "member" (a removal, an unknown answer, an
// outage) ends it, within orgs.CacheTTL plus that interval for a removal.
// The client reconnects and the open decides.
func (s *Service) StreamOrgUpdates(
	ctx context.Context,
	req *connect.Request[reliantv1.StreamOrgUpdatesRequest],
	stream *connect.ServerStream[reliantv1.OrgStreamEvent],
) error {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return svcerr.ErrUnauthenticated
	}
	if err := auth.RequirePerson(ctx, "follow what your organization shares"); err != nil {
		return err
	}
	orgID, err := s.orgFeedOrg(ctx, userID, req.Msg.GetOrgId())
	if err != nil {
		return err
	}

	cursor := int64(0)
	if orgID != "" {
		if err := s.orgFeedMember(ctx, userID, orgID); err != nil {
			return err
		}
		latest, err := s.deps.Repo.LatestOrgUpdateSequence(ctx, orgID)
		if err != nil {
			return svcerr.WrapOr(err, svcerr.Internal("failed to read the org feed"))
		}
		// A fresh start (0), or a cursor from another database (ahead of
		// the feed), replays nothing: the client lists what is shared now.
		cursor = req.Msg.GetSinceSeq()
		if cursor <= 0 || cursor > latest {
			cursor = latest
		}
		if cursor, err = s.sendOrgUpdates(ctx, orgID, userID, cursor, stream); err != nil {
			return err
		}
	}
	if err := stream.Send(&reliantv1.OrgStreamEvent{Event: &reliantv1.OrgStreamEvent_CaughtUp{
		CaughtUp: &reliantv1.OrgCaughtUp{LatestSequence: cursor},
	}}); err != nil {
		return err
	}

	heartbeat := time.NewTicker(s.heartbeatInterval)
	defer heartbeat.Stop()
	poll := time.NewTicker(s.orgFeedPollInterval)
	defer poll.Stop()
	reauthorize := time.NewTicker(s.reauthorizeInterval)
	defer reauthorize.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-poll.C:
			if orgID == "" {
				continue
			}
			if cursor, err = s.sendOrgUpdates(ctx, orgID, userID, cursor, stream); err != nil {
				return err
			}
		case <-reauthorize.C:
			if orgID == "" {
				continue
			}
			if err := s.orgFeedMember(ctx, userID, orgID); err != nil {
				return err
			}
		case <-heartbeat.C:
			if err := stream.Send(&reliantv1.OrgStreamEvent{Event: &reliantv1.OrgStreamEvent_Heartbeat{
				Heartbeat: &reliantv1.Heartbeat{Timestamp: time.Now().Unix()},
			}}); err != nil {
				return err
			}
		}
	}
}

// orgFeedMember is whether userID may follow orgID's feed now: only a
// definite "a live member". An org feed is org-wide, so the owner-only
// fallback of a single row has no meaning here: a removal (not a member), an
// answer the resolver does not have (ok false) and an outage (an error) all
// refuse it, at the open and on every re-check, which ends an open feed.
func (s *Service) orgFeedMember(ctx context.Context, userID, orgID string) error {
	member, ok, err := s.deps.Orgs.IsMember(ctx, userID, orgID)
	switch {
	case err != nil:
		logging.WarnContext(ctx, LOG_PREFIX_STREAM_CHAT+" Could not check org membership; ending the org feed",
			"userID", userID, "orgID", orgID, "error", err)
		return orgs.LookupFailed(ctx, "could not check your organization membership", err)
	case !ok || !member:
		logging.InfoContext(ctx, LOG_PREFIX_STREAM_CHAT+" Caller is not a live member; ending the org feed",
			"userID", userID, "orgID", orgID, "known", ok)
		return orgs.NotMember(orgID)
	}
	return nil
}

// orgFeedOrg is the org a feed follows: the named one, verified, else the
// caller's default; "" when the deployment has no orgs or the caller none.
func (s *Service) orgFeedOrg(ctx context.Context, userID, requested string) (string, error) {
	if s.deps.Orgs == nil {
		return "", nil
	}
	named, err := s.deps.Orgs.Named(ctx, userID, requested)
	if err != nil || named != "" {
		return named, err
	}
	def, err := s.deps.Orgs.DefaultOrg(ctx, userID)
	if err != nil {
		return "", orgs.LookupFailed(ctx, "could not resolve your default organization; try again", err)
	}
	return def, nil
}

// sendOrgUpdates sends orgID's feed after cursor, in batches, and returns the
// new cursor: the last sequence read, sent or skipped (the caller's own).
func (s *Service) sendOrgUpdates(ctx context.Context, orgID, userID string, cursor int64, stream *connect.ServerStream[reliantv1.OrgStreamEvent]) (int64, error) {
	for {
		rows, err := s.deps.Repo.OrgUpdatesSince(ctx, orgID, cursor, orgFeedBatchSize)
		if err != nil {
			return cursor, svcerr.WrapOr(err, svcerr.Internal("failed to read the org feed"))
		}
		if len(rows) == 0 {
			return cursor, nil
		}
		batch := &reliantv1.OrgUpdateBatch{}
		for _, row := range rows {
			cursor = row.SequenceNumber
			if row.OwnerUserID == userID {
				continue // the caller's own: their own list has it
			}
			batch.Updates = append(batch.Updates, orgUpdateToProto(row))
		}
		batch.LatestSequence = cursor
		if len(batch.Updates) > 0 {
			if err := stream.Send(&reliantv1.OrgStreamEvent{Event: &reliantv1.OrgStreamEvent_Updates{Updates: batch}}); err != nil {
				return cursor, err
			}
		}
		if len(rows) < orgFeedBatchSize {
			return cursor, nil
		}
	}
}

// orgUpdateToProto is a feed row on the wire. An UNSHARED or DELETED row is a
// bare marker (ids and owner only); SHARED and CHANGED carry the title and, for
// a chat, the coarse activity of the announcement that wrote it.
func orgUpdateToProto(row core.OrgUpdate) *reliantv1.OrgUpdate {
	out := &reliantv1.OrgUpdate{
		SequenceNumber: row.SequenceNumber,
		Type:           reliantv1.OrgUpdateType(row.UpdateType),
		ResourceKind:   string(row.Kind),
		ResourceId:     row.ResourceID,
		OwnerUserId:    row.OwnerUserID,
		ProjectId:      row.ProjectID,
		CreatedAt:      row.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	switch out.Type {
	case reliantv1.OrgUpdateType_ORG_UPDATE_TYPE_UNSHARED, reliantv1.OrgUpdateType_ORG_UPDATE_TYPE_DELETED:
		return out
	}
	out.Title = row.Title
	var data struct {
		Activity *int32 `json:"activity"`
	}
	if row.Data != "" && json.Unmarshal([]byte(row.Data), &data) == nil && data.Activity != nil {
		activity := reliantv1.ChatActivity(*data.Activity)
		out.Activity = &activity
	}
	return out
}
