package core

import (
	"encoding/json"
	"time"
)

// TriggerDelivery is a verified, normalized inbound delivery, not a trigger
// event. Routing has not run yet. Only internal receivers and workers use it.
type TriggerDelivery struct {
	ID, Source, Provider, ScopeKey, DeliveryKey string
	ReceivedAt                                  time.Time
	Payload                                     json.RawMessage
	LeaseToken                                  string
	LeaseUntil                                  time.Time
	Failures                                    int
	CompletedAt, DeadAt                         *time.Time
	FailureCode                                 string
	EventID                                     string
	Outcome                                     TriggerEventOutcome
}

const (
	TriggerDeliveryIntegration    = "integration"
	TriggerDeliveryWebhook        = "webhook"
	TriggerDeliveryPoll           = "poll"
	TriggerDeliveryCallerNotified = "caller_notified_failure"
)

// TriggerDeliverySettlement is applied only by the holder of the lease.
type TriggerDeliverySettlement struct {
	Now         time.Time
	RetryAt     time.Time
	FailureCode string
	Terminal    bool
	EventID     string
	Outcome     TriggerEventOutcome
}
