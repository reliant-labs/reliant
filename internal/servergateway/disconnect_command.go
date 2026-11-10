// Copyright (c) 2025 Reliant Labs

package servergateway

import (
	"fmt"
	"sync"

	"github.com/nats-io/nats.go"

	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/pkg/daemonwire"
)

const disconnectLogPrefix = "[DaemonDisconnect]"

// daemonDisconnecter ends the stream this replica holds for a daemon, if it
// holds one. Satisfied by *tools_daemon.Service.
type daemonDisconnecter interface {
	DisconnectDaemon(daemonID, reason string) bool
}

// DisconnectCommandSubscriber is the gateway side of the control plane's
// disconnect command (daemonwire.SubjectDisconnectCommand): when a daemon's
// credential is revoked, or its operator leaves the org, the control plane
// tells every gateway replica, and the one holding the daemon's established
// stream ends it.
//
// The subscription is a plain fan-out one, never a queue group: a queue group
// would hand the command to ONE replica, which most of the time is not the
// one holding the stream. Each replica answers only for itself.
type DisconnectCommandSubscriber struct {
	target daemonDisconnecter

	mu  sync.Mutex
	sub *nats.Subscription
}

// NewDisconnectCommandSubscriber returns an unstarted subscriber that ends
// streams through target.
func NewDisconnectCommandSubscriber(target daemonDisconnecter) *DisconnectCommandSubscriber {
	return &DisconnectCommandSubscriber{target: target}
}

// Start subscribes on nc. The subscription is registered with the server
// before Start returns, so a command published after it is not missed.
func (s *DisconnectCommandSubscriber) Start(nc *nats.Conn) error {
	sub, err := nc.Subscribe(daemonwire.SubjectDisconnectCommand, s.handle)
	if err != nil {
		return fmt.Errorf("subscribe %s: %w", daemonwire.SubjectDisconnectCommand, err)
	}
	if err := nc.Flush(); err != nil {
		_ = sub.Unsubscribe()
		return fmt.Errorf("subscribe %s: flush: %w", daemonwire.SubjectDisconnectCommand, err)
	}
	s.mu.Lock()
	s.sub = sub
	s.mu.Unlock()
	return nil
}

// handle acts on one command. A malformed one is dropped: every parse error
// is permanent, and a retry could not change it.
func (s *DisconnectCommandSubscriber) handle(msg *nats.Msg) {
	cmd, err := daemonwire.UnmarshalDisconnectCommand(msg.Data)
	if err != nil {
		logging.Warn(disconnectLogPrefix+" dropping malformed disconnect command", "error", err)
		return
	}
	if !s.target.DisconnectDaemon(cmd.DaemonID, cmd.Reason) {
		// The common case: another replica holds the stream, or the daemon
		// is not connected at all.
		logging.Debug(disconnectLogPrefix+" disconnect command for a daemon this replica does not hold",
			"daemonId", cmd.DaemonID, "requestId", cmd.RequestID, "reason", cmd.Reason)
		return
	}
	logging.Info(disconnectLogPrefix+" ended a daemon stream on the control plane's command",
		"daemonId", cmd.DaemonID, "requestId", cmd.RequestID, "reason", cmd.Reason)
}

// Stop unsubscribes. Safe to call more than once, and before Start.
func (s *DisconnectCommandSubscriber) Stop() {
	s.mu.Lock()
	sub := s.sub
	s.sub = nil
	s.mu.Unlock()
	if sub != nil {
		_ = sub.Unsubscribe()
	}
}
