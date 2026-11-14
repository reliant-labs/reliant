// Copyright (c) 2025 Reliant Labs

// Package authz is the one place reliant decides whether a caller may read or
// change a resource. Every handler, tool and placement check asks it instead
// of comparing user ids itself, so the rule can change in one place.
//
// The rule today is ownership: the caller is the resource's owner
// (`user_id`). A missing resource and one the caller may not see are the same
// answer, false, and every call site turns false into NotFound (or the
// PermissionDenied it already returned), so a caller cannot probe which ids
// exist.
//
// Read and Write are separate questions even though they have the same
// answer today. Organizations (control-plane docs/design/orgs-and-members.md
// §5) make the resource's org part of the decision, and read-only sharing
// (§6) lets a member read what they may not write, so each call site asks the
// question it means:
//
//   - Read: show the resource or anything derived from it (messages, runs,
//     plans, questions, the stream).
//   - Write: change it, act on it, or spend through it (send, pause, approve,
//     place work, wake or rename a machine).
//
// ctx is taken now because the org half of the decision asks control-plane
// whether the caller is a live member, which needs a context.
//
// The resource kinds that will belong to an org: chats, projects, machines,
// saved workflows (workflow_drafts), triggers and their events. A workflow
// run (the workflows table) is authorized through its chat, which every run
// has, and inherits the chat's org.
//
// Personal resources (attachments, scenarios, integration connections,
// connector grants, video jobs) are their owner's alone by design and never
// become org-visible through this package: CanAccessPersonal is the one
// spelling of that check.
//
// Store queries that filter `user_id = ?` in SQL (lists, and the
// *WithUserCheck reads) are visibility predicates, not decisions; the
// *WithUserCheck reads in internal/db confirm their row through this package
// too, so the seam sees every single-resource read.
//
//forge:exclude-contract: pure decisions over rows the caller already read; no state, no I/O, nothing to mock
package authz

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// chatRecord is either read of a chat: the activity view (core.Chat) or the
// bare row (core.ChatRow). Both carry the same ownership.
type chatRecord interface {
	*core.Chat | *core.ChatRow
}

// resource is what a decision reads from a row: whether there is one, and
// who owns it.
type resource struct {
	present     bool
	ownerUserID string
}

// decide is the rule. A caller with no id never owns anything, so an empty
// caller and an unowned row are not a match.
func decide(_ context.Context, userID string, r resource) bool {
	return r.present && userID != "" && r.ownerUserID == userID
}

func chatResource[C chatRecord](chat C) resource {
	switch c := any(chat).(type) {
	case *core.Chat:
		if c != nil {
			return resource{present: true, ownerUserID: c.UserID}
		}
	case *core.ChatRow:
		if c != nil {
			return resource{present: true, ownerUserID: c.UserID}
		}
	}
	return resource{}
}

// CanReadChat reports whether userID may see chat and what hangs off it:
// its messages, runs, plans, tasks, questions and live stream. nil is false.
func CanReadChat[C chatRecord](ctx context.Context, userID string, chat C) bool {
	return decide(ctx, userID, chatResource(chat))
}

// CanWriteChat reports whether userID may change chat or act in it: send,
// branch, pause, resume, approve, answer, place its work. nil is false.
func CanWriteChat[C chatRecord](ctx context.Context, userID string, chat C) bool {
	return decide(ctx, userID, chatResource(chat))
}

func projectResource(p *core.Project) resource {
	if p == nil {
		return resource{}
	}
	return resource{present: true, ownerUserID: p.UserID}
}

// CanReadProject reports whether userID may see project. nil is false.
func CanReadProject(ctx context.Context, userID string, project *core.Project) bool {
	return decide(ctx, userID, projectResource(project))
}

// CanWriteProject reports whether userID may change project or run work in
// it: its worktrees, terminals, checkouts and chats. nil is false.
func CanWriteProject(ctx context.Context, userID string, project *core.Project) bool {
	return decide(ctx, userID, projectResource(project))
}

func machineResource(d *core.Daemon) resource {
	if d == nil {
		return resource{}
	}
	return resource{present: true, ownerUserID: d.UserID}
}

// CanReadMachine reports whether userID may see machine's registration.
// nil is false.
func CanReadMachine(ctx context.Context, userID string, machine *core.Daemon) bool {
	return decide(ctx, userID, machineResource(machine))
}

// CanWriteMachine reports whether userID may act on machine: run work or a
// terminal on it, browse its files, wake, rename or clean it. nil is false.
func CanWriteMachine(ctx context.Context, userID string, machine *core.Daemon) bool {
	return decide(ctx, userID, machineResource(machine))
}

func workflowResource(w *core.WorkflowDraft) resource {
	if w == nil {
		return resource{}
	}
	return resource{present: true, ownerUserID: w.UserID}
}

// CanReadWorkflow reports whether userID may see a saved workflow
// definition, or run it in a chat they may write. nil is false.
func CanReadWorkflow(ctx context.Context, userID string, workflow *core.WorkflowDraft) bool {
	return decide(ctx, userID, workflowResource(workflow))
}

// CanWriteWorkflow reports whether userID may change, rename, publish or
// delete a saved workflow definition. nil is false.
func CanWriteWorkflow(ctx context.Context, userID string, workflow *core.WorkflowDraft) bool {
	return decide(ctx, userID, workflowResource(workflow))
}

func triggerResource(t *core.Trigger) resource {
	if t == nil {
		return resource{}
	}
	return resource{present: true, ownerUserID: t.UserID}
}

// CanReadTrigger reports whether userID may see trigger. nil is false.
func CanReadTrigger(ctx context.Context, userID string, trigger *core.Trigger) bool {
	return decide(ctx, userID, triggerResource(trigger))
}

// CanWriteTrigger reports whether userID may change, enable, fire, replay or
// delete trigger: anything that makes it launch runs. nil is false.
func CanWriteTrigger(ctx context.Context, userID string, trigger *core.Trigger) bool {
	return decide(ctx, userID, triggerResource(trigger))
}

// CanReadTriggerEvent reports whether userID may see one firing of a trigger
// (or an ad hoc launch event). nil is false.
func CanReadTriggerEvent(ctx context.Context, userID string, event *core.TriggerEvent) bool {
	if event == nil {
		return decide(ctx, userID, resource{})
	}
	return decide(ctx, userID, resource{present: true, ownerUserID: event.UserID})
}

// CanAccessPersonal reports whether userID owns a personal resource whose
// owner is ownerUserID. Personal resources are never shared, so there is no
// read/write split and no org: only the owner. An empty caller owns nothing.
//
// The caller checks presence (a nil row) itself, since these rows have no
// common type.
func CanAccessPersonal(userID, ownerUserID string) bool {
	return userID != "" && ownerUserID == userID
}
