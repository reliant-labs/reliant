// Copyright (c) 2025 Reliant Labs
package core

import "time"

// How a chat came to be linked to a pull request (chat_pr_links.source).
const (
	// ChatPRLinkWorktreePR: CreateWorktreePR opened it from the chat's
	// worktree.
	ChatPRLinkWorktreePR = "worktree_pr"
	// ChatPRLinkIntegration: a run of the chat opened it through the GitHub
	// integration (a workflow's github/pr.create step or the agent's tool),
	// recorded from the integration write ledger.
	ChatPRLinkIntegration = "integration"
	// ChatPRLinkHeadRef: inferred when the pull request's record first
	// appeared, from its head branch matching the chat's worktree. The only
	// inferred source: every other one is explicit and beats it.
	ChatPRLinkHeadRef = "head_ref"
)

// ChatPRLinkGitHub is the one provider links are kept for today: the
// provider id pr_records and the integrations use.
const ChatPRLinkGitHub = "github"

// ChatPRLinkExplicit reports whether source was written where the pull
// request was made, rather than inferred from its branch.
func ChatPRLinkExplicit(source string) bool { return source != ChatPRLinkHeadRef }

// PRRef names one pull request the way every link writer knows it.
type PRRef struct {
	// Provider is the forge, today always "github".
	Provider string
	// Repository is owner/name. Writers lowercase it.
	Repository string
	Number     int
}

// ChatPRLink is one chat's link to one pull request (chat_pr_links). It
// holds facts only: no title, body, branch or URL.
type ChatPRLink struct {
	ID     string
	ChatID string
	// UserID and ProjectID are the chat's owner and project when the link
	// was written.
	UserID    string
	ProjectID string
	// RunID is the run (workflows.id) whose step opened the pull request;
	// "" when no run did.
	RunID string
	PRRef
	// RepositoryID is the provider's stable id for the repository, once a
	// pr_records row has named it; "" until then.
	RepositoryID string
	// Source is one of the ChatPRLink* constants.
	Source    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ChatPRLinkWrite is an explicit link to write: the pull request, the chats
// that produced it, and how.
type ChatPRLinkWrite struct {
	PRRef
	// ChatIDs are the chats to link. A chat that does not exist (or was
	// deleted) is skipped, not an error.
	ChatIDs []string
	// RunID is the run (workflows.id) that opened it, or "".
	RunID string
	// Source is an explicit source: ChatPRLinkWorktreePR or
	// ChatPRLinkIntegration.
	Source string
	At     time.Time
}
