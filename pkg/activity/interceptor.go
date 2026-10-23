package activity

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/reliant-labs/forge/pkg/auth"
	"github.com/reliant-labs/forge/pkg/orm"
)

// ViaHeader is the request header a client names itself in: "console",
// "cli", "ci". Browsers sending it need it in the CORS allow-list.
const ViaHeader = "Forge-Via"

// ViaAPI is the via recorded when a request names no client.
const ViaAPI = "api"

// DefaultWriteTimeout bounds one interceptor write when Deps.WriteTimeout is
// zero.
const DefaultWriteTimeout = 2 * time.Second

// Principal is who is calling and the scope the call acts in: the one thing
// the interceptor cannot know, because the app's auth decides where it keeps
// the caller. It is declared here, where it is consumed, and the app
// implements it over its own auth context — control-plane's activitystream,
// for one, resolves a machine credential acting for a person to that person.
//
// It is REQUIRED ([Interceptor] refuses a nil one). There is no default
// because a guessed one fails silently: a project whose auth does not stash
// forge's claims would have every event recorded as system:anonymous with an
// empty scope, and nothing would say so. A project on forge/pkg/authn names
// that choice explicitly with [ClaimsPrincipal](authn.ClaimsFromContext).
type Principal interface {
	// Actor is the caller of the call on ctx.
	Actor(ctx context.Context) Actor
	// Scope is the scope (an org, a project) of an event whose declaration
	// names no scope path.
	Scope(ctx context.Context) string
}

// ClaimsLookup reads the authenticated caller's forge claims from a request
// context: authn.ClaimsFromContext, or a project's own reader when its auth
// stashes claims under a key it owns. It is the same shape as
// middleware.ClaimsLookup and serverkit.CallerLookup.
type ClaimsLookup = func(ctx context.Context) (*auth.Claims, bool)

// ClaimsPrincipal is the [Principal] of an app whose auth puts forge claims on
// the context. The actor is a user named by email (or id); a call with no
// claims — one the auth policy let through anonymously — is the system
// ("anonymous"). The scope is the claims' OrgID. A project whose principals
// include machines (CI tokens, API keys) implements Principal itself, since
// only it knows which callers are machines.
func ClaimsPrincipal(lookup ClaimsLookup) Principal {
	return claimsPrincipal{lookup: lookup}
}

type claimsPrincipal struct{ lookup ClaimsLookup }

func (p claimsPrincipal) Actor(ctx context.Context) Actor {
	c, ok := p.lookup(ctx)
	if !ok || c == nil || c.UserID == "" {
		return System("anonymous")
	}
	name := c.Email
	if name == "" {
		name = c.UserID
	}
	return User(c.UserID, name)
}

func (p claimsPrincipal) Scope(ctx context.Context) string {
	if c, ok := p.lookup(ctx); ok && c != nil {
		return c.OrgID
	}
	return ""
}

// Deps configures [Interceptor]. Principal is required; every other field has
// a working default.
type Deps struct {
	// Principal names each call's caller and scope. Required: see
	// [Principal] for why there is no default.
	Principal Principal
	// Writer persists events. nil is LogWriter(Logger): events are logged,
	// not stored.
	Writer Writer
	// ViaFrom names the channel a request came through. nil is
	// [DefaultVia].
	ViaFrom func(ctx context.Context, req connect.AnyRequest) string
	// Logger reports failed writes and malformed declarations. nil is
	// slog.Default().
	Logger *slog.Logger
	// Meter counts failed writes (forge_activity_write_failures_total). nil
	// is the global meter provider's.
	Meter metric.Meter
	// WriteTimeout bounds one write. Zero is DefaultWriteTimeout.
	WriteTimeout time.Duration
}

// DefaultVia is the request's Forge-Via header, lowercased and limited to a
// short token, or "api" when absent or malformed.
func DefaultVia(_ context.Context, req connect.AnyRequest) string {
	return viaFromHeader(req.Header())
}

func viaFromHeader(h http.Header) string {
	v := strings.ToLower(strings.TrimSpace(h.Get(ViaHeader)))
	if v == "" || len(v) > 32 || !viaRE(v) {
		return ViaAPI
	}
	return v
}

func viaRE(v string) bool {
	for _, r := range v {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

// Interceptor returns the connect interceptor that writes each declared
// activity event. Put it after authentication, as close to the handler as
// the chain allows (serverkit.ChainDeps.Activity does both). It fails when
// Deps.Principal is nil: an interceptor that cannot name the caller would
// record every event as anonymous, so it is a boot error instead.
//
// Per unary call it resolves the method's declaration once ([Resolve],
// cached per procedure), runs the handler, and on success extracts the
// declared fields from the request and response and writes the event,
// best-effort: a write failure is logged and counted and the RPC still
// succeeds. A failed call writes nothing. An rpc declaring nothing, or
// `none`, passes straight through.
func Interceptor(deps Deps) (connect.Interceptor, error) {
	if deps.Principal == nil {
		return nil, errors.New("activity.Interceptor: Deps.Principal is required — it names who is calling. " +
			"An app on forge/pkg/authn passes activity.ClaimsPrincipal(authn.ClaimsFromContext); " +
			"an app whose auth keeps the caller its own way implements activity.Principal over it")
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.Writer == nil {
		deps.Writer = LogWriter(deps.Logger)
	}
	if deps.ViaFrom == nil {
		deps.ViaFrom = DefaultVia
	}
	if deps.WriteTimeout <= 0 {
		deps.WriteTimeout = DefaultWriteTimeout
	}
	if deps.Meter == nil {
		deps.Meter = otel.Meter("github.com/reliant-labs/forge/pkg/activity")
	}
	failures, err := deps.Meter.Int64Counter("forge_activity_write_failures_total",
		metric.WithDescription("Activity events the interceptor failed to write."))
	if err != nil {
		deps.Logger.Warn("activity: failure counter unavailable", "error", err)
	}
	return &interceptor{deps: deps, failures: failures}, nil
}

type interceptor struct {
	deps     Deps
	failures metric.Int64Counter
	// decls caches each procedure's resolution.
	decls sync.Map // procedure -> *resolution
}

type resolution struct {
	decl *Declaration
	err  error
	once sync.Once // logs err once
}

func (i *interceptor) resolve(spec connect.Spec) *resolution {
	if r, ok := i.decls.Load(spec.Procedure); ok {
		return r.(*resolution)
	}
	r := &resolution{}
	md, ok := spec.Schema.(protoreflect.MethodDescriptor)
	if ok {
		r.decl, r.err = Resolve(md)
	}
	actual, _ := i.decls.LoadOrStore(spec.Procedure, r)
	return actual.(*resolution)
}

// active returns the declaration to act on, or nil.
func (i *interceptor) active(spec connect.Spec) *Declaration {
	if spec.IsClient {
		return nil
	}
	r := i.resolve(spec)
	if r.err != nil {
		r.once.Do(func() {
			i.deps.Logger.Error("activity: malformed declaration; no events are recorded for this rpc (forge lint names the fix)",
				"procedure", spec.Procedure, "error", r.err)
		})
		return nil
	}
	if r.decl == nil || r.decl.None != "" {
		return nil
	}
	return r.decl
}

func (i *interceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		decl := i.active(req.Spec())
		if decl == nil {
			return next(ctx, req)
		}
		c := i.newCall(ctx, decl, req)
		resp, err := next(context.WithValue(ctx, callKey{}, c), req)
		if err != nil || c.recorded.Load() {
			return resp, err
		}
		if decl.Writer == WriterHandler {
			i.deps.Logger.WarnContext(ctx, "activity: rpc declares writer HANDLER but its handler did not call activity.Record; the interceptor wrote the event",
				"procedure", c.procedure)
		}
		var respMsg protoreflect.Message
		if resp != nil {
			if m, ok := resp.Any().(proto.Message); ok {
				respMsg = m.ProtoReflect()
			}
		}
		ev := c.event(respMsg)
		if ev.Target.ID == "" {
			i.deps.Logger.WarnContext(ctx, "activity: declared target id is empty in this call; the event is recorded without one",
				"procedure", c.procedure, "path", decl.TargetID.Spec)
		}
		i.write(ctx, ev)
		return resp, nil
	}
}

func (i *interceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

// WrapStreamingHandler installs the call record a HANDLER-written stream's
// activity.Record call reads. The interceptor itself writes nothing for a
// stream: there is no single request and response to extract fields from,
// which is why Resolve requires streaming declarations to be HANDLER.
func (i *interceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		decl := i.active(conn.Spec())
		if decl == nil {
			return next(ctx, conn)
		}
		c := &call{
			decl:      decl,
			actor:     i.deps.Principal.Actor(ctx),
			via:       viaFromHeader(conn.RequestHeader()),
			scope:     i.deps.Principal.Scope(ctx),
			procedure: conn.Spec().Procedure,
		}
		err := next(context.WithValue(ctx, callKey{}, c), conn)
		if err == nil && !c.recorded.Load() {
			i.deps.Logger.WarnContext(ctx, "activity: streaming rpc succeeded without calling activity.Record; no event was written",
				"procedure", c.procedure)
		}
		return err
	}
}

func (i *interceptor) newCall(ctx context.Context, decl *Declaration, req connect.AnyRequest) *call {
	c := &call{
		decl:      decl,
		actor:     i.deps.Principal.Actor(ctx),
		via:       i.deps.ViaFrom(ctx, req),
		scope:     i.deps.Principal.Scope(ctx),
		procedure: req.Spec().Procedure,
	}
	if m, ok := req.Any().(proto.Message); ok {
		c.req = m.ProtoReflect()
	}
	if sc := trace.SpanContextFromContext(ctx); sc.HasTraceID() {
		c.traceID = sc.TraceID().String()
	}
	return c
}

// write persists ev off the caller's cancellation, bounded by the timeout:
// the mutation has committed, so a client hanging up must not drop its event.
func (i *interceptor) write(ctx context.Context, ev Event) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), i.deps.WriteTimeout)
	defer cancel()
	ev.normalize()
	if err := i.deps.Writer.Write(ctx, ev); err != nil {
		if i.failures != nil {
			i.failures.Add(ctx, 1, metric.WithAttributes(attribute.String("procedure", ev.Procedure)))
		}
		i.deps.Logger.ErrorContext(ctx, "activity: write failed; the rpc succeeded and its event is lost",
			"procedure", ev.Procedure, "verb", ev.Verb, "target_kind", ev.Target.Kind,
			"target_id", ev.Target.ID, "error", err)
	}
}

// callKey keys the in-flight call record in a handler's context.
type callKey struct{}

// call is what the interceptor knows about one in-flight declared rpc.
type call struct {
	decl      *Declaration
	actor     Actor
	via       string
	scope     string
	procedure string
	traceID   string
	req       protoreflect.Message
	// recorded flips once the handler's activity.Record commits.
	recorded atomic.Bool
}

// event builds the declared event from the request and, when given, the
// response. Paths into an absent message yield "".
func (c *call) event(resp protoreflect.Message) Event {
	pick := func(p Path) string {
		if p.Response {
			return p.value(resp)
		}
		return p.value(c.req)
	}
	ev := Event{
		Scope:     c.scope,
		Actor:     c.actor,
		Verb:      c.decl.Verb,
		Target:    Target{Kind: c.decl.TargetKind, ID: pick(c.decl.TargetID), Label: pick(c.decl.TargetLabel)},
		Env:       pick(c.decl.Env),
		Via:       c.via,
		Procedure: c.procedure,
		TraceID:   c.traceID,
	}
	if !c.decl.Scope.IsZero() {
		ev.Scope = pick(c.decl.Scope)
	}
	for _, p := range c.decl.Detail {
		if v := pick(p); v != "" {
			if ev.Detail == nil {
				ev.Detail = map[string]string{}
			}
			ev.Detail[p.Key()] = v
		}
	}
	return ev
}

// Record writes ev through w. It is the explicit write: for actors that are
// not RPCs (a converger, an alert transition, CI ingestion), and for a
// handler whose rpc declares `writer: ACTIVITY_WRITER_HANDLER`.
//
// Inside a declared rpc, every field ev leaves zero is filled from the call:
// verb and target kind from the declaration, actor, via and scope from the
// caller, and target id, label, env and detail from the request paths
// (response paths cannot be read yet: the handler is still running). Outside
// one, ev must be complete.
//
// Inside a RunTx, a [Store] backed by *orm.Client writes in the transaction,
// so the event commits or rolls back with the mutation; the call counts as
// recorded only once that transaction commits. Record returns the validation
// or write error; a handler decides whether its mutation should fail with it.
func Record(ctx context.Context, w Writer, ev Event) error {
	if w == nil {
		return errors.New("activity: Record needs a Writer")
	}
	c, _ := ctx.Value(callKey{}).(*call)
	if c != nil {
		c.fill(&ev)
	}
	ev.normalize()
	if err := ev.validate(); err != nil {
		return err
	}
	if err := w.Write(ctx, ev); err != nil {
		return err
	}
	if c != nil {
		mark := func(context.Context) { c.recorded.Store(true) }
		if err := orm.AfterCommit(ctx, mark); errors.Is(err, orm.ErrNoTransaction) {
			c.recorded.Store(true)
		}
	}
	return nil
}

// Skip records that this call, although it succeeded, changed nothing worth
// an activity event, so the interceptor writes none for it: an idempotent
// "ensure" rpc that found its object already there, a dry run, a no-op
// update. It reports whether ctx is a declared rpc's context; outside one it
// does nothing.
//
// A declaration states what an rpc records when it changes something; Skip is
// the handler's way to say this call did not. Inside a RunTx the skip takes
// effect only if the transaction commits, like Record.
func Skip(ctx context.Context) bool {
	c, _ := ctx.Value(callKey{}).(*call)
	if c == nil {
		return false
	}
	mark := func(context.Context) { c.recorded.Store(true) }
	if err := orm.AfterCommit(ctx, mark); errors.Is(err, orm.ErrNoTransaction) {
		c.recorded.Store(true)
	}
	return true
}

// fill sets ev's zero fields from the call.
func (c *call) fill(ev *Event) {
	declared := c.event(nil)
	if ev.Verb == "" {
		ev.Verb = declared.Verb
	}
	if ev.Target.Kind == "" {
		ev.Target.Kind = declared.Target.Kind
	}
	if ev.Target.ID == "" {
		ev.Target.ID = declared.Target.ID
	}
	if ev.Target.Label == "" {
		ev.Target.Label = declared.Target.Label
	}
	if ev.Actor == (Actor{}) {
		ev.Actor = declared.Actor
	}
	if ev.Env == "" {
		ev.Env = declared.Env
	}
	if ev.Via == "" {
		ev.Via = declared.Via
	}
	if ev.Scope == "" {
		ev.Scope = declared.Scope
	}
	if ev.Procedure == "" {
		ev.Procedure = declared.Procedure
	}
	if ev.TraceID == "" {
		ev.TraceID = declared.TraceID
	}
	for k, v := range declared.Detail {
		if ev.Detail == nil {
			ev.Detail = map[string]string{}
		}
		if _, set := ev.Detail[k]; !set {
			ev.Detail[k] = v
		}
	}
}
