// Copyright (c) 2025 Reliant Labs

package services

import (
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemonstate"
	"github.com/reliant-labs/reliant/internal/db"
)

func strp(s string) *string { return &s }

// TestComposeDaemonStatus_AttachmentWinsOverLifecycle pins the precedence rule
// that the original complaint turns on.
//
// A fresh attachment means a stream is attached RIGHT NOW and work can be
// routed — that is observed. A lifecycle phase is at best a slightly older
// mirror of a Workspace CR owned by another service. Letting the phase override
// the attachment would reproduce exactly the reported bug in a new place: a
// connected, serving daemon reading as not-ready because a mirrored column had
// not caught up.
func TestComposeDaemonStatus_AttachmentWinsOverLifecycle(t *testing.T) {
	// Every phase, including the terminal ones, loses to a live attachment.
	phases := []reliantv1.DaemonLifecyclePhase{
		reliantv1.DaemonLifecyclePhase_DAEMON_LIFECYCLE_PHASE_UNSPECIFIED,
		reliantv1.DaemonLifecyclePhase_DAEMON_LIFECYCLE_PHASE_PROVISIONING,
		reliantv1.DaemonLifecyclePhase_DAEMON_LIFECYCLE_PHASE_CLONING,
		reliantv1.DaemonLifecyclePhase_DAEMON_LIFECYCLE_PHASE_READY,
		reliantv1.DaemonLifecyclePhase_DAEMON_LIFECYCLE_PHASE_SUSPENDING,
		reliantv1.DaemonLifecyclePhase_DAEMON_LIFECYCLE_PHASE_SUSPENDED,
		reliantv1.DaemonLifecyclePhase_DAEMON_LIFECYCLE_PHASE_FAILED,
	}
	for _, phase := range phases {
		if got := composeDaemonStatus(phase, true); got != reliantv1.DaemonStatus_DAEMON_STATUS_ACTIVE {
			t.Errorf("attached daemon with phase %v: status = %v, want ACTIVE", phase, got)
		}
	}
}

// TestComposeDaemonStatus_UnattachedTakesTheLifecyclePhase covers what the old
// two-value enum could not express. Before this, an unattached daemon was
// DISCONNECTED whatever was happening to it, so a machine mid-provision and a
// machine that had crashed arrived at the UI as the same value — which is why
// the UI fetched a second list from control-plane to tell them apart.
func TestComposeDaemonStatus_UnattachedTakesTheLifecyclePhase(t *testing.T) {
	cases := []struct {
		name  string
		phase reliantv1.DaemonLifecyclePhase
		want  reliantv1.DaemonStatus
	}{
		{"provisioning is pending", reliantv1.DaemonLifecyclePhase_DAEMON_LIFECYCLE_PHASE_PROVISIONING, reliantv1.DaemonStatus_DAEMON_STATUS_PENDING},
		{"cloning is pending", reliantv1.DaemonLifecyclePhase_DAEMON_LIFECYCLE_PHASE_CLONING, reliantv1.DaemonStatus_DAEMON_STATUS_PENDING},
		{"suspending is suspended", reliantv1.DaemonLifecyclePhase_DAEMON_LIFECYCLE_PHASE_SUSPENDING, reliantv1.DaemonStatus_DAEMON_STATUS_SUSPENDED},
		{"suspended is suspended", reliantv1.DaemonLifecyclePhase_DAEMON_LIFECYCLE_PHASE_SUSPENDED, reliantv1.DaemonStatus_DAEMON_STATUS_SUSPENDED},
		{"failed is failed", reliantv1.DaemonLifecyclePhase_DAEMON_LIFECYCLE_PHASE_FAILED, reliantv1.DaemonStatus_DAEMON_STATUS_FAILED},
		// READY-but-unattached is the operator saying the pod is up while
		// nothing is attached to route work to. That window is precisely what
		// the attachment lease exists to report, so it is DISCONNECTED.
		{"ready but unattached is disconnected", reliantv1.DaemonLifecyclePhase_DAEMON_LIFECYCLE_PHASE_READY, reliantv1.DaemonStatus_DAEMON_STATUS_DISCONNECTED},
		// No phase ever reported: the permanent state of every self-hosted
		// daemon. Must behave exactly as it did before lifecycle existed.
		{"no phase is disconnected", reliantv1.DaemonLifecyclePhase_DAEMON_LIFECYCLE_PHASE_UNSPECIFIED, reliantv1.DaemonStatus_DAEMON_STATUS_DISCONNECTED},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := composeDaemonStatus(tc.phase, false); got != tc.want {
				t.Errorf("status = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestDaemonToProto_SelfHostedIsUnchanged is the regression guard for the half
// of the fleet that has no control plane at all. Self-hosted daemons must keep
// reporting exactly what they reported before lifecycle mirroring existed —
// ACTIVE when attached, DISCONNECTED otherwise, with no lifecycle fields set.
// OSS mode has no publisher, so anything that made the new fields load-bearing
// would break it silently.
func TestDaemonToProto_SelfHostedIsUnchanged(t *testing.T) {
	d := &db.Daemon{
		ID: "d-local", UserID: "u-1",
		Hostname: strp("laptop"), Platform: strp("darwin"),
		DaemonType: strp("self_hosted"),
	}

	got := daemonToProto(d, nil)
	if got.GetStatus() != reliantv1.DaemonStatus_DAEMON_STATUS_DISCONNECTED {
		t.Errorf("unattached self-hosted: status = %v, want DISCONNECTED", got.GetStatus())
	}
	if got.GetLifecyclePhase() != reliantv1.DaemonLifecyclePhase_DAEMON_LIFECYCLE_PHASE_UNSPECIFIED {
		t.Errorf("self-hosted carries a lifecycle phase: %v", got.GetLifecyclePhase())
	}
	if got.GetSize() != "" || got.GetLastStatusMessage() != "" || got.GetLastStatusChangedAt() != nil {
		t.Errorf("self-hosted carries lifecycle detail: size=%q msg=%q changed=%v",
			got.GetSize(), got.GetLastStatusMessage(), got.GetLastStatusChangedAt())
	}

	attached := daemonToProto(d, &db.DaemonAttachment{DaemonID: "d-local", UserID: "u-1"})
	if attached.GetStatus() != reliantv1.DaemonStatus_DAEMON_STATUS_ACTIVE {
		t.Errorf("attached self-hosted: status = %v, want ACTIVE", attached.GetStatus())
	}
}

// TestDaemonToProto_ManagedCarriesLifecycleDetail is the end of the chain this
// PR builds: a managed machine that is provisioning reports PENDING with the
// phase, size and reason that make a spinner explain itself, from the single
// list the UI calls. Every one of these fields previously required a second RPC
// to a second service.
func TestDaemonToProto_ManagedCarriesLifecycleDetail(t *testing.T) {
	changedAt := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	oomAt := changedAt.Add(-time.Hour)
	d := &db.Daemon{
		ID: "d-cloud", UserID: "u-1",
		Hostname: strp("onboarding-daemon"), Platform: strp("linux"),
		DaemonType:          strp("managed"),
		LifecyclePhase:      strp(string(daemonstate.LifecyclePhaseCloning)),
		Size:                strp("medium"),
		LastStatusMessage:   "cloning your repository",
		LastStatusChangedAt: &changedAt,
		LastOOMKilledAt:     &oomAt,
		OOMKillCount:        3,
	}

	got := daemonToProto(d, nil)
	if got.GetStatus() != reliantv1.DaemonStatus_DAEMON_STATUS_PENDING {
		t.Errorf("status = %v, want PENDING", got.GetStatus())
	}
	if got.GetLifecyclePhase() != reliantv1.DaemonLifecyclePhase_DAEMON_LIFECYCLE_PHASE_CLONING {
		t.Errorf("lifecycle_phase = %v, want CLONING", got.GetLifecyclePhase())
	}
	if got.GetSize() != "medium" {
		t.Errorf("size = %q, want %q", got.GetSize(), "medium")
	}
	if got.GetLastStatusMessage() != "cloning your repository" {
		t.Errorf("last_status_message = %q", got.GetLastStatusMessage())
	}
	if got.GetLastStatusChangedAt() == nil || !got.GetLastStatusChangedAt().AsTime().Equal(changedAt) {
		t.Errorf("last_status_changed_at = %v, want %v", got.GetLastStatusChangedAt(), changedAt)
	}
	if got.GetOomKillCount() != 3 {
		t.Errorf("oom_kill_count = %d, want 3", got.GetOomKillCount())
	}
	if got.GetLastOomKilledAt() == nil || !got.GetLastOomKilledAt().AsTime().Equal(oomAt) {
		t.Errorf("last_oom_killed_at = %v, want %v", got.GetLastOomKilledAt(), oomAt)
	}
}

// TestLifecyclePhaseToProto_UnknownIsUnspecified pins forward compatibility at
// the read edge, matching the consumer's behaviour at the write edge. A phase
// string this binary does not recognize must degrade to "no extra detail" so
// the client falls back to status, which is always correct if less specific.
func TestLifecyclePhaseToProto_UnknownIsUnspecified(t *testing.T) {
	if got := lifecyclePhaseToProto(strp("hibernating")); got != reliantv1.DaemonLifecyclePhase_DAEMON_LIFECYCLE_PHASE_UNSPECIFIED {
		t.Errorf("unknown phase = %v, want UNSPECIFIED", got)
	}
	if got := lifecyclePhaseToProto(nil); got != reliantv1.DaemonLifecyclePhase_DAEMON_LIFECYCLE_PHASE_UNSPECIFIED {
		t.Errorf("nil phase = %v, want UNSPECIFIED", got)
	}
}
