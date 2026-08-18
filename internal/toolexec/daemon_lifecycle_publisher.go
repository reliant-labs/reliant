// forge:exclude-contract
//
// Leaf utility package: the exported surface is concrete helpers over the
// stdlib or the OS, with no collaborator to fake and no second implementation.
// An interface here would have exactly one implementor and one caller shape,
// which is indirection without a seam.
package toolexec

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/observability"
)

type daemonLifecycleNATSConn interface {
	PublishMsg(*nats.Msg) error
}

const (
	daemonEventsSubjectPrefix = "daemon.v1.events"
	daemonEventVersion        = 1
)

type daemonLifecycleEvent struct {
	Version   int       `json:"version"`
	Type      string    `json:"type"`
	UserID    string    `json:"userId"`
	DaemonID  string    `json:"daemonId"`
	Timestamp time.Time `json:"timestamp"`
}

// DaemonLifecyclePublisher publishes daemon connect/disconnect lifecycle events
// for control-plane consumers that mirror daemon state into admin-visible tables.
type DaemonLifecyclePublisher struct {
	nc daemonLifecycleNATSConn
}

func NewDaemonLifecyclePublisher(nc *nats.Conn) *DaemonLifecyclePublisher {
	return newDaemonLifecyclePublisher(nc)
}

func newDaemonLifecyclePublisher(nc daemonLifecycleNATSConn) *DaemonLifecyclePublisher {
	return &DaemonLifecyclePublisher{nc: nc}
}

func (p *DaemonLifecyclePublisher) OnDaemonConnected(userID, daemonID string) {
	p.publish(context.Background(), "connected", userID, daemonID)
}

func (p *DaemonLifecyclePublisher) OnDaemonDisconnected(userID, daemonID string) {
	p.publish(context.Background(), "disconnected", userID, daemonID)
}

func (p *DaemonLifecyclePublisher) publish(ctx context.Context, eventType, userID, daemonID string) {
	if p == nil || p.nc == nil {
		return
	}
	if userID == "" || daemonID == "" {
		logging.Warn("[DaemonLifecyclePublisher] Skipping daemon lifecycle event with missing identity", "type", eventType, "userID", userID, "daemonID", daemonID)
		return
	}

	evt := daemonLifecycleEvent{
		Version:   daemonEventVersion,
		Type:      eventType,
		UserID:    userID,
		DaemonID:  daemonID,
		Timestamp: time.Now().UTC(),
	}
	data, err := json.Marshal(evt)
	if err != nil {
		logging.Error("[DaemonLifecyclePublisher] Failed to marshal daemon lifecycle event", "error", err, "type", eventType, "daemonID", daemonID)
		return
	}

	subject := fmt.Sprintf("%s.%s", daemonEventsSubjectPrefix, eventType)
	msg := observability.NATSPublishMsg(ctx, subject, data)
	if err := p.nc.PublishMsg(msg); err != nil {
		logging.Error("[DaemonLifecyclePublisher] Failed to publish daemon lifecycle event", "error", err, "subject", subject, "daemonID", daemonID)
		return
	}
	observability.NATSPublishTotal.WithLabelValues("daemon.lifecycle").Inc()
	logging.Info("[DaemonLifecyclePublisher] Published daemon lifecycle event", "type", eventType, "userID", userID, "daemonID", daemonID)
}
