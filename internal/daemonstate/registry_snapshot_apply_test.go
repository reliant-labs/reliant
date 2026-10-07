// Copyright (c) 2025 Reliant Labs

package daemonstate

import (
	"testing"
	"time"
)

// TestSnapshotApply_Guards pins the translation: the created-after guard is
// the snapshot's read time minus the grace, the attachment guard is measured
// from now, lifecycle keys on the phase's own changed-at (falling back to the
// machine's creation), and an unknown phase keeps the identity but drops the
// lifecycle.
func TestSnapshotApply_Guards(t *testing.T) {
	at := time.Date(2026, 10, 7, 15, 0, 0, 0, time.UTC)
	now := at.Add(3 * time.Second)
	created := at.Add(-48 * time.Hour)
	changed := at.Add(-time.Hour)

	apply := snapshotApply(RegistrySnapshot{
		UserID: "u",
		At:     at,
		Daemons: []RegistryDaemon{
			{DaemonID: "a", Name: "a", DaemonType: "managed", CreatedAt: created, Phase: LifecyclePhaseReady, PhaseChangedAt: &changed},
			{DaemonID: "b", Name: "b", DaemonType: "managed", CreatedAt: created, Phase: LifecyclePhaseProvisioning},
			{DaemonID: "c", Name: "c", DaemonType: "managed", CreatedAt: created, Phase: LifecyclePhase("hibernating")},
			{DaemonID: "d", DaemonType: "self_hosted", CreatedAt: created},
			{DaemonID: "", Name: "no id"},
		},
	}, now)

	if !apply.KeepCreatedAfter.Equal(at.Add(-registrySnapshotGrace)) {
		t.Errorf("KeepCreatedAfter = %v, want snapshot time minus grace", apply.KeepCreatedAfter)
	}
	if !apply.KeepAttachedSince.Equal(now.Add(-registryAttachmentFreshness)) {
		t.Errorf("KeepAttachedSince = %v, want now minus attachment freshness", apply.KeepAttachedSince)
	}
	if len(apply.Daemons) != 4 {
		t.Fatalf("got %d daemons, want 4 (the id-less entry dropped)", len(apply.Daemons))
	}
	if lc := apply.Daemons[0].Lifecycle; lc == nil || !lc.ChangedAt.Equal(changed) || lc.Phase != "ready" {
		t.Errorf("a: lifecycle = %+v, want ready at the phase's changed-at", lc)
	}
	if lc := apply.Daemons[1].Lifecycle; lc == nil || !lc.ChangedAt.Equal(created) {
		t.Errorf("b: lifecycle = %+v, want changed-at falling back to created", lc)
	}
	if apply.Daemons[2].Lifecycle != nil {
		t.Error("c: an unknown phase must not be applied")
	}
	if apply.Daemons[3].Lifecycle != nil {
		t.Error("d: a self-hosted daemon has no lifecycle to apply")
	}
	for _, d := range apply.Daemons {
		if d.Identity.UserID != "u" {
			t.Errorf("%s: identity user = %q, want the snapshot's owner", d.Identity.DaemonID, d.Identity.UserID)
		}
	}
}
