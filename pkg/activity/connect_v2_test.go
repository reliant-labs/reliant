package activity

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	connectv2 "connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/reliant-labs/forge/pkg/authn"
	"github.com/reliant-labs/forge/pkg/forgepb"
)

// harnessV2 serves md over a real connect-go v2 server behind InterceptorV2,
// so the interceptor sees the method descriptor in Spec.Schema as it does in
// production.
type harnessV2 struct {
	t      *testing.T
	md     protoreflect.MethodDescriptor
	spec   connectv2.Spec
	client *connectv2.Client
	log    *bytes.Buffer
}

func newHarnessV2(t *testing.T, md protoreflect.MethodDescriptor, deps Deps, h handlerFunc, outer ...connectv2.ServerInterceptor) *harnessV2 {
	t.Helper()
	logs := &bytes.Buffer{}
	if deps.Logger == nil {
		deps.Logger = slog.New(slog.NewTextHandler(logs, nil))
	}
	if deps.Principal == nil {
		deps.Principal = ClaimsPrincipal(authn.ClaimsFromContext)
	}
	ic, err := InterceptorV2(deps)
	if err != nil {
		t.Fatal(err)
	}
	spec := connectv2.Spec{
		StreamType: connectv2.StreamTypeUnary,
		Schema:     md,
		Procedure:  "/" + string(md.Parent().FullName()) + "/" + string(md.Name()),
	}
	if md.IsStreamingServer() {
		spec.StreamType = connectv2.StreamTypeServer
	}
	// claims plays the auth interceptor: it stashes the caller the activity
	// interceptor reads.
	claims := func(next connectv2.ServerFunc) connectv2.ServerFunc {
		return func(ctx context.Context, spec connectv2.Spec, stream connectv2.ServerStream) error {
			info, _ := connectv2.CallInfoForServerContext(ctx)
			if id := info.RequestHeader().Get("X-Test-User"); id != "" {
				ctx = authn.ContextWithClaims(ctx, &authn.Claims{UserID: id, Email: id + "@example.com", OrgID: "org_1"})
			}
			return next(ctx, spec, stream)
		}
	}
	// outer interceptors run between the claims stash and the interceptor
	// under test, where a chain's outer layers would.
	srv := connectv2.NewServer(append(append([]connectv2.ServerInterceptor{claims}, outer...), ic)...)
	srv.Register(connectv2.Method{Spec: spec, Handler: func(ctx context.Context, _ connectv2.Spec, stream connectv2.ServerStream) error {
		req := dynamicpb.NewMessage(md.Input())
		if err := stream.Receive(req); err != nil {
			return err
		}
		resp := dynamicpb.NewMessage(md.Output())
		if err := h(ctx, req, resp); err != nil {
			return err
		}
		return stream.Send(resp)
	}})
	mux := http.NewServeMux()
	connecthttp.Mount(mux, srv)
	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)
	return &harnessV2{t: t, md: md, spec: spec, client: connectv2.NewClient(connecthttp.NewTransport(hs.Client(), hs.URL)), log: logs}
}

func (h *harnessV2) call(fields map[string]string, headers map[string]string) error {
	h.t.Helper()
	msg := dynamicpb.NewMessage(h.md.Input())
	for k, v := range fields {
		setField(msg, k, v)
	}
	ctx, info := connectv2.NewClientContext(context.Background())
	for k, v := range headers {
		info.RequestHeader().Set(k, v)
	}
	res := dynamicpb.NewMessage(h.md.Output())
	if h.spec.StreamType == connectv2.StreamTypeUnary {
		return h.client.CallUnary(ctx, h.spec, msg, res)
	}
	cs, err := h.client.CallServerStream(ctx, h.spec, msg)
	if err != nil {
		return err
	}
	defer cs.Close()
	for {
		if err := cs.Receive(res); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// The v2 interceptor writes exactly the event the v1 interceptor writes for
// the same call.
func TestInterceptorV2_WritesTheEventV1Writes(t *testing.T) {
	md := fixtureRPC(t, fixtureMethod{name: "Promote", activity: promoteDecl()})
	w1, w2 := &memWriter{}, &memWriter{}
	fields := map[string]string{"release_id": "rel_1", "to_env": "prod", "from_env": "e2e", "project_id": "proj_9"}
	headers := map[string]string{"X-Test-User": "sean", ViaHeader: "CLI"}
	if err := newHarness(t, md, Deps{Writer: w1}, promoteHandler).call(fields, headers); err != nil {
		t.Fatalf("v1 call: %v", err)
	}
	if err := newHarnessV2(t, md, Deps{Writer: w2}, promoteHandler).call(fields, headers); err != nil {
		t.Fatalf("v2 call: %v", err)
	}
	ev1, ev2 := w1.got(), w2.got()
	if len(ev1) != 1 || len(ev2) != 1 {
		t.Fatalf("wrote %d (v1) and %d (v2) events, want 1 each", len(ev1), len(ev2))
	}
	want := Event{
		Scope:     "proj_9",
		Actor:     Actor{ID: "sean", Name: "sean@example.com", Kind: ActorUser},
		Verb:      "promote",
		Target:    Target{Kind: "release", ID: "rel_1", Label: "v1.7.44"},
		Env:       "prod",
		Via:       "cli",
		Procedure: "/fixture.v1.FixtureService/Promote",
	}
	got := ev2[0]
	if got.Scope != want.Scope || got.Actor != want.Actor || got.Verb != want.Verb || got.Target != want.Target ||
		got.Env != want.Env || got.Via != want.Via || got.Procedure != want.Procedure {
		t.Errorf("v2 event =\n  %+v\nwant\n  %+v", got, want)
	}
	if got.Detail["from_env"] != "e2e" || got.Detail["replicas"] != "0" {
		t.Errorf("detail = %v, want from_env=e2e replicas=0", got.Detail)
	}
	a, b := ev1[0], ev2[0]
	a.OccurredAt, b.OccurredAt = time.Time{}, time.Time{}
	if a.Scope != b.Scope || a.Actor != b.Actor || a.Verb != b.Verb || a.Target != b.Target || a.Env != b.Env ||
		a.Via != b.Via || a.Procedure != b.Procedure || a.TraceID != b.TraceID || len(a.Detail) != len(b.Detail) {
		t.Errorf("events differ:\n  v1 %+v\n  v2 %+v", a, b)
	}
	for k, v := range a.Detail {
		if b.Detail[k] != v {
			t.Errorf("detail %s: v1 %q, v2 %q", k, v, b.Detail[k])
		}
	}
}

func TestInterceptorV2_FailedCallWritesNothing(t *testing.T) {
	w := &memWriter{}
	md := fixtureRPC(t, fixtureMethod{name: "Promote", activity: promoteDecl()})
	h := newHarnessV2(t, md, Deps{Writer: w}, func(context.Context, *dynamicpb.Message, *dynamicpb.Message) error {
		return connectv2.NewError(connectv2.CodeFailedPrecondition, "not converged")
	})
	if err := h.call(map[string]string{"release_id": "rel_1"}, nil); connectv2.CodeOf(err) != connectv2.CodeFailedPrecondition {
		t.Fatalf("call err = %v, want FailedPrecondition", err)
	}
	if n := len(w.got()); n != 0 {
		t.Errorf("wrote %d events for a failed call, want 0", n)
	}
}

func TestInterceptorV2_NoneAndUndeclaredPassThrough(t *testing.T) {
	for name, opt := range map[string]*forgepb.ActivityOptions{"none": {None: "dry run"}, "undeclared": nil} {
		t.Run(name, func(t *testing.T) {
			w := &memWriter{}
			h := newHarnessV2(t, fixtureRPC(t, fixtureMethod{name: "Preview", activity: opt}), Deps{Writer: w}, promoteHandler)
			if err := h.call(map[string]string{"release_id": "rel_1"}, nil); err != nil {
				t.Fatal(err)
			}
			if n := len(w.got()); n != 0 {
				t.Errorf("wrote %d events, want 0", n)
			}
		})
	}
}

// The response is held until the event is written, so a caller that has its
// response can rely on the event existing — the v1 interceptor's ordering.
// An outer interceptor marks the moment the response leaves for the client,
// and the writer records whether that had already happened when it was
// called: the order is observed directly, with no timing involved.
func TestInterceptorV2_EventIsWrittenBeforeTheResponse(t *testing.T) {
	var sent atomic.Bool
	w := &orderWriter{sent: &sent}
	markSend := func(next connectv2.ServerFunc) connectv2.ServerFunc {
		return func(ctx context.Context, spec connectv2.Spec, stream connectv2.ServerStream) error {
			return next(ctx, spec, &sendMarker{ServerStream: stream, sent: &sent})
		}
	}
	md := fixtureRPC(t, fixtureMethod{name: "CreateWidget", in: "CreateWidgetRequest", out: "CreateWidgetResponse"})
	h := newHarnessV2(t, md, Deps{Writer: w}, func(_ context.Context, req, resp *dynamicpb.Message) error {
		wd := resp.Mutable(resp.Descriptor().Fields().ByName("widget")).Message()
		wd.Set(wd.Descriptor().Fields().ByName("id"), protoreflect.ValueOfString("wid_1"))
		wd.Set(wd.Descriptor().Fields().ByName("name"), req.Get(req.Descriptor().Fields().ByName("name")))
		return nil
	}, markSend)
	if err := h.call(map[string]string{"name": "Gizmo"}, map[string]string{"X-Test-User": "sean"}); err != nil {
		t.Fatal(err)
	}
	if !sent.Load() {
		t.Fatal("the outer interceptor never saw the response leave")
	}
	if w.sentFirst.Load() {
		t.Fatal("the response was sent before its event was written")
	}
	evs := w.got()
	if len(evs) != 1 {
		t.Fatalf("wrote %d events, want 1", len(evs))
	}
	if evs[0].Verb != "create" || evs[0].Target != (Target{Kind: "widget", ID: "wid_1", Label: "Gizmo"}) {
		t.Errorf("event = %+v; the response paths must be read from the message the handler sent", evs[0])
	}
}

// sendMarker records that a response was sent on the stream it wraps.
type sendMarker struct {
	connectv2.ServerStream
	sent *atomic.Bool
}

func (s *sendMarker) Send(msg any) error {
	s.sent.Store(true)
	return s.ServerStream.Send(msg)
}

// orderWriter records whether the response had already been sent when an
// event was written.
type orderWriter struct {
	memWriter
	sent      *atomic.Bool
	sentFirst atomic.Bool
}

func (w *orderWriter) Write(ctx context.Context, ev Event) error {
	if w.sent.Load() {
		w.sentFirst.Store(true)
	}
	return w.memWriter.Write(ctx, ev)
}

func TestInterceptorV2_WriteFailureDoesNotFailTheCall(t *testing.T) {
	w := &memWriter{err: errors.New("db down")}
	h := newHarnessV2(t, fixtureRPC(t, fixtureMethod{name: "Promote", activity: promoteDecl()}), Deps{Writer: w}, promoteHandler)
	if err := h.call(map[string]string{"release_id": "rel_1"}, nil); err != nil {
		t.Fatalf("call failed because its activity write did: %v", err)
	}
	if !strings.Contains(h.log.String(), "activity: write failed") || !strings.Contains(h.log.String(), "db down") {
		t.Errorf("failure not logged:\n%s", h.log.String())
	}
}

func TestInterceptorV2_HandlerWriterAndSkip(t *testing.T) {
	opt := promoteDecl()
	opt.Writer = forgepb.ActivityWriter_ACTIVITY_WRITER_HANDLER
	opt.Verb = "rotate"
	md := fixtureRPC(t, fixtureMethod{name: "Rotate", activity: opt})

	t.Run("handler records", func(t *testing.T) {
		w := &memWriter{}
		h := newHarnessV2(t, md, Deps{Writer: w}, func(ctx context.Context, _, _ *dynamicpb.Message) error {
			return Record(ctx, w, Event{Target: Target{Label: "STRIPE_KEY (v7)"}})
		})
		if err := h.call(map[string]string{"release_id": "sec_1", "to_env": "prod"},
			map[string]string{"X-Test-User": "sean", ViaHeader: "console"}); err != nil {
			t.Fatal(err)
		}
		evs := w.got()
		if len(evs) != 1 {
			t.Fatalf("wrote %d events, want exactly the handler's 1", len(evs))
		}
		if ev := evs[0]; ev.Verb != "rotate" || ev.Target != (Target{Kind: "release", ID: "sec_1", Label: "STRIPE_KEY (v7)"}) ||
			ev.Actor.ID != "sean" || ev.Via != "console" || ev.Env != "prod" || ev.Procedure == "" {
			t.Errorf("event = %+v", ev)
		}
	})
	t.Run("handler forgets", func(t *testing.T) {
		w := &memWriter{}
		h := newHarnessV2(t, md, Deps{Writer: w}, promoteHandler)
		if err := h.call(map[string]string{"release_id": "sec_1"}, nil); err != nil {
			t.Fatal(err)
		}
		if n := len(w.got()); n != 1 || !strings.Contains(h.log.String(), "did not call activity.Record") {
			t.Errorf("wrote %d events (want the fallback 1), log:\n%s", n, h.log.String())
		}
	})
	t.Run("skip", func(t *testing.T) {
		w := &memWriter{}
		h := newHarnessV2(t, fixtureRPC(t, fixtureMethod{name: "Promote", activity: promoteDecl()}), Deps{Writer: w},
			func(ctx context.Context, req, resp *dynamicpb.Message) error {
				if !Skip(ctx) {
					t.Error("Skip reported no declared call inside a declared rpc")
				}
				return promoteHandler(ctx, req, resp)
			})
		if err := h.call(map[string]string{"release_id": "rel_1"}, nil); err != nil {
			t.Fatal(err)
		}
		if n := len(w.got()); n != 0 {
			t.Errorf("wrote %d events after Skip, want 0", n)
		}
	})
}

// A stream gets the call record a HANDLER-written Record reads, its channel
// from the Forge-Via header, and no event of the interceptor's own.
func TestInterceptorV2_StreamingHandlerRecord(t *testing.T) {
	opt := promoteDecl()
	opt.Writer = forgepb.ActivityWriter_ACTIVITY_WRITER_HANDLER
	md := fixtureRPC(t, fixtureMethod{name: "Watch", activity: opt, streaming: true})

	w := &memWriter{}
	h := newHarnessV2(t, md, Deps{Writer: w, ViaFromV2: func(context.Context, *connectv2.CallInfo) string { return "not-for-streams" }},
		func(ctx context.Context, _, _ *dynamicpb.Message) error {
			return Record(ctx, w, Event{Target: Target{ID: "rel_s", Label: "v9"}})
		})
	if err := h.call(nil, map[string]string{"X-Test-User": "sean", ViaHeader: "cli"}); err != nil {
		t.Fatal(err)
	}
	evs := w.got()
	if len(evs) != 1 || evs[0].Via != "cli" || evs[0].Actor.ID != "sean" || evs[0].Verb != "promote" {
		t.Fatalf("events = %+v, want the handler's one, via cli", evs)
	}

	forgot := newHarnessV2(t, md, Deps{Writer: &memWriter{}}, promoteHandler)
	if err := forgot.call(nil, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(forgot.log.String(), "streaming rpc succeeded without calling activity.Record") {
		t.Errorf("a stream that recorded nothing was not warned about:\n%s", forgot.log.String())
	}
}

func TestInterceptorV2_ViaFromV2(t *testing.T) {
	w := &memWriter{}
	h := newHarnessV2(t, fixtureRPC(t, fixtureMethod{name: "Promote", activity: promoteDecl()}),
		Deps{Writer: w, ViaFromV2: func(_ context.Context, info *connectv2.CallInfo) string {
			return "via-" + info.RequestHeader().Get("X-Client")
		}}, promoteHandler)
	if err := h.call(map[string]string{"release_id": "rel_1"}, map[string]string{"X-Client": "tui"}); err != nil {
		t.Fatal(err)
	}
	if evs := w.got(); len(evs) != 1 || evs[0].Via != "via-tui" {
		t.Fatalf("events = %+v, want via-tui from ViaFromV2", evs)
	}
}

func TestDefaultViaV2(t *testing.T) {
	for in, want := range map[string]string{"": "api", "Console": "console", "ci": "ci", "bad value": "api", strings.Repeat("x", 40): "api"} {
		info := &connectv2.CallInfo{}
		info.RequestHeader().Set(ViaHeader, in)
		if got := DefaultViaV2(context.Background(), info); got != want {
			t.Errorf("DefaultViaV2(%q) = %q, want %q", in, got, want)
		}
	}
}

// Each major's constructor refuses Deps that set only the other major's
// channel hook, and both refuse a missing Principal.
func TestInterceptorV2_Refusals(t *testing.T) {
	p := ClaimsPrincipal(authn.ClaimsFromContext)
	ic, err := InterceptorV2(Deps{})
	if err == nil || ic != nil || !strings.Contains(err.Error(), "activity.InterceptorV2: Deps.Principal is required") {
		t.Errorf("InterceptorV2(Deps{}) = %v, %v; want the Principal refusal", ic != nil, err)
	}
	if _, err := InterceptorV2(Deps{Principal: p, ViaFrom: DefaultVia}); err == nil || !strings.Contains(err.Error(), "ViaFromV2") {
		t.Errorf("InterceptorV2 with only ViaFrom: %v, want a refusal naming ViaFromV2", err)
	}
	if ic1, err := Interceptor(Deps{Principal: p, ViaFromV2: DefaultViaV2}); err == nil || ic1 != nil || !strings.Contains(err.Error(), "ViaFrom") {
		t.Errorf("Interceptor with only ViaFromV2: %v, want a refusal naming ViaFrom", err)
	}
	if _, err := InterceptorV2(Deps{Principal: p, ViaFrom: DefaultVia, ViaFromV2: DefaultViaV2}); err != nil {
		t.Errorf("Deps setting both hooks serves both majors: %v", err)
	}
}
