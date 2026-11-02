package authn

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/auth"
)

// claimsReader is what serverkit.AuthFor looks for on an interceptor to learn
// how to read the caller it admitted.
type claimsReader interface {
	Claims() func(context.Context) (*Claims, bool)
}

// The interceptor names its claims reader only when it uses the library's
// own stash: a project-owned Policy.ContextWithClaims has a reader only the
// project knows, and passthrough stashes nothing. Naming a reader that cannot
// see the claims is how a chain records every caller as anonymous.
func TestInterceptor_ClaimsReaderMatchesItsStash(t *testing.T) {
	validate := func(string) (*auth.Claims, error) { return &auth.Claims{UserID: "u1"}, nil }

	defaultStash := validatePolicy(validate)
	defaultStash.ContextWithClaims = nil
	ownStash := validatePolicy(validate)

	for name, tc := range map[string]struct {
		policy     Policy
		wantReader bool
	}{
		"default stash":     {defaultStash, true},
		"project's stash":   {ownStash, false},
		"external auth":     {Policy{ExternalAuth: true}, false},
		"anonymous allowed": {func() Policy { p := defaultStash; p.AnonymousOK = true; return p }(), true},
	} {
		t.Run(name, func(t *testing.T) {
			ic, err := NewInterceptor(tc.policy)
			if err != nil {
				t.Fatal(err)
			}
			r, ok := ic.(claimsReader)
			if !ok {
				t.Fatalf("%T has no Claims method: serverkit.AuthFor could not tell how to read its caller", ic)
			}
			if got := r.Claims() != nil; got != tc.wantReader {
				t.Fatalf("Claims() != nil is %v, want %v", got, tc.wantReader)
			}
		})
	}

	// The reader it names reads what it stashed.
	ic, err := NewInterceptor(defaultStash)
	if err != nil {
		t.Fatal(err)
	}
	read := ic.(claimsReader).Claims()
	var got *Claims
	handler := ic.WrapUnary(func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		got, _ = read(ctx)
		return nil, nil
	})
	req := connect.NewRequest(&struct{}{})
	req.Header().Set("Authorization", "Bearer t")
	if _, err := handler(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got == nil || got.UserID != "u1" {
		t.Fatalf("the named reader read %+v, want the admitted caller u1", got)
	}
}
