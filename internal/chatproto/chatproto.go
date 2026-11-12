// Copyright (c) 2025 Reliant Labs

// Package chatproto is a chat's wire form: as its owner reads it (Chat), and
// reshaped for a viewer of a chat its owner shared with their org (ForViewer;
// control-plane docs/design/org-sharing.md §4).
//
//forge:exclude-contract: stateless db-to-wire conversion of a chat row; no state, nothing to mock
package chatproto

import (
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/runstateproto"
)

// Chat converts a chat row to its wire form, as its owner reads it. Model,
// temperature and max tokens are not stored on the chat (they are the
// workflow's input params); the root workflow's status comes from the
// chats_with_activity view.
func Chat(c *core.Chat) *reliantv1.Chat {
	proto := &reliantv1.Chat{
		Id:              c.ID,
		UserId:          c.UserID,
		Title:           c.Title,
		ProjectId:       c.ProjectID,
		OrgId:           c.OrgID,
		State:           c.State,
		CreatedAt:       c.CreatedAt.Format(time.RFC3339),
		UpdatedAt:       c.UpdatedAt.Format(time.RFC3339),
		LastActive:      c.LastActive.Format(time.RFC3339),
		SelectedPresets: c.SelectedPresets,
	}
	if c.WorktreeID != nil {
		proto.WorktreeId = c.WorktreeID
	}
	if c.WorkflowName != nil {
		proto.WorkflowName = c.WorkflowName
	}
	if c.WorkflowID != nil {
		proto.WorkflowId = c.WorkflowID
	}
	if c.RunID != nil {
		proto.RunId = c.RunID
	}
	if c.LastMessageAt != nil {
		formatted := c.LastMessageAt.Format(time.RFC3339)
		proto.LastMessageAt = &formatted
	}
	// Activity is the computed activity from chats_with_activity view
	if c.Activity != nil {
		proto.Activity = reliantv1.ChatActivity(*c.Activity)
	}
	proto.Unread = c.Unread
	if c.ActiveDaemonID != nil {
		proto.ActiveDaemonId = c.ActiveDaemonID
	}
	proto.NoMachine = c.NoMachine
	proto.WorkingThreads = int32(c.WorkingThreads)
	if c.UsageLimitResumesAt != nil {
		resumes := c.UsageLimitResumesAt.UTC().Format(time.RFC3339)
		proto.UsageLimitResumesAt = &resumes
		proto.UsageLimitProbing = c.UsageLimitProbing
		if c.UsageLimitProvider != "" {
			provider := c.UsageLimitProvider
			proto.UsageLimitProvider = &provider
		}
	}
	// The root run's lifecycle: the web decides paused/pending from these, and
	// a pending state is what tells it to StartChat rather than SendMessage.
	proto.WorkflowState = runstateproto.State(c.RootStatus.State)
	proto.WorkflowStopReason = runstateproto.StopReason(c.RootStatus.StopReason)
	if c.LaunchKind != "" {
		proto.LaunchKind = &c.LaunchKind
	}
	proto.Origin = c.Origin
	if c.TriggerID != nil {
		proto.TriggerId = c.TriggerID
	}
	if c.AdoptedAt != nil {
		adopted := c.AdoptedAt.Format(time.RFC3339)
		proto.AdoptedAt = &adopted
	}
	proto.Visibility = Visibility(authz.ChatVisibility(c))
	if c.SharedAt != nil {
		shared := c.SharedAt.UTC().Format(time.RFC3339)
		proto.SharedAt = &shared
	}
	return proto
}

// Visibility is v on the wire. A row's visibility is read through
// authz.ChatVisibility / ProjectVisibility, never off the row.
func Visibility(v core.Visibility) reliantv1.ResourceVisibility {
	if v == core.VisibilityOrg {
		return reliantv1.ResourceVisibility_RESOURCE_VISIBILITY_ORG
	}
	return reliantv1.ResourceVisibility_RESOURCE_VISIBILITY_PRIVATE
}

// ForViewer reshapes out, a chat as its owner reads it, for a viewer: someone
// else in its org, reading what its owner shared (control-plane
// docs/design/org-sharing.md §4). It keeps what the conversation is (title,
// state, activity, its run's status, whose it is) and leaves out what is the
// owner's alone: their composer choice (selected presets, selection), their
// workspace and its path, the machine running it, and their unread flag, and
// needs_recovery, which offers a resume only the owner can do. viewer_access
// is set, which is what tells a client to show the chat read-only.
func ForViewer(out *reliantv1.Chat) *reliantv1.Chat {
	if out == nil {
		return nil
	}
	out.ViewerAccess = true
	out.SelectedPresets = nil
	out.Selection = nil
	out.WorktreeId = nil
	out.Worktree = nil
	out.ActiveDaemonId = nil
	out.Unread = false
	out.NeedsRecovery = false
	return out
}

// ElideStepsForViewer leaves what each step produced out of steps, in place:
// its output and its error, which embed the output of commands that ran on
// the owner's machine. What the step was given (input_json) stays, as a tool
// call's input does (messageproto.ElideForViewer).
func ElideStepsForViewer(steps []*reliantv1.StepExecution) {
	for _, s := range steps {
		if s == nil {
			continue
		}
		s.OutputJson = ""
		s.ErrorMessage = ""
	}
}

// ElideWorkflowsForViewer is ElideStepsForViewer over every step of every
// workflow in a run tree.
func ElideWorkflowsForViewer(roots []*reliantv1.WorkflowExecution) {
	for _, w := range roots {
		if w == nil {
			continue
		}
		ElideStepsForViewer(w.GetSteps())
		ElideWorkflowsForViewer(w.GetChildren())
	}
}
