// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/streaming"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// localModelsRefetchType is the ephemeral REFETCH payload type published when a
// daemon's local model inventory changes, so web clients refetch the daemon
// list (DaemonInfo.local_models).
const localModelsRefetchType = "daemon_local_models"

// Compile-time check: the gateway's connection holder is what the NATS bridge
// relays local-model HTTP through.
var _ toolexec.LocalModelRelayManager = (*ToolsDaemonService)(nil)

func (s *ToolsDaemonService) connForUser(userID, daemonID string) (*daemonConnection, error) {
	s.mu.RLock()
	conn := s.connections[daemonID]
	s.mu.RUnlock()
	if conn == nil || conn.userID != userID {
		return nil, fmt.Errorf("daemon %s is not connected for user %s", daemonID, userID)
	}
	return conn, nil
}

// OpenLocalModelRelay implements toolexec.LocalModelRelayManager.
func (s *ToolsDaemonService) OpenLocalModelRelay(userID, daemonID string, req *reliantv1.LocalModelHTTPRequest, deliver func(*reliantv1.LocalModelHTTPChunk)) (func(), error) {
	conn, err := s.connForUser(userID, daemonID)
	if err != nil {
		return nil, err
	}
	requestID := req.GetRequestId()

	conn.localModelMu.Lock()
	if conn.localModelRelays == nil {
		conn.localModelRelays = make(map[string]func(*reliantv1.LocalModelHTTPChunk))
	}
	conn.localModelRelays[requestID] = deliver
	conn.localModelMu.Unlock()

	closeRelay := func() {
		conn.localModelMu.Lock()
		delete(conn.localModelRelays, requestID)
		conn.localModelMu.Unlock()
	}

	msg := &reliantv1.ServerMessage{Message: &reliantv1.ServerMessage_LocalModelHttpRequest{LocalModelHttpRequest: req}}
	if err := s.sendToConn(conn, msg); err != nil {
		closeRelay()
		return nil, fmt.Errorf("send local model request: %w", err)
	}
	return closeRelay, nil
}

// CancelLocalModelRelay implements toolexec.LocalModelRelayManager.
func (s *ToolsDaemonService) CancelLocalModelRelay(userID, daemonID, requestID string) {
	conn, err := s.connForUser(userID, daemonID)
	if err != nil {
		return
	}
	msg := &reliantv1.ServerMessage{Message: &reliantv1.ServerMessage_LocalModelHttpCancel{
		LocalModelHttpCancel: &reliantv1.LocalModelHTTPCancel{RequestId: requestID},
	}}
	_ = s.sendToConn(conn, msg) // best effort: the daemon's own idle timeout is the backstop
}

// RefreshLocalModels implements toolexec.LocalModelRelayManager: it asks the
// daemon to re-probe and waits for the next inventory it publishes.
func (s *ToolsDaemonService) RefreshLocalModels(ctx context.Context, userID, daemonID string) (*reliantv1.LocalModelInventory, error) {
	conn, err := s.connForUser(userID, daemonID)
	if err != nil {
		return nil, err
	}
	waiter := make(chan *reliantv1.LocalModelInventory, 1)
	conn.localModelMu.Lock()
	conn.inventoryWaiters = append(conn.inventoryWaiters, waiter)
	conn.localModelMu.Unlock()
	defer func() {
		conn.localModelMu.Lock()
		for i, w := range conn.inventoryWaiters {
			if w == waiter {
				conn.inventoryWaiters = append(conn.inventoryWaiters[:i], conn.inventoryWaiters[i+1:]...)
				break
			}
		}
		conn.localModelMu.Unlock()
	}()

	msg := &reliantv1.ServerMessage{Message: &reliantv1.ServerMessage_LocalModelRefresh{LocalModelRefresh: &reliantv1.LocalModelRefresh{}}}
	if err := s.sendToConn(conn, msg); err != nil {
		return nil, fmt.Errorf("send local model refresh: %w", err)
	}

	select {
	case inv := <-waiter:
		return inv, nil
	case <-conn.done:
		return nil, fmt.Errorf("daemon %s disconnected while refreshing local models", daemonID)
	case <-ctx.Done():
		return nil, fmt.Errorf("timed out waiting for local model inventory from daemon %s: %w", daemonID, ctx.Err())
	}
}

// dispatchLocalModelChunk hands a relayed chunk to its request's consumer. It
// runs on the connection's receive goroutine, so chunks of one request are
// delivered in the order the daemon sent them. Chunks for unknown requests
// (already finished, cancelled, or never accepted) are dropped.
func (c *daemonConnection) dispatchLocalModelChunk(chunk *reliantv1.LocalModelHTTPChunk) {
	if chunk == nil {
		return
	}
	c.localModelMu.Lock()
	deliver := c.localModelRelays[chunk.GetRequestId()]
	if deliver != nil && chunk.GetDone() {
		delete(c.localModelRelays, chunk.GetRequestId())
	}
	c.localModelMu.Unlock()
	if deliver != nil {
		deliver(chunk)
	}
}

// failLocalModelRelays ends every in-flight relayed request on this connection
// with a terminal error chunk. Called when the connection is torn down.
func (c *daemonConnection) failLocalModelRelays(reason string) {
	c.localModelMu.Lock()
	relays := c.localModelRelays
	c.localModelRelays = nil
	c.localModelMu.Unlock()
	for requestID, deliver := range relays {
		deliver(&reliantv1.LocalModelHTTPChunk{RequestId: requestID, Done: true, Error: reason})
	}
}

// handleLocalModelInventory wakes refresh waiters, stores the inventory
// (skipping byte-identical republishes) and tells web clients to refetch.
func (s *ToolsDaemonService) handleLocalModelInventory(ctx context.Context, conn *daemonConnection, inv *reliantv1.LocalModelInventory) {
	if inv == nil {
		return
	}

	conn.localModelMu.Lock()
	waiters := conn.inventoryWaiters
	conn.inventoryWaiters = nil
	conn.localModelMu.Unlock()
	for _, w := range waiters {
		select {
		case w <- inv:
		default:
		}
	}

	encoded, err := protojson.MarshalOptions{}.Marshal(inv)
	if err != nil {
		logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" Failed to encode local model inventory", "error", err, "daemonID", conn.daemonID)
		return
	}
	encodedJSON := string(encoded)
	if encodedJSON == conn.lastInventoryJSON {
		return
	}
	if err := s.database.SetDaemonLocalModels(ctx, conn.daemonID, encodedJSON); err != nil {
		logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" Failed to store local model inventory", "error", err, "daemonID", conn.daemonID)
		return
	}
	conn.lastInventoryJSON = encodedJSON
	s.publishLocalModelsChanged(conn.userID, conn.daemonID)
}

// publishLocalModelsChanged emits an ephemeral REFETCH user update, the same
// kind of live signal heartbeat-carried facts (detected_ports) ride on, so the
// UI refetches the daemon list.
func (s *ToolsDaemonService) publishLocalModelsChanged(userID, daemonID string) {
	if s.userUpdateHub == nil {
		return
	}
	data, _ := json.Marshal(map[string]string{"type": localModelsRefetchType, "daemon_id": daemonID})
	s.userUpdateHub.Publish(context.Background(), streaming.UpdateEvent[db.UserUpdate]{
		Key: userID,
		Payload: db.UserUpdate{
			UserID:     userID,
			UpdateType: db.UserUpdateRefetch,
			EntityType: db.EntityTypeSystem,
			EntityID:   daemonID,
			Data:       data,
			CreatedAt:  time.Now().UTC(),
		},
	})
}
