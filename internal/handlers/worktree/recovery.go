package worktree

import (
	"context"
	"encoding/json"
	"path/filepath"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/gitcredentialclient"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

// GitCredentialProvider resolves a CURRENT owner token for one operation.
type GitCredentialProvider interface {
	UserAccessToken(context.Context, string, string) (gitcredentialclient.Token, error)
}

func recoveryToProto(row *core.WorktreeRecovery) *pb.WorktreeRecovery {
	out := &pb.WorktreeRecovery{Id: row.ID, SourceWorktreeId: row.SourceWorktreeID, ProjectId: row.ProjectID, DaemonId: row.DaemonID, CaptureId: row.CaptureID, Phase: row.Phase, LastError: row.LastError, CreatedAt: row.CreatedAt.UTC().Format(time.RFC3339Nano), UpdatedAt: row.UpdatedAt.UTC().Format(time.RFC3339Nano), Operation: row.Operation, OperationKey: row.OperationKey, TargetDaemonId: row.TargetDaemonID}
	for _, c := range row.Checkouts {
		out.Checkouts = append(out.Checkouts, &pb.WorktreeRecoveryCheckout{RepoId: c.RepoID, Rel: c.Rel, RemoteUrl: c.RemoteURL, RecoveryRef: c.RecoveryRef, CommitOid: c.CommitOID, ManifestDigest: c.ManifestDigest, Phase: c.Phase, LastError: c.LastError})
	}
	return out
}

// recoveryInstallationPath never treats a project's path on one machine as an
// installation on another. The daemon additionally validates the actual clone.
func recoveryInstallationPath(rows []*core.ProjectDaemon, daemonID string) string {
	for _, row := range rows {
		if row.DaemonID == daemonID && row.InstallState == core.ProjectInstallInstalled && filepath.IsAbs(row.Path) && filepath.Clean(row.Path) == row.Path {
			return row.Path
		}
	}
	return ""
}

func recoveryCaptures(row *core.WorktreeRecovery) ([]worktreereclaim.RecoveryCapture, error) {
	var captures []worktreereclaim.RecoveryCapture
	for _, c := range row.Checkouts {
		var capture worktreereclaim.RecoveryCapture
		if json.Unmarshal([]byte(c.DescriptorJSON), &capture) != nil || capture.RepoID != c.RepoID || capture.CommitOID != c.CommitOID || capture.RecoveryRef != c.RecoveryRef || capture.RemoteURL != c.RemoteURL || capture.ManifestDigest != c.ManifestDigest || capture.Rel != c.Rel || capture.CaptureID != row.CaptureID {
			return nil, svcerr.FailedPrecondition("recovery descriptor does not match durable catalog")
		}
		captures = append(captures, capture)
	}
	if len(captures) == 0 {
		return nil, svcerr.FailedPrecondition("recovery has no captured repositories")
	}
	return captures, nil
}
