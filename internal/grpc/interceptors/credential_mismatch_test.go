package interceptors

import (
	"context"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/orgs"
)

type memberEverywhere struct{}

func (memberEverywhere) IsMember(context.Context, string, string) (bool, bool, error) {
	return true, true, nil
}

// A request whose credential authz refused another org's resource is told
// so, with the way to act there, instead of a bare NotFound; any other
// outcome passes unchanged.
func TestCredentialMismatchNamesTheOrgAndTheFix(t *testing.T) {
	acme := "acme"
	base := authz.WithMembership(context.Background(), memberEverywhere{})
	ctx := authz.WithCredentialOrg(base, "home")
	if authz.CanReadChat(ctx, "alice", &core.Chat{UserID: "alice", OrgID: &acme}) {
		t.Fatal("a credential bound to home read an acme chat")
	}

	err := credentialMismatch(ctx, svcerr.NotFound("chat"))
	if code := svcerr.Code(err); code != connect.CodePermissionDenied {
		t.Fatalf("code %v, want PermissionDenied: %v", code, err)
	}
	var cerr *connect.Error
	if !errors.As(err, &cerr) || cerr.Meta().Get(svcerr.ReasonHeader) != orgs.ReasonOrgMismatch {
		t.Fatalf("want reason %q: %v", orgs.ReasonOrgMismatch, err)
	}
	for _, want := range []string{"home", "acme", "reliant auth login", "forge login --org acme"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}

	if got := credentialMismatch(ctx, svcerr.InvalidArgument("bad")); svcerr.Code(got) != connect.CodeInvalidArgument {
		t.Fatalf("an unrelated error was rewritten: %v", got)
	}
	if got := credentialMismatch(authz.WithCredentialOrg(base, "home"), svcerr.NotFound("chat")); svcerr.Code(got) != connect.CodeNotFound {
		t.Fatalf("a NotFound with no credential refusal was rewritten: %v", got)
	}
	if credentialMismatch(ctx, nil) != nil {
		t.Fatal("success was turned into an error")
	}
}
