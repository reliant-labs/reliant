package core

import "time"

// AttentionUpdate carries a validated policy transition. Tenancy is derived
// exclusively from ChatID; StartedAt and RunID are immutable Temporal identity.
type AttentionUpdate struct {
	ChatID, RunID        string
	StartedAt            time.Time
	Phase                string
	Key, State           string
	RecoveryTimeout      time.Duration
	FailureClass, Detail string
}

// AttentionResult separates legacy-notification ownership from escalation.
type AttentionResult struct {
	Managed bool
	Notify  bool
}

// AttentionIncident is an open episode. Recovering leases expire on reads,
// independently of whether the actor ever completes.
type AttentionIncident struct {
	UserID, ProjectID, Key, EpisodeID, State string
	ChatID, TriggerID, FailureClass, Detail  string
	OpenedAt, UpdatedAt                      time.Time
	LeaseUntil                               *time.Time
}
