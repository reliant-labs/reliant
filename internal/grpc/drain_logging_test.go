// Copyright (c) 2025 Reliant Labs
package grpc

import (
	"context"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/emptypb"
)

// The daemon-gateway ends every daemon stream it holds with
// Unavailable("gateway draining") at shutdown — deliberately: it is what puts
// each daemon on its reconnect path toward a live replica. Through the shared
// interceptor chain that was a "stream failed" record at ERROR, one issue
// event per connected daemon, on every rollout (20 in prod over 7 days).
//
// A drain is an expected outcome, so it is logged the way a success is:
// written, at INFO, with expected=true — and never reported.
func TestDrainingStreamIsAnExpectedOutcomeNotAFailure(t *testing.T) {
	lines := installProcessLogging(t)
	draining := connect.NewError(connect.CodeUnavailable, svcerr.Draining(
		"gateway draining: this daemon-gateway replica is shutting down; reconnect to reach a live replica"))
	api := newFailingAPI(t, map[string]error{
		watchFilesProcedure:  draining,
		sendMessageProcedure: daemonFault("2aab1465-0d3c-4b8e-9b1e-5f0c1d2e3f40"),
	})

	client := connect.NewClient[emptypb.Empty, emptypb.Empty](http.DefaultClient, api.url+watchFilesProcedure)
	stream, err := client.CallServerStream(context.Background(), connect.NewRequest(&emptypb.Empty{}))
	require.NoError(t, err)
	for stream.Receive() {
	}
	require.Equal(t, connect.CodeUnavailable, svcerr.Code(stream.Err()),
		"the client still receives Unavailable: that is its reconnect signal")
	require.NoError(t, stream.Close())

	// A real fault after the drain: the only ERROR, so the only issue event.
	api.call(t, sendMessageProcedure, "req-fault")
	errs := lines.errors()
	require.Len(t, errs, 1, "%v", errs)
	assert.Equal(t, sendMessageProcedure, errs[0]["procedure"], "only the real fault is an ERROR")

	var drained []map[string]any
	for _, record := range lines.records(t, "stream failed") {
		if record["procedure"] == watchFilesProcedure {
			drained = append(drained, record)
		}
	}
	require.Len(t, drained, 1, "the drained stream is still written down")
	assert.Equal(t, "INFO", drained[0]["level"], "a rollout's drain must not log at ERROR")
	assert.Equal(t, true, drained[0]["expected"])
}
