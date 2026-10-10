// Copyright (c) 2025 Reliant Labs
package llm

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
)

// StreamStallPhase names where in a response the content-stall guard fired.
// The two phases have different causes, different deadlines and different
// advice for the user, so the error carries which one it was.
type StreamStallPhase string

const (
	// StallAwaitingContent: no content block was open — before the first one,
	// or between two. Keepalives and nothing else here is the credit-exhaustion
	// signature: Anthropic answered 200 and pinged for 28-43 minutes without
	// ever sending message_start (those turns recorded zero prompt tokens).
	StallAwaitingContent StreamStallPhase = "awaiting_content"

	// StallMidBlock: a content block was open and the provider sent only
	// keepalives for longer than the request's whole output budget could take
	// to generate. The model had started; something wedged partway.
	StallMidBlock StreamStallPhase = "mid_block"
)

// StreamStallError is how a stream cut by the content-stall guard fails.
//
// It matches ErrStreamContentStalled under errors.Is, so every existing check
// keeps working, and adds the phase and the deadline that actually applied —
// the mid-block deadline is per-request, so a fixed constant in a message
// would misreport it. The text keeps "timeout" so autoClassify still reads it
// as transient.
type StreamStallError struct {
	Phase   StreamStallPhase
	Timeout time.Duration
}

func (e *StreamStallError) Error() string {
	if e.Phase == StallMidBlock {
		return fmt.Sprintf("llm stream content stall timeout: a content block stayed open for %s with only keepalives", e.Timeout)
	}
	return fmt.Sprintf("llm stream content stall timeout: provider sent only keepalives for %s with no content block open", e.Timeout)
}

// Is makes every stall match ErrStreamContentStalled.
func (e *StreamStallError) Is(target error) bool {
	return target == ErrStreamContentStalled
}

// DefaultStreamMidBlockStallTimeout caps how long a content block may stay open
// on keepalives alone, and is the deadline when the request's output budget is
// unknown.
//
// The awaiting-content deadline (5m) cannot serve here: it is sized to the time
// before a first token, and a redacted thinking block streams nothing for as
// long as the model thinks. Chat 622675c2's implementer (claude-5.5-sonnet,
// effort high, ~248k-token context) generated 28-38k thinking tokens a turn at
// 122-128 tok/s — 230-301s of pings per turn — so every turn that thought past
// five minutes was cut, and its five retries repeated the same thinking.
const DefaultStreamMidBlockStallTimeout = 30 * time.Minute

// streamMidBlockFloorTokensPerSecond is the slowest generation rate the
// mid-block deadline allows for: a third of the 122-128 tok/s measured above,
// so a provider running at half speed under load is still nowhere near it.
const streamMidBlockFloorTokensPerSecond = 40

// StreamMidBlockStallTimeoutEnv pins the mid-block deadline for every request,
// overriding the budget-derived value.
const StreamMidBlockStallTimeoutEnv = "RELIANT_LLM_STREAM_MID_BLOCK_STALL_TIMEOUT"

// StreamMidBlockStallTimeout returns how long a content block may stay open
// with only keepalives on a request that may generate up to maxOutputTokens
// (0 when unknown).
//
// The bound is the output budget at the floor rate: a block cannot
// legitimately take longer than generating every token the request allows,
// slowly. So a 4k-token title request is cut at the awaiting deadline, a
// 32k-token haiku turn after ~13m, and a 64k+ turn at the 30m cap. Prompt size
// does not enter into it — prefill happens before the block opens, and measured
// it is seconds even at 585k tokens.
func StreamMidBlockStallTimeout(maxOutputTokens int64) time.Duration {
	return midBlockStallTimeout(maxOutputTokens, StreamContentStallTimeout())
}

// midBlockStallTimeout takes the awaiting deadline explicitly so a transport
// whose awaiting deadline is pinned (tests) derives from that, not the env.
func midBlockStallTimeout(maxOutputTokens int64, awaiting time.Duration) time.Duration {
	if raw := strings.TrimSpace(os.Getenv(StreamMidBlockStallTimeoutEnv)); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			return d
		}
		logging.Warn("[Transport] Ignoring unusable stream mid-block stall timeout override",
			"env", StreamMidBlockStallTimeoutEnv, "value", raw)
	}

	budget := DefaultStreamMidBlockStallTimeout
	if maxOutputTokens > 0 {
		seconds := maxOutputTokens / streamMidBlockFloorTokensPerSecond
		if seconds < int64(DefaultStreamMidBlockStallTimeout/time.Second) {
			budget = time.Duration(seconds) * time.Second
		}
	}
	// A block being open is more evidence of work than no block, never less,
	// so this deadline is never the tighter of the two.
	return max(budget, awaiting)
}

type streamOutputBudgetKey struct{}

// WithStreamOutputBudget records the most tokens the request made with ctx may
// generate — the max_tokens a driver sends — so the transport can size the
// mid-block deadline to it.
func WithStreamOutputBudget(ctx context.Context, maxOutputTokens int64) context.Context {
	return context.WithValue(ctx, streamOutputBudgetKey{}, maxOutputTokens)
}

func streamOutputBudget(ctx context.Context) int64 {
	budget, _ := ctx.Value(streamOutputBudgetKey{}).(int64)
	return budget
}

// StreamLiveness is the transport's report that a stream is mid-block and its
// reader is still pulling keepalives — progress the driver has no event for.
//
// CallLLM's progress guard watches driver events, and a redacted thinking block
// produces none until it ends; without this the guard would cut, at 10 minutes,
// a block the transport still considers healthy. The report is honest about
// what it proves: the transport only sees a Read when the driver's own
// goroutine calls it, so a touch means that goroutine is alive and the provider
// is mid-block. A driver that wedges stops reading and stops touching.
type StreamLiveness struct {
	lastNanos atomic.Int64
}

// NewStreamLiveness returns a liveness record nothing has touched yet.
func NewStreamLiveness() *StreamLiveness {
	return &StreamLiveness{}
}

// Touch records liveness now. Safe on a nil receiver.
func (l *StreamLiveness) Touch() {
	if l != nil {
		l.lastNanos.Store(time.Now().UnixNano())
	}
}

// Since reports how long ago the last touch was, and false if there never was
// one.
func (l *StreamLiveness) Since() (time.Duration, bool) {
	if l == nil {
		return 0, false
	}
	last := l.lastNanos.Load()
	if last == 0 {
		return 0, false
	}
	return time.Since(time.Unix(0, last)), true
}

type streamLivenessKey struct{}

// WithStreamLiveness attaches l to ctx; the transport touches it for the
// request made with that context.
func WithStreamLiveness(ctx context.Context, l *StreamLiveness) context.Context {
	return context.WithValue(ctx, streamLivenessKey{}, l)
}

// StreamLivenessFrom returns the liveness record attached to ctx, or nil.
func StreamLivenessFrom(ctx context.Context) *StreamLiveness {
	l, _ := ctx.Value(streamLivenessKey{}).(*StreamLiveness)
	return l
}

// streamStallPolicy is one response's content-stall configuration.
type streamStallPolicy struct {
	// awaiting bounds keepalives-only while no content block is open.
	awaiting time.Duration
	// midBlock bounds keepalives-only while a content block is open.
	midBlock time.Duration
	// liveness, when set, is touched for every keepalive read mid-block.
	liveness *StreamLiveness
}

func (p streamStallPolicy) deadline(midBlock bool) time.Duration {
	if midBlock {
		return p.midBlock
	}
	return p.awaiting
}
