// Copyright (c) 2026 Reliant Labs
package worktreesweep

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/gitcredentialclient"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

const (
	CommandRecoveryObserve  = "worktree.recovery_observe"
	CommandRecoveryCapture  = "worktree.recovery_capture"
	CommandRecoveryFinalize = "worktree.recovery_finalize"
)

// RecoveryStore is a durable journal, not an in-memory cleanup flag. Every
// destructive authorization is checked by the DB under its source-parent lock.
type RecoveryStore interface {
	ArchiveWorktreeForRecovery(context.Context, string, string, int64) error
	CreateWorktreeRecovery(context.Context, *core.WorktreeRecovery) error
	GetWorktreeRecovery(context.Context, string, string) (*core.WorktreeRecovery, error)
	ListWorktreeRecoveries(context.Context, string, string) ([]*core.WorktreeRecovery, error)
	ClaimWorktreeRecovery(context.Context, string, string, int64) error
	RecordWorktreeRecoveryReceipt(context.Context, string, string, string, string) error
	AuthorizeWorktreeRecovery(context.Context, string, string, time.Time) error
	CompleteWorktreeRecoveryCheckout(context.Context, string, string, string, string, string) error
	CancelWorktreeRecovery(context.Context, string, string, string) error
}

type RecoveryCredentials interface {
	UserAccessToken(context.Context, string, string) (gitcredentialclient.Token, error)
}

type recoveryEngine struct {
	store       RecoveryStore
	credentials RecoveryCredentials
	enabled     bool
}

// WithGitRecovery installs dependencies and an explicit rollout gate. Enabling
// it never bypasses per-workspace inactivity, eligibility or remote proof.
func (s *Sweeper) WithGitRecovery(store RecoveryStore, credentials RecoveryCredentials, enabled bool) *Sweeper {
	s.recovery = &recoveryEngine{store: store, credentials: credentials, enabled: enabled}
	return s
}

// reconcileRecoveryJournals releases obsolete server admission only after the
// exact durable authorization has expired plus a bounded daemon cancellation
// grace. It never converts missing source paths into a successful removal.
// The journal remains held/visible for separate restore and retry repair.
func (s *Sweeper) reconcileRecoveryJournals(ctx context.Context, owner string) {
	if s.recovery == nil || s.recovery.store == nil {
		return
	}
	journals, err := s.recovery.store.ListWorktreeRecoveries(ctx, owner, "")
	if err != nil {
		return
	}
	for _, j := range journals {
		if j.Operation != "" {
			continue
		}
		if j.AuthorizedUntil == nil && (j.Phase == "publishing" || j.Phase == "verified") && !j.UpdatedAt.IsZero() && !s.now().Before(j.UpdatedAt.Add(5*time.Minute)) {
			// A worker can die after committing the admission claim and before
			// committing authorization. No remover exists yet. Cancellation
			// locks the same parent/journal and refuses any newly live authority;
			// a delayed worker cannot authorize once its claim is released.
			_ = s.recovery.store.CancelWorktreeRecovery(ctx, owner, j.ID, "preauthorization claim expired; originals and recovery retained, conversation admission released")
			continue
		}
		if j.Phase != "delete_authorized" || j.AuthorizedUntil == nil || s.now().Before(j.AuthorizedUntil.Add(time.Minute)) {
			continue
		}
		_ = s.recovery.store.CancelWorktreeRecovery(ctx, owner, j.ID, "finalization outcome uncertain after authorization expired; recovery retained, retry repair or restore separately")
	}
}

func (s *Sweeper) recoverySend(ctx context.Context, t target, command string, request any, response any) error {
	payload, err := json.Marshal(request)
	if err != nil {
		return err
	}
	raw, err := s.sender.SendDaemonCommandToDaemon(ctx, t.userID, t.daemonID, command, payload, snapshotRemoveTimeoutMs)
	if err != nil {
		return errors.New("recovery daemon command unavailable")
	}
	if err := json.Unmarshal(raw, response); err != nil {
		return errors.New("invalid recovery daemon response")
	}
	return nil
}

// recoverHeld never falls through to snapshot_remove. Failures retain the
// journal/refs and source, and release any DB admission claim for conversation
// retry. A subsequent attempt uses a new capture; old recoveries stay catalogued.
func (s *Sweeper) recoverHeld(ctx context.Context, t target, cand *core.ReclaimCandidate, w worktreereclaim.Worktree) bool {
	e := s.recovery
	if e == nil || !e.enabled || e.store == nil || e.credentials == nil || w.KeepFiles || (w.State != worktreereclaim.StateArchived && w.State != worktreereclaim.StateActive) {
		return false
	}
	current, err := s.store.GetWorktree(ctx, w.ID)
	if err != nil || current == nil || current.IsMain || Fence(current) != w.Fence {
		return true
	}
	req := worktreereclaim.RecoveryRequest{Worktree: w}
	var observed worktreereclaim.RecoveryResponse
	if err := s.recoverySend(ctx, t, CommandRecoveryObserve, req, &observed); err != nil || !worktreereclaim.RecoveryIdleEligible(observed.Activity, s.now()) {
		return true
	}
	req.Activity = observed.Activity
	repos, err := s.store.ListReposByProject(ctx, current.ProjectID)
	if err != nil || len(repos) == 0 || len(repos) != len(w.Checkouts) {
		return true
	}
	captureID := uuid.NewString()
	recoveryID := uuid.NewString()
	req.RecoveryID = recoveryID
	for _, repo := range repos {
		if repo.RemoteURL == nil || *repo.RemoteURL == "" {
			return true
		}
		req.Captures = append(req.Captures, worktreereclaim.RecoveryCapture{Version: 1, CaptureID: captureID, WorktreeID: w.ID, RepoID: repo.ID, Rel: filepath.ToSlash(repo.RelativePath), RemoteURL: *repo.RemoteURL, RecoveryRef: "refs/heads/reliant-recovery/" + w.ID + "/" + captureID + "/" + repo.ID})
	}
	var captured worktreereclaim.RecoveryResponse
	if err := s.recoverySend(ctx, t, CommandRecoveryCapture, req, &captured); err != nil || captured.Outcome != worktreereclaim.OutcomeCaptured || len(captured.Captures) != len(req.Captures) {
		return true
	}
	// The daemon may enrich descriptors but cannot redirect publication to a
	// different repository/ref, omit a checkout, or swap identities.
	expected := map[string]worktreereclaim.RecoveryCapture{}
	for _, c := range req.Captures {
		expected[c.RepoID] = c
	}
	for _, c := range captured.Captures {
		old, ok := expected[c.RepoID]
		if !ok || c.CaptureID != old.CaptureID || c.WorktreeID != old.WorktreeID || c.Rel != old.Rel || c.RemoteURL != old.RemoteURL || c.RecoveryRef != old.RecoveryRef || c.CommitOID == "" || c.ManifestDigest == "" || c.InventoryDigest == "" {
			return true
		}
		delete(expected, c.RepoID)
	}
	if len(expected) > 0 {
		return true
	}
	req.Captures = captured.Captures
	if current.DeletedAt == nil {
		// Capture eligibility and complete observation alone never justify
		// archiving a live conversation: enroll atomically under the DB parent
		// lock with owner, generation, live-chat and workflow checks.
		if err := e.store.ArchiveWorktreeForRecovery(ctx, t.userID, w.ID, current.ActivityGeneration); err != nil {
			return true
		}
		archived, err := s.store.GetWorktree(ctx, w.ID)
		if err != nil || archived == nil || archived.DeletedAt == nil || archived.ActivityGeneration != current.ActivityGeneration {
			return true
		}
		current = archived
		w.State = worktreereclaim.StateArchived
		w.Fence = Fence(archived)
		req.Worktree = w
		if s.auto != nil && s.auto.notify != nil {
			s.auto.notify(ctx, t.userID, archived.ProjectID, w.ID)
		}
	}
	journal := &core.WorktreeRecovery{ID: recoveryID, OwnerUserID: t.userID, SourceWorktreeID: w.ID, ProjectID: current.ProjectID, DaemonID: t.daemonID, CaptureID: captureID, Fence: w.Fence, ActivityGeneration: current.ActivityGeneration, ObservationID: req.Activity.ObservationID, ManifestDigest: worktreereclaim.RecoveryWorkspaceDigest(req.Captures), IdempotencyKey: captureID}
	for _, c := range req.Captures {
		raw, _ := json.Marshal(c)
		journal.Checkouts = append(journal.Checkouts, core.RecoveryCheckout{RepoID: c.RepoID, Rel: c.Rel, RemoteURL: c.RemoteURL, RecoveryRef: c.RecoveryRef, CommitOID: c.CommitOID, ManifestDigest: c.ManifestDigest, DescriptorJSON: string(raw)})
	}
	if err := e.store.CreateWorktreeRecovery(ctx, journal); err != nil {
		return true
	}
	cancel := func(detail string) {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = e.store.CancelWorktreeRecovery(cctx, t.userID, recoveryID, detail)
	}
	for _, c := range req.Captures {
		// Resolve a CURRENT credential at each outbound operation; never store it in
		// the journal, a descriptor, an error, or a log.
		token, err := e.credentials.UserAccessToken(ctx, t.userID, "github")
		if err != nil || token.Empty() {
			cancel("repository credential unavailable")
			return true
		}
		var published struct {
			Receipt *worktreereclaim.RecoveryReceipt `json:"receipt"`
			Held    bool                             `json:"held"`
		}
		err = token.Use(func(secret string) error {
			return s.recoverySend(ctx, t, "git.recovery_publish", struct {
				SourcePath string                          `json:"source_path"`
				Capture    worktreereclaim.RecoveryCapture `json:"capture"`
				Token      string                          `json:"token"`
			}{filepath.Join(w.Path, filepath.FromSlash(c.Rel)), c, secret}, &published)
		})
		if err != nil || published.Held || published.Receipt == nil || !reflect.DeepEqual(published.Receipt.Capture, c) || published.Receipt.VerifiedAt.IsZero() || published.Receipt.VerifierVersion == "" {
			cancel("remote publication or independent verification failed")
			return true
		}
		raw, _ := json.Marshal(published.Receipt)
		if err := e.store.RecordWorktreeRecoveryReceipt(ctx, t.userID, recoveryID, c.RepoID, string(raw)); err != nil {
			cancel("verified receipt could not be persisted")
			return true
		}
	}
	// Claim AFTER every receipt is durable, rechecking live chat/run admissions,
	// archive fence and server activity under the DB parent lock.
	if err := e.store.ClaimWorktreeRecovery(ctx, t.userID, recoveryID, current.ActivityGeneration); err != nil {
		cancel("workspace activity prevents finalization")
		return true
	}
	until := s.now().Add(worktreereclaim.RecoveryAuthorizationLifetime)
	if err := e.store.AuthorizeWorktreeRecovery(ctx, t.userID, recoveryID, until); err != nil {
		cancel("recovery authorization refused")
		return true
	}
	saved, err := e.store.GetWorktreeRecovery(ctx, t.userID, recoveryID)
	if err != nil || saved == nil || saved.AuthorizedUntil == nil || saved.ManifestDigest != journal.ManifestDigest || saved.Fence != w.Fence || saved.CaptureID != captureID || saved.ActivityGeneration != current.ActivityGeneration || saved.ObservationID != req.Activity.ObservationID || len(saved.Checkouts) != len(req.Captures) {
		cancel("persisted authorization could not be reloaded")
		return true
	}
	req.Receipts = nil
	for _, checkout := range saved.Checkouts {
		var receipt worktreereclaim.RecoveryReceipt
		if json.Unmarshal([]byte(checkout.ReceiptJSON), &receipt) != nil {
			cancel("persisted receipt is invalid")
			return true
		}
		req.Receipts = append(req.Receipts, receipt)
	}
	req.Authorization = &worktreereclaim.RecoveryAuthorization{RecoveryID: saved.ID, CaptureID: saved.CaptureID, WorktreeID: saved.SourceWorktreeID, Fence: saved.Fence, ManifestDigest: saved.ManifestDigest, ObservationID: saved.ObservationID, ActivityGeneration: saved.ActivityGeneration, ExpiresAt: *saved.AuthorizedUntil}
	// Finalizer re-verifies all remote refs before taking its short local lease.
	token, err := e.credentials.UserAccessToken(ctx, t.userID, "github")
	if err != nil || token.Empty() {
		cancel("repository credential unavailable")
		return true
	}
	var finalized worktreereclaim.RecoveryResponse
	err = token.Use(func(secret string) error {
		return s.recoverySend(ctx, t, CommandRecoveryFinalize, struct {
			worktreereclaim.RecoveryRequest
			Token string `json:"token"`
		}{req, secret}, &finalized)
	})
	if err != nil {
		// The daemon may still be executing. Do NOT release server admission while
		// an authorized remover can still run. Reconciliation must first establish
		// exact per-checkout state after the authorization window has ended.
		return true
	}
	allowed := map[string]bool{}
	for _, c := range req.Captures {
		allowed[c.RepoID] = true
	}
	for _, repoID := range finalized.RemovedRepoIDs {
		if !allowed[repoID] {
			cancel("invalid finalization result")
			return true
		}
		delete(allowed, repoID)
		if err := e.store.CompleteWorktreeRecoveryCheckout(ctx, t.userID, recoveryID, repoID, "removed", ""); err != nil {
			return true
		}
	}
	if finalized.Outcome != worktreereclaim.OutcomeRemoved || len(allowed) != 0 {
		cancel("finalization held; remaining originals retained")
		return true
	}
	s.apply(ctx, t.userID, t.daemonID, cand.Worktree, cand.ProjectPath, worktreereclaim.Result{ID: w.ID, Outcome: worktreereclaim.OutcomeRemoved}, false)
	return true
}
