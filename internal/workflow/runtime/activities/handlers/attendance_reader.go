// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
)

// Attendance answers whether a chat's next call has nobody behind it, by the
// rule every call made for the chat is scoped with (chatAttendance,
// org-provider-accounts §11.1), for a reader outside the activities that must
// judge a chat the way its calls are judged: GetPolicyImpact's preview of an
// org's automations policy.
type Attendance struct {
	Store AttendanceStore
}

// ChatUnattended reports that chat's next call is an unattended run's: a run
// a stored trigger launched (deleted since or not) that nobody has sent a
// message into, or an agent-started run whose lineage root is one. A chat a
// person adopted is attended. Attendance that cannot be decided is
// unattended, as the chat's calls are then scoped (fail closed).
func (a Attendance) ChatUnattended(ctx context.Context, chat *db.Chat) bool {
	return chatAttended(ctx, a.Store, chat) != verdictAttended
}
