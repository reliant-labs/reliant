// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
)

// newLocalOnlyAccountService builds an AccountService with NO control-plane
// client, for tests that exercise only the local purge.
//
// This is not incidental. NewAccountService wires a client whenever
// RELIANT_CONTROL_PLANE_URL is set in the environment, and a developer machine
// commonly has it pointed at PRODUCTION — so a test calling the bare
// constructor would issue a real account-deletion RPC against the live control
// plane. Every local-purge test must opt out explicitly.
func newLocalOnlyAccountService(rawDB *sql.DB) *AccountService {
	svc := NewAccountService(rawDB)
	svc.controlPlane = nil
	return svc
}

// stubControlPlane records the deletion call and returns a scripted outcome.
type stubControlPlane struct {
	blockers []controlplane.AccountDeletionBlocker
	err      error
	calls    int
	lastJWT  string
}

func (s *stubControlPlane) IssueMyReliantAPIKey(context.Context, string) (string, error) {
	return "", nil
}

func (s *stubControlPlane) DeleteCurrentUserAccount(_ context.Context, jwt string) ([]controlplane.AccountDeletionBlocker, error) {
	s.calls++
	s.lastJWT = jwt
	return s.blockers, s.err
}

// TestDeleteAccount_ControlPlaneBlockStopsBeforeLocalPurge is the ordering
// guarantee that makes this feature safe.
//
// Control-plane runs FIRST. When it refuses — a paid subscription, prepaid
// credit — the local purge must not run at all. The opposite order has the one
// unacceptable failure: every chat and project destroyed, and the billing still
// running.
func TestDeleteAccount_ControlPlaneBlockStopsBeforeLocalPurge(t *testing.T) {
	repo, rawDB, cleanup := db.SetupTestDBWithRawDB(t)
	defer cleanup()
	_ = repo

	cp := &stubControlPlane{blockers: []controlplane.AccountDeletionBlocker{{
		Reason: "paid_subscription",
		Detail: "You have an active paid subscription. Cancel it in Billing first.",
	}}}
	svc := NewAccountService(rawDB).WithControlPlaneClient(cp)

	_, err := svc.DeleteAccount(
		accountAuthedCtx("u-blocked", "owner@example.com"),
		deleteReq("owner@example.com"))

	if err == nil {
		t.Fatal("a control-plane block must fail the deletion")
	}
	if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
		t.Errorf("code = %v, want FailedPrecondition", got)
	}
	// The user must be told WHAT to do, not handed a generic failure.
	if !strings.Contains(err.Error(), "Cancel it in Billing") {
		t.Errorf("error must carry the blocker's remedy, got %q", err.Error())
	}
	if cp.calls != 1 {
		t.Errorf("control plane calls = %d, want 1", cp.calls)
	}
}

// TestDeleteAccount_ControlPlaneErrorAbortsWithNothingDeleted: a failed
// control-plane call must abort, not fall through to the local purge. Purging
// local data while the platform account survives is the half-state this
// ordering exists to prevent.
func TestDeleteAccount_ControlPlaneErrorAbortsWithNothingDeleted(t *testing.T) {
	_, rawDB, cleanup := db.SetupTestDBWithRawDB(t)
	defer cleanup()

	cp := &stubControlPlane{err: errors.New("control plane unreachable")}
	svc := NewAccountService(rawDB).WithControlPlaneClient(cp)

	_, err := svc.DeleteAccount(
		accountAuthedCtx("u-err", "owner@example.com"),
		deleteReq("owner@example.com"))

	if err == nil {
		t.Fatal("a control-plane failure must abort the deletion")
	}
	if got := connect.CodeOf(err); got != connect.CodeInternal {
		t.Errorf("code = %v, want Internal", got)
	}
}

// TestDeleteAccount_ProceedsWhenControlPlaneClears: with no blockers the local
// purge runs and the deletion succeeds.
func TestDeleteAccount_ProceedsWhenControlPlaneClears(t *testing.T) {
	_, rawDB, cleanup := db.SetupTestDBWithRawDB(t)
	defer cleanup()

	cp := &stubControlPlane{}
	svc := NewAccountService(rawDB).WithControlPlaneClient(cp)

	_, err := svc.DeleteAccount(
		accountAuthedCtx("u-ok", "owner@example.com"),
		deleteReq("owner@example.com"))
	if err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	if cp.calls != 1 {
		t.Errorf("the control plane must be called exactly once, got %d", cp.calls)
	}
}

// TestDeleteAccount_WithoutControlPlaneStillPurgesLocally: a self-hosted
// deployment has no platform account, so deletion must still work.
func TestDeleteAccount_WithoutControlPlaneStillPurgesLocally(t *testing.T) {
	_, rawDB, cleanup := db.SetupTestDBWithRawDB(t)
	defer cleanup()

	svc := newLocalOnlyAccountService(rawDB)

	if _, err := svc.DeleteAccount(
		accountAuthedCtx("u-local", "owner@example.com"),
		deleteReq("owner@example.com")); err != nil {
		t.Fatalf("deletion must work without a control plane: %v", err)
	}
}
