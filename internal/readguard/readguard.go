// Copyright (c) 2025 Reliant Labs

package readguard

import (
	"context"
	"errors"
	"runtime/debug"
	"slices"
	"strconv"
	"sync"
	"time"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/proto"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/logging"
)

// Defaults for a zero Deps field.
const (
	DefaultPerUser     = 4
	DefaultQueue       = 32
	DefaultWait        = 2 * time.Second
	DefaultReadTimeout = 30 * time.Second
	DefaultRetryAfter  = time.Second
)

// Read names the unary read procedure whose response message is *Res.
func Read[Res any](procedure string) Procedure {
	return read[Res]{name: procedure}
}

// read is the Procedure Read builds.
type read[Res any] struct{ name string }

func (r read[Res]) Name() string { return r.name }

func (r read[Res]) Respond(msg proto.Message) connect.AnyResponse {
	return connect.NewResponse(any(msg).(*Res))
}

func (c Deps) withDefaults() Deps {
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
	cfg := deps.withDefaults()
	g := &guard{cfg: cfg, procs: make(map[string]Procedure, len(cfg.Procedures)), users: map[string]*userReads{}}
	for _, p := range cfg.Procedures {
		g.procs[p.Name()] = p
	}
	return g
}

type guard struct {
	cfg   Deps
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
	resp connect.AnyResponse
	err  error
	// callers still waiting for it; when the last one leaves, cancel stops the
	// read.
	callers int
	cancel  context.CancelFunc
}

func (g *guard) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		proc, ok := g.procs[req.Spec().Procedure]
		if !ok {
			return next(ctx, req)
		}
		userID, _ := auth.GetUserIDFromContext(ctx)
		msg, isMsg := req.Any().(proto.Message)
		if userID == "" || !isMsg {
			return next(ctx, req)
		}
		body, err := proto.MarshalOptions{Deterministic: true}.Marshal(msg)
		if err != nil {
			return next(ctx, req)
		}
		run := func(ctx context.Context) (connect.AnyResponse, error) { return next(ctx, req) }
		return g.do(ctx, userID, proc.Name()+"\x00"+string(body), proc, run)
	}
}

func (g *guard) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (g *guard) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

// do answers one caller's read: from an identical read in flight, from a read
// it starts, or with a refusal.
func (g *guard) do(ctx context.Context, userID, key string, proc Procedure, run func(context.Context) (connect.AnyResponse, error)) (connect.AnyResponse, error) {
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
// authenticated user, the trace) and bounded by ReadTimeout.
func (g *guard) startLocked(ctx context.Context, userID string, u *userReads, key string, run func(context.Context) (connect.AnyResponse, error)) *flight {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), g.cfg.ReadTimeout)
	f := &flight{done: make(chan struct{}), callers: 1, cancel: cancel}
	u.flights[key] = f
	g.signalLocked(u)
	go func() {
		defer cancel()
		resp, err := runSafely(rctx, run)
		// Connect initializes metadata lazily. Initialize while exclusively
		// owned, before publishing; the original is never returned to a waiter.
		if resp != nil {
			resp.Header()
			resp.Trailer()
		}
		var ce *connect.Error
		if errors.As(err, &ce) {
			ce.Meta()
		}
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
// to give up cancels the read.
func (g *guard) await(ctx context.Context, userID string, u *userReads, f *flight, proc Procedure) (connect.AnyResponse, error) {
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
	if f.err != nil {
		return nil, copyError(f.err)
	}
	return copyResponse(f.resp, proc), nil
}

// The caller's recovery interceptor cannot catch a panic on this goroutine.
func runSafely(ctx context.Context, run func(context.Context) (connect.AnyResponse, error)) (resp connect.AnyResponse, err error) {
	defer func() {
		if p := recover(); p != nil {
			logging.ErrorContext(ctx, "[ReadGuard] list handler panicked", "panic", p, "stack", string(debug.Stack()))
			resp = nil
			err = connect.NewError(connect.CodeInternal, errors.New("internal server error"))
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
	err := connect.NewError(connect.CodeResourceExhausted,
		errors.New("too many list requests at once; retry shortly"))
	err.Meta().Set("Retry-After", strconv.Itoa(int(max(g.cfg.RetryAfter.Round(time.Second), time.Second)/time.Second)))
	return err
}

// annotate records on the request's span how the guard answered it.
func annotate(ctx context.Context, outcome string) {
	trace.SpanFromContext(ctx).SetAttributes(attribute.String("reliant.read_guard", outcome))
}

// copyResponse is resp for one more caller: its own message and headers, so
// nothing the transport or an outer interceptor writes is shared.
func copyResponse(resp connect.AnyResponse, proc Procedure) connect.AnyResponse {
	msg, ok := resp.Any().(proto.Message)
	if !ok {
		return resp
	}
	out := proc.Respond(proto.Clone(msg))
	for k, v := range resp.Header() {
		out.Header()[k] = slices.Clone(v)
	}
	for k, v := range resp.Trailer() {
		out.Trailer()[k] = slices.Clone(v)
	}
	return out
}

// copyError is err for one more caller. A *connect.Error carries metadata an
// outer interceptor may write to, so each caller gets its own; any other error
// is a value the callers only read.
func copyError(err error) error {
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return err
	}
	cause := ce.Unwrap()
	if cause == nil {
		cause = errors.New(ce.Message())
	}
	out := connect.NewError(ce.Code(), cause)
	for k, v := range ce.Meta() {
		out.Meta()[k] = slices.Clone(v)
	}
	for _, d := range ce.Details() {
		out.AddDetail(d)
	}
	return out
}
