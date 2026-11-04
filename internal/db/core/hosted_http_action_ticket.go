package core

import "time"

// HostedHTTPActionTicket is reference-only authority for one fixed Action
// activity. WorkflowID is the runtime execution ID, NOT a definition ID or a
// claim that an immutable workflow snapshot was approved.
type HostedHTTPActionTicket struct {
	ID            string     `bun:"id"`
	WorkflowID    string     `bun:"workflow_id"`
	TemporalRunID string     `bun:"temporal_run_id"`
	ActivityID    string     `bun:"activity_id"`
	OwnerUserID   string     `bun:"owner_user_id"`
	ProjectID     string     `bun:"project_id"`
	WorkflowName  string     `bun:"workflow_name"`
	NodeID        string     `bun:"node_id"`
	GrantID       string     `bun:"grant_id"`
	RequestSHA256 string     `bun:"request_sha256"`
	CreatedAt     time.Time  `bun:"created_at"`
	ExpiresAt     time.Time  `bun:"expires_at"`
	RedeemedAt    *time.Time `bun:"redeemed_at"`
}
