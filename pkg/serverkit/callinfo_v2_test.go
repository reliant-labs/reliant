package serverkit_test

import (
	"context"
	"testing"

	connectv2 "connectrpc.com/connect/v2"

	"github.com/reliant-labs/forge/pkg/serverkit"
)

func TestCallInfoV2_InsideACallIsTheCallsInfo(t *testing.T) {
	server := connectv2.NewServer()
	var got *connectv2.CallInfo
	server.SetUnknownHandler(func(ctx context.Context, _ connectv2.Spec, _ connectv2.ServerStream) error {
		got = serverkit.CallInfoV2(ctx)
		got.ResponseHeader().Set("X-Seen", got.RequestHeader().Get("X-Tenant"))
		return nil
	})
	info := &connectv2.CallInfo{}
	info.RequestHeader().Set("X-Tenant", "acme")
	if err := server.Call(context.Background(), "/acme.v1.Svc/Hello", info, nil); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if got != info {
		t.Fatal("CallInfoV2 inside a call must return the call's own CallInfo")
	}
	if seen := info.ResponseHeader().Get("X-Seen"); seen != "acme" {
		t.Fatalf("handler read request header %q and wrote it back; want acme", seen)
	}
}

func TestCallInfoV2_OutsideACallIsEmptyAndUsable(t *testing.T) {
	ctx := context.Background()
	info := serverkit.CallInfoV2(ctx)
	if info == nil {
		t.Fatal("CallInfoV2 outside a call returned nil")
	}
	if n := info.RequestHeader().Len(); n != 0 {
		t.Fatalf("request header has %d keys outside a call, want 0", n)
	}
	if v := info.RequestHeader().Get("X-Tenant"); v != "" {
		t.Fatalf("Get on the empty header = %q, want empty", v)
	}
	// Writable without panicking, and discarded: the next caller gets a
	// fresh CallInfo.
	info.ResponseHeader().Set("X-Cost", "3")
	info.ResponseTrailer().Set("X-Done", "1")
	if again := serverkit.CallInfoV2(ctx); again == info || again.ResponseHeader().Len() != 0 {
		t.Fatal("writes outside a call must not leak into a later CallInfoV2")
	}
}
