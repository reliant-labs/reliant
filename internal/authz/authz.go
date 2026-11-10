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
// ── THE ORG HALF ──────────────────────────────────────────────────────
//
// A resource that belongs to an org (its org_id is set) is the caller's only
// while they are a LIVE member of that org, so a removed member loses the
// org's chats, projects, saved workflows and triggers even though they created
// them (control-plane docs/design/orgs-and-members.md §5). Membership is
// control-plane's: the decision asks the Membership the context carries
// (WithMembership; *orgs.Resolver, cached for orgs.CacheTTL), which every
// process installs once, at its edge: the API and daemon listeners for every
// request, the worker for every activity.
//
// It FAILS CLOSED. An error asking is a denial (the call site's NotFound), and
// so is an org resource decided on a context that carries no Membership at all,
// logged at ERROR because it means a path was never wired. A Membership that
// has no answer (a deployment with no control plane, AUTH_MODE=apikey, or a
// control plane without the rpc; IsMember's ok=false) keeps today's rule:
// owner only. Rows with no org (written before orgs) are owner-only too.
//
// No sharing yet: the owner is still required. Read-only sharing (phase 5)
// widens CanRead*, never CanWrite*.
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
// too, so the seam sees every single-resource read. Owner-filtered lists keep
// the rows of an org their owner has left, so a list a caller sees passes
// through Visible as well, whether or not the request named an org.
//
//forge:exclude-contract: pure decisions over rows the caller already read; no state, no I/O, nothing to mock
package authz

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
)

// Membership answers whether userID is a live member of orgID. ok is false
// when the deployment has no answer (no orgs); err is an outage.
// *orgs.Resolver is one.
type Membership interface {
	IsMember(ctx context.Context, userID, orgID string) (member, ok bool, err error)
}

type membershipKey struct{}

// WithMembership returns ctx carrying m, which every decision about an org
// resource on that context asks. A process installs it once, at its edge.
func WithMembership(ctx context.Context, m Membership) context.Context {
	return context.WithValue(ctx, membershipKey{}, m)
}

// NoOrgs is the Membership of a deployment without orgs: it never has an
// answer, so every decision is owner-only.
type NoOrgs struct{}

// IsMember implements Membership.
func (NoOrgs) IsMember(context.Context, string, string) (bool, bool, error) { return false, false, nil }

// chatRecord is either read of a chat: the activity view (core.Chat) or the
// bare row (core.ChatRow). Both carry the same ownership.
type chatRecord interface {
	*core.Chat | *core.ChatRow
}

// resource is what a decision reads from a row: whether there is one, who
// owns it, and the org it belongs to ("" for none).
type resource struct {
	present     bool
	ownerUserID string
	orgID       string
}

func orgOf(org *string) string {
	if org == nil {
		return ""
	}
	return *org
}

// decide is the rule: the owner, and, for a resource in an org, only while
// they are a live member of it. A caller with no id never owns anything, so
// an empty caller and an unowned row are not a match.
func decide(ctx context.Context, userID string, r resource) bool {
	if !r.present || userID == "" || r.ownerUserID != userID {
		return false
	}
	if r.orgID == "" {
		return true
	}
	m, wired := ctx.Value(membershipKey{}).(Membership)
	if !wired || m == nil {
		logging.ErrorContext(ctx, "[authz] no membership on the context; denying an org resource (a path was never wired with authz.WithMembership)",
			"user_id", userID, "org_id", r.orgID)
		return false
	}
	member, ok, err := m.IsMember(ctx, userID, r.orgID)
	if err != nil {
		logging.WarnContext(ctx, "[authz] could not check org membership; denying", "user_id", userID, "org_id", r.orgID, "error", err)
		return false
	}
	if !ok {
		return true
	}
	return member
}

func chatResource[C chatRecord](chat C) resource {
	switch c := any(chat).(type) {
	case *core.Chat:
		if c != nil {
			return resource{present: true, ownerUserID: c.UserID, orgID: orgOf(c.OrgID)}
		}
	case *core.ChatRow:
		if c != nil {
			return resource{present: true, ownerUserID: c.UserID, orgID: orgOf(c.OrgID)}
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
	return resource{present: true, ownerUserID: p.UserID, orgID: orgOf(p.OrgID)}
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
	return resource{present: true, ownerUserID: w.UserID, orgID: orgOf(w.OrgID)}
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
	return resource{present: true, ownerUserID: t.UserID, orgID: orgOf(t.OrgID)}
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

// Visible returns the rows of a list that can lets userID read, in order: a
// collection shows exactly what the single-resource read would. Membership
// is cached per org (orgs.CacheTTL), so a list costs one lookup per distinct
// org, not per row. rows is not modified.
func Visible[T any](ctx context.Context, userID string, rows []T, can func(context.Context, string, T) bool) []T {
	out := make([]T, 0, len(rows))
	for _, row := range rows {
		if can(ctx, userID, row) {
			out = append(out, row)
		}
	}
	return out
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
