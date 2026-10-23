package activity

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/reliant-labs/forge/pkg/authn"
	"github.com/reliant-labs/forge/pkg/forgepb"
)

func protoName(s string) protoreflect.Name   { return protoreflect.Name(s) }
func protoValue(s string) protoreflect.Value { return protoreflect.ValueOfString(s) }
func protoInt32(n int32) protoreflect.Value  { return protoreflect.ValueOfInt32(n) }
func setField(m *dynamicpb.Message, name, v string) {
	m.Set(m.Descriptor().Fields().ByName(protoreflect.Name(name)), protoreflect.ValueOfString(v))
}

// memWriter records events; err, when set, fails every write.
type memWriter struct {
	mu     sync.Mutex
	events []Event
	err    error
}

func (w *memWriter) Write(_ context.Context, ev Event) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return w.err
	}
	w.events = append(w.events, ev)
	return nil
}

func (w *memWriter) got() []Event {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]Event(nil), w.events...)
}

// handlerFunc is a fixture handler: it receives the request and fills the
// response.
type handlerFunc func(ctx context.Context, req, resp *dynamicpb.Message) error

// harness serves md over a real connect handler, so the interceptor sees the
// method descriptor the way it does in production (Spec().Schema).
type harness struct {
	t      *testing.T
	md     protoreflect.MethodDescriptor
	client *connect.Client[dynamicpb.Message, dynamicpb.Message]
	log    *bytes.Buffer
}

func newHarness(t *testing.T, md protoreflect.MethodDescriptor, deps Deps, h handlerFunc) *harness {
	t.Helper()
	logs := &bytes.Buffer{}
	if deps.Logger == nil {
		deps.Logger = slog.New(slog.NewTextHandler(logs, nil))
	}
	if deps.Principal == nil {
		deps.Principal = ClaimsPrincipal(authn.ClaimsFromContext)
	}
	ic, err := Interceptor(deps)
	if err != nil {
		t.Fatal(err)
	}
	procedure := "/" + string(md.Parent().FullName()) + "/" + string(md.Name())
	initReq := connect.WithRequestInitializer(func(spec connect.Spec, msg any) error {
		*msg.(*dynamicpb.Message) = *dynamicpb.NewMessage(spec.Schema.(protoreflect.MethodDescriptor).Input())
		return nil
	})
	// claims plays the auth interceptor: it stashes the caller the activity
	// interceptor reads.
	claims := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if id := req.Header().Get("X-Test-User"); id != "" {
				ctx = authn.ContextWithClaims(ctx, &authn.Claims{UserID: id, Email: id + "@example.com", OrgID: "org_1"})
			}
			return next(ctx, req)
		}
	})
	handler := connect.NewUnaryHandler(procedure,
		func(ctx context.Context, req *connect.Request[dynamicpb.Message]) (*connect.Response[dynamicpb.Message], error) {
			resp := dynamicpb.NewMessage(md.Output())
			if err := h(ctx, req.Msg, resp); err != nil {
				return nil, err
			}
			return connect.NewResponse(resp), nil
		},
		connect.WithSchema(md), initReq, connect.WithInterceptors(claims, ic),
	)
	mux := http.NewServeMux()
	mux.Handle(procedure, handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := connect.NewClient[dynamicpb.Message, dynamicpb.Message](srv.Client(), srv.URL+procedure,
		connect.WithSchema(md), connect.WithResponseInitializer(func(spec connect.Spec, msg any) error {
			*msg.(*dynamicpb.Message) = *dynamicpb.NewMessage(spec.Schema.(protoreflect.MethodDescriptor).Output())
			return nil
		}))
	return &harness{t: t, md: md, client: client, log: logs}
}

func (h *harness) call(fields map[string]string, headers map[string]string) error {
	h.t.Helper()
	msg := dynamicpb.NewMessage(h.md.Input())
	for k, v := range fields {
		setField(msg, k, v)
	}
	req := connect.NewRequest(msg)
	for k, v := range headers {
		req.Header().Set(k, v)
	}
	_, err := h.client.CallUnary(context.Background(), req)
	return err
}

// promoteHandler answers with the release's version.
func promoteHandler(_ context.Context, req, resp *dynamicpb.Message) error {
	rel := resp.Mutable(resp.Descriptor().Fields().ByName("release")).Message()
	rel.Set(rel.Descriptor().Fields().ByName("version"), protoreflect.ValueOfString("v1.7.44"))
	return nil
}

func TestInterceptor_WritesDeclaredEvent(t *testing.T) {
	w := &memWriter{}
	md := fixtureRPC(t, fixtureMethod{name: "Promote", activity: promoteDecl()})
	h := newHarness(t, md, Deps{Writer: w}, promoteHandler)

	if err := h.call(map[string]string{"release_id": "rel_1", "to_env": "prod", "from_env": "e2e", "project_id": "proj_9"},
		map[string]string{"X-Test-User": "sean", ViaHeader: "CLI"}); err != nil {
		t.Fatalf("call: %v", err)
	}
	evs := w.got()
	if len(evs) != 1 {
		t.Fatalf("wrote %d events, want 1", len(evs))
	}
	ev := evs[0]
	want := Event{
		Scope:     "proj_9",
		Actor:     Actor{ID: "sean", Name: "sean@example.com", Kind: ActorUser},
		Verb:      "promote",
		Target:    Target{Kind: "release", ID: "rel_1", Label: "v1.7.44"},
		Env:       "prod",
		Via:       "cli",
		Procedure: "/fixture.v1.FixtureService/Promote",
	}
	if ev.Scope != want.Scope || ev.Actor != want.Actor || ev.Verb != want.Verb || ev.Target != want.Target ||
		ev.Env != want.Env || ev.Via != want.Via || ev.Procedure != want.Procedure {
		t.Errorf("event =\n  %+v\nwant\n  %+v", ev, want)
	}
	if ev.Detail["from_env"] != "e2e" || ev.Detail["replicas"] != "0" {
		t.Errorf("detail = %v, want from_env=e2e replicas=0", ev.Detail)
	}
	if ev.OccurredAt.IsZero() {
		t.Error("OccurredAt not set")
	}
}

func TestInterceptor_DefaultsWithoutHeaderOrScopePath(t *testing.T) {
	w := &memWriter{}
	opt := promoteDecl()
	opt.Scope = ""
	md := fixtureRPC(t, fixtureMethod{name: "Promote", activity: opt})
	h := newHarness(t, md, Deps{Writer: w}, promoteHandler)
	if err := h.call(map[string]string{"release_id": "rel_1"}, map[string]string{"X-Test-User": "sean"}); err != nil {
		t.Fatal(err)
	}
	ev := w.got()[0]
	if ev.Via != ViaAPI || ev.Scope != "org_1" || ev.Env != "" {
		t.Errorf("via/scope/env = %q/%q/%q, want api/org_1/\"\"", ev.Via, ev.Scope, ev.Env)
	}
}

func TestInterceptor_FailedCallWritesNothing(t *testing.T) {
	w := &memWriter{}
	md := fixtureRPC(t, fixtureMethod{name: "Promote", activity: promoteDecl()})
	h := newHarness(t, md, Deps{Writer: w}, func(context.Context, *dynamicpb.Message, *dynamicpb.Message) error {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("not converged"))
	})
	if err := h.call(map[string]string{"release_id": "rel_1"}, nil); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("call err = %v, want FailedPrecondition", err)
	}
	if n := len(w.got()); n != 0 {
		t.Errorf("wrote %d events for a failed call, want 0", n)
	}
}

func TestInterceptor_NoneAndUndeclaredPassThrough(t *testing.T) {
	for name, opt := range map[string]*forgepb.ActivityOptions{
		"none":       {None: "dry run"},
		"undeclared": nil,
	} {
		t.Run(name, func(t *testing.T) {
			w := &memWriter{}
			md := fixtureRPC(t, fixtureMethod{name: "Preview", activity: opt})
			h := newHarness(t, md, Deps{Writer: w}, promoteHandler)
			if err := h.call(map[string]string{"release_id": "rel_1"}, nil); err != nil {
				t.Fatal(err)
			}
			if n := len(w.got()); n != 0 {
				t.Errorf("wrote %d events, want 0", n)
			}
		})
	}
}

func TestInterceptor_ImpliedCRUDCreate(t *testing.T) {
	w := &memWriter{}
	md := fixtureRPC(t, fixtureMethod{name: "CreateWidget", in: "CreateWidgetRequest", out: "CreateWidgetResponse"})
	h := newHarness(t, md, Deps{Writer: w}, func(_ context.Context, req, resp *dynamicpb.Message) error {
		wd := resp.Mutable(resp.Descriptor().Fields().ByName("widget")).Message()
		wd.Set(wd.Descriptor().Fields().ByName("id"), protoreflect.ValueOfString("wid_1"))
		wd.Set(wd.Descriptor().Fields().ByName("name"), req.Get(req.Descriptor().Fields().ByName("name")))
		return nil
	})
	if err := h.call(map[string]string{"name": "Gizmo"}, map[string]string{"X-Test-User": "sean"}); err != nil {
		t.Fatal(err)
	}
	ev := w.got()[0]
	if ev.Verb != "create" || ev.Target != (Target{Kind: "widget", ID: "wid_1", Label: "Gizmo"}) {
		t.Errorf("event = %+v", ev)
	}
}

func TestInterceptor_WriteFailureDoesNotFailTheCall(t *testing.T) {
	w := &memWriter{err: errors.New("db down")}
	md := fixtureRPC(t, fixtureMethod{name: "Promote", activity: promoteDecl()})
	h := newHarness(t, md, Deps{Writer: w}, promoteHandler)
	if err := h.call(map[string]string{"release_id": "rel_1"}, nil); err != nil {
		t.Fatalf("call failed because its activity write did: %v", err)
	}
	if !strings.Contains(h.log.String(), "activity: write failed") || !strings.Contains(h.log.String(), "db down") {
		t.Errorf("failure not logged:\n%s", h.log.String())
	}
}

func TestInterceptor_HandlerWriter(t *testing.T) {
	opt := promoteDecl()
	opt.Writer = forgepb.ActivityWriter_ACTIVITY_WRITER_HANDLER
	opt.Verb = "rotate"
	md := fixtureRPC(t, fixtureMethod{name: "Rotate", activity: opt})

	t.Run("handler records", func(t *testing.T) {
		w := &memWriter{}
		h := newHarness(t, md, Deps{Writer: w}, func(ctx context.Context, _, _ *dynamicpb.Message) error {
			// Only the label (a response path) is the handler's to supply.
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
		ev := evs[0]
		if ev.Verb != "rotate" || ev.Target != (Target{Kind: "release", ID: "sec_1", Label: "STRIPE_KEY (v7)"}) ||
			ev.Actor.ID != "sean" || ev.Via != "console" || ev.Env != "prod" || ev.Procedure == "" {
			t.Errorf("event = %+v", ev)
		}
	})

	t.Run("handler forgets", func(t *testing.T) {
		w := &memWriter{}
		h := newHarness(t, md, Deps{Writer: w}, promoteHandler)
		if err := h.call(map[string]string{"release_id": "sec_1"}, nil); err != nil {
			t.Fatal(err)
		}
		if n := len(w.got()); n != 1 {
			t.Fatalf("wrote %d events, want the interceptor's fallback 1", n)
		}
		if !strings.Contains(h.log.String(), "did not call activity.Record") {
			t.Errorf("fallback not warned:\n%s", h.log.String())
		}
	})
}

// A handler that changed nothing calls Skip, and the interceptor writes no
// event for that call: the declaration's verb is not a lie on a no-op.
func TestInterceptor_SkipSuppressesTheEvent(t *testing.T) {
	for name, writer := range map[string]forgepb.ActivityWriter{
		"interceptor writer": forgepb.ActivityWriter_ACTIVITY_WRITER_UNSPECIFIED,
		"handler writer":     forgepb.ActivityWriter_ACTIVITY_WRITER_HANDLER,
	} {
		t.Run(name, func(t *testing.T) {
			opt := promoteDecl()
			opt.Writer = writer
			w := &memWriter{}
			md := fixtureRPC(t, fixtureMethod{name: "Promote", activity: opt})
			h := newHarness(t, md, Deps{Writer: w}, func(ctx context.Context, req, resp *dynamicpb.Message) error {
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
			if strings.Contains(h.log.String(), "did not call activity.Record") {
				t.Errorf("a skipped HANDLER call was warned about:\n%s", h.log.String())
			}
		})
	}
	if Skip(context.Background()) {
		t.Error("Skip outside an rpc reported a declared call")
	}
}

func TestRecord_OutsideAnRPCNeedsACompleteEvent(t *testing.T) {
	w := &memWriter{}
	err := Record(context.Background(), w, Event{Verb: "observe"})
	if err == nil {
		t.Fatal("Record accepted an event with no actor or target")
	}
	for _, want := range []string{"actor id is empty", "actor kind", "target kind", "target id is empty"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	ok := Event{
		Actor: System("converger"), Verb: "observe", Via: "reliant", Env: "prod",
		Target: Target{Kind: "deploy_tree", ID: "hounders/prod"},
	}
	if err := Record(context.Background(), w, ok); err != nil {
		t.Fatalf("Record: %v", err)
	}
	got := w.got()[0]
	if got.Target.Label != "hounders/prod" || got.Actor.Name != "converger" {
		t.Errorf("defaults not applied: %+v", got)
	}
}

func TestDefaultVia(t *testing.T) {
	for in, want := range map[string]string{"": "api", "Console": "console", "ci": "ci", "bad value": "api", strings.Repeat("x", 40): "api"} {
		h := http.Header{}
		h.Set(ViaHeader, in)
		if got := viaFromHeader(h); got != want {
			t.Errorf("via(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClaimsPrincipal(t *testing.T) {
	p := ClaimsPrincipal(authn.ClaimsFromContext)
	if a, s := p.Actor(context.Background()), p.Scope(context.Background()); a.Kind != ActorSystem || s != "" {
		t.Errorf("no claims: actor %+v scope %q, want system and no scope", a, s)
	}
	ctx := authn.ContextWithClaims(context.Background(), &authn.Claims{UserID: "u1", OrgID: "org_1"})
	if a := p.Actor(ctx); a != (Actor{ID: "u1", Name: "u1", Kind: ActorUser}) {
		t.Errorf("claims without email: %+v", a)
	}
	if s := p.Scope(ctx); s != "org_1" {
		t.Errorf("scope = %q, want the claims' org", s)
	}
}

// A guessed caller fails silently: an app whose auth does not stash forge's
// claims would record every event as system:anonymous. So the interceptor
// has no default Principal, and building one without it is a boot error.
func TestInterceptorRequiresPrincipal(t *testing.T) {
	ic, err := Interceptor(Deps{})
	if err == nil || ic != nil {
		t.Fatalf("Interceptor(Deps{}) = %v, %v; want a refusal", ic, err)
	}
	if !strings.Contains(err.Error(), "Deps.Principal is required") || !strings.Contains(err.Error(), "ClaimsPrincipal") {
		t.Errorf("the refusal must name the field and the fix: %v", err)
	}
}

// A project's own Principal is what every event records — the seam an app
// whose auth keeps the caller its own way (not as forge claims) fills. The
// harness still stashes forge claims for X-Test-User; the event must carry the
// project's answer, not those.
func TestInterceptorRecordsTheProjectsPrincipal(t *testing.T) {
	w := &memWriter{}
	opt := promoteDecl()
	opt.Scope = ""
	md := fixtureRPC(t, fixtureMethod{name: "Promote", activity: opt})
	h := newHarness(t, md, Deps{Writer: w, Principal: appPrincipal{}}, promoteHandler)
	if err := h.call(map[string]string{"release_id": "rel_1"}, map[string]string{"X-Test-User": "sean"}); err != nil {
		t.Fatal(err)
	}
	evs := w.got()
	if len(evs) != 1 {
		t.Fatalf("wrote %d events, want 1", len(evs))
	}
	if got := evs[0]; got.Actor != (Actor{ID: "tok_1", Name: "ci", Kind: ActorMachine}) || got.Scope != "workspace_7" {
		t.Errorf("event actor %+v scope %q, want the project's machine tok_1 in workspace_7", got.Actor, got.Scope)
	}
}

// appPrincipal is a project's own answer: a CI machine credential in a workspace.
type appPrincipal struct{}

func (appPrincipal) Actor(context.Context) Actor  { return Machine("tok_1", "ci") }
func (appPrincipal) Scope(context.Context) string { return "workspace_7" }
