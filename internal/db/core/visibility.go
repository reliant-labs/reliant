// Copyright (c) 2025 Reliant Labs

package core

import "time"

// Visibility is who besides its owner may read a project, chat or saved
// workflow (control-plane docs/design/org-sharing.md §2). It is a closed set
// stored as text so a later value (explicit per-person shares) widens it
// without a rename.
//
// Only internal/authz decides from it and only internal/store reads or writes
// it: every other package asks authz (CanRead*, IsViewer) rather than reading
// the field, so the sharing rule lives in one place.
type Visibility string

const (
	// VisibilityPrivate is the owner alone: every row before sharing, and
	// NULL in the database.
	VisibilityPrivate Visibility = ""
	// VisibilityOrg is every live member of the row's org, read-only. A row
	// with no org cannot be shared (the database refuses it).
	VisibilityOrg Visibility = "org"
)

// Valid reports whether v is a visibility this build knows.
func (v Visibility) Valid() bool {
	return v == VisibilityPrivate || v == VisibilityOrg
}

// ShareKind is the kind of a shared resource, as share_events records it.
type ShareKind string

const (
	ShareKindProject  ShareKind = "project"
	ShareKindChat     ShareKind = "chat"
	ShareKindWorkflow ShareKind = "workflow"
)

// ShareAction is one change of a resource's visibility.
type ShareAction string

const (
	// ShareActionShare is the owner sharing it with the org.
	ShareActionShare ShareAction = "share"
	// ShareActionUnshare is the owner making it private again.
	ShareActionUnshare ShareAction = "unshare"
	// ShareActionAdminUnshare is an org admin clearing a member's share.
	ShareActionAdminUnshare ShareAction = "admin_unshare"
)

// ShareChange is one visibility change of one resource and the audit row it
// writes (share_events), applied together.
type ShareChange struct {
	Kind       ShareKind
	ResourceID string
	// OrgID is the resource's org, the share's audience; OwnerUserID whose
	// resource it is. The write applies only while the row still has both,
	// so a change decided on a stale read writes nothing.
	OrgID       string
	OwnerUserID string
	// ActorUserID is who made the change: the owner, or an org admin
	// (ShareActionAdminUnshare).
	ActorUserID string
	Action      ShareAction
	At          time.Time
}

// To is the visibility the change leaves the resource with.
func (c ShareChange) To() Visibility {
	if c.Action == ShareActionShare {
		return VisibilityOrg
	}
	return VisibilityPrivate
}

// ShareEvent is one recorded visibility change (share_events).
type ShareEvent struct {
	ID          string
	OrgID       string
	Kind        ShareKind
	ResourceID  string
	OwnerUserID string
	ActorUserID string
	Action      ShareAction
	CreatedAt   time.Time
}

// SharedChatFilters lists the chats shared with an org that a viewer did not
// create.
type SharedChatFilters struct {
	// OrgID is the org whose shared chats to list (required).
	OrgID string
	// ViewerUserID is the caller: their own chats are in their own list,
	// never in the shared one (required).
	ViewerUserID string
	// ProjectID narrows the list to one project's shared chats.
	ProjectID *string
	Limit     int
	Offset    int
}

// SharedProjectFilters lists the projects shared with an org that a viewer
// did not create.
type SharedProjectFilters struct {
	OrgID        string
	ViewerUserID string
	Limit        int
	Offset       int
}
