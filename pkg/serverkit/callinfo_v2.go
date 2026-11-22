package serverkit

import (
	"context"

	connectv2 "connectrpc.com/connect/v2"
)

// CallInfoV2 returns the *connect.CallInfo of the connect-go v2 call ctx is
// serving, or a fresh, empty one when ctx is not inside a call. It never
// returns nil, so a handler can read and write metadata the same way inside
// a call and when a test invokes the method directly:
//
//	info := serverkit.CallInfoV2(ctx)
//	tenant := info.RequestHeader().Get("X-Tenant")
//	info.ResponseHeader().Set("X-Request-Cost", cost)
//
// It is the nil-safe form of connectv2.CallInfoForServerContext, whose
// CallInfo is nil outside a call: connect-go-v2-migrate rewrites a handler's
// header reads to `info, _ := connect.CallInfoForServerContext(ctx)` followed
// by info.RequestHeader(), which panics when a unit test calls the handler
// without a server. Replacing that one line with
// `info := serverkit.CallInfoV2(ctx)` leaves the rest of the handler as the
// tool wrote it.
//
// Outside a call the CallInfo belongs to nobody: its headers are empty and
// writable, and what a handler writes to them is discarded. A test that
// needs to set request headers or read the response headers a handler set
// calls the handler through a real server instead (tdd.CallV2, or a
// tdd.CaseV2 row's Header).
func CallInfoV2(ctx context.Context) *connectv2.CallInfo {
	if info, ok := connectv2.CallInfoForServerContext(ctx); ok && info != nil {
		return info
	}
	return &connectv2.CallInfo{}
}
