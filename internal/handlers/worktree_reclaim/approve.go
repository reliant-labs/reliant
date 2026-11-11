package worktree_reclaim

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemonerr"
)

const (
	// approveCommand carries recorded approvals to the machine, which
	// removes each approved worktree (daemonruntime's
	// WorktreeReclaimApproveCommand). It answers at once; the removal runs
	// after it.
	approveCommand   = "worktree.reclaim_approve"
	approveTimeoutMs = 30_000
)

// The removal states WorktreeReclaimDecisionResult.removal names.
const (
	removalRemoving     = "removing"
	removalRemoved      = "removed"
	removalRefused      = "refused"
	removalNotSupported = "not_supported"
)

// approveRequest and approveResponse are worktree.reclaim_approve's wire
// shape (daemonruntime's worktreeReclaimApproveRequest; worktreereclaim's
// ApprovalResult).
type approveRequest struct {
	Approvals  []approveItem `json:"approvals"`
	ApprovedBy string        `json:"approved_by"`
	CommandID  string        `json:"command_id"`
	// IssuedAtMs orders this decision against others about the same
	// worktree: the machine never acts on one older than one it answered.
	IssuedAtMs int64 `json:"issued_at_ms"`
}

type approveItem struct {
	Key         string `json:"key"`
	Fingerprint string `json:"fingerprint"`
	Path        string `json:"path"`
}

type approveResponse struct {
	Results []struct {
		Key        string `json:"key"`
		Status     string `json:"status"`
		Reason     string `json:"reason"`
		Detail     string `json:"detail"`
		CaptureRef string `json:"capture_ref"`
	} `json:"results"`
}

// sendApprovals sends the recorded approvals (indexes into decisions) to the
// machine as one worktree.reclaim_approve, each with the path and the
// fingerprint the person saw, and fills each result's removal fields. The
// caller has authenticated the person and checked they may write the
// machine. A Reliant workspace whose chat is still open is refused here and
// never sent. Nothing here fails the call: the decisions are recorded
// whatever the removal's fate, and the result says what it was.
func (s *Service) sendApprovals(ctx context.Context, userID, daemonID string, decisions []*pb.WorktreeReclaimDecision,
	shown map[string]*pb.WorktreeReclaimProposal, results []*pb.WorktreeReclaimDecisionResult, approved []int,
) {
	// DecideWorktreeReclaim already refuses without a Sender; this keeps the
	// helper safe on its own (forge-optional-deps-guard).
	if s.deps.Sender == nil {
		for _, i := range approved {
			refuse(results[i], "unavailable", "no machine can be reached; the approval is recorded, approve again to remove it")
		}
		return
	}
	send := approveRequest{ApprovedBy: "inbox:" + userID, CommandID: uuid.NewString(), IssuedAtMs: time.Now().UnixMilli()}
	sentAt := map[string]int{}
	for _, i := range approved {
		p := shown[decisions[i].Key]
		if p.GetRowId() != "" {
			chat, err := s.deps.Repo.GetLiveChatIDForWorktree(ctx, p.GetRowId())
			switch {
			case err != nil:
				refuse(results[i], "unverified", "could not check whether a chat is still open in it; approve again")
				continue
			case chat != "":
				refuse(results[i], "live_chat", "a chat is still open in this workspace; archive the chat to remove it")
				continue
			}
		}
		sentAt[p.Key] = i
		send.Approvals = append(send.Approvals, approveItem{Key: p.Key, Fingerprint: p.Fingerprint, Path: p.Path})
	}
	if len(send.Approvals) == 0 {
		return
	}
	payload, err := json.Marshal(send)
	if err != nil {
		for _, i := range sentAt {
			refuse(results[i], "unavailable", "the approval could not be sent; approve again")
		}
		return
	}
	reply, err := s.deps.Sender.SendDaemonCommandToDaemon(ctx, userID, daemonID, approveCommand, payload, approveTimeoutMs)
	if err != nil {
		state, reason, detail := removalRefused, "unavailable", "the machine did not answer; the approval is recorded, approve again to remove it"
		if daemonerr.IsOutdated(err) {
			state, reason, detail = removalNotSupported, "daemon_outdated", "this machine's Reliant is too old to remove approved worktrees; the approval is recorded, and nothing is removed until it is updated"
		}
		for _, i := range sentAt {
			results[i].Removal, results[i].RemovalReason, results[i].RemovalDetail = state, reason, detail
		}
		s.deps.Logger.WarnContext(ctx, "[WorktreeReclaim] approvals not delivered", "daemon_id", daemonID, "error", err)
		return
	}
	var resp approveResponse
	if err := json.Unmarshal(reply, &resp); err != nil {
		for _, i := range sentAt {
			refuse(results[i], "unreadable", "the machine's answer could not be read")
		}
		return
	}
	answered := map[string]bool{}
	for _, r := range resp.Results {
		i, ok := sentAt[r.Key]
		if !ok {
			continue
		}
		answered[r.Key] = true
		res := results[i]
		switch r.Status {
		case "accepted":
			res.Removal, res.RemovalDetail = removalRemoving, "saving it to a local git ref, then removing it"
		case "reclaimed":
			res.Removal, res.RemovalDetail = removalRemoved, "removed; restore it with: reliant worktree restore "+r.CaptureRef
		default:
			refuse(res, r.Reason, r.Detail)
		}
	}
	for key, i := range sentAt {
		if !answered[key] {
			refuse(results[i], "unknown", "the machine did not answer about it")
		}
	}
	s.deps.Logger.InfoContext(ctx, "[WorktreeReclaim] approvals sent", "daemon_id", daemonID, "approvals", len(send.Approvals), "command_id", send.CommandID)
}

func refuse(r *pb.WorktreeReclaimDecisionResult, reason, detail string) {
	if reason == "" {
		reason = "refused"
	}
	r.Removal, r.RemovalReason, r.RemovalDetail = removalRefused, reason, detail
}
