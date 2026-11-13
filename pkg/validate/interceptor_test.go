package validate

import (
	"context"
	"errors"
	"testing"

	"buf.build/go/protovalidate"
	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

// fakeValidator lets the interceptor wiring be tested without a compiled
// proto carrying rules — the real rule enforcement is protovalidate's own
// concern (and is exercised end-to-end by the scaffold e2e).
type fakeValidator struct{ err error }

func (f fakeValidator) Validate(proto.Message, ...protovalidate.ValidationOption) error {
	return f.err
}

func TestInterceptor_WrapUnary_RejectsInvalid(t *testing.T) {
	i := &interceptor{v: fakeValidator{err: errors.New("amount_cents: must be >= 0")}}
	called := false
	next := connect.UnaryFunc(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		called = true
		return nil, nil
	})
	_, err := i.WrapUnary(next)(context.Background(), connect.NewRequest(&emptypb.Empty{}))
	if err == nil {
		t.Fatal("expected an error for an invalid request")
	}
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", got)
	}
	if called {
		t.Error("handler must NOT run when validation fails")
	}
}

func TestInterceptor_WrapUnary_PassesValid(t *testing.T) {
	i := &interceptor{v: fakeValidator{err: nil}}
	called := false
	next := connect.UnaryFunc(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		called = true
		return connect.NewResponse(&emptypb.Empty{}), nil
	})
	if _, err := i.WrapUnary(next)(context.Background(), connect.NewRequest(&emptypb.Empty{})); err != nil {
		t.Fatalf("valid request should pass: %v", err)
	}
	if !called {
		t.Error("handler must run when validation passes")
	}
}

func TestInterceptor_Constructs(t *testing.T) {
	if _, err := Interceptor(); err != nil {
		t.Fatalf("Interceptor() failed to build a protovalidate validator: %v", err)
	}
}
