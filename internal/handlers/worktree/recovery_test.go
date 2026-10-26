package worktree

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

func TestRecoveryDescriptorMustMatchCatalog(t *testing.T) {
	capture := worktreereclaim.RecoveryCapture{CaptureID: "capture", RepoID: "repo", Rel: ".", RemoteURL: "https://github.com/example/repo.git", RecoveryRef: "refs/heads/reliant-recovery/w/c/r", CommitOID: "oid", ManifestDigest: "digest"}
	data, _ := json.Marshal(capture)
	row := &core.WorktreeRecovery{ID: "recovery", CaptureID: "capture", Checkouts: []core.RecoveryCheckout{{RepoID: capture.RepoID, Rel: capture.Rel, RemoteURL: capture.RemoteURL, RecoveryRef: capture.RecoveryRef, CommitOID: capture.CommitOID, ManifestDigest: capture.ManifestDigest, DescriptorJSON: string(data)}}}
	got, err := recoveryCaptures(row)
	if err != nil || len(got) != 1 {
		t.Fatalf("valid descriptor: %v", err)
	}
	for _, mutate := range []func(*core.RecoveryCheckout){
		func(c *core.RecoveryCheckout) { c.RemoteURL = "https://github.com/other/repo.git" },
		func(c *core.RecoveryCheckout) { c.CommitOID = "moved" },
		func(c *core.RecoveryCheckout) { c.RecoveryRef = "refs/heads/main" },
		func(c *core.RecoveryCheckout) { c.ManifestDigest = "different" },
		func(c *core.RecoveryCheckout) { c.DescriptorJSON = "{}" },
	} {
		copyRow := *row
		copyRow.Checkouts = append([]core.RecoveryCheckout(nil), row.Checkouts...)
		mutate(&copyRow.Checkouts[0])
		if _, err := recoveryCaptures(&copyRow); err == nil {
			t.Fatal("accepted catalog/descriptor mismatch")
		}
	}
}

func TestRecoveryDestinationRequiresInstalledProject(t *testing.T) {
	rows := []*core.ProjectDaemon{
		{DaemonID: "other", Path: "/other", InstallState: core.ProjectInstallInstalled},
		{DaemonID: "queued", Path: "/queued", InstallState: core.ProjectInstallInstalling},
		{DaemonID: "target", Path: "/machine-specific/clone", InstallState: core.ProjectInstallInstalled},
	}
	if got := recoveryInstallationPath(rows, "target"); got != "/machine-specific/clone" {
		t.Fatalf("destination installation = %q", got)
	}
	for _, daemon := range []string{"missing", "queued"} {
		if recoveryInstallationPath(rows, daemon) != "" {
			t.Fatalf("accepted uninstalled destination %s", daemon)
		}
	}
}

func TestRecoveryPublicPayloadDoesNotExposeDescriptor(t *testing.T) {
	row := &core.WorktreeRecovery{ID: "retained", SourceWorktreeID: "deleted-source", DaemonID: "deleted-machine", Phase: "held", Operation: "restoring", OperationKey: "stable-key", TargetDaemonID: "target", CreatedAt: time.Now(), UpdatedAt: time.Now(), Checkouts: []core.RecoveryCheckout{{RepoID: "repo", Phase: "held", LastError: "remote denied", DescriptorJSON: "internal", ReceiptJSON: "internal"}}}
	got := recoveryToProto(row)
	if got.Id != "retained" || got.SourceWorktreeId != "deleted-source" || len(got.Checkouts) != 1 || got.Checkouts[0].LastError != "remote denied" {
		t.Fatal("retained recovery identity/progress missing")
	}
	if got.Operation != "restoring" || got.OperationKey != "stable-key" || got.TargetDaemonId != "target" {
		t.Fatal("durable operation retry identity missing")
	}
}
