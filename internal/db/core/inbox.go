package core

import "time"

// InboxPendingKind identifies which source an InboxPending row came from. The
// values are the reliantv1.InboxItemKind wire values for the kinds SQL
// produces; AUTOMATION_FAILING (4) is derived from trigger health in the
// service, not read from a table.
type InboxPendingKind int32

const (
	InboxPendingApproval          InboxPendingKind = 1
	InboxPendingQuestion          InboxPendingKind = 2
	InboxPendingWaitingForMachine InboxPendingKind = 3
	InboxPendingLaunchFailed      InboxPendingKind = 5
	InboxPendingRunPaused         InboxPendingKind = 10
)

// InboxPending is one row of the cross-chat pending list. Text1, Text2 and
// Int1 are kind-specific:
//
//	approval:        Text1 title, Int1 approval type
//	question:        Text1 thread id, Text2 metadata JSON
//	launch failed:   Text1 newest outcome detail, Text2 event kind, Int1 failures in the episode; ItemKey is the episode's first failed event
//	run paused:      Text1 the failure that paused it; ItemKey is the chat id and when that failure happened
//	waiting machine: Text1 daemon id, Text2 daemon name; ItemKey is the chat id and the block's start
type InboxPending struct {
	Kind         InboxPendingKind
	ItemKey      string
	ChatID       string
	RunID        string
	TriggerID    string
	ProjectID    string
	ProjectName  string
	WorkflowName string
	ChatTitle    string
	TriggerName  string
	WaitingSince time.Time
	Text1        string
	Text2        string
	Int1         int32
}
