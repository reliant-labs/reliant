package worktree_reclaim

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// machineSender records every command sent to the machine. Decisions are
// recorded; an approval is answered by approve(key), or accepted.
type machineSender struct {
	commands []string
	approved []approveRequest
	approve  func(key string) (status, reason, detail string)
	// approveErr fails the approve command (an older daemon, a dropped link).
	approveErr error
}

func (m *machineSender) SendDaemonCommandToDaemon(_ context.Context, userID, daemonID, command string, payload []byte, _ int32) ([]byte, error) {
	if userID != testUser || daemonID != testDaemon {
		return nil, errors.New("wrong machine")
	}
	m.commands = append(m.commands, command)
	switch command {
	case decideCommand:
		var req decideRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, err
		}
		out := map[string][]map[string]any{"results": {}}
		for _, d := range req.Decisions {
			out["results"] = append(out["results"], map[string]any{"key": d.Key, "recorded": true})
		}
		return json.Marshal(out)
	case approveCommand:
		if m.approveErr != nil {
			return nil, m.approveErr
		}
		var req approveRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, err
		}
		m.approved = append(m.approved, req)
		out := map[string][]map[string]any{"results": {}}
		for _, a := range req.Approvals {
			status, reason, detail := "accepted", "", ""
			if m.approve != nil {
				status, reason, detail = m.approve(a.Key)
			}
			out["results"] = append(out["results"], map[string]any{"key": a.Key, "status": status, "reason": reason, "detail": detail})
		}
		return json.Marshal(out)
	}
	return nil, errors.New("unexpected command " + command)
}

func workspaceProposal(key, fp, rowID string) *pb.WorktreeReclaimProposal {
	p := shownProposal(key, fp)
	p.Owner, p.RowId = "reliant", rowID
	return p
}

// withLiveChats makes the repo report a live chat for these workspace rows.
func withLiveChats(r *StubRepo, rows ...string) *StubRepo {
	r.GetLiveChatIDForWorktreeFunc = func(_ context.Context, id string) (string, error) {
		for _, row := range rows {
			if row == id {
				return "chat-" + id, nil
			}
		}
		return "", nil
	}
	return r
}

// A recorded approval goes to the machine as worktree.reclaim_approve, after
// the decision is recorded, naming exactly the proposal shown (key, path,
// fingerprint) and the person; a decline is never sent as one.
func TestDecideWorktreeReclaim_ApprovalIsSentAsTheApproveCommand(t *testing.T) {
	t.Parallel()
	m := &machineSender{}
	repo := withLiveChats(machineRepo(t, true, shownProposal("a", "fa"), workspaceProposal("b", "fb", "row-b"), shownProposal("c", "fc")))
	got, err := decide(t, repo, m, testUser,
		&pb.WorktreeReclaimDecision{Key: "a", Fingerprint: "fa", Approve: true},
		&pb.WorktreeReclaimDecision{Key: "b", Fingerprint: "fb", Approve: true},
		&pb.WorktreeReclaimDecision{Key: "c", Fingerprint: "fc", Approve: false},
	)
	require.NoError(t, err)
	assert.Equal(t, []string{decideCommand, approveCommand}, m.commands)
	require.Len(t, m.approved, 1)
	assert.Equal(t, "inbox:"+testUser, m.approved[0].ApprovedBy)
	assert.NotEmpty(t, m.approved[0].CommandID)
	assert.Equal(t, []approveItem{{Key: "a", Fingerprint: "fa", Path: "/w/a"}, {Key: "b", Fingerprint: "fb", Path: "/w/b"}}, m.approved[0].Approvals)
	for i, want := range []string{removalRemoving, removalRemoving, ""} {
		assert.Equal(t, want, got.Results[i].Removal, got.Results[i].Key)
	}
}

// A person who may not write the machine is not found, and nothing at all
// reaches it: no decision, no approval.
func TestDecideWorktreeReclaim_UnauthorizedApproverIsDenied(t *testing.T) {
	t.Parallel()
	for name, repo := range map[string]*StubRepo{
		"another user's machine": machineRepo(t, true, shownProposal("a", "fa")),
		"a machine nobody owns": func() *StubRepo {
			r := machineRepo(t, true, shownProposal("a", "fa"))
			r.GetDaemonFunc = func(_ context.Context, id string) (*core.Daemon, error) { return &core.Daemon{ID: id}, nil }
			return r
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			m := &machineSender{}
			user := "someone-else"
			if name == "a machine nobody owns" {
				user = testUser
			}
			_, err := decide(t, repo, m, user, &pb.WorktreeReclaimDecision{Key: "a", Fingerprint: "fa", Approve: true})
			assert.Equal(t, connect.CodeNotFound, svcerr.Code(err))
			assert.Empty(t, m.commands)
		})
	}
}

// A Reliant workspace whose chat is still open is never sent for removal:
// the approval is recorded and the result says why.
func TestDecideWorktreeReclaim_LiveChatIsRefused(t *testing.T) {
	t.Parallel()
	m := &machineSender{}
	repo := withLiveChats(machineRepo(t, true, workspaceProposal("a", "fa", "row-a")), "row-a")
	got, err := decide(t, repo, m, testUser, &pb.WorktreeReclaimDecision{Key: "a", Fingerprint: "fa", Approve: true})
	require.NoError(t, err)
	assert.Equal(t, []string{decideCommand}, m.commands)
	assert.True(t, got.Results[0].Recorded)
	assert.Equal(t, removalRefused, got.Results[0].Removal)
	assert.Equal(t, "live_chat", got.Results[0].RemovalReason)
}

// The machine's own refusals (and removals) come back per approval.
func TestDecideWorktreeReclaim_MapsTheMachinesRemovals(t *testing.T) {
	t.Parallel()
	m := &machineSender{approve: func(key string) (string, string, string) {
		switch key {
		case "a":
			return "refused", "in_use", "open by process 42 (vim)"
		case "b":
			return "reclaimed", "", ""
		}
		return "accepted", "", ""
	}}
	got, err := decide(t, machineRepo(t, true, shownProposal("a", "fa"), shownProposal("b", "fb")), m, testUser,
		&pb.WorktreeReclaimDecision{Key: "a", Fingerprint: "fa", Approve: true},
		&pb.WorktreeReclaimDecision{Key: "b", Fingerprint: "fb", Approve: true},
	)
	require.NoError(t, err)
	assert.Equal(t, removalRefused, got.Results[0].Removal)
	assert.Equal(t, "in_use", got.Results[0].RemovalReason)
	assert.Equal(t, "open by process 42 (vim)", got.Results[0].RemovalDetail)
	assert.Equal(t, removalRemoved, got.Results[1].Removal)
}

// A daemon older than the approve command answers that it has no handler
// for it: the decision stays recorded, nothing is removed, and the result
// says the machine needs updating; the call itself succeeds.
func TestDecideWorktreeReclaim_OlderDaemonIsNotSupported(t *testing.T) {
	t.Parallel()
	m := &machineSender{approveErr: errors.New(`daemon error: this machine is running an older version of Reliant (v1.9.0) that doesn't support "worktree.reclaim_approve" — update it to continue`)}
	got, err := decide(t, machineRepo(t, true, shownProposal("a", "fa")), m, testUser, &pb.WorktreeReclaimDecision{Key: "a", Fingerprint: "fa", Approve: true})
	require.NoError(t, err)
	assert.True(t, got.Results[0].Recorded)
	assert.Equal(t, removalNotSupported, got.Results[0].Removal)
}
