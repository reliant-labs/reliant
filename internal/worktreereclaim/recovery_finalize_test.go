// Copyright (c) 2026 Reliant Labs
package worktreereclaim

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestFinalizeRecoveryQuarantineInventoryMismatchReturnsOriginal(t *testing.T) {
	heavyUnderShort(t, "real Git capture/quarantine removal proof")
	ctx := context.Background()
	parent := recoveryTestRepo(t)
	root := t.TempDir()
	id := uuid.NewString()
	path := filepath.Join(root, id)
	recoveryTestGit(t, parent, "worktree", "add", "-q", "-b", "recovery-test", path)
	require.NoError(t, LockCheckout(ctx, path, id))
	recoveryTestWrite(t, path, "file.txt", []byte("dirty captured bytes\n"), 0644)
	c := recoveryTestDescriptor()
	c.WorktreeID = id
	c.Rel = ""
	c.RecoveryRef = "refs/heads/reliant-recovery/" + id + "/" + c.CaptureID + "/" + c.RepoID
	capture, err := CaptureRecovery(ctx, path, c)
	require.NoError(t, err)
	// Keep the real context deadline clear of loaded-host Git subprocess
	// latency while the injected logical clock still enforces a 25s lease.
	now := time.Now().UTC().Add(5 * time.Minute)
	activity := RecoveryActivity{Generation: 1, ObservationID: "epoch", ObservedSince: now.Add(-8 * 24 * time.Hour), LastActivity: now.Add(-8 * 24 * time.Hour), ObservedThrough: now, Complete: true}
	req := RecoveryRequest{RecoveryID: "journal", Worktree: Worktree{ID: id, Path: path, State: StateArchived, Fence: "123", Checkouts: []Checkout{{RepoPath: parent, Rel: ""}}}, Captures: []RecoveryCapture{capture}, Activity: activity, Receipts: []RecoveryReceipt{{Capture: capture, VerifiedAt: now, VerifierVersion: "test"}}}
	req.Authorization = &RecoveryAuthorization{RecoveryID: req.RecoveryID, CaptureID: capture.CaptureID, WorktreeID: id, Fence: "123", ObservationID: activity.ObservationID, ManifestDigest: RecoveryWorkspaceDigest(req.Captures), ExpiresAt: now.Add(25 * time.Second)}
	r := NewReclaimer(root)
	// Freeze the authorization clock: this fixture tests inventory mutation,
	// not host scheduling latency. Expiry is tested independently below.
	r.Now = func() time.Time { return now }
	mutated := false
	deps := RecoveryFinalizeDeps{Verify: func(context.Context, RecoveryCapture) (RecoveryReceipt, error) { return req.Receipts[0], nil }, Observe: func(context.Context, string) (RecoveryActivity, error) { return activity, nil }, Acquire: func(context.Context, []string) (RecoveryLease, error) { return finalizeTestLease{}, nil }, CheckProcessUse: func(context.Context, []string) error {
		if !mutated {
			parked := r.quarantined(id)
			if len(parked) > 0 {
				mutated = true
				return os.WriteFile(filepath.Join(parked[0], "file.txt"), []byte("concurrent write\n"), 0644)
			}
		}
		return nil
	}}
	out := r.FinalizeRecovery(ctx, req, deps)
	require.Equal(t, OutcomeHeld, out.Outcome)
	require.Empty(t, out.RemovedRepoIDs)
	require.True(t, mutated, "fixture must reach quarantine before mutation: %+v", out)
	data, err := os.ReadFile(filepath.Join(path, "file.txt"))
	require.NoError(t, err)
	require.Equal(t, "concurrent write\n", string(data))
	require.Empty(t, r.quarantined(id))
}

func TestRecoveryIdleEligibility(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	good := RecoveryActivity{Generation: 1, ObservationID: "epoch", ObservedSince: now.Add(-RecoveryIdleFloor), LastActivity: now.Add(-RecoveryIdleFloor), ObservedThrough: now, Complete: true}
	require.True(t, RecoveryIdleEligible(good, now))
	cases := map[string]func(*RecoveryActivity){
		"gap":          func(a *RecoveryActivity) { a.Complete = false },
		"future":       func(a *RecoveryActivity) { a.ObservedThrough = now.Add(time.Second) },
		"stale":        func(a *RecoveryActivity) { a.ObservedThrough = now.Add(-2 * time.Minute) },
		"recent write": func(a *RecoveryActivity) { a.LastActivity = now.Add(-RecoveryIdleFloor + time.Nanosecond) },
		"recent epoch": func(a *RecoveryActivity) { a.ObservedSince = now.Add(-time.Hour) },
		"no epoch":     func(a *RecoveryActivity) { a.ObservationID = "" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) { a := good; change(&a); require.False(t, RecoveryIdleEligible(a, now)) })
	}
}

func TestRecoveryWorkspaceDigestBindsAllDescriptors(t *testing.T) {
	a := RecoveryCapture{RepoID: "a", ManifestDigest: "one", InventoryDigest: "disk"}
	b := RecoveryCapture{RepoID: "b", ManifestDigest: "two"}
	require.Equal(t, RecoveryWorkspaceDigest([]RecoveryCapture{a, b}), RecoveryWorkspaceDigest([]RecoveryCapture{b, a}))
	changed := a
	changed.InventoryDigest = "different"
	require.NotEqual(t, RecoveryWorkspaceDigest([]RecoveryCapture{a, b}), RecoveryWorkspaceDigest([]RecoveryCapture{changed, b}))
	require.NotEqual(t, RecoveryWorkspaceDigest([]RecoveryCapture{a, b}), RecoveryWorkspaceDigest([]RecoveryCapture{a}))
}

type finalizeTestLease struct{}

func (finalizeTestLease) Check(context.Context) error { return nil }
func (finalizeTestLease) Release()                    {}

func TestFinalizeRecoveryNeverAdmitsWithoutEveryRemoteProof(t *testing.T) {
	now := time.Now().UTC()
	id := uuid.NewString()
	captures := []RecoveryCapture{{Version: 1, WorktreeID: id, CaptureID: "capture", RepoID: "a"}, {Version: 1, WorktreeID: id, CaptureID: "capture", RepoID: "b"}}
	req := RecoveryRequest{RecoveryID: "journal", Worktree: Worktree{ID: id, State: StateArchived, Fence: "123", Remove: true, Trees: map[string]string{"": "local"}}, Captures: captures, Activity: RecoveryActivity{Generation: 1, ObservationID: "epoch", ObservedSince: now.Add(-8 * 24 * time.Hour), LastActivity: now.Add(-8 * 24 * time.Hour), ObservedThrough: now, Complete: true}}
	for _, c := range captures {
		req.Receipts = append(req.Receipts, RecoveryReceipt{Capture: c, VerifiedAt: now, VerifierVersion: "proof"})
	}
	req.Authorization = &RecoveryAuthorization{RecoveryID: req.RecoveryID, WorktreeID: id, CaptureID: "capture", Fence: "123", ObservationID: "epoch", ManifestDigest: RecoveryWorkspaceDigest(captures), ExpiresAt: now.Add(20 * time.Second)}
	admitted := false
	verified := 0
	deps := RecoveryFinalizeDeps{
		Verify: func(_ context.Context, c RecoveryCapture) (RecoveryReceipt, error) {
			verified++
			if c.RepoID == "b" {
				return RecoveryReceipt{}, errors.New("remote missing")
			}
			return RecoveryReceipt{Capture: c, VerifiedAt: now, VerifierVersion: "proof"}, nil
		},
		Observe: func(context.Context, string) (RecoveryActivity, error) {
			t.Fatal("observed before remote proof")
			return RecoveryActivity{}, nil
		},
		Acquire: func(context.Context, []string) (RecoveryLease, error) {
			admitted = true
			return finalizeTestLease{}, nil
		},
		CheckProcessUse: func(context.Context, []string) error { return nil },
	}
	r := &Reclaimer{Now: func() time.Time { return now }}
	out := r.FinalizeRecovery(context.Background(), req, deps)
	require.Equal(t, ReasonRemoteProof, out.Reason)
	require.Equal(t, 2, verified)
	require.False(t, admitted)
	req.Authorization = nil
	out = r.FinalizeRecovery(context.Background(), req, deps)
	require.Equal(t, ReasonStale, out.Reason)
	require.Equal(t, 2, verified)
}
