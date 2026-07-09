// Copyright (c) 2025 Reliant Labs

package daemonquery

import (
	"encoding/json"
	"sync"

	"github.com/nats-io/nats.go"

	"github.com/reliant-labs/reliant/internal/logging"
)

// UserLivenessSource is implemented by the gateway's connection registry
// (ToolsDaemonService). The responder calls it during request handling to
// read current in-memory state — the source of truth for the streams this
// gateway owns.
type UserLivenessSource interface {
	// ConnectedDaemonCountForUser returns how many daemon streams this
	// gateway currently holds for the user. 0 means none (e.g. teardown race
	// between the request arriving and the subscription tearing down).
	ConnectedDaemonCountForUser(userID string) int
}

// UserResponder satisfies toolexec.DaemonConnectionListener and manages one
// NATS subscription per user with at least one connected daemon, on the
// per-user any-live subject (SubjectUserAnyLive). Safe for concurrent calls.
//
// Multi-replica semantics: every gateway replica holding at least one stream
// for the user subscribes to the same subject; NATS request/reply picks one,
// and any of them answering live=true is correct. A replica whose LAST daemon
// for the user disconnects must UNSUBSCRIBE — never answer live=false — so a
// different replica that still holds a stream can answer. No responders at
// all therefore means "no daemon connected for this user anywhere", which is
// the correct aggregate.
type UserResponder struct {
	nc     *nats.Conn
	source UserLivenessSource

	mu sync.Mutex
	// userDaemons tracks which daemonIDs this responder has seen connect per
	// user, so re-registrations of the same daemonID stay idempotent and the
	// subscription is torn down exactly when the LAST one disconnects.
	userDaemons map[string]map[string]struct{}
	subs        map[string]*nats.Subscription
}

// NewUserResponder builds a UserResponder. The source must be set; the
// responder holds a reference to it for the lifetime of each subscription.
func NewUserResponder(nc *nats.Conn, source UserLivenessSource) *UserResponder {
	return &UserResponder{
		nc:          nc,
		source:      source,
		userDaemons: make(map[string]map[string]struct{}),
		subs:        make(map[string]*nats.Subscription),
	}
}

// OnDaemonConnected records the daemon under its user and, if this is the
// user's first connected daemon on this gateway (or a previous subscribe
// attempt failed), subscribes to the user's any-live query subject.
func (r *UserResponder) OnDaemonConnected(userID, daemonID string) {
	if r == nil || r.nc == nil || userID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	set := r.userDaemons[userID]
	if set == nil {
		set = make(map[string]struct{})
		r.userDaemons[userID] = set
	}
	set[daemonID] = struct{}{}

	if r.subs[userID] != nil {
		return // already subscribed for this user
	}
	subject := SubjectUserAnyLive(userID)
	sub, err := r.nc.Subscribe(subject, r.handle(userID))
	if err != nil {
		// Leave the daemonID recorded; the next connect for this user retries
		// the subscribe (subs[userID] is still nil).
		logging.Warn(logPrefix+" failed to subscribe to user any-live subject",
			"userID", userID, "subject", subject, "error", err)
		return
	}
	r.subs[userID] = sub
}

// OnDaemonDisconnected removes the daemon from its user's set and, when the
// LAST daemon for the user disconnects, unsubscribes from the any-live
// subject. Subsequent requests get no responder from this replica — which is
// the correct signal, and leaves the floor to replicas that still hold a
// stream for the user.
func (r *UserResponder) OnDaemonDisconnected(userID, daemonID string) {
	if r == nil || userID == "" {
		return
	}
	var sub *nats.Subscription
	r.mu.Lock()
	if set := r.userDaemons[userID]; set != nil {
		delete(set, daemonID)
		if len(set) == 0 {
			delete(r.userDaemons, userID)
			sub = r.subs[userID]
			delete(r.subs, userID)
		}
	}
	r.mu.Unlock()
	if sub != nil {
		_ = sub.Unsubscribe()
	}
}

// CloseAll unsubscribes every active subscription. Call on shutdown.
func (r *UserResponder) CloseAll() {
	if r == nil {
		return
	}
	r.mu.Lock()
	subs := r.subs
	r.subs = make(map[string]*nats.Subscription)
	r.userDaemons = make(map[string]map[string]struct{})
	r.mu.Unlock()
	for _, s := range subs {
		_ = s.Unsubscribe()
	}
}

func (r *UserResponder) handle(userID string) nats.MsgHandler {
	return func(msg *nats.Msg) {
		count := r.source.ConnectedDaemonCountForUser(userID)
		// Race: the last stream may have torn down between the message being
		// routed here and us handling it. Do NOT reply live=false — stay
		// silent so a different gateway replica that still holds a stream can
		// answer; no responders at all reads as not-live, which is the
		// correct aggregate.
		if count <= 0 {
			return
		}
		payload, err := json.Marshal(UserLiveness{Live: true, Count: count})
		if err != nil {
			logging.Warn(logPrefix+" failed to marshal user liveness",
				"userID", userID, "error", err)
			return
		}
		if msg.Reply == "" {
			return // not a request — nothing to reply to
		}
		if err := msg.Respond(payload); err != nil {
			logging.Warn(logPrefix+" failed to respond to user any-live query",
				"userID", userID, "error", err)
		}
	}
}
