// Copyright (c) 2025 Reliant Labs
package controlplane

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
)

// The client speaks the contract control-plane serves: the path of
// ConversationTraceInternalService.GetConversationTrace, the internal-service
// bearer, the request's protojson names, and int64 durations as proto3 JSON
// strings. (protoFields skips map fields: attributes is read by name below.)
func TestGetConversationTrace_SpeaksTheVendoredContract(t *testing.T) {
	raw, err := os.ReadFile("testdata/conversation_trace_internal.proto")
	require.NoError(t, err)
	src := string(raw)
	require.Contains(t, src, "package controlplane.v1;")
	require.Contains(t, src, "rpc GetConversationTrace(GetConversationTraceRequest) returns (GetConversationTraceResponse)")
	assert.Equal(t, []string{"conversationId", "endTime", "orgId", "startTime"}, protoFields(t, src, "GetConversationTraceRequest"))
	assert.Equal(t, []string{"spans", "truncated"}, protoFields(t, src, "GetConversationTraceResponse"))
	assert.Equal(t, []string{"durationNs", "events", "kind", "links", "name", "parentSpanId", "serviceName", "spanId",
		"startTime", "statusCode", "traceId"}, protoFields(t, src, "ConversationSpan"))
	assert.Contains(t, src, "map<string, string> attributes = 10;")
	assert.Equal(t, []string{"name", "time"}, protoFields(t, src, "ConversationSpanEvent"))
	assert.Equal(t, []string{"spanId", "traceId"}, protoFields(t, src, "ConversationSpanLink"))

	var path, bearer string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, bearer = r.URL.Path, r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		_, _ = w.Write([]byte(`{"spans":[
			{"traceId":"t1","spanId":"s1","name":"chat claude","kind":"Client","serviceName":"reliant-temporal-worker",
			 "startTime":"2026-11-20T10:00:00.5Z","durationNs":"1500000000","statusCode":"Ok",
			 "attributes":{"org.id":"acme","forge.ai.cost.usd":"0.01"},
			 "events":[{"name":"gen_ai.first_token","time":"2026-11-20T10:00:00.9Z","attributes":{"k":"v"}}],
			 "links":[{"traceId":"t0","spanId":"s0"}]},
			{"traceId":"t1","spanId":"s2","parentSpanId":"s1","name":"execute_tool bash","startTime":"2026-11-20T10:00:01Z"}],
			"truncated":true}`))
	}))
	defer srv.Close()
	c := New(Deps{ControlPlane: &configv1.ControlPlaneConfig{Url: srv.URL}, Sign: signWith("s")})

	start := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	got, err := c.GetConversationTrace(context.Background(), ConversationTraceQuery{
		OrgID: "acme", ConversationID: "chat-1", Start: start, End: start.Add(MaxConversationTraceWindow),
	})
	require.NoError(t, err)
	assert.Equal(t, "/controlplane.v1.ConversationTraceInternalService/GetConversationTrace", path)
	assert.Equal(t, "Bearer signed-s", bearer)
	assert.Equal(t, map[string]any{
		"orgId": "acme", "conversationId": "chat-1",
		"startTime": "2026-11-18T00:00:00Z", "endTime": "2026-11-25T00:00:00Z",
	}, body)
	assert.Equal(t, ConversationTrace{Truncated: true, Spans: []ConversationSpan{
		{
			TraceID: "t1", SpanID: "s1", Name: "chat claude", Kind: "Client", ServiceName: "reliant-temporal-worker",
			Start: time.Date(2026, 11, 20, 10, 0, 0, 5e8, time.UTC), DurationNs: 15e8, StatusCode: "Ok",
			Attributes: map[string]string{"org.id": "acme", "forge.ai.cost.usd": "0.01"},
			Events: []ConversationSpanEvent{{Name: "gen_ai.first_token", Time: time.Date(2026, 11, 20, 10, 0, 0, 9e8, time.UTC),
				Attributes: map[string]string{"k": "v"}}},
			Links: []ConversationSpanLink{{TraceID: "t0", SpanID: "s0"}},
		},
		{TraceID: "t1", SpanID: "s2", ParentSpanID: "s1", Name: "execute_tool bash", Start: time.Date(2026, 11, 20, 10, 0, 1, 0, time.UTC)},
	}}, got)
}

// A window the control plane would refuse is refused before the call; a
// control plane without the service (404 or unimplemented), or no identity to
// sign with, is ErrConversationTraceUnsupported; a refusal carries its code.
func TestGetConversationTrace_Refusals(t *testing.T) {
	status, answer, calls := http.StatusOK, `{}`, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(status)
		_, _ = w.Write([]byte(answer))
	}))
	defer srv.Close()
	c := New(Deps{ControlPlane: &configv1.ControlPlaneConfig{Url: srv.URL}, Sign: signWith("s")})
	start := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	q := ConversationTraceQuery{OrgID: "acme", ConversationID: "chat-1", Start: start, End: start.Add(time.Hour)}
	ctx := context.Background()

	for name, bad := range map[string]ConversationTraceQuery{
		"no org":          {ConversationID: "chat-1", Start: q.Start, End: q.End},
		"no conversation": {OrgID: "acme", Start: q.Start, End: q.End},
		"empty window":    {OrgID: "acme", ConversationID: "chat-1", Start: q.Start, End: q.Start},
		"over seven days": {OrgID: "acme", ConversationID: "chat-1", Start: q.Start, End: q.Start.Add(MaxConversationTraceWindow + time.Second)},
	} {
		_, err := c.GetConversationTrace(ctx, bad)
		require.Error(t, err, name)
	}
	require.Zero(t, calls, "a window control-plane would refuse is never sent")

	got, err := c.GetConversationTrace(ctx, q)
	require.NoError(t, err)
	assert.Empty(t, got.Spans)

	status, answer = http.StatusNotFound, ""
	_, err = c.GetConversationTrace(ctx, q)
	require.ErrorIs(t, err, ErrConversationTraceUnsupported)

	status, answer = http.StatusNotImplemented, `{"code":"unimplemented","message":"no"}`
	_, err = c.GetConversationTrace(ctx, q)
	require.ErrorIs(t, err, ErrConversationTraceUnsupported)

	status, answer = http.StatusTooManyRequests, `{"code":"resource_exhausted","message":"slow down"}`
	_, err = c.GetConversationTrace(ctx, q)
	var refusal *ProjectOrgMoveError
	require.ErrorAs(t, err, &refusal)
	assert.Equal(t, "resource_exhausted", refusal.Code)

	status, answer = http.StatusOK, `{"spans":[{"spanId":"`+strings.Repeat("x", maxConversationTraceBytes)+`"}]}`
	_, err = c.GetConversationTrace(ctx, q)
	require.ErrorContains(t, err, "more than", "an answer over the bound is an error, never a truncated decode")

	unsigned := New(Deps{ControlPlane: &configv1.ControlPlaneConfig{Url: srv.URL}})
	_, err = unsigned.GetConversationTrace(ctx, q)
	require.ErrorIs(t, err, ErrConversationTraceUnsupported)
}
