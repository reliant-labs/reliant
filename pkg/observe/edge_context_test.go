package observe

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
)

const testVisitorDay = "0123456789abcdef"

// edgeRequest runs one request with the given headers through the HTTP
// middleware and returns the context the handler saw.
func edgeRequest(t *testing.T, trusted bool, headers map[string]string) context.Context {
	t.Helper()
	var seen context.Context
	h := edgeContextMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { seen = r.Context() }), trusted)
	req := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	h.ServeHTTP(httptest.NewRecorder(), req)
	return seen
}

func TestEdgeContextIsReadOnlyBehindATrustedEdge(t *testing.T) {
	headers := map[string]string{HeaderVisitorDay: testVisitorDay}
	if got := EdgeContextFrom(edgeRequest(t, false, headers)); got != (EdgeContext{}) {
		t.Errorf("an untrusted edge's header was read: %+v", got)
	}
	if got := EdgeContextFrom(edgeRequest(t, true, headers)); got.VisitorDay != testVisitorDay {
		t.Errorf("a trusted edge's header was not read: %+v", got)
	}
}

func TestEdgeContextDropsTheHashUnderAPrivacySignalOrAWrongShape(t *testing.T) {
	for name, tc := range map[string]struct {
		headers map[string]string
		want    EdgeContext
	}{
		"gpc":           {map[string]string{HeaderVisitorDay: testVisitorDay, HeaderPrivacy: "GPC"}, EdgeContext{Privacy: "gpc"}},
		"dnt":           {map[string]string{HeaderVisitorDay: testVisitorDay, HeaderPrivacy: "dnt"}, EdgeContext{Privacy: "dnt"}},
		"unknown":       {map[string]string{HeaderPrivacy: "sideways"}, EdgeContext{Privacy: "other"}},
		"upper hex":     {map[string]string{HeaderVisitorDay: "0123456789ABCDEF"}, EdgeContext{}},
		"too long":      {map[string]string{HeaderVisitorDay: testVisitorDay + "00"}, EdgeContext{}},
		"an email":      {map[string]string{HeaderVisitorDay: "ada@example.com"}, EdgeContext{}},
		"no headers":    {nil, EdgeContext{}},
		"the hash only": {map[string]string{HeaderVisitorDay: testVisitorDay}, EdgeContext{VisitorDay: testVisitorDay}},
	} {
		if got := EdgeContextFrom(edgeRequest(t, true, tc.headers)); got != tc.want {
			t.Errorf("%s: %+v, want %+v", name, got, tc.want)
		}
	}
}

func TestTrackAndTrackAuthRecordTheVisitorDay(t *testing.T) {
	var buf bytes.Buffer
	ctx := WithLogger(WithEdgeContext(context.Background(), EdgeContext{VisitorDay: testVisitorDay}),
		slog.New(slog.NewJSONHandler(&buf, nil)))
	TrackAuth(ctx, AuthSignedUp, "u-1", WithMethod(AuthMethodPassword))
	Track(ctx, "project_created")
	dec := json.NewDecoder(&buf)
	for i := 0; i < 2; i++ {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			t.Fatal(err)
		}
		if m[AttrVisitorDay] != testVisitorDay {
			t.Errorf("record %d has no visitor.day: %v", i, m)
		}
	}

	buf.Reset()
	Track(WithLogger(context.Background(), slog.New(slog.NewJSONHandler(&buf, nil))), "project_created")
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m[AttrVisitorDay]; ok {
		t.Errorf("a request with no edge context recorded visitor.day: %v", m)
	}
}

func TestEdgeContextInterceptorReadsConnectHeaders(t *testing.T) {
	var seen EdgeContext
	handler := func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		seen = EdgeContextFrom(ctx)
		return nil, nil
	}
	req := connect.NewRequest(&struct{}{})
	req.Header().Set(HeaderVisitorDay, testVisitorDay)
	if _, err := (edgeContextInterceptor{trusted: true}).WrapUnary(handler)(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if seen.VisitorDay != testVisitorDay {
		t.Errorf("trusted: %+v", seen)
	}
	seen = EdgeContext{}
	if _, err := (edgeContextInterceptor{trusted: false}).WrapUnary(handler)(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if seen != (EdgeContext{}) {
		t.Errorf("untrusted: %+v", seen)
	}
}
