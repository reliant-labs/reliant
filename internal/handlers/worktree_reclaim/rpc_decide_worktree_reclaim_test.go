package worktree_reclaim

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/diskwatch"
)

const (
	testUser   = "u-1"
	testDaemon = "d-1"
)

// machineRepo is testUser's machine testDaemon, last reporting proposals,
// connected or not.
func machineRepo(t *testing.T, connected bool, proposals ...*pb.WorktreeReclaimProposal) *StubRepo {
	t.Helper()
	stored, err := diskwatch.Encode(&pb.DiskReport{
		Volumes:         []*pb.DiskVolume{{Label: "home", TotalBytes: 100 << 30, FreeBytes: 80 << 30}},
		WorktreeReclaim: &pb.WorktreeReclaimProposals{Mode: "on", Total: int32(len(proposals)), Proposals: proposals},
	})
	require.NoError(t, err)
	return &StubRepo{
		GetDaemonFunc: func(_ context.Context, id string) (*core.Daemon, error) {
			return &core.Daemon{ID: id, UserID: testUser}, nil
		},
		GetDaemonStorageStateFunc: func(context.Context, string) (string, error) { return stored, nil },
		ListAttachedDaemonIDsForUserFunc: func(context.Context, string, time.Duration) ([]string, error) {
			if connected {
				return []string{testDaemon}, nil
			}
			return nil, nil
		},
	}
}

// echoSender records what reached the daemon and answers each decision with
// reply(key), or "recorded" when reply is nil.
type echoSender struct {
	got   []decideRequest
	reply func(key string) (bool, string)
}

func (e *echoSender) SendDaemonCommandToDaemon(_ context.Context, userID, daemonID, command string, payload []byte, _ int32) ([]byte, error) {
	if userID != testUser || daemonID != testDaemon || command != decideCommand {
		return nil, connect.NewError(connect.CodeInternal, nil)
	}
	var req decideRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, err
	}
	e.got = append(e.got, req)
	type result struct {
		Key      string `json:"key"`
		Recorded bool   `json:"recorded"`
		Reason   string `json:"reason,omitempty"`
	}
	var out struct {
		Results []result `json:"results"`
	}
	for _, d := range req.Decisions {
		r := result{Key: d.Key, Recorded: true}
		if e.reply != nil {
			r.Recorded, r.Reason = e.reply(d.Key)
		}
		out.Results = append(out.Results, r)
	}
	return json.Marshal(out)
}

func decide(t *testing.T, repo Repository, sender Sender, userID string, decisions ...*pb.WorktreeReclaimDecision) (*pb.DecideWorktreeReclaimResponse, error) {
	t.Helper()
	svc := NewTestWorktreeReclaim(t, WithWorktreeReclaimDeps(Deps{Repo: repo, Sender: sender}))
	ctx := auth.WithUser(context.Background(), userID, "user", userID+"@example.com")
	resp, err := svc.DecideWorktreeReclaim(ctx, connect.NewRequest(&pb.DecideWorktreeReclaimRequest{DaemonId: testDaemon, Decisions: decisions}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func shownProposal(key, fp string) *pb.WorktreeReclaimProposal {
	return &pb.WorktreeReclaimProposal{Key: key, Fingerprint: fp, Path: "/w/" + key, Reason: "dirty"}
}

// Another user's machine is not found, and nothing is sent to it.
func TestDecideWorktreeReclaim_AnotherUsersMachineIsNotFound(t *testing.T) {
	t.Parallel()
	sender := &echoSender{}
	_, err := decide(t, machineRepo(t, true, shownProposal("a", "fa")), sender, "someone-else",
		&pb.WorktreeReclaimDecision{Key: "a", Fingerprint: "fa", Approve: true})
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(err))
	assert.Empty(t, sender.got)
}

// internal/authz decides who may act on the machine, and fails closed: a
// machine nobody owns, or one the store does not return, is not found too,
// and neither its report nor the machine is reached.
func TestDecideWorktreeReclaim_AuthzDeniesUnownedAndMissingMachines(t *testing.T) {
	t.Parallel()
	for name, daemon := range map[string]*core.Daemon{
		"unowned": {ID: testDaemon},
		"missing": nil,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			repo := machineRepo(t, true, shownProposal("a", "fa"))
			repo.GetDaemonFunc = func(context.Context, string) (*core.Daemon, error) { return daemon, nil }
			repo.GetDaemonStorageStateFunc = func(context.Context, string) (string, error) {
				t.Error("read the report of a machine the caller may not act on")
				return "", nil
			}
			sender := &echoSender{}
			_, err := decide(t, repo, sender, testUser, &pb.WorktreeReclaimDecision{Key: "a", Fingerprint: "fa", Approve: true})
			assert.Equal(t, connect.CodeNotFound, svcerr.Code(err))
			assert.Empty(t, sender.got)
		})
	}
}

// Only what the machine last reported can be decided: an unknown key, or a
// known key at a fingerprint the item did not show, is not_shown and never
// reaches the machine.
func TestDecideWorktreeReclaim_OnlyShownProposalsReachTheMachine(t *testing.T) {
	t.Parallel()
	sender := &echoSender{}
	got, err := decide(t, machineRepo(t, true, shownProposal("a", "fa"), shownProposal("b", "fb")), sender, testUser,
		&pb.WorktreeReclaimDecision{Key: "a", Fingerprint: "fa", Approve: true},
		&pb.WorktreeReclaimDecision{Key: "b", Fingerprint: "stale", Approve: true},
		&pb.WorktreeReclaimDecision{Key: "zz", Fingerprint: "fz", Approve: true},
	)
	require.NoError(t, err)
	require.Len(t, got.Results, 3)
	assert.True(t, got.Results[0].Recorded)
	assert.Equal(t, "not_shown", got.Results[1].Reason)
	assert.Equal(t, "not_shown", got.Results[2].Reason)
	require.Len(t, sender.got, 1)
	assert.Equal(t, []decision{{Key: "a", Fingerprint: "fa", Decision: "approve"}}, sender.got[0].Decisions)
	assert.Equal(t, "inbox:"+testUser, sender.got[0].DecidedBy)
}

// Nothing decidable means nothing is sent, so an offline machine is not an
// error then.
func TestDecideWorktreeReclaim_NothingShownSendsNothing(t *testing.T) {
	t.Parallel()
	sender := &echoSender{}
	got, err := decide(t, machineRepo(t, false), sender, testUser,
		&pb.WorktreeReclaimDecision{Key: "a", Fingerprint: "fa", Approve: true})
	require.NoError(t, err)
	assert.Equal(t, "not_shown", got.Results[0].Reason)
	assert.Empty(t, sender.got)
}

// An offline machine is FailedPrecondition, before anything is sent.
func TestDecideWorktreeReclaim_OfflineMachineIsFailedPrecondition(t *testing.T) {
	t.Parallel()
	sender := &echoSender{}
	_, err := decide(t, machineRepo(t, false, shownProposal("a", "fa")), sender, testUser,
		&pb.WorktreeReclaimDecision{Key: "a", Fingerprint: "fa"})
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
	assert.Empty(t, sender.got)
}

// With no daemon router wired the decision is unavailable, not lost silently.
func TestDecideWorktreeReclaim_NoSenderIsUnavailable(t *testing.T) {
	t.Parallel()
	_, err := decide(t, machineRepo(t, true, shownProposal("a", "fa")), nil, testUser,
		&pb.WorktreeReclaimDecision{Key: "a", Fingerprint: "fa"})
	assert.Equal(t, connect.CodeUnavailable, svcerr.Code(err))
}

// The machine's verdict for each decision comes back in request order; a
// decision the machine did not answer is "unknown", never recorded.
func TestDecideWorktreeReclaim_MapsTheMachinesResults(t *testing.T) {
	t.Parallel()
	sender := &echoSender{reply: func(key string) (bool, string) {
		switch key {
		case "a":
			return true, ""
		case "b":
			return false, "changed"
		}
		return false, ""
	}}
	got, err := decide(t, machineRepo(t, true, shownProposal("a", "fa"), shownProposal("b", "fb"), shownProposal("c", "fc")), sender, testUser,
		&pb.WorktreeReclaimDecision{Key: "c", Fingerprint: "fc", Approve: true},
		&pb.WorktreeReclaimDecision{Key: "b", Fingerprint: "fb", Approve: false},
		&pb.WorktreeReclaimDecision{Key: "a", Fingerprint: "fa", Approve: true},
	)
	require.NoError(t, err)
	require.Len(t, got.Results, 3)
	assert.Equal(t, "c", got.Results[0].Key)
	assert.False(t, got.Results[0].Recorded)
	assert.Equal(t, "unknown", got.Results[0].Reason)
	assert.Equal(t, "b", got.Results[1].Key)
	assert.False(t, got.Results[1].Recorded)
	assert.Equal(t, "changed", got.Results[1].Reason)
	assert.Equal(t, "a", got.Results[2].Key)
	assert.True(t, got.Results[2].Recorded)
	require.Len(t, sender.got, 1)
	assert.Equal(t, "decline", sender.got[0].Decisions[1].Decision)
}
