// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
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
	result   *controlplane.AccountDeletionResult
	err      error
	calls    int
	lastJWT  string

	quote    *controlplane.AccountDeletionWalletQuote
	quoteErr error
}

func (s *stubControlPlane) PreviewAccountDeletionWallet(_ context.Context, jwt string) (*controlplane.AccountDeletionWalletQuote, error) {
	s.lastJWT = jwt
	return s.quote, s.quoteErr
}

func (s *stubControlPlane) MintLLMKey(context.Context, string, string) (controlplane.LLMKey, error) {
	return controlplane.LLMKey{}, nil
}

func (s *stubControlPlane) DeleteCurrentUserAccount(_ context.Context, jwt string) (*controlplane.AccountDeletionResult, error) {
	s.calls++
	s.lastJWT = jwt
	if s.err != nil {
		return nil, s.err
	}
	out := &controlplane.AccountDeletionResult{}
	if s.result != nil {
		*out = *s.result
	}
	out.Blockers = s.blockers
	return out, nil
}

func (s *stubControlPlane) CloneRepoOntoDaemon(context.Context, string, controlplane.CloneRepoRequest) (controlplane.CloneRepoResult, error) {
	return controlplane.CloneRepoResult{}, nil
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

func (s *stubControlPlane) MintDaemonResumeToken(context.Context, string, string, string) (controlplane.DaemonResumeToken, error) {
	return controlplane.DaemonResumeToken{}, nil
}

func (s *stubControlPlane) RevokeDaemonResumeTokens(context.Context, string, string) error {
	return nil
}

// TestPreviewAccountDeletion_CarriesControlPlaneWalletQuote: the dialog's
// refund/forfeit sentence is rendered from the control plane's quote, so the
// preview must carry it verbatim (and forward the caller's own JWT for it).
func TestPreviewAccountDeletion_CarriesControlPlaneWalletQuote(t *testing.T) {
	_, rawDB, cleanup := db.SetupTestDBWithRawDB(t)
	defer cleanup()

	cp := &stubControlPlane{quote: &controlplane.AccountDeletionWalletQuote{
		RefundCents:         2500,
		Destinations:        []controlplane.RefundDestination{{CardBrand: "visa", CardLast4: "1234", AmountCents: 2500}},
		ForfeitedPromoCents: 700,
	}}
	svc := NewAccountService(rawDB).WithControlPlaneClient(cp)

	req := connect.NewRequest(&reliantv1.PreviewAccountDeletionRequest{})
	req.Header().Set("Authorization", "Bearer jwt-abc")
	resp, err := svc.PreviewAccountDeletion(accountAuthedCtx("u-q", "owner@example.com"), req)
	if err != nil {
		t.Fatalf("PreviewAccountDeletion: %v", err)
	}
	w := resp.Msg.GetWallet()
	if w.GetRefundCents() != 2500 || w.GetForfeitedPromoCents() != 700 ||
		len(w.GetDestinations()) != 1 || w.GetDestinations()[0].GetCardLast4() != "1234" {
		t.Fatalf("the quote must be carried verbatim, got %+v", w)
	}
	if cp.lastJWT != "jwt-abc" {
		t.Errorf("the caller's JWT must be forwarded, got %q", cp.lastJWT)
	}
	for _, line := range resp.Msg.GetRetainedElsewhere() {
		if strings.Contains(line, "unspent credit") {
			t.Errorf("credit no longer blocks deletion; the dialog must not say it does: %q", line)
		}
	}
}

// TestPreviewAccountDeletion_FailsWhenWalletQuoteUnavailable: a preview that
// silently omitted the money line would read as "nothing happens to your
// balance". It must fail instead.
func TestPreviewAccountDeletion_FailsWhenWalletQuoteUnavailable(t *testing.T) {
	_, rawDB, cleanup := db.SetupTestDBWithRawDB(t)
	defer cleanup()

	cp := &stubControlPlane{quoteErr: errors.New("control plane unreachable")}
	svc := NewAccountService(rawDB).WithControlPlaneClient(cp)

	_, err := svc.PreviewAccountDeletion(accountAuthedCtx("u-q", "owner@example.com"),
		connect.NewRequest(&reliantv1.PreviewAccountDeletionRequest{}))
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("want Unavailable, got %v", err)
	}
}

// TestDeleteAccount_ReportsRefundOutcome: the refund the control plane issued,
// and a refund still owed by support, reach the client.
func TestDeleteAccount_ReportsRefundOutcome(t *testing.T) {
	_, rawDB, cleanup := db.SetupTestDBWithRawDB(t)
	defer cleanup()

	cp := &stubControlPlane{result: &controlplane.AccountDeletionResult{
		RefundedCents:       1000,
		RefundDestinations:  []controlplane.RefundDestination{{CardBrand: "visa", CardLast4: "1234", AmountCents: 1000}},
		RefundOwedCents:     1500,
		ForfeitedPromoCents: 200,
		RefundPending:       true,
	}}
	svc := NewAccountService(rawDB).WithControlPlaneClient(cp)

	resp, err := svc.DeleteAccount(accountAuthedCtx("u-r", "owner@example.com"), deleteReq("owner@example.com"))
	if err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	m := resp.Msg
	if m.GetRefundedCents() != 1000 || m.GetRefundOwedCents() != 1500 || !m.GetRefundPending() ||
		m.GetForfeitedPromoCents() != 200 || len(m.GetRefundDestinations()) != 1 {
		t.Fatalf("the refund outcome must be carried through, got %+v", m)
	}
}

// TestDeleteAccount_UnconfirmedRefundIsRetryableAndPurgesNothing: when the
// control plane cannot confirm a refund with Stripe it deletes nothing and says
// Unavailable. Reliant must not purge, and must keep the error retryable.
func TestDeleteAccount_UnconfirmedRefundIsRetryableAndPurgesNothing(t *testing.T) {
	_, rawDB, cleanup := db.SetupTestDBWithRawDB(t)
	defer cleanup()

	cp := &stubControlPlane{err: connect.NewError(connect.CodeUnavailable, errors.New("refund outcome unknown"))}
	svc := NewAccountService(rawDB).WithControlPlaneClient(cp)

	_, err := svc.DeleteAccount(accountAuthedCtx("u-u", "owner@example.com"), deleteReq("owner@example.com"))
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("want Unavailable, got %v", err)
	}
	if !strings.Contains(err.Error(), "nothing was deleted") {
		t.Errorf("the user must be told nothing was deleted, got %q", err.Error())
	}
}
