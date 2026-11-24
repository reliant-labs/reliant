// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// A CHAT'S TOOLS CAN RUN IN ANY OF ITS PROJECTS.
//
// A chat has a primary project (chats.project_id) and may have others
// attached (chat_projects). When it has more than one, call_llm offers the
// file, search and shell tools a `project` argument (tools.WithProjectParam)
// and lists the projects in the system prompt, and execute_tools routes each
// call (routeCallProject):
//
//   - `project` omitted, or naming the primary: exactly what a single-project
//     chat does — the chat's (or thread's) worktree, the run's path, the
//     primary's repos;
//   - naming an attached project: that project's MAIN checkout on the machine
//     the call runs on (a branch worktree is the primary's alone), and its
//     repos; a machine without one refuses the call with the reason, and the
//     conversation carries on;
//   - naming anything else: an error listing the values it accepts.
//
// `project` is taken out of the input before dispatch, so the daemon — of any
// version — receives an ordinary single-project request. The recorded call
// keeps it, so the UI can show which project a call ran in.

// ChatProjectLister reads the projects attached to a chat beside its primary,
// oldest first (db.Repo.ListChatProjects).
type ChatProjectLister interface {
	ListChatProjects(ctx context.Context, chatID string) ([]*core.ChatProject, error)
}

// projectReader reads a project row.
type projectReader interface {
	GetProject(ctx context.Context, id string) (*db.Project, error)
}

// chatProject is one of the projects a chat's tools can run in.
type chatProject struct {
	project *db.Project
	primary bool
	// value is what `project` names it by: its name, or its id when it has
	// no name or another of the chat's projects has the same one.
	value string
}

// chatProjects is the chat's projects, its primary first and then its
// attachments oldest first. A chat whose attachments cannot be read is its
// primary alone, as every chat was before attachments; an attachment whose
// project cannot be read is left out. Either way a call naming it is answered
// with the projects that are left.
func chatProjects(ctx context.Context, lister ChatProjectLister, projects projectReader, chat *db.Chat, primary *db.Project) []chatProject {
	out := []chatProject{{project: primary, primary: true}}
	if lister != nil && projects != nil && chat != nil && !chat.NoMachine {
		attached, err := lister.ListChatProjects(ctx, chat.ID)
		if err != nil {
			logging.WarnContext(ctx, "[ChatProjects] Attached projects could not be read; the chat's tools reach its primary only", "chatID", chat.ID, "error", err)
		}
		for _, cp := range attached {
			if cp == nil || cp.ProjectID == "" || cp.ProjectID == primary.ID {
				continue
			}
			p, err := projects.GetProject(ctx, cp.ProjectID)
			if err != nil || p == nil {
				logging.WarnContext(ctx, "[ChatProjects] An attached project could not be read; left out", "chatID", chat.ID, "projectID", cp.ProjectID, "error", err)
				continue
			}
			out = append(out, chatProject{project: p})
		}
	}
	names := make(map[string]int, len(out))
	for _, p := range out {
		names[p.project.Name]++
	}
	for i := range out {
		if name := out[i].project.Name; name != "" && names[name] == 1 && name != out[i].project.ID {
			out[i].value = name
		} else {
			out[i].value = out[i].project.ID
		}
	}
	return out
}

// projectValues is every value `project` accepts, in the chat's order.
func projectValues(projects []chatProject) []string {
	values := make([]string, len(projects))
	for i, p := range projects {
		values[i] = p.value
	}
	return values
}

// findChatProject is the project a `project` value names: the value it is
// offered under, else its id, else its name when no other has it.
func findChatProject(projects []chatProject, value string) (chatProject, bool) {
	value = strings.TrimSpace(value)
	for _, p := range projects {
		if p.value == value || p.project.ID == value {
			return p, true
		}
	}
	var found []chatProject
	for _, p := range projects {
		if p.project.Name == value {
			found = append(found, p)
		}
	}
	if len(found) == 1 {
		return found[0], true
	}
	return chatProject{}, false
}

// unknownProjectRefusal answers a call whose `project` names none of the
// chat's projects.
func unknownProjectRefusal(value string, projects []chatProject) string {
	quoted := make([]string, len(projects))
	for i, p := range projects {
		quoted[i] = fmt.Sprintf("%q", p.value)
		if p.primary {
			quoted[i] += " (the primary)"
		}
	}
	return fmt.Sprintf("%q is not one of this chat's projects, so the call was not run. `%s` accepts %s; omit it to run in the primary project.",
		value, tools.ProjectParam, strings.Join(quoted, ", "))
}

// attachedMissingRefusal is what a call to an attached project answers when
// the machine it runs on has no checkout of that project: the reason and the
// way out, as missingRefusal says it for the primary. Only this project's
// calls stop; the chat's others run.
func attachedMissingRefusal(err error) string {
	return svcerr.ToConnect(err).Message() + ". Nothing ran. Retrying will not help: stop calling tools with this `" + tools.ProjectParam + "`, " +
		"tell the user exactly this, and ask them to clone the project onto this chat's machine or detach it from the chat. Calls to the chat's other projects still run."
}

// namesProject reports whether a call picks its project itself: a routable
// tool whose input carries a non-empty `project`.
func namesProject(call message.ToolCall) bool {
	if !tools.RoutesByProject(call.Name) {
		return false
	}
	value, _, present, err := tools.TakeProjectArg(call.Input)
	return present && (err != nil || strings.TrimSpace(value) != "")
}

// batchBlockedKey carries the batch's workspace refusal (batchWorkspace's
// blocked) to a call that named its project, to apply once it is routed.
type batchBlockedKey struct{}

func withBatchBlocked(ctx context.Context, blocked string) context.Context {
	return context.WithValue(ctx, batchBlockedKey{}, blocked)
}

func batchBlockedFrom(ctx context.Context) string {
	blocked, _ := ctx.Value(batchBlockedKey{}).(string)
	return blocked
}

// WithChatProjects gives the activity the reader of the projects attached to
// a chat. Without it every chat is its primary project alone.
func (a *ExecuteToolsActivity) WithChatProjects(l ChatProjectLister) *ExecuteToolsActivity {
	a.chatProjects = l
	return a
}

// routeCallProject is the ONE place that decides which of the chat's projects
// a call runs in, and so which checkout, on which machine, with which
// worktree and repos. It runs after the call's machine is chosen
// (tec.daemonSelector) and sets tec.checkout; for an attached project it also
// replaces tec's project, repos and worktree, and takes `project` out of the
// dispatched input either way. It returns the refusal when the call may not
// run (an unknown project); a machine without the checkout is tec.checkout's
// missing, as for the primary.
//
// Per-project machines would change only this function.
func (a *ExecuteToolsActivity) routeCallProject(ctx context.Context, tec *toolExecutionContext) (attached bool, refusal string) {
	primaryCheckout := func() callCheckout {
		return resolveCallCheckout(ctx, a.repo, a.machines, tec.project, tec.daemonSelector, tec.worktree.Path == "")
	}
	if !tools.RoutesByProject(tec.toolName) {
		tec.checkout = primaryCheckout()
		return false, ""
	}
	value, rest, present, err := tools.TakeProjectArg(tec.dispatchInput)
	if !present {
		// Byte for byte what a single-project chat dispatches.
		tec.checkout = primaryCheckout()
		return false, ""
	}
	if err != nil {
		return false, err.Error()
	}
	tec.dispatchInput = rest
	if strings.TrimSpace(value) == "" || value == tec.project.ID {
		tec.checkout = primaryCheckout()
		return false, ""
	}
	projects := chatProjects(ctx, a.chatProjects, a.rows, tec.chat, tec.project)
	target, ok := findChatProject(projects, value)
	if !ok {
		return false, unknownProjectRefusal(value, projects)
	}
	primary := primaryCheckout()
	if target.primary {
		tec.checkout = primary
		return false, ""
	}

	// The machine is the one the call would run on in the primary (the
	// selector the primary's checkout pinned): one machine per chat for now.
	// Its main checkout of the attached project, never a branch worktree, a
	// thread's, or the run's path, which are all the primary's.
	tec.checkout = resolveCallCheckout(ctx, a.repo, a.machines, target.project, primary.pin(tec.daemonSelector), true)
	tec.project = target.project
	tec.worktree = &rctx.WorktreeInfo{}
	tec.worktreeThreadBound = false
	tec.projectPathOverride = ""
	tec.repos = nil
	if repos, err := a.rows.ListReposByProject(ctx, target.project.ID); err == nil {
		tec.repos = repos
	}
	return true, ""
}
