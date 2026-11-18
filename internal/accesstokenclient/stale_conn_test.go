package accesstokenclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// staleOnSecond is a control plane that answers the first request and then
// drops the keep-alive connection under the second without a response: what a
// pooled connection the peer closed or reset looks like to the client (prod:
// "EOF" and "read: connection reset by peer" ~1ms into ResolveOrgMembership).
func staleOnSecond(t *testing.T, answer string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 2 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			_ = conn.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

// A read is replayable, so a dead pooled connection is retried once on a
// fresh one by net/http instead of failing the caller's request.
func TestCall_ReadRetriesADeadKeepAliveConnection(t *testing.T) {
	srv, n := staleOnSecond(t, `{"orgId":"org-home","member":true}`)
	c := New(Deps{BaseURL: srv.URL, Sign: sign})
	ctx := context.Background()

	if _, err := c.ResolveOrgMembership(ctx, "user-1", ""); err != nil {
		t.Fatalf("first call: %v", err)
	}
	got, err := c.ResolveOrgMembership(ctx, "user-1", "")
	if err != nil {
		t.Fatalf("second call on the dead connection was not retried: %v", err)
	}
	if got.OrgID != "org-home" || n.Load() != 3 {
		t.Fatalf("got %+v after %d requests, want org-home after 3", got, n.Load())
	}
}

// A write is never replayed: a mint whose connection died may have been
// minted, so the caller sees the failure.
func TestCall_WriteIsNotRetried(t *testing.T) {
	srv, n := staleOnSecond(t, `{"orgId":"org-home","member":true}`)
	c := New(Deps{BaseURL: srv.URL, Sign: sign})
	ctx := context.Background()

	if _, err := c.ResolveOrgMembership(ctx, "user-1", ""); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := c.RevokeForUser(ctx, "user-1", "tok-1"); err == nil {
		t.Fatal("a revoke on a dead connection succeeded; it must not be replayed")
	}
	if n.Load() != 2 {
		t.Fatalf("server saw %d requests, want 2 (no replay)", n.Load())
	}
}

// The replay allowlist is exactly the methods whose server-side effects were
// shown read-only or idempotent (see replayable). Growing it means showing the
// same for the new method, so a change here must be deliberate.
func TestReplayable_IsExactlyTheVerifiedReads(t *testing.T) {
	want := map[string]bool{
		"Introspect":           true,
		"ListForUser":          true,
		"ResolveOrgMembership": true,
		"ResolveGoneOrgs":      true,
		"ResolveOrgPolicy":     true,
	}
	if len(replayable) != len(want) {
		t.Fatalf("replayable has %d methods, want %d: %v", len(replayable), len(want), replayable)
	}
	for method, ok := range replayable {
		if !ok || !want[method] {
			t.Errorf("replayable[%q] = %v: not one of the verified reads", method, ok)
		}
	}
}

func TestRPCError_ClassFollowsWhoMustAct(t *testing.T) {
	for _, tc := range []struct {
		err  *RPCError
		want svcerr.Class
	}{
		{&RPCError{Code: "failed_precondition"}, svcerr.ClassUser},
		{&RPCError{Code: "not_found"}, svcerr.ClassUser},
		{&RPCError{Code: "resource_exhausted"}, svcerr.ClassUser},
		// The internal-service door refuses OUR bearer as permission_denied
		// with no reason: ours to fix.
		{&RPCError{Code: "permission_denied"}, svcerr.ClassServer},
		{&RPCError{Code: "permission_denied", Reason: "scope_missing"}, svcerr.ClassUser},
		{&RPCError{Code: "unauthenticated"}, svcerr.ClassServer},
		{&RPCError{Code: "unavailable"}, svcerr.ClassServer},
		{&RPCError{Code: "internal"}, svcerr.ClassServer},
		{&RPCError{Code: "canceled"}, svcerr.ClassCanceled},
	} {
		if got := svcerr.Classify(tc.err); got != tc.want {
			t.Errorf("%+v: class %v, want %v", tc.err, got, tc.want)
		}
	}
}
