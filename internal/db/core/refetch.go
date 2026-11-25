package core

// RefetchType identifies what data the frontend should re-fetch.
type RefetchType string

const (
	RefetchWorktreeChanges    RefetchType = "worktree_changes"
	RefetchWorkflowExecutions RefetchType = "workflow_executions"
	RefetchConfigHealth       RefetchType = "config_health"
	RefetchPlanTasks          RefetchType = "plan_tasks"
	RefetchFileTree           RefetchType = "file_tree"
	// RefetchAgentMailbox is chat-scoped: a thread in the chat gained a queued
	// agent message, so the pending-queue strip should re-read its mailbox.
	RefetchAgentMailbox RefetchType = "agent_mailbox"
	// RefetchInbox is user-scoped: the inbox gained or lost an item that no
	// chat activity change announces — an async question, which is no gate,
	// so the chat's activity does not move when one is asked or answered.
	RefetchInbox RefetchType = "inbox"
	// RefetchProjectOrgMoved is project-scoped: the project, its chats, runs
	// and triggers moved to another org (MoveProjectOrg), so every list that
	// follows the active org is stale.
	RefetchProjectOrgMoved RefetchType = "project_org_moved"
)

// RefetchData is the JSON payload of a refetch update, on either stream.
type RefetchData struct {
	Type RefetchType `json:"type"`
}

// RefetchOpts scopes a user-level refetch to the project, worktree or chat it
// is about.
type RefetchOpts struct {
	ProjectID  *string
	WorktreeID *string
	ChatID     *string
}
