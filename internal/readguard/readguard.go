// Copyright (c) 2025 Reliant Labs

package readguard

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"runtime/debug"
	"slices"
	"strconv"
	"sync"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/logging"
)

// Defaults for a zero Limits field.
const (
	DefaultPerUser     = 4
	DefaultQueue       = 32
	DefaultWait        = 2 * time.Second
	DefaultReadTimeout = 30 * time.Second
	DefaultRetryAfter  = time.Second
)

// Read names the unary read procedure whose response message is *Res. The
// guard needs only the name (it takes the request's message type from the
// call's Spec); Res keeps each declaration naming the read it guards.
func Read[Res any](procedure string) Procedure {
	return read[Res]{name: procedure}
}

// read is the Procedure Read builds.
type read[Res any] struct{ name string }

func (r read[Res]) Name() string { return r.name }

func (c Limits) withDefaults() Limits {
	if c.PerUser <= 0 {
		c.PerUser = DefaultPerUser
	}
	if c.Queue <= 0 {
		c.Queue = DefaultQueue
	}
	if c.Wait <= 0 {
		c.Wait = DefaultWait
	}
	if c.ReadTimeout <= 0 {
		c.ReadTimeout = DefaultReadTimeout
	}
	if c.RetryAfter <= 0 {
		c.RetryAfter = DefaultRetryAfter
	}
	return c
}

// New returns the guard for deps' procedures.
func New(deps Deps) Service {
	g := &guard{cfg: deps.Limits.withDefaults(), procs: make(map[string]Procedure, len(deps.Procedures)), users: map[string]*userReads{}}
	for _, p := range deps.Procedures {
		g.procs[p.Name()] = p
	}
	return g
}

type guard struct {
	cfg   Limits
	procs map[string]Procedure

	mu    sync.Mutex
	users map[string]*userReads
}

// userReads is one user's reads in flight and waiting. It exists only while
// the user has either; mu guards it.
type userReads struct {
	flights map[string]*flight
	waiting int
	// changed is closed, and replaced, whenever a read starts (a waiter may
	// join it) or ends (a slot is free).
	changed chan struct{}
}

// flight is one shared read.
type flight struct {
	done chan struct{}
	resp *answer
	err  error
	// callers still waiting for it; when the last one leaves, cancel stops the
	// read.
	callers int
	cancel  context.CancelFunc
}

// answer is what a shared read sent: its response message and the response
// metadata the handler set, held apart from every caller's own call.
type answer struct {
	msg     proto.Message
	header  http.Header
	trailer http.Header
}

func (g *guard) Intercept(next connect.ServerFunc) connect.ServerFunc {
	return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
		proc, ok := g.procs[spec.Procedure]
		if !ok || spec.StreamType != connect.StreamTypeUnary {
			return next(ctx, spec, stream)
		}
		userID, _ := auth.GetUserIDFromContext(ctx)
		req := newRequest(spec)
		if userID == "" || req == nil {
			return next(ctx, spec, stream)
		}
		if err := stream.Receive(req); err != nil {
			return err
		}
		body, err := proto.MarshalOptions{Deterministic: true}.Marshal(req)
		if err != nil {
			return next(ctx, spec, &replayStream{ServerStream: stream, req: req})
		}
		run := func(rctx context.Context) (*answer, error) {
			own := &replayStream{req: req}
			if err := next(rctx, spec, own); err != nil {
				return nil, err
			}
			if own.resp == nil {
				return nil, nil
			}
			ans := &answer{msg: own.resp}
			if info, ok := connect.CallInfoForServerContext(rctx); ok {
				ans.header, ans.trailer = cloneHeader(info.ResponseHeader()), cloneHeader(info.ResponseTrailer())
			}
			return ans, nil
		}
		ans, err := g.do(ctx, userID, proc.Name()+"\x00"+string(body), proc, run)
		if err != nil || ans == nil {
			return err
		}
		if info, ok := connect.CallInfoForServerContext(ctx); ok {
			addHeader(info.ResponseHeader(), ans.header)
			addHeader(info.ResponseTrailer(), ans.trailer)
		}
		return stream.Send(proto.Clone(ans.msg))
	}
}

// newRequest is an empty request message of spec's procedure, or nil when the
// spec does not say (a hand-built spec with no protobuf schema).
func newRequest(spec connect.Spec) proto.Message {
	md, ok := spec.Schema.(protoreflect.MethodDescriptor)
	if !ok {
		return nil
	}
	mt, err := protoregistry.GlobalTypes.FindMessageByName(md.Input().FullName())
	if err != nil {
		return nil
	}
	return mt.New().Interface()
}

// replayStream is the stream a guarded handler runs on: it receives the
// request the guard already read, and keeps the response for the guard to
// hand every caller. With a ServerStream (a request passed through unshared)
// it sends on that stream instead.
type replayStream struct {
	connect.ServerStream
	req      proto.Message
	received bool
	resp     proto.Message
}

func (s *replayStream) Receive(msg any) error {
	if s.received {
		return io.EOF
	}
	dst, ok := msg.(proto.Message)
	if !ok {
		return fmt.Errorf("readguard: cannot receive into %T", msg)
	}
	s.received = true
	proto.Reset(dst)
	proto.Merge(dst, s.req)
	return nil
}

func (s *replayStream) SendHeaders() error {
	if s.ServerStream != nil {
		return s.ServerStream.SendHeaders()
	}
	return nil
}

func (s *replayStream) Send(msg any) error {
	if s.ServerStream != nil {
		return s.ServerStream.Send(msg)
	}
	m, ok := msg.(proto.Message)
	if !ok {
		return fmt.Errorf("readguard: cannot send %T", msg)
	}
	s.resp = m
	return nil
}

// do answers one caller's read: from an identical read in flight, from a read
// it starts, or with a refusal.
func (g *guard) do(ctx context.Context, userID, key string, proc Procedure, run func(context.Context) (*answer, error)) (*answer, error) {
	var deadline <-chan time.Time
	queued := false

	g.mu.Lock()
	u := g.users[userID]
	if u == nil {
		u = &userReads{flights: map[string]*flight{}, changed: make(chan struct{})}
		g.users[userID] = u
	}
	for {
		if f := u.flights[key]; f != nil {
			f.callers++
			if queued {
				u.waiting--
			}
			g.mu.Unlock()
			annotate(ctx, "joined")
			return g.await(ctx, userID, u, f, proc)
		}
		if len(u.flights) < g.cfg.PerUser {
			if queued {
				u.waiting--
			}
			f := g.startLocked(ctx, userID, u, key, run)
			g.mu.Unlock()
			annotate(ctx, "led")
			return g.await(ctx, userID, u, f, proc)
		}
		if !queued {
			if u.waiting >= g.cfg.Queue {
				g.mu.Unlock()
				return nil, g.refuse(ctx, userID, proc, "queue full")
			}
			u.waiting++
			queued = true
			timer := time.NewTimer(g.cfg.Wait)
			defer timer.Stop()
			deadline = timer.C
		}
		changed := u.changed
		g.mu.Unlock()

		select {
		case <-changed:
			g.mu.Lock()
		case <-deadline:
			g.mu.Lock()
			u.waiting--
			g.forgetIfIdleLocked(userID, u)
			g.mu.Unlock()
			return nil, g.refuse(ctx, userID, proc, "no slot free")
		case <-ctx.Done():
			g.mu.Lock()
			u.waiting--
			g.forgetIfIdleLocked(userID, u)
			g.mu.Unlock()
			return nil, ctx.Err()
		}
	}
}

// startLocked starts the read for key in a goroutine of its own, on a context
// detached from the caller's cancellation (but carrying its values: the
// authenticated user, the trace) and bounded by ReadTimeout. The read gets a
// call info of its own (ownCall), so nothing it writes lands on the call that
// started it, which may have ended by the time the read finishes.
func (g *guard) startLocked(ctx context.Context, userID string, u *userReads, key string, run func(context.Context) (*answer, error)) *flight {
	rctx, cancel := context.WithTimeout(ownCall(context.WithoutCancel(ctx)), g.cfg.ReadTimeout)
	f := &flight{done: make(chan struct{}), callers: 1, cancel: cancel}
	u.flights[key] = f
	g.signalLocked(u)
	go func() {
		defer cancel()
		resp, err := runSafely(rctx, run)
		g.mu.Lock()
		f.resp, f.err = resp, err
		delete(u.flights, key)
		g.signalLocked(u)
		g.forgetIfIdleLocked(userID, u)
		g.mu.Unlock()
		close(f.done)
	}()
	return f
}

// await waits for f's answer, or for the caller to give up. The last caller
// to give up cancels the read. The answer is shared, never written: each
// caller sends a clone of its message and copies its metadata, and a v2
// *connect.Error (or any svcerr wrapper) is immutable.
func (g *guard) await(ctx context.Context, userID string, u *userReads, f *flight, proc Procedure) (*answer, error) {
	select {
	case <-f.done:
	case <-ctx.Done():
		g.mu.Lock()
		f.callers--
		if f.callers == 0 {
			f.cancel()
		}
		g.mu.Unlock()
		return nil, ctx.Err()
	}
	return f.resp, f.err
}

// The caller's recovery interceptor cannot catch a panic on this goroutine.
func runSafely(ctx context.Context, run func(context.Context) (*answer, error)) (resp *answer, err error) {
	defer func() {
		if p := recover(); p != nil {
			logging.ErrorContext(ctx, "[ReadGuard] list handler panicked", "panic", p, "stack", string(debug.Stack()))
			resp = nil
			err = svcerr.ToConnectV2(svcerr.Internal(svcerr.InternalMessage))
		}
	}()
	return run(ctx)
}

func (g *guard) signalLocked(u *userReads) {
	close(u.changed)
	u.changed = make(chan struct{})
}

func (g *guard) forgetIfIdleLocked(userID string, u *userReads) {
	if len(u.flights) == 0 && u.waiting == 0 && g.users[userID] == u {
		delete(g.users, userID)
	}
}

func (g *guard) refuse(ctx context.Context, userID string, proc Procedure, why string) error {
	annotate(ctx, "refused")
	logging.WarnContext(ctx, "[ReadGuard] refused a list read: the user has too many running",
		"userID", userID, "procedure", proc.Name(), "reason", why,
		"perUser", g.cfg.PerUser, "queue", g.cfg.Queue)
	retryAfter := strconv.Itoa(int(max(g.cfg.RetryAfter.Round(time.Second), time.Second) / time.Second))
	return svcerr.ToConnectV2(svcerr.WithMetadata(
		svcerr.ResourceExhausted("too many list requests at once; retry shortly"), "Retry-After", retryAfter))
}

// annotate records on the request's span how the guard answered it.
func annotate(ctx context.Context, outcome string) {
	trace.SpanFromContext(ctx).SetAttributes(attribute.String("reliant.read_guard", outcome))
}

// ownCall returns ctx with a server call info of its own in place of the
// call's: the same spec, peer and protocol, a copy of the request headers, and
// empty response metadata. connect has no constructor for a server call
// context, so the call's info is swapped where ctx yields it.
func ownCall(ctx context.Context) context.Context {
	shared, ok := connect.CallInfoForServerContext(ctx)
	if !ok {
		return ctx
	}
	own := &connect.CallInfo{
		Spec:             shared.Spec,
		PeerAddr:         shared.PeerAddr,
		Protocol:         shared.Protocol,
		Codec:            shared.Codec,
		RequestEncoding:  shared.RequestEncoding,
		ResponseEncoding: shared.ResponseEncoding,
		TransportInfo:    shared.TransportInfo,
	}
	addHeader(own.RequestHeader(), cloneHeader(shared.RequestHeader()))
	return callInfoContext{Context: ctx, shared: shared, own: own}
}

// callInfoContext yields own wherever its parent yields the shared call info.
type callInfoContext struct {
	context.Context
	shared, own *connect.CallInfo
}

func (c callInfoContext) Value(key any) any {
	v := c.Context.Value(key)
	if info, ok := v.(*connect.CallInfo); ok && info == c.shared {
		return c.own
	}
	return v
}

func cloneHeader(h *connect.Header) http.Header {
	if h.Len() == 0 {
		return nil
	}
	out := make(http.Header, h.Len())
	for k, v := range h.All() {
		out[k] = slices.Clone(v)
	}
	return out
}

func addHeader(dst *connect.Header, src http.Header) {
	for k, vs := range src {
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}
