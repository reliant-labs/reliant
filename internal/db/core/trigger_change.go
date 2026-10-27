package core

import "time"

// TriggerChange is an owner-scoped configuration audit entry, not a firing.
type TriggerChange struct {
	ID          string
	UserID      string
	ChatID      string
	RunID       string
	ToolCallID  string
	Operation   string
	BeforeState string
	AfterState  string
	CreatedAt   time.Time
}
