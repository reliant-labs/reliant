package core

import (
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// These errors reveal neither another owner's catalog nor source identity.
var (
	ErrWorktreeFinalizing = svcerr.FailedPrecondition("worktree recovery is finalizing; retry after reconciliation")
	ErrRecoveryNotFound   = svcerr.NotFound("worktree recovery")
	ErrRecoveryConflict   = svcerr.FailedPrecondition("recovery operation conflicts with current state")
)

// WorktreeRecovery is a durable owner-scoped journal. Source identifiers are
// retained labels, deliberately not cascading foreign keys. No expiration.
type WorktreeRecovery struct {
	ID                 string
	OwnerUserID        string
	SourceWorktreeID   string
	ProjectID          string
	DaemonID           string
	CaptureID          string
	Fence              string
	ActivityGeneration int64
	ObservationID      string
	ManifestDigest     string
	Phase              string
	LastError          string
	IdempotencyKey     string
	OperationKey       string
	Operation          string
	TargetDaemonID     string
	AuthorizedUntil    *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
	Checkouts          []RecoveryCheckout `bun:"-"`
}

// RecoveryCheckout records publication intent BEFORE upload. JSON contains
// concrete worktreereclaim descriptors/receipts, never credentials or file bytes.
type RecoveryCheckout struct {
	RecoveryID     string
	RepoID         string
	Rel            string
	RemoteURL      string
	RecoveryRef    string
	CommitOID      string
	ManifestDigest string
	DescriptorJSON string
	ReceiptJSON    string
	Phase          string
	LastError      string
}
