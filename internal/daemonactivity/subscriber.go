// Copyright (c) 2025 Reliant Labs

// Package daemonactivity implements the gateway side of the cross-process
// activity ping. Anyone (e.g. the control-plane LLM proxy) can publish to
// `daemon.v1.activity.user.<userID>` to signal "this user is doing something
// right now". The gateway bumps lastActivity for every daemon connection it
// holds for that user, so the pull-RPC status query reflects real usage and
// not just the 15s heartbeat cadence.
//
// Fire-and-forget: no JetStream, no replay, no reply. If the signal is lost
// the next heartbeat or the next inbound/outbound message will close the gap.
//
// forge:exclude-contract
//
// Leaf utility package: the exported surface is concrete helpers over the
// stdlib or the OS, with no collaborator to fake and no second implementation.
// An interface here would have exactly one implementor and one caller shape,
// which is indirection without a seam.
package daemonactivity

import (
	"strings"
	"sync"

	"github.com/nats-io/nats.go"

	"github.com/reliant-labs/reliant/internal/logging"
)

const (
	// Subject is the prefix for per-user activity ping subjects. Publishers
	// append the userID as the next token: daemon.v1.activity.user.<userID>.
	// Hard-coded in both repos — keep them in sync.
	Subject = "daemon.v1.activity.user."

	// SubjectWildcard is the subscription pattern that captures all userIDs.
	SubjectWildcard = Subject + ">"

	logPrefix = "[daemonactivity]"
)

// SubjectForUser returns the NATS subject for the given userID.
func SubjectForUser(userID string) string {
	return Subject + userID
}

// UserTarget is the subset of ToolsDaemonService the subscriber needs.
type UserTarget interface {
	TouchDaemonsForUser(userID string)
}

// Subscriber holds the active NATS subscription. Safe for concurrent use.
type Subscriber struct {
	mu  sync.Mutex
	sub *nats.Subscription
}

// NewSubscriber returns an unstarted Subscriber.
func NewSubscriber() *Subscriber {
	return &Subscriber{}
}

// Start subscribes to the wildcard subject and forwards each message's
// trailing userID token to target.TouchDaemonsForUser.
func (s *Subscriber) Start(nc *nats.Conn, target UserTarget) error {
	if s == nil {
		return nil
	}
	sub, err := nc.Subscribe(SubjectWildcard, func(msg *nats.Msg) {
		userID := strings.TrimPrefix(msg.Subject, Subject)
		if userID == "" || strings.Contains(userID, ".") {
			// Malformed: extra tokens or empty. Ignore — publisher bug.
			logging.Warn(logPrefix+" ignoring activity ping with malformed subject",
				"subject", msg.Subject)
			return
		}
		target.TouchDaemonsForUser(userID)
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.sub = sub
	s.mu.Unlock()
	return nil
}

// Stop tears down the subscription. Safe to call multiple times.
func (s *Subscriber) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	sub := s.sub
	s.sub = nil
	s.mu.Unlock()
	if sub != nil {
		_ = sub.Unsubscribe()
	}
}
