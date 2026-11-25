// Copyright (c) 2025 Reliant Labs
package account

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/accountpurge"
	"github.com/reliant-labs/reliant/internal/controlplane"
)

// scriptedPurger is a Purger whose Purge returns results[i] on its i-th call
// (the last result repeats), and records whom each call purged. It proves
// the destructive collaborator ran exactly when, and as often as, allowed.
type scriptedPurger struct {
	results []error
	// cancel, when set, runs at the start of each Purge call: a client that
	// goes away while the purge is in flight.
	cancel func()
	calls  []string
}

func (p *scriptedPurger) Preview(context.Context, string) (accountpurge.Counts, error) {
	return accountpurge.Counts{}, nil
}

func (p *scriptedPurger) Purge(_ context.Context, userID string) (int64, error) {
	p.calls = append(p.calls, userID)
	if p.cancel != nil {
		p.cancel()
	}
	if len(p.results) == 0 {
		return 7, nil
	}
	i := min(len(p.calls), len(p.results)) - 1
	if err := p.results[i]; err != nil {
		return 0, err
	}
	return 7, nil
}

func newScriptedAccountService(t *testing.T, purger Purger, cp ControlPlane) *Service {
	t.Helper()
	deps := Deps{Purger: purger}
	if cp != nil {
		deps.ControlPlane = cp
	}
	return NewTestAccount(t, WithAccountDeps(deps))
}

// deletedAccountRefusal is the control plane's deleted-account refusal as the
// controlplane adapter returns it: PermissionDenied with the message and the
// account_deleted reason, wrapped in controlplane.ErrAccountDeletionConfirmed
// only when the control plane confirmed the tombstone.
func deletedAccountRefusal(confirmed bool) error {
	ce := connect.NewError(connect.CodePermissionDenied, errors.New("this account has been deleted"))
	ce.Meta().Set(svcerr.ReasonHeader, "account_deleted")
	if confirmed {
		return fmt.Errorf("%w: %w", controlplane.ErrAccountDeletionConfirmed, ce)
	}
	return ce
}

// badConn is a dead pooled connection as the database layer reports it:
// wrapped, so only errors.Is finds it.
func badConn() error { return fmt.Errorf("account purge: begin: %w", driver.ErrBadConn) }

// TestDeleteAccount_ResumesPurgeOnConfirmedDeletion: a deletion whose
// control-plane half succeeded and whose local purge failed must be
// finishable. The retry finds the platform account confirmed deleted and
// purges the caller's own rows, once.
func TestDeleteAccount_ResumesPurgeOnConfirmedDeletion(t *testing.T) {
	purger := &scriptedPurger{}
	cp := &stubControlPlane{err: deletedAccountRefusal(true)}
	svc := newScriptedAccountService(t, purger, cp)

	resp, err := svc.DeleteAccount(accountAuthedCtx("u-resume", "owner@example.com"), deleteReq("owner@example.com"))
	if err != nil {
		t.Fatalf("a retry after a confirmed platform deletion must finish the purge: %v", err)
	}
	if resp.Msg.GetUserId() != "u-resume" {
		t.Errorf("user id = %q, want u-resume", resp.Msg.GetUserId())
	}
	if len(purger.calls) != 1 || purger.calls[0] != "u-resume" {
		t.Fatalf("purge calls = %v, want exactly [u-resume]", purger.calls)
	}
	if cp.calls != 1 {
		t.Errorf("control plane calls = %d, want 1", cp.calls)
	}
}

// TestDeleteAccount_UnconfirmedDeletedRefusalDoesNotPurge is review blocker
// B1's regression test. control-plane sends the same refusal (code, message
// and reason) when its tombstone LOOKUP failed, without the confirmation.
// Purging on it would wipe local data with no platform deletion and no wallet
// settlement, so it must fail with nothing deleted.
func TestDeleteAccount_UnconfirmedDeletedRefusalDoesNotPurge(t *testing.T) {
	purger := &scriptedPurger{}
	svc := newScriptedAccountService(t, purger, &stubControlPlane{err: deletedAccountRefusal(false)})

	_, err := svc.DeleteAccount(accountAuthedCtx("u-ambiguous", "owner@example.com"), deleteReq("owner@example.com"))
	if err == nil {
		t.Fatal("an unconfirmed deleted-account refusal must fail the deletion")
	}
	if len(purger.calls) != 0 {
		t.Fatalf("an unconfirmed refusal must not purge, got calls %v", purger.calls)
	}
	if !strings.Contains(err.Error(), "nothing was deleted") {
		t.Errorf("the user must be told nothing was deleted, got %q", err.Error())
	}
}

// TestDeleteAccount_ControlPlaneRefusalsNeverPurge: only the confirmed
// deletion reaches the purger. Every other outcome of the control-plane call
// leaves local data alone.
func TestDeleteAccount_ControlPlaneRefusalsNeverPurge(t *testing.T) {
	for name, cp := range map[string]*stubControlPlane{
		"other PermissionDenied": {err: connect.NewError(connect.CodePermissionDenied, errors.New("admin role required"))},
		"unavailable":            {err: connect.NewError(connect.CodeUnavailable, errors.New("refund outcome unknown"))},
		"transport error":        {err: errors.New("dial tcp: connection refused")},
		"blocker": {blockers: []controlplane.AccountDeletionBlocker{{
			Reason: "paid_subscription", Detail: "Cancel it in Billing first.",
		}}},
	} {
		t.Run(name, func(t *testing.T) {
			purger := &scriptedPurger{}
			svc := newScriptedAccountService(t, purger, cp)

			if _, err := svc.DeleteAccount(accountAuthedCtx("u-refused", "owner@example.com"), deleteReq("owner@example.com")); err == nil {
				t.Fatal("a refused control-plane call must fail the deletion")
			}
			if len(purger.calls) != 0 {
				t.Fatalf("a refused control-plane call must not purge, got calls %v", purger.calls)
			}
		})
	}
}

// TestDeleteAccount_PurgeRetriesADeadConnection: a pooled connection that
// died under the purge (driver.ErrBadConn, wrapped) is retried, and the
// retry succeeds. The platform account is deleted once, not once per try.
func TestDeleteAccount_PurgeRetriesADeadConnection(t *testing.T) {
	purger := &scriptedPurger{results: []error{badConn(), badConn(), nil}}
	cp := &stubControlPlane{}
	svc := newScriptedAccountService(t, purger, cp)

	resp, err := svc.DeleteAccount(accountAuthedCtx("u-retry", "owner@example.com"), deleteReq("owner@example.com"))
	if err != nil {
		t.Fatalf("the third attempt succeeds, so the deletion must: %v", err)
	}
	if len(purger.calls) != 3 {
		t.Fatalf("purge calls = %d, want 3", len(purger.calls))
	}
	for _, id := range purger.calls {
		if id != "u-retry" {
			t.Fatalf("every attempt must purge the caller, got %v", purger.calls)
		}
	}
	if cp.calls != 1 {
		t.Errorf("control plane calls = %d, want exactly 1", cp.calls)
	}
	if resp.Msg.GetUserId() != "u-retry" {
		t.Errorf("user id = %q, want u-retry", resp.Msg.GetUserId())
	}
}

// TestDeleteAccount_PurgeRetriesStopAtThree: a connection that keeps dying
// fails the deletion after purgeAttempts tries, telling the user their
// platform account is gone and to retry, never that nothing was deleted.
func TestDeleteAccount_PurgeRetriesStopAtThree(t *testing.T) {
	purger := &scriptedPurger{results: []error{badConn()}}
	cp := &stubControlPlane{}
	svc := newScriptedAccountService(t, purger, cp)

	_, err := svc.DeleteAccount(accountAuthedCtx("u-dead", "owner@example.com"), deleteReq("owner@example.com"))
	if err == nil {
		t.Fatal("a purge that never succeeds must fail the deletion")
	}
	if len(purger.calls) != purgeAttempts {
		t.Fatalf("purge calls = %d, want %d", len(purger.calls), purgeAttempts)
	}
	if cp.calls != 1 {
		t.Errorf("control plane calls = %d, want exactly 1", cp.calls)
	}
	assertPartialDeletionMessage(t, err)
}

// TestDeleteAccount_PurgeDoesNotRetryOtherErrors: only a dead connection is
// retried. Any other purge error fails at once.
func TestDeleteAccount_PurgeDoesNotRetryOtherErrors(t *testing.T) {
	purger := &scriptedPurger{results: []error{errors.New("constraint violation")}}
	svc := newScriptedAccountService(t, purger, &stubControlPlane{})

	_, err := svc.DeleteAccount(accountAuthedCtx("u-other", "owner@example.com"), deleteReq("owner@example.com"))
	if err == nil {
		t.Fatal("a failed purge must fail the deletion")
	}
	if len(purger.calls) != 1 {
		t.Fatalf("purge calls = %d, want 1 (no retry)", len(purger.calls))
	}
	assertPartialDeletionMessage(t, err)
}

// TestDeleteAccount_PurgeDoesNotRetryAfterCancellation: once the caller's
// context is done, a dead connection is not retried.
func TestDeleteAccount_PurgeDoesNotRetryAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(accountAuthedCtx("u-gone", "owner@example.com"))
	defer cancel()
	purger := &scriptedPurger{results: []error{badConn()}, cancel: cancel}
	svc := newScriptedAccountService(t, purger, &stubControlPlane{})

	if _, err := svc.DeleteAccount(ctx, deleteReq("owner@example.com")); err == nil {
		t.Fatal("a failed purge must fail the deletion")
	}
	if len(purger.calls) != 1 {
		t.Fatalf("purge calls = %d, want 1 (no retry after cancellation)", len(purger.calls))
	}
}

// TestDeleteAccount_PurgeFailureAfterResumeSaysPlatformDeleted: a resumed
// purge that fails again must still say the platform account is gone, so the
// user retries rather than believing nothing happened.
func TestDeleteAccount_PurgeFailureAfterResumeSaysPlatformDeleted(t *testing.T) {
	purger := &scriptedPurger{results: []error{errors.New("purge failed")}}
	svc := newScriptedAccountService(t, purger, &stubControlPlane{err: deletedAccountRefusal(true)})

	_, err := svc.DeleteAccount(accountAuthedCtx("u-again", "owner@example.com"), deleteReq("owner@example.com"))
	if err == nil {
		t.Fatal("a failed purge must fail the deletion")
	}
	assertPartialDeletionMessage(t, err)
}

// TestDeleteAccount_PurgeFailureWithoutPlatformSaysNothingDeleted: with no
// control plane (self-hosted) nothing outside this database was touched, so
// a failed purge says nothing was deleted.
func TestDeleteAccount_PurgeFailureWithoutPlatformSaysNothingDeleted(t *testing.T) {
	purger := &scriptedPurger{results: []error{errors.New("purge failed")}}
	svc := newScriptedAccountService(t, purger, nil)

	_, err := svc.DeleteAccount(accountAuthedCtx("u-local", "owner@example.com"), deleteReq("owner@example.com"))
	if err == nil {
		t.Fatal("a failed purge must fail the deletion")
	}
	if !strings.Contains(err.Error(), "nothing was deleted") {
		t.Errorf("want the nothing-was-deleted message, got %q", err.Error())
	}
}

func assertPartialDeletionMessage(t *testing.T, err error) {
	t.Helper()
	msg := err.Error()
	if strings.Contains(msg, "nothing was deleted") {
		t.Errorf("the platform account is gone; the user must not be told nothing was deleted: %q", msg)
	}
	if !strings.Contains(msg, "your platform account was deleted") || !strings.Contains(msg, "try again") {
		t.Errorf("the user must be told the platform account is gone and to retry, got %q", msg)
	}
}

// TestPreviewAccountDeletion_LoadsOnConfirmedDeletion: the dialog is the only
// way to retry a half-finished deletion, so its preview must load (with no
// wallet left to quote) once the platform deletion is confirmed.
func TestPreviewAccountDeletion_LoadsOnConfirmedDeletion(t *testing.T) {
	svc := newScriptedAccountService(t, &scriptedPurger{}, &stubControlPlane{quoteErr: deletedAccountRefusal(true)})

	resp, err := svc.PreviewAccountDeletion(accountAuthedCtx("u-q", "owner@example.com"),
		connect.NewRequest(&reliantv1.PreviewAccountDeletionRequest{}))
	if err != nil {
		t.Fatalf("PreviewAccountDeletion: %v", err)
	}
	if resp.Msg.GetWallet() != nil {
		t.Errorf("a deleted platform account has no wallet to quote, got %+v", resp.Msg.GetWallet())
	}
}

// TestPreviewAccountDeletion_UnconfirmedRefusalStillFails: an unconfirmed
// refusal must not hide the wallet. The account may still hold an unsettled
// balance, and a preview without the money line reads as "nothing happens to
// your balance".
func TestPreviewAccountDeletion_UnconfirmedRefusalStillFails(t *testing.T) {
	svc := newScriptedAccountService(t, &scriptedPurger{}, &stubControlPlane{quoteErr: deletedAccountRefusal(false)})

	_, err := svc.PreviewAccountDeletion(accountAuthedCtx("u-q", "owner@example.com"),
		connect.NewRequest(&reliantv1.PreviewAccountDeletionRequest{}))
	if svcerr.Code(err) != connect.CodeUnavailable {
		t.Fatalf("want Unavailable, got %v", err)
	}
}
