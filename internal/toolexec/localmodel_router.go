// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/observability"
)

func localModelRequestSubjectFor(userID, daemonID string) string {
	return daemonSubject(localModelRequestSubject, userID, daemonID)
}

func localModelCancelSubjectFor(userID, daemonID string) string {
	return daemonSubject(localModelCancelSubject, userID, daemonID)
}

func localModelRefreshSubjectFor(userID, daemonID string) string {
	return daemonSubject(localModelRefreshSubject, userID, daemonID)
}

func localModelChunkSubjectFor(userID, requestID string) string {
	return localModelChunkSubject + "." + userID + "." + requestID
}

// localModelIdleTimeout is how long the idle window is for a request: the
// request's own idle_timeout_ms when set, otherwise the default.
func localModelIdleTimeout(req *reliantv1.LocalModelHTTPRequest) time.Duration {
	if ms := req.GetIdleTimeoutMs(); ms > 0 {
		return time.Duration(ms) * time.Millisecond
	}
	return defaultLocalModelIdleTimeout
}

// LocalModelHTTPStream yields the ordered chunks of one relayed local-model
// HTTP response. Next must be driven by one goroutine; Close may be called
// from any goroutine and cancels the request if it has not completed.
type LocalModelHTTPStream struct {
	nc        *nats.Conn
	userID    string
	daemonID  string
	requestID string
	sub       *nats.Subscription
	idle      time.Duration

	ctx       context.Context
	cancelCtx context.CancelFunc

	nextSeq  uint64
	finished atomic.Bool
	closed   atomic.Bool
	closeMu  sync.Mutex
}

// RequestID is the id the relay assigned to this request.
func (s *LocalModelHTTPStream) RequestID() string { return s.requestID }

// DaemonID is the daemon serving this request.
func (s *LocalModelHTTPStream) DaemonID() string { return s.daemonID }

// Next returns the next chunk in order. After the chunk with done=true has been
// returned it returns io.EOF. A daemon-reported failure arrives as a chunk with
// Error set (the caller decides what it means); Next itself errors only for
// transport problems: the caller's context ending, the idle timeout, a gap in
// the sequence, or a slow-consumer drop.
func (s *LocalModelHTTPStream) Next() (*reliantv1.LocalModelHTTPChunk, error) {
	if s.finished.Load() {
		return nil, io.EOF
	}
	ctx, cancel := context.WithTimeout(s.ctx, s.idle)
	defer cancel()
	msg, err := s.sub.NextMsgWithContext(ctx)
	if err != nil {
		switch {
		case s.ctx.Err() != nil || s.closed.Load():
			if cause := s.ctx.Err(); cause != nil && !s.closed.Load() {
				return nil, cause
			}
			return nil, context.Canceled
		case errors.Is(err, context.DeadlineExceeded):
			return nil, fmt.Errorf("%w: daemon %s sent nothing for %s", ErrLocalModelIdleTimeout, s.daemonID, s.idle)
		case errors.Is(err, nats.ErrSlowConsumer):
			return nil, fmt.Errorf("local model relay via daemon %s dropped chunks (consumer too slow): %w", s.daemonID, err)
		default:
			return nil, fmt.Errorf("local model relay via daemon %s: %w", s.daemonID, err)
		}
	}
	var chunk reliantv1.LocalModelHTTPChunk
	if err := proto.Unmarshal(msg.Data, &chunk); err != nil {
		return nil, fmt.Errorf("local model relay via daemon %s: undecodable chunk: %w", s.daemonID, err)
	}
	if chunk.GetSequence() != s.nextSeq {
		return nil, fmt.Errorf("local model relay via daemon %s: chunk out of order (got %d, want %d)",
			s.daemonID, chunk.GetSequence(), s.nextSeq)
	}
	s.nextSeq++
	if chunk.GetDone() {
		s.finished.Store(true)
	}
	return &chunk, nil
}

// Close releases the stream. If the response had not completed, the daemon is
// told to abort the in-flight request. Idempotent.
func (s *LocalModelHTTPStream) Close() {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if s.closed.Swap(true) {
		return
	}
	s.cancelCtx()
	_ = s.sub.Unsubscribe()
	if s.finished.Load() {
		return
	}
	payload, err := json.Marshal(localModelCancelWire{RequestID: s.requestID})
	if err != nil {
		return
	}
	if err := s.nc.Publish(localModelCancelSubjectFor(s.userID, s.daemonID), payload); err != nil {
		observability.NATSErrorsTotal.WithLabelValues("daemon.localmodel.cancel", "publish").Inc()
		return
	}
	observability.NATSPublishTotal.WithLabelValues("daemon.localmodel.cancel").Inc()
}

// OpenLocalModelHTTP relays one HTTP request to a local model endpoint on the
// named daemon's machine. The chunk subscription is established BEFORE the
// request is published so the head chunk cannot be lost. The returned stream
// is bound to ctx: cancelling ctx aborts the request on the daemon.
//
// A daemon that is offline (nothing subscribed for it) or that does not accept
// the request fails fast with *LocalModelDaemonUnavailableError.
func (r *NATSDaemonRouter) OpenLocalModelHTTP(ctx context.Context, userID, daemonID string, req *reliantv1.LocalModelHTTPRequest) (*LocalModelHTTPStream, error) {
	if userID == "" || daemonID == "" {
		return nil, fmt.Errorf("local model relay requires a user id and a daemon id")
	}
	if req == nil || req.GetEndpointId() == "" {
		return nil, fmt.Errorf("local model relay requires an endpoint id")
	}
	if r.nc == nil {
		return nil, fmt.Errorf("local model relay: nats connection is nil")
	}

	outgoing := proto.Clone(req).(*reliantv1.LocalModelHTTPRequest)
	if outgoing.RequestId == "" {
		outgoing.RequestId = uuid.NewString()
	}
	data, err := proto.Marshal(outgoing)
	if err != nil {
		return nil, fmt.Errorf("marshal local model request: %w", err)
	}
	if len(data) > maxChunkedRequestBytes {
		return nil, fmt.Errorf("local model request: %s", oversizeNATSPayloadError("request", len(data), maxChunkedRequestBytes, oversizeRequestHint))
	}

	streamCtx, cancelCtx := context.WithCancel(ctx)
	stream := &LocalModelHTTPStream{
		nc:        r.nc,
		userID:    userID,
		daemonID:  daemonID,
		requestID: outgoing.RequestId,
		idle:      localModelIdleTimeout(outgoing) + localModelWorkerIdleGrace,
		ctx:       streamCtx,
		cancelCtx: cancelCtx,
	}

	// Subscribe first: the head chunk may follow the ack by microseconds.
	chunkSub, err := r.nc.SubscribeSync(localModelChunkSubjectFor(userID, outgoing.RequestId))
	if err != nil {
		cancelCtx()
		return nil, fmt.Errorf("subscribe to local model chunks via NATS: %w", err)
	}
	_ = chunkSub.SetPendingLimits(8192, 64<<20)
	stream.sub = chunkSub

	fail := func(err error) (*LocalModelHTTPStream, error) {
		cancelCtx()
		_ = chunkSub.Unsubscribe()
		return nil, err
	}

	ackSub, err := r.nc.SubscribeSync(r.nc.NewRespInbox())
	if err != nil {
		return fail(fmt.Errorf("subscribe to local model ack via NATS: %w", err))
	}
	defer func() { _ = ackSub.Unsubscribe() }()

	reqMsg := observability.NATSPublishMsg(ctx, localModelRequestSubjectFor(userID, daemonID), data)
	reqMsg.Reply = ackSub.Subject
	if _, err := publishChunkedRequest(r.nc, reqMsg); err != nil {
		observability.NATSErrorsTotal.WithLabelValues("daemon.localmodel.request", "publish").Inc()
		return fail(fmt.Errorf("publish local model request via NATS: %w", err))
	}
	observability.NATSPublishTotal.WithLabelValues("daemon.localmodel.request").Inc()

	ackCtx, ackCancel := context.WithTimeout(ctx, localModelAckTimeout)
	defer ackCancel()
	ackMsg, err := ackSub.NextMsgWithContext(ackCtx)
	switch {
	case errors.Is(err, nats.ErrNoResponders):
		return fail(&LocalModelDaemonUnavailableError{DaemonID: daemonID, Reason: "daemon is not connected"})
	case err != nil && ctx.Err() != nil:
		return fail(ctx.Err())
	case err != nil:
		return fail(&LocalModelDaemonUnavailableError{DaemonID: daemonID, Reason: fmt.Sprintf("no answer from the daemon gateway within %s", localModelAckTimeout)})
	}
	if ackMsg.Header.Get("Status") == "503" {
		return fail(&LocalModelDaemonUnavailableError{DaemonID: daemonID, Reason: "daemon is not connected"})
	}
	var ack localModelAck
	if err := json.Unmarshal(ackMsg.Data, &ack); err != nil {
		return fail(fmt.Errorf("local model relay via daemon %s: bad ack: %w", daemonID, err))
	}
	if !ack.OK {
		return fail(&LocalModelDaemonUnavailableError{DaemonID: daemonID, Reason: ack.Error})
	}
	return stream, nil
}

// RefreshLocalModels asks the daemon to re-probe its local model servers and
// waits (bounded to about LocalModelRefreshTimeout) for the next inventory the
// daemon sends.
func (r *NATSDaemonRouter) RefreshLocalModels(ctx context.Context, userID, daemonID string) (*reliantv1.LocalModelInventory, error) {
	if userID == "" || daemonID == "" {
		return nil, fmt.Errorf("local model refresh requires a user id and a daemon id")
	}
	if r.nc == nil {
		return nil, fmt.Errorf("local model refresh: nats connection is nil")
	}
	msg := observability.NATSPublishMsg(ctx, localModelRefreshSubjectFor(userID, daemonID), nil)
	// The bridge bounds its own wait at LocalModelRefreshTimeout; the extra
	// margin lets its (more informative) reply win over our timeout.
	reply, err := r.nc.RequestMsgWithContext(withTimeoutCap(ctx, LocalModelRefreshTimeout+3*time.Second), msg)
	if err != nil {
		if errors.Is(err, nats.ErrNoResponders) {
			return nil, &LocalModelDaemonUnavailableError{DaemonID: daemonID, Reason: "daemon is not connected"}
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &LocalModelDaemonUnavailableError{DaemonID: daemonID, Reason: fmt.Sprintf("no inventory within %s", LocalModelRefreshTimeout)}
	}
	var out localModelRefreshReply
	if err := json.Unmarshal(reply.Data, &out); err != nil {
		return nil, fmt.Errorf("local model refresh via daemon %s: bad reply: %w", daemonID, err)
	}
	if out.Error != "" {
		return nil, &LocalModelDaemonUnavailableError{DaemonID: daemonID, Reason: out.Error}
	}
	var inv reliantv1.LocalModelInventory
	if len(out.Inventory) > 0 {
		if err := protojson.Unmarshal(out.Inventory, &inv); err != nil {
			return nil, fmt.Errorf("local model refresh via daemon %s: bad inventory: %w", daemonID, err)
		}
	}
	return &inv, nil
}

// withTimeoutCap returns a context bounded by d. The cancel is intentionally
// dropped into the context's own timer: the context is used for one request.
func withTimeoutCap(ctx context.Context, d time.Duration) context.Context {
	capped, cancel := context.WithTimeout(ctx, d)
	go func() {
		<-capped.Done()
		cancel()
	}()
	return capped
}
