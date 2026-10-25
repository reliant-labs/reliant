package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

// recoveryRepositoryIdentity accepts only the supported explicit GitHub HTTPS
// identity. Case/.git normalization is comparison only, never a remote rewrite.
func recoveryRepositoryIdentity(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || u.Opaque != "" {
		return "", false
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) != 2 {
		return "", false
	}
	parts[1] = strings.TrimSuffix(parts[1], ".git")
	for _, p := range parts {
		if !regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9_.-]{0,99}$`).MatchString(p) || strings.HasSuffix(p, ".") {
			return "", false
		}
	}
	return strings.ToLower(strings.Join(parts, "/")), true
}

// recoveryAdmissionError maps the database's fail-closed writer fence. The
// trigger is intentional: generated CRUD and future binding writers obey it too.
func recoveryAdmissionError(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "55P03" {
		return core.ErrWorktreeFinalizing
	}
	return err
}

func recoveryTx(ctx context.Context, db orm.Context, fn func(context.Context) error) error {
	return recoveryAdmissionError(db.RunTx(ctx, orm.ReadCommitted.WithMaxAttempts(orm.DefaultTxMaxAttempts), fn))
}

// GetWorktreeRecovery does not join source rows: deletion/offline sources cannot
// hide an owner's catalog. Not-owned and absent have the same result.
func GetWorktreeRecovery(ctx context.Context, db orm.Context, owner, id string) (*core.WorktreeRecovery, error) {
	r := new(core.WorktreeRecovery)
	err := db.Bun().NewRaw(`SELECT id,owner_user_id,source_worktree_id,project_id,daemon_id,capture_id,fence,activity_generation,observation_id,manifest_digest,phase,last_error,idempotency_key,operation_key,operation,target_daemon_id,authorized_until,created_at,updated_at FROM worktree_recoveries WHERE id = ? AND owner_user_id = ?`, id, owner).Scan(ctx, r)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, core.ErrRecoveryNotFound
	}
	if err != nil {
		return nil, err
	}
	err = db.Bun().NewRaw(`SELECT recovery_id,repo_id,rel,remote_url,recovery_ref,commit_oid,manifest_digest,descriptor_json,receipt_json,phase,last_error FROM worktree_recovery_checkouts WHERE recovery_id = ? ORDER BY repo_id`, id).Scan(ctx, &r.Checkouts)
	return r, err
}

func ListWorktreeRecoveries(ctx context.Context, db orm.Context, owner, project string) ([]*core.WorktreeRecovery, error) {
	var ids []string
	err := db.Bun().NewRaw(`SELECT id FROM worktree_recoveries WHERE owner_user_id = ? AND (? = '' OR project_id = ?) ORDER BY created_at DESC, id`, owner, project, project).Scan(ctx, &ids)
	if err != nil {
		return nil, err
	}
	out := make([]*core.WorktreeRecovery, 0, len(ids))
	for _, id := range ids {
		r, err := GetWorktreeRecovery(ctx, db, owner, id)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func InsertWorktreeRecovery(ctx context.Context, db orm.Context, r *core.WorktreeRecovery) error {
	if r == nil || r.ID == "" || r.OwnerUserID == "" || r.IdempotencyKey == "" || r.CaptureID == "" || r.ManifestDigest == "" || r.ObservationID == "" || len(r.Checkouts) == 0 || len(r.Checkouts) > 64 {
		return core.ErrRecoveryConflict
	}
	return recoveryTx(ctx, db, func(ctx context.Context) error {
		var owner string
		err := db.Bun().NewRaw(`SELECT p.user_id FROM worktrees w JOIN projects p ON p.id=w.project_id WHERE w.id=? AND w.project_id=? FOR UPDATE OF w`, r.SourceWorktreeID, r.ProjectID).Scan(ctx, &owner)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && owner != r.OwnerUserID) {
			return core.ErrRecoveryNotFound
		}
		if err != nil {
			return err
		}
		var existing string
		err = db.Bun().NewRaw(`SELECT id FROM worktree_recoveries WHERE owner_user_id=? AND idempotency_key=?`, owner, r.IdempotencyKey).Scan(ctx, &existing)
		if err == nil {
			old, e := GetWorktreeRecovery(ctx, db, owner, existing)
			if e != nil {
				return e
			}
			if old.ID != r.ID || old.SourceWorktreeID != r.SourceWorktreeID || old.CaptureID != r.CaptureID || old.ManifestDigest != r.ManifestDigest || len(old.Checkouts) != len(r.Checkouts) {
				return core.ErrRecoveryConflict
			}
			for _, c := range r.Checkouts {
				found := false
				for _, prior := range old.Checkouts {
					if c.RepoID == prior.RepoID && c.DescriptorJSON == prior.DescriptorJSON {
						found = true
					}
				}
				if !found {
					return core.ErrRecoveryConflict
				}
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		_, err = db.Bun().NewRaw(`INSERT INTO worktree_recoveries (id,owner_user_id,source_worktree_id,project_id,daemon_id,capture_id,fence,activity_generation,observation_id,manifest_digest,idempotency_key) VALUES (?,?,?,?,?,?,?,?,?,?,?)`, r.ID, owner, r.SourceWorktreeID, r.ProjectID, r.DaemonID, r.CaptureID, r.Fence, r.ActivityGeneration, r.ObservationID, r.ManifestDigest, r.IdempotencyKey).Exec(ctx)
		if err != nil {
			return err
		}
		captures := make([]worktreereclaim.RecoveryCapture, 0, len(r.Checkouts))
		for _, c := range r.Checkouts {
			var d worktreereclaim.RecoveryCapture
			if json.Unmarshal([]byte(c.DescriptorJSON), &d) != nil || d.Version != 1 || d.CaptureID != r.CaptureID || d.WorktreeID != r.SourceWorktreeID || d.RepoID != c.RepoID || d.Rel != c.Rel || d.RemoteURL != c.RemoteURL || d.RecoveryRef != c.RecoveryRef || d.CommitOID != c.CommitOID || d.ManifestDigest != c.ManifestDigest || d.SecretPolicy == "" {
				return core.ErrRecoveryConflict
			}
			var registered struct {
				RelativePath string
				RemoteURL    *string
			}
			if err := db.Bun().NewRaw(`SELECT relative_path,remote_url FROM repos WHERE id=? AND project_id=?`, c.RepoID, r.ProjectID).Scan(ctx, &registered); err != nil {
				return core.ErrRecoveryConflict
			}
			if registered.RelativePath != c.Rel || registered.RemoteURL == nil {
				return core.ErrRecoveryConflict
			}
			registeredURL, ok := recoveryRepositoryIdentity(*registered.RemoteURL)
			captureURL, captureOK := recoveryRepositoryIdentity(c.RemoteURL)
			if !ok || !captureOK || registeredURL != captureURL {
				return core.ErrRecoveryConflict
			}
			if c.RecoveryRef != "refs/heads/reliant-recovery/"+r.SourceWorktreeID+"/"+r.CaptureID+"/"+c.RepoID {
				return core.ErrRecoveryConflict
			}
			captures = append(captures, d)
			_, err = db.Bun().NewRaw(`INSERT INTO worktree_recovery_checkouts (recovery_id,repo_id,rel,remote_url,recovery_ref,commit_oid,manifest_digest,descriptor_json) VALUES (?,?,?,?,?,?,?,?)`, r.ID, c.RepoID, c.Rel, c.RemoteURL, c.RecoveryRef, c.CommitOID, c.ManifestDigest, c.DescriptorJSON).Exec(ctx)
			if err != nil {
				return err
			}
		}
		if worktreereclaim.RecoveryWorkspaceDigest(captures) != r.ManifestDigest {
			return core.ErrRecoveryConflict
		}
		return nil
	})
}

// lockRecovery always locks parent BEFORE journal; chat admission takes the same
// parent lock. No transaction is retained across daemon/network work.
func lockRecovery(ctx context.Context, db orm.Context, owner, id string, source bool) (*core.WorktreeRecovery, error) {
	r, err := GetWorktreeRecovery(ctx, db, owner, id)
	if err != nil {
		return nil, err
	}
	// Always parent first when present, including cancel/complete. Catalog-only
	// operations still work after source deletion.
	var found string
	err = db.Bun().NewRaw(`SELECT id FROM worktrees WHERE id=? FOR UPDATE`, r.SourceWorktreeID).Scan(ctx, &found)
	if errors.Is(err, sql.ErrNoRows) {
		if source {
			return nil, core.ErrRecoveryConflict
		}
	} else if err != nil {
		return nil, err
	}
	var locked string
	err = db.Bun().NewRaw(`SELECT id FROM worktree_recoveries WHERE id=? AND owner_user_id=? FOR UPDATE`, id, owner).Scan(ctx, &locked)
	if err != nil {
		return nil, err
	}
	return GetWorktreeRecovery(ctx, db, owner, id)
}

func checkRecoveryClaim(ctx context.Context, db orm.Context, r *core.WorktreeRecovery, claim bool) error {
	var w struct {
		DeletedAt            *time.Time
		ActivityGeneration   int64
		RecoveryFinalizingID *string
		IsMain               bool
	}
	if err := db.Bun().NewRaw(`SELECT deleted_at,activity_generation,recovery_finalizing_id,is_main FROM worktrees WHERE id=?`, r.SourceWorktreeID).Scan(ctx, &w); err != nil {
		return err
	}
	if w.IsMain || w.DeletedAt == nil || strconv.FormatInt(w.DeletedAt.UnixNano(), 10) != r.Fence || w.ActivityGeneration != r.ActivityGeneration {
		return core.ErrRecoveryConflict
	}
	if w.RecoveryFinalizingID != nil && *w.RecoveryFinalizingID != r.ID {
		return core.ErrWorktreeFinalizing
	}
	if claim && (w.RecoveryFinalizingID == nil || *w.RecoveryFinalizingID != r.ID) {
		return core.ErrRecoveryConflict
	}
	live, err := GetLiveChatIDForWorktree(ctx, db, r.SourceWorktreeID)
	if err != nil {
		return err
	}
	if live != "" {
		return core.ErrRecoveryConflict
	}
	var running bool
	if err := db.Bun().NewRaw(`SELECT EXISTS(SELECT 1 FROM workflows f JOIN chats c ON c.id=f.chat_id WHERE c.worktree_id=? AND (f.state IN (1,2) OR (f.state=3 AND f.stop_reason=3)))`, r.SourceWorktreeID).Scan(ctx, &running); err != nil {
		return err
	}
	if running {
		return core.ErrRecoveryConflict
	}
	return nil
}

// ArchiveWorktreeForRecovery atomically enrolls an active candidate only after
// parent-locked owner, generation, live-chat and active-workflow checks. Ordinary
// manual archive behavior remains unchanged.
func ArchiveWorktreeForRecovery(ctx context.Context, db orm.Context, owner, id string, generation int64) error {
	return recoveryTx(ctx, db, func(ctx context.Context) error {
		var row struct {
			UserID               string
			ActivityGeneration   int64
			IsMain               bool
			RecoveryFinalizingID *string
		}
		err := db.Bun().NewRaw(`SELECT p.user_id,w.activity_generation,w.is_main,w.recovery_finalizing_id FROM worktrees w JOIN projects p ON p.id=w.project_id WHERE w.id=? FOR UPDATE OF w`, id).Scan(ctx, &row)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && row.UserID != owner) {
			return core.ErrRecoveryNotFound
		}
		if err != nil {
			return err
		}
		if row.IsMain || row.ActivityGeneration != generation {
			return core.ErrRecoveryConflict
		}
		if row.RecoveryFinalizingID != nil {
			return core.ErrWorktreeFinalizing
		}
		live, err := GetLiveChatIDForWorktree(ctx, db, id)
		if err != nil {
			return err
		}
		if live != "" {
			return core.ErrRecoveryConflict
		}
		var running bool
		if err = db.Bun().NewRaw(`SELECT EXISTS(SELECT 1 FROM workflows f JOIN chats c ON c.id=f.chat_id WHERE c.worktree_id=? AND (f.state IN (1,2) OR (f.state=3 AND f.stop_reason=3)))`, id).Scan(ctx, &running); err != nil {
			return err
		}
		if running {
			return core.ErrRecoveryConflict
		}
		_, err = db.Bun().NewRaw(`UPDATE worktrees SET deleted_at=COALESCE(deleted_at,now()),updated_at=now() WHERE id=?`, id).Exec(ctx)
		return err
	})
}

func ClaimWorktreeRecovery(ctx context.Context, db orm.Context, owner, id string, generation int64) error {
	return recoveryTx(ctx, db, func(ctx context.Context) error {
		r, err := lockRecovery(ctx, db, owner, id, true)
		if err != nil {
			return err
		}
		if r.AuthorizedUntil != nil && r.AuthorizedUntil.After(time.Now()) {
			return core.ErrRecoveryConflict
		}
		if r.Operation != "" {
			return core.ErrRecoveryConflict
		}
		if generation != r.ActivityGeneration || (r.Phase != "capturing" && r.Phase != "verified" && r.Phase != "held" && r.Phase != "publishing") {
			return core.ErrRecoveryConflict
		}
		if err = checkRecoveryClaim(ctx, db, r, false); err != nil {
			return err
		}
		_, err = db.Bun().NewRaw(`UPDATE worktrees SET recovery_finalizing_id=? WHERE id=?`, id, r.SourceWorktreeID).Exec(ctx)
		if err != nil {
			return err
		}
		// Claim follows durable receipts in the production pipeline. Do not
		// demote verified to publishing or authorization can never succeed.
		_, err = db.Bun().NewRaw(`UPDATE worktree_recoveries SET phase=CASE WHEN NOT EXISTS(SELECT 1 FROM worktree_recovery_checkouts WHERE recovery_id=? AND receipt_json='') THEN 'verified' ELSE 'publishing' END,authorized_until=NULL,updated_at=now() WHERE id=?`, id, id).Exec(ctx)
		return err
	})
}

func RecordWorktreeRecoveryReceipt(ctx context.Context, db orm.Context, owner, id, repo, encoded string) error {
	return recoveryTx(ctx, db, func(ctx context.Context) error {
		r, err := lockRecovery(ctx, db, owner, id, false)
		if err != nil {
			return err
		}
		if r.Operation != "" {
			return core.ErrRecoveryConflict
		}
		if r.Phase != "capturing" && r.Phase != "publishing" && r.Phase != "verified" && r.Phase != "held" {
			return core.ErrRecoveryConflict
		}
		var receipt worktreereclaim.RecoveryReceipt
		if json.Unmarshal([]byte(encoded), &receipt) != nil || receipt.VerifierVersion == "" || receipt.VerifiedAt.IsZero() || receipt.VerifiedAt.After(time.Now().Add(time.Minute)) {
			return core.ErrRecoveryConflict
		}
		found := false
		for _, c := range r.Checkouts {
			if c.RepoID == repo {
				var d worktreereclaim.RecoveryCapture
				if json.Unmarshal([]byte(c.DescriptorJSON), &d) != nil || !reflect.DeepEqual(d, receipt.Capture) {
					return core.ErrRecoveryConflict
				}
				found = true
			}
		}
		if !found {
			return core.ErrRecoveryConflict
		}
		_, err = db.Bun().NewRaw(`UPDATE worktree_recovery_checkouts SET receipt_json=?,phase='verified',last_error='' WHERE recovery_id=? AND repo_id=?`, encoded, id, repo).Exec(ctx)
		if err != nil {
			return err
		}
		_, err = db.Bun().NewRaw(`UPDATE worktree_recoveries SET phase=CASE WHEN NOT EXISTS(SELECT 1 FROM worktree_recovery_checkouts WHERE recovery_id=? AND receipt_json='') THEN 'verified' ELSE phase END,updated_at=now() WHERE id=?`, id, id).Exec(ctx)
		return err
	})
}

func AuthorizeWorktreeRecovery(ctx context.Context, db orm.Context, owner, id string, until time.Time) error {
	if !until.After(time.Now()) || until.After(time.Now().Add(5*time.Minute)) {
		return core.ErrRecoveryConflict
	}
	return recoveryTx(ctx, db, func(ctx context.Context) error {
		r, err := lockRecovery(ctx, db, owner, id, true)
		if err != nil {
			return err
		}
		if r.Phase != "verified" {
			return core.ErrRecoveryConflict
		}
		if err = checkRecoveryClaim(ctx, db, r, true); err != nil {
			return err
		}
		for _, c := range r.Checkouts {
			var receipt worktreereclaim.RecoveryReceipt
			if json.Unmarshal([]byte(c.ReceiptJSON), &receipt) != nil || time.Since(receipt.VerifiedAt) > 5*time.Minute {
				return core.ErrRecoveryConflict
			}
		}
		_, err = db.Bun().NewRaw(`UPDATE worktree_recoveries SET phase='delete_authorized',authorized_until=?,updated_at=now() WHERE id=?`, until, id).Exec(ctx)
		return err
	})
}

func CancelWorktreeRecovery(ctx context.Context, db orm.Context, owner, id, detail string) error {
	return recoveryTx(ctx, db, func(ctx context.Context) error {
		r, err := lockRecovery(ctx, db, owner, id, false)
		if err != nil {
			return err
		}
		if r.Phase != "capturing" && r.Phase != "publishing" && r.Phase != "verified" && r.Phase != "delete_authorized" && r.Phase != "held" {
			return core.ErrRecoveryConflict
		}
		if r.AuthorizedUntil != nil && r.AuthorizedUntil.After(time.Now()) {
			return core.ErrRecoveryConflict
		}
		_, err = db.Bun().NewRaw(`UPDATE worktrees SET recovery_finalizing_id=NULL WHERE id=? AND recovery_finalizing_id=?`, r.SourceWorktreeID, id).Exec(ctx)
		if err != nil {
			return err
		}
		_, err = db.Bun().NewRaw(`UPDATE worktree_recoveries SET phase='held',last_error=?,authorized_until=NULL,updated_at=now() WHERE id=?`, detail, id).Exec(ctx)
		return err
	})
}

func RecordWorktreeActivity(ctx context.Context, db orm.Context, id string) error {
	result, err := db.Bun().NewRaw(`UPDATE worktrees SET activity_generation=activity_generation+1,last_active=now() WHERE id=? AND recovery_finalizing_id IS NULL`, id).Exec(ctx)
	if err != nil {
		return recoveryAdmissionError(err)
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return core.ErrWorktreeFinalizing
	}
	return err
}

func BeginWorktreeRecoveryOperation(ctx context.Context, db orm.Context, owner, id, op, key, target string) error {
	if (op != "restoring" && op != "deletion_requested") || key == "" || (op == "restoring" && target == "") || (op == "deletion_requested" && target != "") {
		return core.ErrRecoveryConflict
	}
	return recoveryTx(ctx, db, func(ctx context.Context) error {
		r, err := lockRecovery(ctx, db, owner, id, false)
		if err != nil {
			return err
		}
		var finalizing *string
		err = db.Bun().NewRaw(`SELECT recovery_finalizing_id FROM worktrees WHERE id=?`, r.SourceWorktreeID).Scan(ctx, &finalizing)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if finalizing != nil {
			return core.ErrWorktreeFinalizing
		}
		terminal := map[string]string{"restoring": "restored", "deletion_requested": "refs_deleted"}[op]
		same := r.Operation == op && r.OperationKey == key && r.TargetDaemonID == target
		if r.OperationKey == key && !same {
			return core.ErrRecoveryConflict
		}
		if same && (r.Phase == op || r.Phase == terminal) {
			return nil
		}
		resume := same && r.Phase == "held"
		rescue := r.Phase == "held" && r.Operation == "" && len(r.Checkouts) > 0
		for _, c := range r.Checkouts {
			if c.ReceiptJSON == "" {
				rescue = false
			}
		}
		if !resume && !rescue && r.Phase != "verified" && r.Phase != "removed" && r.Phase != "restored" {
			return core.ErrRecoveryConflict
		}
		_, err = db.Bun().NewRaw(`UPDATE worktree_recoveries SET phase=?,operation=?,operation_key=?,target_daemon_id=?,authorized_until=NULL,updated_at=now() WHERE id=?`, op, op, key, target, id).Exec(ctx)
		return err
	})
}

func CompleteWorktreeRecoveryCheckout(ctx context.Context, db orm.Context, owner, id, repo, phase, detail string) error {
	if phase != "removed" && phase != "restored" && phase != "refs_deleted" && phase != "held" {
		return core.ErrRecoveryConflict
	}
	return recoveryTx(ctx, db, func(ctx context.Context) error {
		r, err := lockRecovery(ctx, db, owner, id, false)
		if err != nil {
			return err
		}
		expected := map[string]string{"removed": "delete_authorized", "restored": "restoring", "refs_deleted": "deletion_requested"}
		if phase != "held" && r.Phase != expected[phase] && r.Phase != phase {
			return core.ErrRecoveryConflict
		}
		if phase == "held" && (r.Phase == "removed" || r.Phase == "restored" || r.Phase == "refs_deleted") {
			return core.ErrRecoveryConflict
		}
		for _, c := range r.Checkouts {
			if c.RepoID == repo && c.Phase == phase && phase != "held" {
				return nil
			}
		}
		if phase == "removed" {
			if r.AuthorizedUntil == nil || !r.AuthorizedUntil.After(time.Now()) {
				return core.ErrRecoveryConflict
			}
			if err := checkRecoveryClaim(ctx, db, r, true); err != nil {
				return err
			}
		}
		found := false
		for _, c := range r.Checkouts {
			if c.RepoID == repo {
				found = true
			}
		}
		if !found {
			return core.ErrRecoveryConflict
		}
		_, err = db.Bun().NewRaw(`UPDATE worktree_recovery_checkouts SET phase=?,last_error=? WHERE recovery_id=? AND repo_id=?`, phase, detail, id, repo).Exec(ctx)
		if err != nil {
			return err
		}
		_, err = db.Bun().NewRaw(`UPDATE worktree_recoveries SET phase=CASE WHEN NOT EXISTS(SELECT 1 FROM worktree_recovery_checkouts WHERE recovery_id=? AND phase<>?) THEN ? ELSE phase END,last_error=?,updated_at=now() WHERE id=?`, id, phase, phase, detail, id).Exec(ctx)
		if err != nil {
			return err
		}
		if phase == "removed" {
			_, err = db.Bun().NewRaw(`UPDATE worktrees SET recovery_finalizing_id=NULL WHERE id=? AND recovery_finalizing_id=? AND EXISTS(SELECT 1 FROM worktree_recoveries WHERE id=? AND phase='removed')`, r.SourceWorktreeID, id, id).Exec(ctx)
		}
		return err
	})
}
