package crud

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// Entity and Pack are OVERRIDE seams: the generated shim is the default, and an
// application that scopes CRUD to an owner replaces them to reject rows the
// caller may not touch. Those rejections are bare svcerr sentinels, so this
// package must read them as the verdicts they are.
//
// The regression these pin: mapPackErr once matched *connect.Error alone, which
// meant an svcerr sentinel from Entity became CodeInternal while the identical
// value from Persist became a 404 through mapRepoErr. One error type, two
// answers, depending only on which closure returned it.
func TestOverrideSeams_PreserveClassifiedCodes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want connect.Code
	}{
		{"not found", svcerr.NotFound("user"), connect.CodeNotFound},
		{"invalid argument", svcerr.InvalidArgument("bad reference"), connect.CodeInvalidArgument},
		{"permission denied", svcerr.PermissionDenied("not yours"), connect.CodePermissionDenied},
		{"unauthenticated", svcerr.Unauthenticated("no principal"), connect.CodeUnauthenticated},
		{"failed precondition", svcerr.FailedPrecondition("wrong state"), connect.CodeFailedPrecondition},
		{"already exists", svcerr.AlreadyExists("duplicate"), connect.CodeAlreadyExists},
	}

	for _, tc := range cases {
		t.Run("entity/"+tc.name, func(t *testing.T) {
			h := HandleCreate(CreateOp[createReq, createResp, *user]{
				EntityLower: "user",
				Entity:      func(context.Context, *createReq) (*user, error) { return nil, tc.err },
				Persist:     func(context.Context, *user) error { return nil },
				Pack:        func(*user) (*createResp, error) { return &createResp{}, nil },
			})
			_, err := h(context.Background(), connect.NewRequest(&createReq{}))
			if got := connect.CodeOf(err); got != tc.want {
				t.Errorf("Entity returned %v: code = %v, want %v", tc.err, got, tc.want)
			}
		})

		t.Run("pack/"+tc.name, func(t *testing.T) {
			h := HandleCreate(CreateOp[createReq, createResp, *user]{
				EntityLower: "user",
				Entity:      func(context.Context, *createReq) (*user, error) { return &user{}, nil },
				Persist:     func(context.Context, *user) error { return nil },
				Pack:        func(*user) (*createResp, error) { return nil, tc.err },
			})
			_, err := h(context.Background(), connect.NewRequest(&createReq{}))
			if got := connect.CodeOf(err); got != tc.want {
				t.Errorf("Pack returned %v: code = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// The message a classified error carries is part of the application's API — it
// was written on this side of the wire — so it must survive, unlike the driver
// prose an unclassified error carries.
func TestOverrideSeams_PreserveClassifiedMessage(t *testing.T) {
	const msg = "customer_id does not name a customer of this company"
	h := HandleCreate(CreateOp[createReq, createResp, *user]{
		EntityLower: "user",
		Entity:      func(context.Context, *createReq) (*user, error) { return nil, svcerr.InvalidArgument(msg) },
		Persist:     func(context.Context, *user) error { return nil },
		Pack:        func(*user) (*createResp, error) { return &createResp{}, nil },
	})
	_, err := h(context.Background(), connect.NewRequest(&createReq{}))

	cerr := new(connect.Error)
	if !errors.As(err, &cerr) {
		t.Fatalf("want *connect.Error, got %T", err)
	}
	if cerr.Message() != msg {
		t.Errorf("message = %q, want %q", cerr.Message(), msg)
	}
}

// The redaction guarantee is unchanged for errors nobody classified: a driver
// error reaching a seam is still a server fault with its prose withheld.
// Widening the pass-through must not widen what leaks.
func TestOverrideSeams_UnclassifiedStillRedacted(t *testing.T) {
	raw := errors.New(`pq: relation "users" does not exist dsn=postgres://app:s3cr3t@db/prod`)
	h := HandleCreate(CreateOp[createReq, createResp, *user]{
		EntityLower: "user",
		Entity:      func(context.Context, *createReq) (*user, error) { return nil, raw },
		Persist:     func(context.Context, *user) error { return nil },
		Pack:        func(*user) (*createResp, error) { return &createResp{}, nil },
	})
	_, err := h(context.Background(), connect.NewRequest(&createReq{}))

	if got := connect.CodeOf(err); got != connect.CodeInternal {
		t.Errorf("code = %v, want Internal", got)
	}
	cerr := new(connect.Error)
	if !errors.As(err, &cerr) {
		t.Fatalf("want *connect.Error, got %T", err)
	}
	for _, leak := range []string{"s3cr3t", "relation", "dsn="} {
		if contains(cerr.Message(), leak) {
			t.Errorf("client message leaks %q: %q", leak, cerr.Message())
		}
	}
	// The original must stay reachable server-side: sanitize the wire, never
	// the log.
	if !errors.Is(err, raw) {
		t.Error("original error no longer reachable via errors.Is — the cause was dropped")
	}
}

// An sentinel's own reason must not be overwritten by this package's default,
// and an unreasoned one must still get a reason so the vocabulary stays total.
func TestOverrideSeams_ReasonHandling(t *testing.T) {
	t.Run("application reason survives", func(t *testing.T) {
		err := svcerr.WithReason(svcerr.FailedPrecondition("no active subscription"), "no_subscription")
		h := HandleCreate(CreateOp[createReq, createResp, *user]{
			EntityLower: "user",
			Entity:      func(context.Context, *createReq) (*user, error) { return nil, err },
			Persist:     func(context.Context, *user) error { return nil },
			Pack:        func(*user) (*createResp, error) { return &createResp{}, nil },
		})
		_, got := h(context.Background(), connect.NewRequest(&createReq{}))
		cerr := new(connect.Error)
		if !errors.As(got, &cerr) {
			t.Fatalf("want *connect.Error, got %T", got)
		}
		if r := cerr.Meta().Get(svcerr.ReasonHeader); r != "no_subscription" {
			t.Errorf("reason = %q, want %q", r, "no_subscription")
		}
	})

	t.Run("unreasoned gets one", func(t *testing.T) {
		h := HandleCreate(CreateOp[createReq, createResp, *user]{
			EntityLower: "user",
			Entity:      func(context.Context, *createReq) (*user, error) { return nil, svcerr.NotFound("user") },
			Persist:     func(context.Context, *user) error { return nil },
			Pack:        func(*user) (*createResp, error) { return &createResp{}, nil },
		})
		_, got := h(context.Background(), connect.NewRequest(&createReq{}))
		cerr := new(connect.Error)
		if !errors.As(got, &cerr) {
			t.Fatalf("want *connect.Error, got %T", got)
		}
		if r := cerr.Meta().Get(svcerr.ReasonHeader); r == "" {
			t.Error("no reason stamped: the reason vocabulary must stay total")
		}
	})
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		func() bool {
			for i := 0; i+len(needle) <= len(haystack); i++ {
				if haystack[i:i+len(needle)] == needle {
					return true
				}
			}
			return false
		}()
}
