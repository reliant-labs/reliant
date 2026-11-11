package worktree_reclaim

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/activity"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/diskwatch"
)

const (
	// decideCommand is the daemon command that records the decisions
	// (daemonruntime.WorktreeReclaimDecideCommand).
	decideCommand   = "worktree.reclaim_decide"
	decideTimeoutMs = 20_000
	// maxDecisions matches what one item lists (worktreereclaim.MaxProposals).
	maxDecisions = 50
	// connectedLease is how recent a machine's attachment must be to count
	// as online (as the storage clean-up counts it).
	connectedLease = 90 * time.Second
)

// decideRequest and decideResult are worktree.reclaim_decide's wire shape
// (daemonruntime's worktreeReclaimDecideRequest; worktreereclaim's
// DecisionRequest and DecisionResult).
type decideRequest struct {
	Decisions []decision `json:"decisions"`
	DecidedBy string     `json:"decided_by"`
}

type decision struct {
	Key         string `json:"key"`
	Fingerprint string `json:"fingerprint"`
	Decision    string `json:"decision"`
}

type decideResponse struct {
	Results []struct {
		Key      string `json:"key"`
		Recorded bool   `json:"recorded"`
		Reason   string `json:"reason"`
	} `json:"results"`
}

// DecideWorktreeReclaim implements the DecideWorktreeReclaim RPC.
func (s *Service) DecideWorktreeReclaim(
	ctx context.Context,
	req *connect.Request[pb.DecideWorktreeReclaimRequest],
) (*connect.Response[pb.DecideWorktreeReclaimResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return nil, svcerr.Unauthenticated("user ID not found in context")
	}
	msg := req.Msg
	if msg.DaemonId == "" || len(msg.Decisions) == 0 {
		return nil, svcerr.InvalidArgument("daemon_id and decisions are required")
	}
	if len(msg.Decisions) > maxDecisions {
		return nil, svcerr.InvalidArgument(fmt.Sprintf("at most %d decisions per call", maxDecisions))
	}
	daemon, err := s.deps.Repo.GetDaemon(ctx, msg.DaemonId)
	if err != nil && !svcerr.IsNotFound(err) {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get machine"))
	}
	// Deciding sends a command to the machine, so it is a write. A missing
	// machine, an unowned one and another user's are all not found.
	if err != nil || !authz.CanWriteMachine(ctx, userID, daemon) {
		return nil, svcerr.NotFound("machine")
	}
	if s.deps.Sender == nil {
		return nil, svcerr.Unavailable("worktree reclaim decisions are not available")
	}

	// Only what the machine last reported, and so what the item showed, can
	// be decided: a key or fingerprint that is not there never reaches it.
	raw, err := s.deps.Repo.GetDaemonStorageState(ctx, msg.DaemonId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to read the machine's report"))
	}
	shown := map[string]*pb.WorktreeReclaimProposal{}
	for _, p := range diskwatch.Decode(raw).GetWorktreeReclaim().GetProposals() {
		shown[p.Key] = p
	}
	results := make([]*pb.WorktreeReclaimDecisionResult, len(msg.Decisions))
	send := decideRequest{DecidedBy: "inbox:" + userID}
	sentAt := map[string]int{}
	for i, d := range msg.Decisions {
		results[i] = &pb.WorktreeReclaimDecisionResult{Key: d.Key}
		if d.Key == "" || d.Fingerprint == "" || shown[d.Key].GetFingerprint() != d.Fingerprint {
			results[i].Reason = "not_shown"
			continue
		}
		verdict := "decline"
		if d.Approve {
			verdict = "approve"
		}
		sentAt[d.Key] = i
		send.Decisions = append(send.Decisions, decision{Key: d.Key, Fingerprint: d.Fingerprint, Decision: verdict})
	}
	if len(send.Decisions) == 0 {
		activity.Skip(ctx) // nothing reached the machine
		return connect.NewResponse(&pb.DecideWorktreeReclaimResponse{Results: results}), nil
	}

	connected, err := s.deps.Repo.ListAttachedDaemonIDsForUser(ctx, userID, connectedLease)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to read connected machines"))
	}
	if !slices.Contains(connected, msg.DaemonId) {
		return nil, svcerr.FailedPrecondition("that machine is offline; reconnect it and try again")
	}
	payload, err := json.Marshal(send)
	if err != nil {
		return nil, svcerr.Internal("failed to encode decisions")
	}
	reply, err := s.deps.Sender.SendDaemonCommandToDaemon(ctx, userID, msg.DaemonId, decideCommand, payload, decideTimeoutMs)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Unavailable("the machine did not record the decisions; try again"))
	}
	var resp decideResponse
	if err := json.Unmarshal(reply, &resp); err != nil {
		return nil, svcerr.Internal("the machine's reply could not be read")
	}
	for _, r := range resp.Results {
		i, ok := sentAt[r.Key]
		if !ok {
			continue
		}
		results[i].Recorded, results[i].Reason = r.Recorded, r.Reason
	}
	for _, i := range sentAt {
		if !results[i].Recorded && results[i].Reason == "" {
			results[i].Reason = "unknown"
		}
	}
	s.deps.Logger.InfoContext(ctx, "[WorktreeReclaim] decisions sent", "daemon_id", msg.DaemonId, "decisions", len(send.Decisions))
	var approved []int
	for i, d := range msg.Decisions {
		if d.Approve && results[i].Recorded {
			approved = append(approved, i)
		}
	}
	if len(approved) > 0 {
		s.sendApprovals(ctx, userID, msg.DaemonId, msg.Decisions, shown, results, approved)
	}
	return connect.NewResponse(&pb.DecideWorktreeReclaimResponse{Results: results}), nil
}
