package account_test

import (
	"context"
	"database/sql"
	"testing"

	"connectrpc.com/connect"

	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	account "github.com/reliant-labs/reliant/internal/handlers/account"
)

// The counts and the wallet quote are read against a real database in
// account_test.go and account_controlplane_test.go; these rows pin the
// session-only rule that refuses a request before either is read.
func TestPreviewAccountDeletion_Generated(t *testing.T) {
	t.Parallel()

	// Never opened: every row is refused before the preview reads it.
	svc := account.NewTestAccount(t, account.WithAccountDeps(account.Deps{SQL: &sql.DB{}}))

	minted, err := fat.Mint()
	if err != nil {
		t.Fatal(err)
	}
	withAccessToken := connect.NewRequest(&pb.PreviewAccountDeletionRequest{})
	withAccessToken.Header().Set("Authorization", "Bearer "+minted.Plaintext)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.PreviewAccountDeletionRequest, pb.PreviewAccountDeletionResponse]{
		{
			Name:    "no caller is unauthenticated",
			Req:     connect.NewRequest(&pb.PreviewAccountDeletionRequest{}),
			Ctx:     context.Background(),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name:    "an access token is not an interactive session",
			Req:     withAccessToken,
			Ctx:     account.AuthedContext(t),
			WantErr: connect.CodeUnauthenticated,
		},
	}, svc.PreviewAccountDeletion)
}
