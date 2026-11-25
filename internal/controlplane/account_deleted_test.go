package controlplane

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	userv1 "github.com/reliant-labs/reliant/gen/controlplane/services/user/v1"
	userv1connect "github.com/reliant-labs/reliant/gen/controlplane/services/user/v1/controlplanev1connect"
	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
)

// deletedAccountRefusal is the control plane's deleted-account refusal:
// PermissionDenied, the message, the account_deleted reason, and, only when
// the control plane verified the tombstone, the AccountDeletionConfirmed
// detail.
func deletedAccountRefusal(t *testing.T, confirmed bool) *connect.Error {
	t.Helper()
	ce := connect.NewError(connect.CodePermissionDenied, errors.New("this account has been deleted"))
	ce.Meta().Set(svcerr.ReasonHeader, "account_deleted")
	if confirmed {
		d, err := connect.NewErrorDetail(&userv1.AccountDeletionConfirmed{})
		if err != nil {
			t.Fatal(err)
		}
		ce.AddDetail(d)
	}
	return ce
}

// refusingUserService answers both account-deletion rpcs with refusal.
func refusingUserService(t *testing.T, refusal *connect.Error) Service {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(userv1connect.UserServicePreviewAccountDeletionProcedure, connect.NewUnaryHandler(
		userv1connect.UserServicePreviewAccountDeletionProcedure,
		func(context.Context, *connect.Request[userv1.PreviewAccountDeletionRequest]) (*connect.Response[userv1.PreviewAccountDeletionResponse], error) {
			return nil, refusal
		}))
	mux.Handle(userv1connect.UserServiceDeleteCurrentUserAccountProcedure, connect.NewUnaryHandler(
		userv1connect.UserServiceDeleteCurrentUserAccountProcedure,
		func(context.Context, *connect.Request[userv1.DeleteCurrentUserAccountRequest]) (*connect.Response[userv1.DeleteCurrentUserAccountResponse], error) {
			return nil, refusal
		}))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return New(Deps{ControlPlane: &configv1.ControlPlaneConfig{Url: server.URL}})
}

// TestAccountDeletionConfirmed_OnlyWithTheDetail pins what reliant's
// DeleteAccount resumes its local purge on: the AccountDeletionConfirmed
// detail, decoded off the wire through the real client stack. The same
// refusal without it (the control plane's tombstone lookup failed) must not
// read as a confirmed deletion, and either way the remote error stays
// classifiable by its code.
func TestAccountDeletionConfirmed_OnlyWithTheDetail(t *testing.T) {
	for _, tc := range []struct {
		name      string
		confirmed bool
	}{
		{"verified tombstone", true},
		{"unverified refusal", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cp := refusingUserService(t, deletedAccountRefusal(t, tc.confirmed))
			ctx := context.Background()

			_, delErr := cp.DeleteCurrentUserAccount(ctx, "jwt")
			_, previewErr := cp.PreviewAccountDeletionWallet(ctx, "jwt")
			for rpc, err := range map[string]error{"DeleteCurrentUserAccount": delErr, "PreviewAccountDeletionWallet": previewErr} {
				if err == nil {
					t.Fatalf("%s: want the refusal, got nil", rpc)
				}
				if got := errors.Is(err, ErrAccountDeletionConfirmed); got != tc.confirmed {
					t.Errorf("%s: errors.Is(ErrAccountDeletionConfirmed) = %v, want %v (err %v)", rpc, got, tc.confirmed, err)
				}
				if got := connect.CodeOf(err); got != connect.CodePermissionDenied {
					t.Errorf("%s: code = %v, want PermissionDenied", rpc, got)
				}
			}
		})
	}
}

// TestClassifyAccountDeleted_LeavesOtherErrorsAlone: a non-Connect error, and
// a Connect error with an unrelated detail, are returned unchanged.
func TestClassifyAccountDeleted_LeavesOtherErrorsAlone(t *testing.T) {
	plain := errors.New("dial tcp: connection refused")
	if got := classifyAccountDeleted(plain); got != plain {
		t.Errorf("a non-Connect error must pass through, got %v", got)
	}
	ce := connect.NewError(connect.CodePermissionDenied, errors.New("admin role required"))
	d, err := connect.NewErrorDetail(&userv1.AccountDeletionBlocker{Reason: "paid_subscription"})
	if err != nil {
		t.Fatal(err)
	}
	ce.AddDetail(d)
	if got := classifyAccountDeleted(ce); errors.Is(got, ErrAccountDeletionConfirmed) {
		t.Errorf("an unrelated detail must not confirm a deletion, got %v", got)
	}
}
