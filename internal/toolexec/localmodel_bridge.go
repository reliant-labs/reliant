// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/observability"
)

// LocalModelRelayManager is what the bridge needs from the gateway's daemon
// connection holder to relay local-model HTTP. It is a separate, optional
// interface (the bridge type-asserts its DaemonConnectionManager) so managers
// that never relay need no stub.
type LocalModelRelayManager interface {
	// OpenLocalModelRelay forwards req to the daemon as
	// ServerMessage.local_model_http_request and calls deliver, in order and
	// from a single goroutine, with every chunk the daemon sends back. If the
	// daemon disconnects first, deliver receives one done chunk with error
	// set. The returned closeRelay unregisters deliver; it is idempotent.
	OpenLocalModelRelay(userID, daemonID string, req *reliantv1.LocalModelHTTPRequest, deliver func(*reliantv1.LocalModelHTTPChunk)) (closeRelay func(), err error)

	// CancelLocalModelRelay sends local_model_http_cancel to the daemon.
	CancelLocalModelRelay(userID, daemonID, requestID string)

	// RefreshLocalModels sends local_model_refresh and waits (bounded by ctx)
	// for the daemon's next inventory.
	RefreshLocalModels(ctx context.Context, userID, daemonID string) (*reliantv1.LocalModelInventory, error)
}

// localModelRelayState is the bridge's bookkeeping for one in-flight relayed
// request: it re-sequences and size-splits chunks for NATS, enforces the idle
// backstop, and releases the in-flight slot exactly once.
type localModelRelayState struct {
	b         *NATSToolBridge
	mgr       LocalModelRelayManager
	userID    string
	daemonID  string
	requestID string
	idle      time.Duration

	mu       sync.Mutex
	nextSeq  uint64
	finished bool
	timer    *time.Timer
	closeFn  func()
}

// localModelSubscriptions returns the per-daemon subscriptions of the relay
// subject family. Called from OnDaemonConnected.
func (b *NATSToolBridge) localModelSubscriptions(userID, daemonID string) []*nats.Subscription {
	var subs []*nats.Subscription
	add := func(sub *nats.Subscription, err error) {
		if err != nil {
			logging.Error("[NATSToolBridge] Failed to subscribe local model subject", "userID", userID, "error", err)
			return
		}
		subs = append(subs, sub)
	}

	add(chunkedRequestSub(b.nc.Subscribe(localModelRequestSubjectFor(userID, daemonID), func(chunk *nats.Msg) {
		msg, ok := b.assembleRequest("daemon.localmodel.request", chunk, func(errMsg string) {
			respondLocalModelAck(chunk, false, errMsg)
		})
		if !ok {
			return
		}
		observability.NATSReceiveTotal.WithLabelValues("daemon.localmodel.request").Inc()
		b.handleLocalModelRequest(userID, daemonID, msg)
	})))

	add(b.nc.Subscribe(localModelCancelSubjectFor(userID, daemonID), func(msg *nats.Msg) {
		observability.NATSReceiveTotal.WithLabelValues("daemon.localmodel.cancel").Inc()
		var cancel localModelCancelWire
		if err := json.Unmarshal(msg.Data, &cancel); err != nil || cancel.RequestID == "" {
			return
		}
		if relayer, ok := b.mgr.(LocalModelRelayManager); ok {
			relayer.CancelLocalModelRelay(userID, daemonID, cancel.RequestID)
		}
		b.finishLocalModelRelay(cancel.RequestID)
	}))

	add(b.nc.Subscribe(localModelRefreshSubjectFor(userID, daemonID), func(msg *nats.Msg) {
		observability.NATSReceiveTotal.WithLabelValues("daemon.localmodel.refresh").Inc()
		b.handleAsync("daemon.localmodel.refresh", msg, func() {
			replyLocalModelRefresh(msg, nil, "gateway overloaded")
		}, func() {
			relayer, ok := b.mgr.(LocalModelRelayManager)
			if !ok {
				replyLocalModelRefresh(msg, nil, "daemon connection holder does not support local models")
				return
			}
			ctx, cancel := context.WithTimeout(b.ctx, LocalModelRefreshTimeout)
			defer cancel()
			inv, err := relayer.RefreshLocalModels(ctx, userID, daemonID)
			if err != nil {
				replyLocalModelRefresh(msg, nil, err.Error())
				return
			}
			replyLocalModelRefresh(msg, inv, "")
		})
	}))
	return subs
}

func respondLocalModelAck(msg *nats.Msg, ok bool, errMsg string) {
	data, _ := json.Marshal(localModelAck{OK: ok, Error: errMsg})
	if err := msg.Respond(data); err != nil {
		logging.Warn("[NATSToolBridge] Failed to publish local model ack", "error", err)
	}
}

func replyLocalModelRefresh(msg *nats.Msg, inv *reliantv1.LocalModelInventory, errMsg string) {
	out := localModelRefreshReply{Error: errMsg}
	if inv != nil {
		raw, err := protojson.Marshal(inv)
		if err != nil {
			out.Error = fmt.Sprintf("encode inventory: %v", err)
		} else {
			out.Inventory = raw
		}
	}
	data, _ := json.Marshal(out)
	if err := msg.Respond(data); err != nil {
		logging.Warn("[NATSToolBridge] Failed to publish local model refresh reply", "error", err)
	}
}

func (b *NATSToolBridge) handleLocalModelRequest(userID, daemonID string, msg *nats.Msg) {
	relayer, ok := b.mgr.(LocalModelRelayManager)
	if !ok {
		respondLocalModelAck(msg, false, "daemon connection holder does not support local models")
		return
	}
	var req reliantv1.LocalModelHTTPRequest
	if err := proto.Unmarshal(msg.Data, &req); err != nil || req.GetRequestId() == "" {
		respondLocalModelAck(msg, false, "malformed local model request")
		return
	}

	select {
	case b.localModelInFlight <- struct{}{}:
	default:
		logging.Error("[NATSToolBridge] Local model in-flight budget exhausted, rejecting",
			"daemonID", daemonID, "limit", maxInFlightLocalModelRequests)
		respondLocalModelAck(msg, false, "daemon gateway is overloaded with local model requests")
		return
	}

	state := &localModelRelayState{
		b: b, mgr: relayer, userID: userID, daemonID: daemonID,
		requestID: req.GetRequestId(),
		idle:      localModelIdleTimeout(&req) + localModelBridgeIdleGrace,
	}
	b.localModelMu.Lock()
	b.localModelRelays[state.requestID] = state
	b.localModelMu.Unlock()

	state.mu.Lock()
	closeFn, err := relayer.OpenLocalModelRelay(userID, daemonID, &req, state.deliver)
	if err != nil {
		state.mu.Unlock()
		state.release()
		respondLocalModelAck(msg, false, err.Error())
		return
	}
	state.closeFn = closeFn
	state.timer = time.AfterFunc(state.idle, state.onIdle)
	state.mu.Unlock()
	respondLocalModelAck(msg, true, "")
}

// finishLocalModelRelay is the cancel path: stop tracking and free the slot.
func (b *NATSToolBridge) finishLocalModelRelay(requestID string) {
	b.localModelMu.Lock()
	state := b.localModelRelays[requestID]
	b.localModelMu.Unlock()
	if state != nil {
		state.release()
	}
}

// release frees the slot and the registration exactly once.
func (s *localModelRelayState) release() {
	s.mu.Lock()
	if s.finished && s.closeFn == nil && s.timer == nil {
		s.mu.Unlock()
		return
	}
	s.finished = true
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	closeFn := s.closeFn
	s.closeFn = nil
	s.mu.Unlock()

	if closeFn != nil {
		closeFn()
	}
	s.b.localModelMu.Lock()
	_, tracked := s.b.localModelRelays[s.requestID]
	delete(s.b.localModelRelays, s.requestID)
	s.b.localModelMu.Unlock()
	if tracked {
		<-s.b.localModelInFlight
	}
}

func (s *localModelRelayState) onIdle() {
	s.deliver(&reliantv1.LocalModelHTTPChunk{
		Done:  true,
		Error: fmt.Sprintf("no data from daemon for %s (gateway idle timeout)", s.idle),
	})
	s.mgr.CancelLocalModelRelay(s.userID, s.daemonID, s.requestID)
}

// deliver publishes one daemon chunk to the worker, re-sequenced (and split if
// it would not fit a NATS message). Called in order from one goroutine.
func (s *localModelRelayState) deliver(chunk *reliantv1.LocalModelHTTPChunk) {
	s.mu.Lock()
	if s.finished {
		s.mu.Unlock()
		return
	}
	if s.timer != nil && !chunk.GetDone() {
		s.timer.Reset(s.idle)
	}
	budget := chunkPayloadBudget(s.b.nc.MaxPayload()) - 4096
	if budget < 1024 {
		budget = 1024
	}
	data := chunk.GetData()
	subject := localModelChunkSubjectFor(s.userID, s.requestID)

	first := true
	for first || len(data) > 0 {
		piece := data
		if len(piece) > budget {
			piece = piece[:budget]
		}
		data = data[len(piece):]
		out := &reliantv1.LocalModelHTTPChunk{
			RequestId: s.requestID,
			Sequence:  s.nextSeq,
			Data:      piece,
		}
		if first {
			out.Status = chunk.GetStatus()
			out.Headers = chunk.GetHeaders()
			out.Error = chunk.GetError()
		}
		last := len(data) == 0
		out.Done = chunk.GetDone() && last
		first = false
		s.nextSeq++
		raw, err := proto.Marshal(out)
		if err == nil {
			err = s.b.nc.Publish(subject, raw)
		}
		if err != nil {
			observability.NATSErrorsTotal.WithLabelValues("daemon.localmodel.chunk", "publish").Inc()
			logging.Warn("[NATSToolBridge] Failed to publish local model chunk",
				"requestID", s.requestID, "error", err)
			break
		}
		observability.NATSPublishTotal.WithLabelValues("daemon.localmodel.chunk").Inc()
	}
	done := chunk.GetDone()
	s.mu.Unlock()
	if done {
		s.release()
	}
}
