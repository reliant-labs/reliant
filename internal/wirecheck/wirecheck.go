// Copyright (c) 2025 Reliant Labs

// Package wirecheck answers one question for tests: what does a CLIENT see
// when a handler returns this error?
//
// An error's reason (svcerr.ReasonHeader) and its other metadata exist only on
// the wire. In process they are wrappers that svcerr.ToConnect materializes,
// and a test that inspects the wrapper proves nothing about the response the
// web actually reads. So RoundTrip serves the error from a real Connect
// handler, behind svcerr.Interceptor as every reliant server is, and returns what
// a real Connect client received: the code, the message and the response
// metadata.
//
//forge:exclude-contract: a test helper over connect and httptest; it has no dependencies to seam
package wirecheck

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"google.golang.org/protobuf/types/known/emptypb"
)

const procedure = "/wirecheck.v1.Wirecheck/Fail"

// RoundTrip returns the *connect.Error a real client receives when a unary
// handler returns err. It fails the test if the call succeeded or the client
// got something other than a Connect error.
func RoundTrip(t testing.TB, err error) *connect.Error {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(procedure, connect.NewUnaryHandler(procedure,
		func(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
			return nil, err
		}, connect.WithInterceptors(svcerr.Interceptor())))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := connect.NewClient[emptypb.Empty, emptypb.Empty](srv.Client(), srv.URL+procedure)
	_, callErr := client.CallUnary(t.Context(), connect.NewRequest(&emptypb.Empty{}))
	var ce *connect.Error
	if !errors.As(callErr, &ce) {
		t.Fatalf("want a Connect error over the wire, got %v", callErr)
	}
	return ce
}

// Want is what a client must see. Legacy, when set, is the value expected
// under the pre-svcerr X-Reliant-Reason header (expand/contract, R3w2 drops it;
// in reliant only forge_api_disabled ever sent it).
type Want struct {
	Code    connect.Code
	Message string
	Reason  string
	Legacy  string
	// Meta is any further metadata the error must carry (scopes, say).
	Meta map[string]string
}

// LegacyReasonHeader is the header a reason was sent under before
// svcerr.ReasonHeader, and still is beside it until R3w2.
const LegacyReasonHeader = "X-Reliant-Reason"

// Assert round-trips err and checks the client saw exactly want. The message
// is compared byte for byte: desktop builds already in the field still match
// on it, so a reason must never come with rewritten copy.
func Assert(t testing.TB, err error, want Want) {
	t.Helper()
	ce := RoundTrip(t, err)
	if ce.Code() != want.Code {
		t.Errorf("code = %v, want %v", ce.Code(), want.Code)
	}
	if ce.Message() != want.Message {
		t.Errorf("message = %q, want %q (byte-identical)", ce.Message(), want.Message)
	}
	if got := ce.Meta().Get(svcerr.ReasonHeader); got != want.Reason {
		t.Errorf("%s = %q, want %q", svcerr.ReasonHeader, got, want.Reason)
	}
	if got := ce.Meta().Get(LegacyReasonHeader); got != want.Legacy {
		t.Errorf("%s = %q, want %q", LegacyReasonHeader, got, want.Legacy)
	}
	for k, v := range want.Meta {
		if got := ce.Meta().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}
