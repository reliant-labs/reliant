// Copyright (c) 2025 Reliant Labs

/**
 * The rules of an issue's discussion, pure so the components stay layout:
 * how the timeline's pages merge and which issue each entry came from, how an
 * entry reads, who may edit or delete a comment, what the assignee filter
 * asks the server, and what a refused write means.
 */

import { Code, ConnectError } from "@connectrpc/connect";

import { obsErrorMessage } from "@/services/controlPlane/observability/errors";
import type {
  IssueActor,
  IssueComment,
  IssueTimelineBody,
  IssueTimelineEntry,
  IssueTimelineList,
  IssueUser,
} from "@/services/controlPlane/observability/issuesTypes";

import { formatWhen } from "../format";
import { STATUS_LABEL } from "./issueModel";

// ── The timeline ──────────────────────────────────────────────────────────────

/**
 * Every loaded page as one oldest-first list. The server orders a page; pages
 * are appended, and a refetch can repeat an entry, so entries are keyed by id
 * and the sort is stable on time (ties keep the server's order).
 */
export function mergeTimelinePages(pages: readonly IssueTimelineList[]): IssueTimelineEntry[] {
  const seen = new Set<string>();
  const out: IssueTimelineEntry[] = [];
  for (const page of pages) {
    for (const entry of page.entries) {
      if (seen.has(entry.id)) continue;
      seen.add(entry.id);
      out.push(entry);
    }
  }
  return out
    .map((entry, index) => ({ entry, index }))
    .sort((a, b) => a.entry.timeMs - b.entry.timeMs || a.index - b.index)
    .map(({ entry }) => entry);
}

/**
 * Where an entry happened, when that is not the issue the timeline is of. A
 * merged issue's timeline carries the entries of every issue merged into it,
 * each with its own issue id: those are labelled so a reader knows a comment
 * was made on an issue that has since folded into this one. null: it happened
 * on the timeline's own issue.
 *
 * `viewedIssueId` is the issue the reader opened. When that issue was itself
 * merged away, the timeline is its target's and its own entries say so.
 */
export function entryOrigin(entry: IssueTimelineEntry, timelineIssueId: string, viewedIssueId: string): { issueId: string; label: string } | null {
  if (!entry.issueId || entry.issueId === timelineIssueId) return null;
  if (entry.issueId === viewedIssueId) return { issueId: entry.issueId, label: "on this issue, before it was merged" };
  return { issueId: entry.issueId, label: "on a merged issue" };
}

export function actorName(actor: IssueActor | null): string {
  if (!actor) return "Someone";
  if (actor.kind === "system") return "Reliant";
  if (actor.kind === "machine") return actor.name || "A credential";
  return actor.name || "Someone";
}

/** A status change, as the words after the actor ("resolved in v1.8.4"). */
export function describeStatus(body: Extract<IssueTimelineBody, { kind: "status" }>, now: number = Date.now()): string {
  const { from, to } = body;
  if (!from) return "opened the issue";
  switch (to) {
    case "resolved":
      return body.version ? `resolved it in ${body.version}` : "resolved it";
    case "ignored":
      return body.ignoredUntilMs ? `ignored it until ${formatWhen(body.ignoredUntilMs, now)}` : "ignored it until it is unignored";
    case "regressed":
      return "saw it regress";
    case "new":
      return "opened the issue";
    case "unresolved":
      return from === "resolved" || from === "ignored" ? `reopened it (it was ${STATUS_LABEL[from].toLowerCase()})` : "marked it unresolved";
    default:
      return "changed its status";
  }
}

export function describeAssignment(actor: IssueActor | null, from: IssueUser | null, to: IssueUser | null): string {
  if (!to) return from ? `unassigned ${from.name}` : "cleared the assignee";
  if (actor?.kind === "user" && actor.id === to.id) return "took it";
  return from ? `reassigned it from ${from.name} to ${to.name}` : `assigned it to ${to.name}`;
}

export function chatLabel(link: { chatId: string; chatTitle: string }): string {
  return link.chatTitle.trim() || `chat ${link.chatId.slice(0, 8)}`;
}

export function isEdited(comment: IssueComment): boolean {
  return comment.updatedMs > comment.createdMs + 1000;
}

// ── Who may do what to a comment ──────────────────────────────────────────────

/** Only the author edits a comment. */
export function canEditComment(comment: IssueComment, me: IssueUser | null | undefined): boolean {
  return !!me && !!comment.author && comment.author.id === me.id;
}

/**
 * The author, or an owner or admin of the org, deletes a comment. The role is
 * the reader's own membership in the explicit issue org. If membership lookup
 * is refused or absent, only their own comments offer a delete.
 * The server decides either way.
 */
export function canDeleteComment(comment: IssueComment, me: IssueUser | null | undefined, myRole: string | undefined): boolean {
  if (canEditComment(comment, me)) return true;
  return myRole === "owner" || myRole === "admin";
}

// ── The assignee filter ───────────────────────────────────────────────────────

/**
 * TODO(unassigned): ListIssues cannot filter to "unassigned" (an unset assignee
 * means every issue, and an empty one is refused). A control-plane follow-up adds
 * an explicit unassigned filter, and possibly a server-side "me". Once its proto
 * is vendored, add `{ kind: "unassigned" }` here, send it in issuesApi.listIssues,
 * and add the option to the ErrorsView select. It is deliberately not filtered
 * client-side: that would only cover the pages loaded so far.
 */
export type AssigneeFilter = { kind: "anyone" } | { kind: "me" } | { kind: "user"; userId: string };

/** `assignee=me` | `<user id>`; absent: anyone. */
export function assigneeFilterOf(param: string | undefined): AssigneeFilter {
  const value = (param ?? "").trim();
  if (!value) return { kind: "anyone" };
  if (value === "me") return { kind: "me" };
  return { kind: "user", userId: value };
}

export function assigneeParam(filter: AssigneeFilter): string | undefined {
  switch (filter.kind) {
    case "anyone":
      return undefined;
    case "me":
      return "me";
    case "user":
      return filter.userId;
  }
}

/** The user id the server is asked for; "me" is the signed-in user's id, known to this app. */
export function serverAssignee(filter: AssigneeFilter, meId: string | undefined): string | undefined {
  if (filter.kind === "me") return meId;
  if (filter.kind === "user") return filter.userId;
  return undefined;
}

/** "Mine" cannot be asked until the reader is known. */
export function assigneeFilterWaitsForMe(filter: AssigneeFilter, meId: string | undefined): boolean {
  return filter.kind === "me" && !meId;
}

// ── Refusals ──────────────────────────────────────────────────────────────────

/**
 * What a refused write means to the reader. Not issuesErrorKind: there a
 * FailedPrecondition reads "not set up", but on a write it is "this issue was
 * merged into another", and the server's message names the target.
 */
export function discussionErrorText(error: unknown, what: string): string {
  const message = obsErrorMessage(error);
  if (error instanceof ConnectError) {
    switch (error.code) {
      case Code.PermissionDenied:
      case Code.Unauthenticated:
        return message || `You can read this issue but not ${what}: that needs a member of the organization, or a credential with obs:triage.`;
      case Code.FailedPrecondition:
        return message || "This issue was merged into another; make the change there.";
      case Code.NotFound:
        return "That issue or comment is gone (deleted, merged, or no longer yours to see).";
      case Code.InvalidArgument:
        return message || `That can't be ${what}.`;
    }
  }
  return message || `It didn't go through. Try again.`;
}
