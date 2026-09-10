// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
)

// accountAuthedCtx builds a context shaped like the one the auth interceptor
// installs after validating a Supabase JWT. Named for this file because the
// package already has an unrelated authedCtx helper.
func accountAuthedCtx(userID, email string) context.Context {
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)
	return context.WithValue(ctx, auth.UserEmailContextKey, email)
}

func deleteReq(confirmEmail string) *connect.Request[reliantv1.DeleteAccountRequest] {
	return connect.NewRequest(&reliantv1.DeleteAccountRequest{ConfirmEmail: confirmEmail})
}

// TestDeleteAccount_ConfirmEmailMismatch: the typed confirmation is enforced
// server-side, so a client that skips its own dialog cannot delete by
// accident. Nothing must be deleted on a mismatch.
func TestDeleteAccount_ConfirmEmailMismatch(t *testing.T) {
	repo, rawDB, cleanup := db.SetupTestDBWithRawDB(t)
	defer cleanup()
	_ = repo

	svc := newLocalOnlyAccountService(rawDB)

	_, err := svc.DeleteAccount(accountAuthedCtx("u1", "owner@example.com"), deleteReq("someone@else.com"))
	if err == nil {
		t.Fatal("DeleteAccount accepted a mismatched confirmation")
	}
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", got)
	}
}

// TestDeleteAccount_ConfirmEmailIsCaseAndSpaceInsensitive: a user who types
// their address with different capitalisation, or whose paste carries a
// trailing space, must not be blocked from an action they clearly intended.
func TestDeleteAccount_ConfirmEmailIsCaseAndSpaceInsensitive(t *testing.T) {
	_, rawDB, cleanup := db.SetupTestDBWithRawDB(t)
	defer cleanup()

	svc := newLocalOnlyAccountService(rawDB)

	resp, err := svc.DeleteAccount(
		accountAuthedCtx("u-case", "Owner@Example.com"),
		deleteReq("  owner@example.COM  "))
	if err != nil {
		t.Fatalf("DeleteAccount rejected an equivalent email: %v", err)
	}
	if resp.Msg.GetDeletedRowCount() != 0 {
		t.Errorf("deleted %d rows for an empty account, want 0", resp.Msg.GetDeletedRowCount())
	}
}

// TestDeleteAccount_AnonymousSessionNeedsNoConfirmation: an un-upgraded
// anonymous user has no email claim to type. Requiring one would strand
// exactly the half-upgraded accounts this feature exists to rescue.
func TestDeleteAccount_AnonymousSessionNeedsNoConfirmation(t *testing.T) {
	_, rawDB, cleanup := db.SetupTestDBWithRawDB(t)
	defer cleanup()

	svc := newLocalOnlyAccountService(rawDB)

	if _, err := svc.DeleteAccount(accountAuthedCtx("anon-user", ""), deleteReq("")); err != nil {
		t.Fatalf("DeleteAccount rejected an anonymous session: %v", err)
	}
}

// TestDeleteAccount_RequiresAuth: no identity, no deletion.
func TestDeleteAccount_RequiresAuth(t *testing.T) {
	_, rawDB, cleanup := db.SetupTestDBWithRawDB(t)
	defer cleanup()

	svc := newLocalOnlyAccountService(rawDB)

	_, err := svc.DeleteAccount(context.Background(), deleteReq("owner@example.com"))
	if err == nil {
		t.Fatal("DeleteAccount succeeded with no authenticated user")
	}
	if got := connect.CodeOf(err); got != connect.CodeUnauthenticated {
		t.Errorf("code = %v, want Unauthenticated", got)
	}
}

// TestPreviewAccountDeletion_ReportsScopeBoundary: the dialog must be able to
// state what survives. An empty list here would let the UI imply a
// completeness the RPC does not deliver.
func TestPreviewAccountDeletion_ReportsScopeBoundary(t *testing.T) {
	_, rawDB, cleanup := db.SetupTestDBWithRawDB(t)
	defer cleanup()

	svc := newLocalOnlyAccountService(rawDB)

	resp, err := svc.PreviewAccountDeletion(
		accountAuthedCtx("u-preview", "owner@example.com"),
		connect.NewRequest(&reliantv1.PreviewAccountDeletionRequest{}))
	if err != nil {
		t.Fatalf("PreviewAccountDeletion: %v", err)
	}
	if len(resp.Msg.GetRetainedElsewhere()) == 0 {
		t.Error("retained_elsewhere is empty; the UI cannot state what survives")
	}
	if resp.Msg.GetConfirmEmail() != "owner@example.com" {
		t.Errorf("confirm_email = %q, want the caller's email",
			resp.Msg.GetConfirmEmail())
	}
}
