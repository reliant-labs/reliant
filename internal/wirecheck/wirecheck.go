// Copyright (c) 2025 Reliant Labs

// Package wirecheck answers one question for tests: what does a CLIENT see
// when a handler returns this error?
//
// An error's reason (svcerr.ReasonHeader) and its other metadata exist only on
// the wire. In process they are wrappers that svcerr.ToConnect materializes,
// and a test that inspects the wrapper proves nothing about the response the
// web actually reads. So RoundTrip serves the error from a real Connect
// handler, behind svcerr.InterceptorV2 as every reliant server is, and returns what
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

	"connectrpc.com/connect/v2"
	"github.com/reliant-labs/forge/pkg/mountkit"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/reliant-labs/reliant/internal/rpcclient"
)

var spec = connect.Spec{StreamType: connect.StreamTypeUnary, Procedure: "/wirecheck.v1.Wirecheck/Fail"}

// RoundTrip returns the *connect.Error a real client receives when a unary
// handler returns err. It fails the test if the call succeeded or the client
// got something other than a Connect error. A v2 *connect.Error carries no
// metadata: RoundTripMeta also returns the response metadata the client got.
func RoundTrip(t testing.TB, err error) *connect.Error {
	t.Helper()
	ce, _ := RoundTripMeta(t, err)
	return ce
}

// RoundTripMeta is RoundTrip plus the metadata (response headers and
// trailers) the client received with the error, as a v1 *connect.Error's
// Meta() held it: read the reason with meta.Get(svcerr.ReasonHeader). It is
// never nil.
func RoundTripMeta(t testing.TB, err error) (*connect.Error, http.Header) {
	t.Helper()
	callErr := ClientError(t, err)
	var ce *connect.Error
	if !errors.As(callErr, &ce) {
		t.Fatalf("want a Connect error over the wire, got %v", callErr)
	}
	meta := svcerr.MetaOf(callErr)
	if meta == nil {
		meta = http.Header{}
	}
	return ce, meta
}

// ClientError returns the error a real client's call returned when a unary
// handler returns err, with the response metadata attached the way every
// rpcclient client attaches it (svcerr.MetaOf reads it). Use
// it to hand code under test an error exactly as it arrives off the wire. It
// fails the test if the call succeeded.
func ClientError(t testing.TB, err error) error {
	t.Helper()
	server := connect.NewServer(svcerr.InterceptorV2())
	server.Register(connect.Method{
		Spec: spec,
		Handler: func(_ context.Context, _ connect.Spec, stream connect.ServerStream) error {
			if recvErr := stream.Receive(&emptypb.Empty{}); recvErr != nil {
				return recvErr
			}
			return err
		},
	})
	mux := http.NewServeMux()
	mountkit.MountV2(mux, server)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	callErr := rpcclient.New(srv.Client(), srv.URL, nil).CallUnary(t.Context(), spec, &emptypb.Empty{}, &emptypb.Empty{})
	if callErr == nil {
		t.Fatalf("the call succeeded; want the handler's error %v", err)
	}
	return callErr
}

// Want is what a client must see.
type Want struct {
	Code    connect.Code
	Message string
	Reason  string
	// Meta is any further metadata the error must carry (scopes, say).
	Meta map[string]string
}

// retiredReasonHeader is the header a reason was sent under before
// svcerr.ReasonHeader. R3w2 retired it; no error may carry it.
const retiredReasonHeader = "X-Reliant-Reason"

// Assert round-trips err and checks the client saw exactly want. The message
// is compared byte for byte: desktop builds already in the field still match
// on it, so a reason must never come with rewritten copy.
func Assert(t testing.TB, err error, want Want) {
	t.Helper()
	ce, meta := RoundTripMeta(t, err)
	if ce.Code() != want.Code {
		t.Errorf("code = %v, want %v", ce.Code(), want.Code)
	}
	if ce.Message() != want.Message {
		t.Errorf("message = %q, want %q (byte-identical)", ce.Message(), want.Message)
	}
	if got := meta.Get(svcerr.ReasonHeader); got != want.Reason {
		t.Errorf("%s = %q, want %q", svcerr.ReasonHeader, got, want.Reason)
	}
	if got := meta.Get(retiredReasonHeader); got != "" {
		t.Errorf("%s = %q, want it absent (retired)", retiredReasonHeader, got)
	}
	for k, v := range want.Meta {
		if got := meta.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}
