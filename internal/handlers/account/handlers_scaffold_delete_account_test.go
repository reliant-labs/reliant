package account_test

import (
	"context"
	"database/sql"
	"testing"

	"connectrpc.com/connect"

	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/tdd"
	"github.com/reliant-labs/forge/pkg/testkit"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	account "github.com/reliant-labs/reliant/internal/handlers/account"
)

// The purge itself, and its ordering against the control plane, run against a
// real database in account_test.go and account_controlplane_test.go; these
// rows pin the checks that refuse a request before either is touched.
func TestDeleteAccount_Generated(t *testing.T) {
	t.Parallel()

	// Never opened: every row is refused before the purge reaches it.
	svc := account.NewTestAccount(t, account.WithAccountDeps(account.Deps{SQL: &sql.DB{}}))

	minted, err := fat.Mint()
	if err != nil {
		t.Fatal(err)
	}
	withAccessToken := connect.NewRequest(&pb.DeleteAccountRequest{ConfirmEmail: "owner@example.com"})
	withAccessToken.Header().Set("Authorization", "Bearer "+minted.Plaintext)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.DeleteAccountRequest, pb.DeleteAccountResponse]{
		{
			Name:    "no caller is unauthenticated",
			Req:     connect.NewRequest(&pb.DeleteAccountRequest{ConfirmEmail: "owner@example.com"}),
			Ctx:     context.Background(),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name:    "an access token cannot delete its owner's account",
			Req:     withAccessToken,
			Ctx:     account.AuthedContext(t, testkit.WithEmail("owner@example.com")),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name:    "confirmation must match the caller's email",
			Req:     connect.NewRequest(&pb.DeleteAccountRequest{ConfirmEmail: "someone@else.com"}),
			Ctx:     account.AuthedContext(t, testkit.WithEmail("owner@example.com")),
			WantErr: connect.CodeInvalidArgument,
		},
	}, svc.DeleteAccount)
}
