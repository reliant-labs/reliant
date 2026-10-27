// Copyright (c) 2025 Reliant Labs

// Package worktreereclaim is the daemon-side half of worktree disk hygiene: it
// locks the worktrees reliant creates, decides whether an archived one is safe
// to delete, and snapshots work before a user-confirmed removal. (Disk space is
// measured by internal/diskwatch, for every volume, on the heartbeat.)
//
// Only the daemon sees the filesystem, so every decision about a directory is
// made here. The server sends a description of the worktrees it wants settled
// (these wire types) and records what came back; it never touches a path.
//
//forge:exclude-contract: daemon-side filesystem and git helpers plus wire types; no collaborator to fake — tests run against real temp git repos
package worktreereclaim

import (
	"strconv"
	"strings"
	"time"
)

// RecoveryCapture identifies one immutable, locally pinned, publication-safe
// capture. It contains neither credentials nor filesystem contents.
type RecoveryCapture struct {
	Version         int       `json:"version"`
	CaptureID       string    `json:"capture_id"`
	WorktreeID      string    `json:"worktree_id"`
	RepoID          string    `json:"repo_id"`
	Rel             string    `json:"rel"`
	RemoteURL       string    `json:"remote_url"`
	RecoveryRef     string    `json:"recovery_ref"`
	CommitOID       string    `json:"commit_oid"`
	ManifestDigest  string    `json:"manifest_digest"`
	HeadOID         string    `json:"head_oid"`
	IndexTreeOID    string    `json:"index_tree_oid"`
	DiskTreeOID     string    `json:"disk_tree_oid"`
	ObjectFormat    string    `json:"object_format"`
	InventoryDigest string    `json:"inventory_digest"`
	SecretPolicy    string    `json:"secret_policy"`
	HistoryTips     []string  `json:"history_tips"`
	UntrackedPaths  []string  `json:"untracked_paths"`
	CapturedAt      time.Time `json:"captured_at"`
}

// RecoveryReceipt is issued only after an independent exact remote fetch,
// complete object-closure validation, and manifest verification.
type RecoveryReceipt struct {
	Capture         RecoveryCapture `json:"capture"`
	VerifiedAt      time.Time       `json:"verified_at"`
	VerifierVersion string          `json:"verifier_version"`
}

// RecoveryActivity is complete daemon observation, not directory mtime.
type RecoveryActivity struct {
	Generation      int64     `json:"generation"`
	ObservationID   string    `json:"observation_id"`
	ObservedSince   time.Time `json:"observed_since"`
	LastActivity    time.Time `json:"last_activity"`
	ObservedThrough time.Time `json:"observed_through"`
	Complete        bool      `json:"complete"`
}

// RecoveryAuthorization fences one short-lived finalization attempt.
type RecoveryAuthorization struct {
	RecoveryID         string    `json:"recovery_id"`
	CaptureID          string    `json:"capture_id"`
	WorktreeID         string    `json:"worktree_id"`
	Fence              string    `json:"fence"`
	ManifestDigest     string    `json:"manifest_digest"`
	ObservationID      string    `json:"observation_id"`
	ActivityGeneration int64     `json:"activity_generation"`
	ExpiresAt          time.Time `json:"expires_at"`
}

// RecoveryRequest is the additive Git-only recovery protocol. Legacy Remove
// and local Trees are not authorization for this protocol.
type RecoveryRequest struct {
	Worktree      Worktree               `json:"worktree"`
	RecoveryID    string                 `json:"recovery_id"`
	Captures      []RecoveryCapture      `json:"captures"`
	Receipts      []RecoveryReceipt      `json:"receipts"`
	Activity      RecoveryActivity       `json:"activity"`
	Authorization *RecoveryAuthorization `json:"authorization,omitempty"`
}

type RecoveryResponse struct {
	Outcome        Outcome           `json:"outcome"`
	Reason         HeldReason        `json:"reason,omitempty"`
	Detail         string            `json:"detail,omitempty"`
	Captures       []RecoveryCapture `json:"captures,omitempty"`
	Receipts       []RecoveryReceipt `json:"receipts,omitempty"`
	Activity       RecoveryActivity  `json:"activity"`
	RemovedRepoIDs []string          `json:"removed_repo_ids,omitempty"`
}

// RecoveryRestoreRequest always names a separate destination and branch.
type RecoveryRestoreRequest struct {
	RecoveryID      string            `json:"recovery_id"`
	IdempotencyKey  string            `json:"idempotency_key"`
	DestinationPath string            `json:"destination_path"`
	Branch          string            `json:"branch"`
	Captures        []RecoveryCapture `json:"captures"`
}

type RecoveryRestoreResponse struct {
	Outcome         Outcome    `json:"outcome"`
	Reason          HeldReason `json:"reason,omitempty"`
	Detail          string     `json:"detail,omitempty"`
	Path            string     `json:"path,omitempty"`
	Branch          string     `json:"branch,omitempty"`
	RestoredRepoIDs []string   `json:"restored_repo_ids,omitempty"`
}

const (
	ReasonUnsupportedRecovery HeldReason = "unsupported_recovery"
	ReasonSensitiveRecovery   HeldReason = "sensitive_recovery"
	ReasonRemoteProof         HeldReason = "remote_proof_required"
	ReasonObservationGap      HeldReason = "observation_gap"
	OutcomeCaptured           Outcome    = "captured"
	OutcomeVerified           Outcome    = "verified"
	OutcomeRestored           Outcome    = "restored"
)

// HeldReason says why a worktree was not removed.
type HeldReason string

const (
	// ReasonInUse: a live process has its working directory inside the worktree.
	ReasonInUse HeldReason = "in_use"
	// ReasonDirty: modified or untracked files, or edits hidden by
	// skip-worktree / assume-unchanged.
	ReasonDirty HeldReason = "dirty"
	// ReasonUnpushed: HEAD is on no remote ref, and its work is not in base
	// either as commits or as an equivalent patch (a squash or rebase merge),
	// nor the head of a merged pull request, nor saved under the worktree's
	// snapshot refs. The automatic path saves such commits (saveCommits) and
	// removes the worktree, so this holds only under "Keep everything" or when
	// saving failed.
	ReasonUnpushed HeldReason = "unpushed"
	// ReasonUnverified: git could not answer, so safety was not established.
	ReasonUnverified HeldReason = "unverified"
	// ReasonUnmanaged: reliant cannot prove it owns this worktree (a lock with
	// someone else's reason, another row's id, or not a linked worktree).
	ReasonUnmanaged HeldReason = "unmanaged"
	// ReasonData: ignored files that are not known to be rebuildable (data,
	// .env*, *.db, ...). Never removed by reliant; remove them by hand.
	ReasonData HeldReason = "data"
	// ReasonNestedRepo: a git repository (or submodule) lives inside the
	// worktree. Its history is not part of the worktree's.
	ReasonNestedRepo HeldReason = "nested-repository"
	// ReasonOutsideFiles: files exist in the workspace outside every checkout.
	ReasonOutsideFiles HeldReason = "files-outside-checkout"
	// ReasonTooLarge: the work to save is too large to snapshot onto a full disk.
	ReasonTooLarge HeldReason = "too-large-to-snapshot"
	// ReasonUnreachable: the worktree's own reflog, per-worktree refs
	// (refs/worktree, refs/bisect), pseudo-refs or detached HEAD reach commits
	// that are on no branch, remote or tag. Deleting its admin directory would
	// orphan them. Saved like ReasonUnpushed before an automatic removal.
	ReasonUnreachable HeldReason = "unreachable_history"
	// ReasonQuarantined: a checkout is parked under <root>/.reclaim, still locked
	// with its fence, because it could not be removed or put back. Never deleted
	// automatically; the detail names its path.
	ReasonQuarantined HeldReason = "quarantined"
	// ReasonKept: safe to remove, but the user's setting is "keep everything".
	ReasonKept HeldReason = "kept"
	// ReasonSharedWithLive: an unarchived row's directory or checkout is at or
	// under this archived row's directory, so removing it would remove theirs.
	// Decided by the server, which never sends such a row to the daemon.
	ReasonSharedWithLive HeldReason = "shared-with-live-workspace"
	// ReasonRecentlyActive: touched within the idle floor.
	ReasonRecentlyActive HeldReason = "recently_active"
	// ReasonStale: the request's fence no longer matches the lock.
	ReasonStale HeldReason = "stale"
	// ReasonError: the pass failed (a timeout, a git failure). Recorded so the
	// row backs off instead of being retried at the front of every pass.
	ReasonError HeldReason = "error"
)

// Removable reports whether the confirmed Clean up can ever remove a worktree
// held for this reason.
func (r HeldReason) Removable() bool {
	switch r {
	case ReasonDirty, ReasonUnpushed, ReasonInUse, ReasonUnverified, ReasonTooLarge, ReasonUnreachable, ReasonKept, ReasonError:
		return true
	}
	return false
}

// State is what the server believes about a worktree row.
type State string

const (
	StateActive   State = "active"
	StateArchived State = "archived"
)

// Outcome is the per-worktree result of a pass.
type Outcome string

const (
	// OutcomeLocked: an active worktree is locked as reliant-owned.
	OutcomeLocked Outcome = "locked"
	// OutcomeRemoved: the directory was removed.
	OutcomeRemoved Outcome = "removed"
	// OutcomeGone: the directory did not exist; stale git registration pruned.
	OutcomeGone Outcome = "gone"
	// OutcomeHeld: not removed; Reason says why.
	OutcomeHeld Outcome = "held"
	// OutcomeClaimed: the checkouts are safe to remove and now carry this
	// archive's fence in their lock. Removal needs a SECOND request, sent after
	// the server re-reads the row, so an unarchive in between (which re-locks as
	// active) invalidates it.
	OutcomeClaimed Outcome = "claimed"
	// OutcomeIdleDone: an ACTIVE worktree that is finished: it was asked about
	// with ProbeIdle, it is locked as usual, and every checkout is clean, its
	// work is merged or pushed, nothing uses it and it has been untouched for
	// the idle floor. The server decides whether to archive it; the daemon
	// never removes an active worktree.
	OutcomeIdleDone Outcome = "idle_done"
	// OutcomeProbeInconclusive: an ACTIVE worktree asked about with ProbeIdle is
	// locked as usual, but the question "is it finished" could not be answered
	// (the probe ran out of time, or a git or process check failed). It says
	// nothing about the worktree; the server asks again soon, not after the
	// back-off of a worktree that was found in use.
	OutcomeProbeInconclusive Outcome = "probe_inconclusive"
	// OutcomeKept: safe to remove, but the user's setting says keep files.
	OutcomeKept Outcome = "kept"
	// OutcomeDeferred: the daemon ran out of its per-request time budget before
	// reaching this worktree. Nothing was done; ask again next pass.
	OutcomeDeferred Outcome = "deferred"
	// OutcomeForeign: not under this machine's worktree root, or the root is
	// missing. The server must conclude nothing from it.
	OutcomeForeign Outcome = "foreign"
	// OutcomeError: something unexpected failed; nothing was removed.
	OutcomeError Outcome = "error"
)

// Checkout describes one nested git checkout of a workspace. Rel is its path
// relative to the workspace root ("" when the root is itself the checkout).
type Checkout struct {
	// RepoPath is the parent repository the checkout was created from. Used to
	// prune a registration whose directory has vanished and to find snapshots.
	RepoPath string `json:"repo_path"`
	Rel      string `json:"rel"`
	// BaseBranch: HEAD merged into it counts as safe. Empty falls back to
	// Worktree.BaseBranch.
	BaseBranch string `json:"base_branch,omitempty"`
}

// Worktree is one workspace the server wants settled.
type Worktree struct {
	ID    string `json:"id"`
	Path  string `json:"path"`
	State State  `json:"state"`
	// Fence identifies this archive: the server derives it from the row's
	// archive time, so it only ever grows. The daemon records it in the
	// checkout's lock reason and refuses to remove anything whose lock carries a
	// different one.
	Fence string `json:"fence,omitempty"`
	// Retire is sent on an ACTIVE request by an unarchive, and names the archive
	// being undone. The daemon writes a "restored <fence>" lock, and every later
	// claim must carry a strictly newer fence, which makes a fence single-use:
	// nothing built from a read taken before the restore can claim the worktree
	// again. A lock that already carries an archive fence is retired too, whether
	// or not Retire is set.
	Retire string `json:"retire,omitempty"`
	// Remove selects the second phase. The first phase decides and, when the
	// worktree is safe to remove, claims it by writing Fence into each
	// checkout's lock. The server then re-reads the row and, only if it is still
	// archived with the same Fence, sends the same worktree again with Remove
	// set. The daemon removes only a checkout whose lock is exactly that claim,
	// and re-verifies everything it removes.
	Remove bool `json:"remove,omitempty"`
	// Trees (phase two of a confirmed clean-up) maps each checkout's Rel to the
	// tree that was saved. The daemon removes it only if its files still hash to
	// that tree.
	Trees map[string]string `json:"trees,omitempty"`
	// KeepFiles: the user's setting is "keep everything". Nothing is removed
	// automatically; held worktrees are still reported.
	KeepFiles bool `json:"keep_files,omitempty"`
	// ProbeIdle (ACTIVE requests only) asks whether the worktree is finished
	// with, in addition to locking it; see OutcomeIdleDone.
	ProbeIdle bool `json:"probe_idle,omitempty"`
	// ProbeRecoverableAfterSeconds asks for read-only whole-worktree idle
	// eligibility. It never authorizes deleting files from a live checkout.
	ProbeRecoverableAfterSeconds int64 `json:"probe_recoverable_after_s,omitempty"`
	// ProbeLanded (ACTIVE requests only) asks whether the worktree's work has
	// landed: every checkout clean and its HEAD merged into its base branch
	// on the remote (see Result.Landed). Always read-only.
	ProbeLanded bool `json:"probe_landed,omitempty"`
	// ShedLanded is ignored; complete worktree retirement requires archival.
	// It cannot authorize deleting any file from a live checkout.
	ShedLanded bool       `json:"shed_landed,omitempty"`
	BaseBranch string     `json:"base_branch,omitempty"`
	Checkouts  []Checkout `json:"checkouts,omitempty"`
}

// Result reports what happened to one worktree.
type Result struct {
	ID      string     `json:"id"`
	Outcome Outcome    `json:"outcome"`
	Reason  HeldReason `json:"reason,omitempty"`
	Detail  string     `json:"detail,omitempty"`
	// SizeBytes is the on-disk size of a held worktree.
	SizeBytes int64 `json:"size_bytes,omitempty"`
	// SnapshotRefs are the local refs the work was saved to, including ones a
	// previous attempt left behind.
	SnapshotRefs []string `json:"snapshot_refs,omitempty"`
	// Trees are the saved trees by checkout Rel (see Worktree.Trees).
	Trees map[string]string `json:"trees,omitempty"`
	// Stripped names the regenerable paths (node_modules, build output, tool
	// caches) deleted from a worktree that is kept for its work, relative to
	// the workspace root; StrippedBytes is at least what that freed.
	Stripped      []string `json:"stripped,omitempty"`
	StrippedBytes int64    `json:"stripped_bytes,omitempty"`
	// ReclaimedTo is the directory the files that sat loose at the workspace
	// root (outside every checkout) were saved to before the workspace was
	// removed; empty when there were none.
	ReclaimedTo string `json:"reclaimed_to,omitempty"`
	// Landed (an ACTIVE worktree asked with ProbeLanded) says its work has
	// landed and, once archived, the automatic removal would take it: every
	// checkout is clean, holds nothing on no branch, and its HEAD is in its
	// base branch on the remote (an ancestor, the same patches, one squash
	// commit, or the head of a merged pull request), and the workspace holds
	// no data, foreign checkout or other repository. A checkout with no
	// commits beyond its base counts as landed. The daemon never removes an
	// active worktree; whether to archive it is the server's call.
	Landed bool `json:"landed,omitempty"`
	// RecoverableIdle reports a complete clean, pushed/merged, unused checkout
	// whose files (including caches) have all been quiet for the requested floor.
	RecoverableIdle bool   `json:"recoverable_idle,omitempty"`
	Error           string `json:"error,omitempty"`
}

// Request is the payload of worktree.reconcile and worktree.snapshot_remove.
type Request struct {
	Worktrees []Worktree `json:"worktrees"`
}

// Response is the reply to either command. It carried the worktree root's
// disk once; disk telemetry now rides the heartbeat (internal/diskwatch),
// covering every volume rather than the one that never fills.
type Response struct {
	Results []Result `json:"results"`
}

const lockReasonPrefix = "reliant: worktree "

// LockReason is the reason recorded on `git worktree lock` for an ACTIVE
// worktree. It is a cross-tool contract: forge's storage reaper skips locked
// worktrees, so this lock keeps a clean, idle, pushed worktree safe while a
// chat is using it.
func LockReason(worktreeID string) string { return lockReasonPrefix + worktreeID }

// RestoredLockReason is the reason for a worktree that was archived under
// `fence` and then restored. It is an active lock that remembers the highest
// fence retired for the row.
func RestoredLockReason(worktreeID, fence string) string {
	return lockReasonPrefix + worktreeID + " restored " + fence
}

// ArchivedLockReason is the reason for an ARCHIVED worktree awaiting removal.
// It carries the archive fence; removal requires an exact match.
func ArchivedLockReason(worktreeID, fence string) string {
	return lockReasonPrefix + worktreeID + " archived " + fence
}

// ReliantLock reports whether a lock reason is one reliant itself wrote.
func ReliantLock(reason string) bool { return strings.HasPrefix(reason, lockReasonPrefix) }

// parsedLock is a reliant lock reason taken apart.
type parsedLock struct {
	ID       string
	Archived bool
	Restored bool
	Fence    string
}

func parseLock(reason string) (parsedLock, bool) {
	if !ReliantLock(reason) {
		return parsedLock{}, false
	}
	fields := strings.Fields(strings.TrimPrefix(reason, lockReasonPrefix))
	switch {
	case len(fields) == 1:
		return parsedLock{ID: fields[0]}, true
	case len(fields) == 3 && fields[1] == "archived":
		return parsedLock{ID: fields[0], Archived: true, Fence: fields[2]}, true
	case len(fields) == 3 && fields[1] == "restored":
		return parsedLock{ID: fields[0], Restored: true, Fence: fields[2]}, true
	}
	return parsedLock{}, false
}

// fenceNewer reports whether fence a is strictly newer than b. Fences are
// archive times in nanoseconds; anything else only ever compares equal.
func fenceNewer(a, b string) bool {
	x, err1 := strconv.ParseInt(a, 10, 64)
	y, err2 := strconv.ParseInt(b, 10, 64)
	return err1 == nil && err2 == nil && x > y
}

// SnapshotRefPrefix is where a worktree's saved work lives in the repository.
func SnapshotRefPrefix(worktreeID string) string { return "refs/reliant/wip/" + worktreeID + "/" }

// snapshotRefDir names the ref directory for one checkout of a workspace.
func snapshotRefDir(worktreeID, rel string) string {
	if rel == "" {
		rel = "root"
	}
	return SnapshotRefPrefix(worktreeID) + sanitizeRefComponent(rel) + "/"
}

func sanitizeRefComponent(rel string) string {
	out := make([]byte, 0, len(rel))
	for i := 0; i < len(rel); i++ {
		c := rel[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
			out = append(out, c)
		default:
			out = append(out, '-')
		}
	}
	return string(out)
}

// RefsForCheckout keeps the refs, from those recorded on a row, that hold the
// saved work of one checkout (rel is its path under the workspace root).
func RefsForCheckout(worktreeID, rel string, refs []string) []string {
	dir := snapshotRefDir(worktreeID, rel)
	var out []string
	for _, r := range refs {
		if strings.HasPrefix(r, dir) {
			out = append(out, r)
		}
	}
	return out
}
