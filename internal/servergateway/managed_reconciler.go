// Copyright (c) 2025 Reliant Labs

package servergateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/reliant-labs/reliant/internal/logging"
)

const (
	reconcilerLogPrefix = "[ManagedReconciler]"

	streamDaemonState         = "DAEMON_STATE"
	subjectManagedDaemonState = "daemon.v1.state.managed"
	consumerDaemonState       = "gateway-daemon-state"

	// consumerCreateRetryInitial / consumerCreateRetryMax bound the backoff
	// used while waiting for the DAEMON_STATE stream to exist. The stream is
	// owned and created by the control plane (see control-plane's
	// natsio.ensureDaemonStateStream); on a cold `forge up` the gateway can
	// boot before the control plane has run EnsureStreams, so creating our
	// consumer must tolerate the stream being absent and retry rather than
	// failing the reconciler permanently.
	consumerCreateRetryInitial = 500 * time.Millisecond
	consumerCreateRetryMax     = 10 * time.Second
)

// ManagedDaemonState mirrors the control-plane's published struct.
type ManagedDaemonState struct {
	DaemonID string `json:"daemonId"`
	UserID   string `json:"userId"`
	PodIP    string `json:"podIp"`
	PodPort  int    `json:"podPort"`
}

type managedDaemonStateSnapshot struct {
	Version   int                  `json:"version"`
	Timestamp time.Time            `json:"timestamp"`
	Daemons   []ManagedDaemonState `json:"daemons"`
}

// ManagedDaemonReconciler consumes the DAEMON_STATE snapshot stream and
// brings the gateway's outbound attachments into alignment with the
// authoritative desired set. Snapshots are the source of truth — anything
// not in the latest snapshot is disconnected; anything in it but not
// currently attached is dialed.
//
// Runs alongside DaemonConnector's edge-triggered DAEMON_COMMANDS consumer.
// The edge trigger remains for low-latency first-attach; the reconciler is
// the eventual-correctness layer.
type ManagedDaemonReconciler struct {
	js        jetstream.JetStream
	connector *DaemonConnector

	mu        sync.Mutex
	lastSnap  time.Time
	lastCount int
}

// NewManagedDaemonReconciler constructs a reconciler bound to the given
// JetStream context and DaemonConnector.
func NewManagedDaemonReconciler(js jetstream.JetStream, connector *DaemonConnector) *ManagedDaemonReconciler {
	return &ManagedDaemonReconciler{
		js:        js,
		connector: connector,
	}
}

// Start blocks until ctx is cancelled, consuming DAEMON_STATE messages and
// reconciling on each one.
func (r *ManagedDaemonReconciler) Start(ctx context.Context) error {
	consumer, err := r.createConsumerWithRetry(ctx, r.createConsumer)
	if err != nil {
		return fmt.Errorf("create consumer: %w", err)
	}

	logging.Info(reconcilerLogPrefix + " started — subscribing to " + subjectManagedDaemonState)
	defer r.connector.CloseAll()

	cc, err := consumer.Consume(func(msg jetstream.Msg) {
		r.handleMessage(ctx, msg)
	})
	if err != nil {
		return fmt.Errorf("start consuming: %w", err)
	}
	defer cc.Stop()

	<-ctx.Done()
	logging.Info(reconcilerLogPrefix + " stopped")
	return nil
}

// createConsumer creates (or updates) the durable DAEMON_STATE consumer.
func (r *ManagedDaemonReconciler) createConsumer(ctx context.Context) (jetstream.Consumer, error) {
	return r.js.CreateOrUpdateConsumer(ctx, streamDaemonState, jetstream.ConsumerConfig{
		Durable:        consumerDaemonState,
		AckPolicy:      jetstream.AckExplicitPolicy,
		FilterSubjects: []string{subjectManagedDaemonState},
		// DeliverNew + replay-on-startup behavior: with MaxMsgs=1 on the
		// stream there's at most one message ever, so any new consumer
		// immediately gets the latest snapshot.
		DeliverPolicy: jetstream.DeliverLastPolicy,
	})
}

// createConsumerWithRetry invokes create until it succeeds, ctx is cancelled,
// or a non-retryable error occurs. A missing DAEMON_STATE stream
// (ErrStreamNotFound / API err_code 10059 / "stream not found") is the one
// retryable case: the stream is owned by the control plane and may not exist
// yet on a cold boot. Backoff grows exponentially up to consumerCreateRetryMax
// so the gateway converges without manual restart once the stream appears.
func (r *ManagedDaemonReconciler) createConsumerWithRetry(
	ctx context.Context,
	create func(context.Context) (jetstream.Consumer, error),
) (jetstream.Consumer, error) {
	backoff := consumerCreateRetryInitial
	logged := false
	for {
		consumer, err := create(ctx)
		if err == nil {
			return consumer, nil
		}
		if !isStreamNotFound(err) {
			// Non-retryable: surface immediately.
			return nil, err
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !logged {
			logging.Info(reconcilerLogPrefix +
				" DAEMON_STATE stream not found yet — waiting for control plane to create it; retrying consumer creation")
			logged = true
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		if backoff < consumerCreateRetryMax {
			backoff *= 2
			if backoff > consumerCreateRetryMax {
				backoff = consumerCreateRetryMax
			}
		}
	}
}

// isStreamNotFound reports whether err indicates the target stream does not
// (yet) exist. The nats.go client normalizes the 404 / err_code 10059 API
// error into jetstream.ErrStreamNotFound for consumer creation; matching the
// sentinel also matches a raw *jetstream.APIError carrying the same code.
func isStreamNotFound(err error) bool {
	return errors.Is(err, jetstream.ErrStreamNotFound)
}

func (r *ManagedDaemonReconciler) handleMessage(ctx context.Context, msg jetstream.Msg) {
	var snap managedDaemonStateSnapshot
	if err := json.Unmarshal(msg.Data(), &snap); err != nil {
		logging.Error(reconcilerLogPrefix+" unmarshal snapshot failed", "error", err)
		_ = msg.Term()
		return
	}
	_ = msg.Ack()
	r.reconcile(ctx, snap)
}

func (r *ManagedDaemonReconciler) reconcile(ctx context.Context, snap managedDaemonStateSnapshot) {
	desired := make(map[string]ManagedDaemonState, len(snap.Daemons))
	for _, d := range snap.Daemons {
		if d.DaemonID == "" {
			continue
		}
		desired[d.DaemonID] = d
	}

	// Snapshot the connector's current outbound attachments.
	r.connector.mu.Lock()
	active := make(map[string]bool, len(r.connector.activeConns))
	for id := range r.connector.activeConns {
		active[id] = true
	}
	r.connector.mu.Unlock()

	started := 0
	stopped := 0

	// Start any desired that aren't currently active.
	for daemonID, d := range desired {
		if active[daemonID] {
			continue
		}
		if d.PodIP == "" || d.PodPort == 0 {
			continue
		}
		r.connector.startConnection(ctx, d.DaemonID, d.UserID, d.PodIP, d.PodPort)
		started++
	}

	// Stop any active that aren't in the desired set.
	for daemonID := range active {
		if _, ok := desired[daemonID]; !ok {
			r.connector.stopConnection(daemonID)
			stopped++
		}
	}

	r.mu.Lock()
	r.lastSnap = time.Now()
	r.lastCount = len(desired)
	r.mu.Unlock()

	if started > 0 || stopped > 0 {
		logging.Info(reconcilerLogPrefix+" reconciled snapshot",
			"desired", len(desired), "active", len(active), "started", started, "stopped", stopped)
	}
}
