package daemonruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

const CommandWorktreeRecoveryRestore = "worktree.recovery_restore"

// Serialize publication/retry reconciliation on this daemon. Persisted markers
// handle restart; this mutex only prevents overlapping same-process attempts.
var recoveryRestoreMu sync.Mutex

// RecoveryRestoreCommand is ephemeral. Tokens are never persisted or returned.
// Repositories are the destination project's owner-checked registered clones.
type RecoveryRestoreCommand struct {
	worktreereclaim.RecoveryRestoreRequest
	Repositories map[string]string `json:"repositories"`
	Token        string            `json:"token"`
}

type recoveryRestoreMarker struct {
	Digest    string   `json:"digest"`
	RepoIDs   []string `json:"repo_ids"`
	StagePath string   `json:"stage_path"`
	Prepared  bool     `json:"prepared"`
}

func init() { RegisterCommand(CommandWorktreeRecoveryRestore, handleWorktreeRecoveryRestore) }

func handleWorktreeRecoveryRestore(ctx context.Context, payload []byte) ([]byte, error) {
	var req RecoveryRestoreCommand
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid recovery restore command")
	}
	recoveryRestoreMu.Lock()
	defer recoveryRestoreMu.Unlock()
	result := restoreRecoveryWorkspace(ctx, req)
	return json.Marshal(result)
}

func restoreRecoveryWorkspace(ctx context.Context, req RecoveryRestoreCommand) worktreereclaim.RecoveryRestoreResponse {
	held := func(detail string) worktreereclaim.RecoveryRestoreResponse {
		return worktreereclaim.RecoveryRestoreResponse{Outcome: worktreereclaim.OutcomeHeld, Reason: worktreereclaim.ReasonUnsupportedRecovery, Detail: detail}
	}
	if req.RecoveryID == "" || req.IdempotencyKey == "" || req.Token == "" || len(req.Captures) == 0 || req.Branch == "" {
		return held("Recovery identity, credential and captures are required.")
	}
	if req.DestinationPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return held("Cannot resolve destination machine home.")
		}
		key := sha256.Sum256([]byte(req.RecoveryID + "\x00" + req.IdempotencyKey))
		req.DestinationPath = filepath.Join(home, ".reliant", "worktrees", "recovery-"+hex.EncodeToString(key[:16]))
		if err := os.MkdirAll(filepath.Dir(req.DestinationPath), 0700); err != nil {
			return held("Cannot create managed recovery root.")
		}
	}
	if !filepath.IsAbs(req.DestinationPath) || filepath.Clean(req.DestinationPath) != req.DestinationPath {
		return held("Invalid separate recovery destination.")
	}
	parent := filepath.Dir(req.DestinationPath)
	real, err := filepath.EvalSymlinks(parent)
	if err != nil || real != parent {
		return held("Recovery destination parent must exist without symlink aliases.")
	}
	// The exact operation/capture mapping binds retries. A marker alone is not
	// success: every restored repository is revalidated on response-loss retries.
	identity, _ := json.Marshal(struct {
		Request      worktreereclaim.RecoveryRestoreRequest
		Repositories map[string]string
	}{req.RecoveryRestoreRequest, req.Repositories})
	sum := sha256.Sum256(identity)
	digest := hex.EncodeToString(sum[:])
	markerPath := req.DestinationPath + ".reliant-recovery.json"
	if data, err := os.ReadFile(markerPath); err == nil {
		var marker recoveryRestoreMarker
		if json.Unmarshal(data, &marker) != nil || marker.Digest != digest {
			return held("Destination belongs to a different recovery operation.")
		}
		if _, err := os.Lstat(req.DestinationPath); os.IsNotExist(err) {
			if !strings.HasPrefix(filepath.Base(filepath.Dir(marker.StagePath)), ".reliant-recovery-") || filepath.Dir(filepath.Dir(marker.StagePath)) != parent || filepath.Base(marker.StagePath) != "workspace" {
				return held("Invalid restore staging journal; retained for review.")
			}
			if !marker.Prepared {
				for _, c := range req.Captures {
					if err := worktreereclaim.RollbackInterruptedRecovery(ctx, req.Repositories[c.RepoID], filepath.Join(marker.StagePath, c.Rel), req.Branch, c.HeadOID); err != nil {
						return held("Interrupted owned registration changed; retained for review.")
					}
				}
				if err := os.RemoveAll(filepath.Dir(marker.StagePath)); err != nil {
					return held("Cannot reconcile owned staging directory.")
				}
				if err := os.Remove(markerPath); err != nil {
					return held("Cannot reconcile owned staging journal.")
				}
				return restoreRecoveryWorkspace(ctx, req)
			}
			if _, err := os.Lstat(marker.StagePath); os.IsNotExist(err) {
				for _, c := range req.Captures {
					if err := worktreereclaim.RollbackInterruptedRecovery(ctx, req.Repositories[c.RepoID], filepath.Join(marker.StagePath, c.Rel), req.Branch, c.HeadOID); err != nil {
						return held("Missing staging registration requires review.")
					}
				}
				if err := os.Remove(markerPath); err != nil {
					return held("Cannot reconcile interrupted restore journal.")
				}
				return restoreRecoveryWorkspace(ctx, req)
			}
			for _, c := range req.Captures {
				if err := worktreereclaim.ValidateRestoredRecovery(ctx, filepath.Join(marker.StagePath, c.Rel), req.Branch, c); err != nil {
					return held("Interrupted staging changed; preserved for review.")
				}
			}
			if err := publishRecoveryDirectory(marker.StagePath, req.DestinationPath); err != nil {
				return held("Interrupted restore publication held; destination untouched.")
			}
		}
		for _, c := range req.Captures {
			if err := worktreereclaim.RepairRecoveryRegistration(ctx, req.Repositories[c.RepoID], filepath.Join(req.DestinationPath, c.Rel)); err != nil {
				return held("Published recovery registration repair is held; destination preserved.")
			}
			if err := worktreereclaim.ValidateRestoredRecovery(ctx, filepath.Join(req.DestinationPath, c.Rel), req.Branch, c); err != nil {
				return held("Previously restored destination changed or cannot be validated; kept for review.")
			}
		}
		return worktreereclaim.RecoveryRestoreResponse{Outcome: worktreereclaim.OutcomeRestored, Path: req.DestinationPath, Branch: req.Branch, RestoredRepoIDs: marker.RepoIDs}
	}
	if _, err := os.Lstat(req.DestinationPath); !os.IsNotExist(err) {
		return held("Recovery destination already exists; nothing was overwritten.")
	}
	rels := map[string]bool{}
	ids := map[string]bool{}
	for _, c := range req.Captures {
		rel := filepath.ToSlash(c.Rel)
		if rel == "." {
			rel = ""
		}
		if rel != "" {
			for _, part := range strings.Split(rel, "/") {
				if part == "" || part == "." || part == ".." || strings.EqualFold(part, ".git") || strings.ContainsAny(part, "\\:") {
					return held("Unsafe repository mapping.")
				}
			}
		}
		if filepath.IsAbs(rel) || ids[c.RepoID] || rels[strings.ToLower(rel)] || req.Repositories[c.RepoID] == "" {
			return held("Install the project's registered repositories on the destination machine before restoring.")
		}
		ids[c.RepoID] = true
		rels[strings.ToLower(rel)] = true
	}
	// v1 does not support overlapping checkout roots. Never let one checkout
	// materialize over another checkout's files or .git administration.
	for a := range rels {
		for b := range rels {
			if a != b && (a == "" || strings.HasPrefix(b, a+"/")) {
				return held("Overlapping checkout mapping is unsupported; recovery retained.")
			}
		}
	}
	stage, err := os.MkdirTemp(parent, ".reliant-recovery-")
	if err != nil {
		return held("Cannot reserve recovery staging directory.")
	}
	defer os.RemoveAll(stage)
	// Journal exact owned staging before any registration. A killed process can
	// reconcile only these registered paths/expected new branches on retry.
	initial, _ := json.Marshal(recoveryRestoreMarker{Digest: digest, StagePath: filepath.Join(stage, "workspace")})
	journal, err := os.OpenFile(markerPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return held("Cannot reserve restore operation journal.")
	}
	_, err = journal.Write(initial)
	if err == nil {
		err = journal.Sync()
	}
	closeErr := journal.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return held("Cannot persist restore operation journal.")
	}
	type registered struct {
		capture      worktreereclaim.RecoveryCapture
		path, parent string
	}
	var registeredRepos []registered
	published := false
	defer func() {
		if !published {
			for i := len(registeredRepos) - 1; i >= 0; i-- {
				r := registeredRepos[i]
				worktreereclaim.RollbackRecoveryRegistration(context.WithoutCancel(ctx), r.parent, r.path, req.Branch, r.capture.HeadOID)
			}
		}
	}()
	// Fetches are independently verified by C; restore verifies raw tree/index
	// before this checkout joins the all-or-held workspace.
	for _, c := range req.Captures {
		dest := filepath.Join(stage, "workspace", c.Rel)
		if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			return held("Cannot prepare recovery checkout parent.")
		}
		_, err := WithVerifiedGitRecovery(ctx, c, req.Token, func(bare string) error {
			return worktreereclaim.RestoreRecoveryLinkedCheckout(ctx, bare, req.Repositories[c.RepoID], dest, req.Branch, c)
		})
		if err != nil {
			return held("Recovery fetch or exact reconstruction failed. Originals and durable recovery branches were retained; check repository access and destination clone.")
		}
		registeredRepos = append(registeredRepos, registered{c, dest, req.Repositories[c.RepoID]})
	}
	repoIDs := make([]string, 0, len(ids))
	for id := range ids {
		repoIDs = append(repoIDs, id)
	}
	sort.Strings(repoIDs)
	data, _ := json.Marshal(recoveryRestoreMarker{Digest: digest, RepoIDs: repoIDs, StagePath: filepath.Join(stage, "workspace"), Prepared: true})
	preparedPath := filepath.Join(stage, "prepared.json")
	f, err := os.OpenFile(preparedPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return held("Restored files retained, but completion marker could not be committed.")
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	cerr := f.Close()
	if err == nil {
		err = cerr
	}
	if err != nil {
		return held("Restored files retained; completion journal requires reconciliation.")
	}
	if err := os.Rename(preparedPath, markerPath); err != nil {
		return held("Cannot persist prepared recovery journal.")
	}
	if dir, err := os.Open(parent); err == nil {
		err = dir.Sync()
		_ = dir.Close()
		if err != nil {
			return held("Cannot sync prepared recovery journal.")
		}
	} else {
		return held("Cannot open recovery journal directory.")
	}
	if err := publishRecoveryDirectory(filepath.Join(stage, "workspace"), req.DestinationPath); err != nil {
		return held("Recovery destination collision or atomic publication unavailable; nothing overwritten.")
	}
	published = true
	for _, r := range registeredRepos {
		if err := worktreereclaim.RepairRecoveryRegistration(ctx, r.parent, filepath.Join(req.DestinationPath, r.capture.Rel)); err != nil {
			return held("Published recovery registration repair held; retry resumes exact operation.")
		}
	}
	return worktreereclaim.RecoveryRestoreResponse{Outcome: worktreereclaim.OutcomeRestored, Path: req.DestinationPath, Branch: req.Branch, RestoredRepoIDs: repoIDs}
}
