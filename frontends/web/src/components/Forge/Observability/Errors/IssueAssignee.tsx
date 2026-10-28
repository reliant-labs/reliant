// Copyright (c) 2025 Reliant Labs

/**
 * Who an issue is assigned to: the current assignee, a picker of org members,
 * "Assign to me", and Unassign.
 *
 * Assigning is optimistic-concurrency like a triage (IssueTriage.tsx): the
 * request echoes the Issue.version read, and a stale one comes back Aborted.
 * The view then re-reads the issue and SAYS what changed; it never re-sends
 * the request over the other change.
 *
 * The explicit issue org's assignable roster is open to its members. If the
 * read is refused, the picker offers only "me" and the current assignee.
 */

import { useState } from "react";
import { AlertTriangle, User, UserMinus, UserPlus } from "lucide-react";

import Button from "@/components/forge-ui/button";
import { useAssignIssue, useCurrentIssueUser, useOrgMembers } from "@/hooks/observability-queries";
import { issuesErrorKind } from "@/services/controlPlane/observability/errors";
import type { Issue, IssueUser } from "@/services/controlPlane/observability/issuesTypes";

import { conflictSummary } from "./issueModel";
import { discussionErrorText } from "./issueTimelineModel";

const FIELD = "h-8 rounded-md border border-border bg-surface-sunken px-2 text-sm text-ink focus:outline-none focus-visible:ring-2 focus-visible:ring-ring";

/** People the picker can offer: the roster, plus anyone it knows of that the roster did not list. */
export function assigneeChoices(roster: readonly IssueUser[] | undefined, me: IssueUser | null | undefined, current: IssueUser | null): IssueUser[] {
  const byId = new Map<string, IssueUser>();
  for (const person of roster ?? []) byId.set(person.id, person);
  for (const person of [me, current]) if (person && !byId.has(person.id)) byId.set(person.id, person);
  return [...byId.values()].sort((a, b) => a.name.localeCompare(b.name));
}

export function IssueAssignee({ issue, orgId }: { issue: Issue; orgId: string }) {
  const assign = useAssignIssue();
  const me = useCurrentIssueUser();
  const members = useOrgMembers(orgId);
  const [readAtClick, setReadAtClick] = useState<Issue | null>(null);

  const meUser = me.data ?? null;
  const choices = assigneeChoices(members.data?.users, meUser, issue.assignee);
  const errorKind = assign.error ? issuesErrorKind(assign.error) : null;
  const conflict = errorKind === "conflict" && readAtClick;
  const refreshed = conflict && (issue.version !== readAtClick.version || issue.assignee?.id !== readAtClick.assignee?.id);

  const send = (assigneeUserId: string) => {
    setReadAtClick(issue);
    assign.mutate({ issueId: issue.id, assigneeUserId, expectedVersion: issue.version });
  };
  const mine = !!meUser && issue.assignee?.id === meUser.id;

  return (
    <div className="space-y-2" data-testid="obs-issue-assignee" data-assignee={issue.assignee?.id ?? ""}>
      <div className="flex flex-wrap items-center gap-2">
        <span className="inline-flex items-center gap-1.5 text-xs text-ink-muted">
          <User className="h-3.5 w-3.5" aria-hidden="true" />
          Assignee
        </span>
        <span className="text-sm font-medium text-ink" data-testid="obs-issue-assignee-name">
          {issue.assignee ? (mine ? `${issue.assignee.name} (you)` : issue.assignee.name) : <span className="font-normal text-ink-muted">Unassigned</span>}
        </span>
        <label htmlFor="obs-issue-assignee-pick" className="sr-only">
          Assign to
        </label>
        <select
          id="obs-issue-assignee-pick"
          value={issue.assignee?.id ?? ""}
          onChange={(event) => send(event.target.value)}
          disabled={assign.isPending}
          className={FIELD}
          data-testid="obs-issue-assignee-pick"
        >
          <option value="">Unassigned</option>
          {choices.map((person) => (
            <option key={person.id} value={person.id}>
              {person.id === meUser?.id ? `${person.name} (you)` : person.name}
            </option>
          ))}
        </select>
        {meUser && !mine && (
          <Button
            size="sm"
            variant="outline"
            leftIcon={<UserPlus className="h-3.5 w-3.5" />}
            onClick={() => send(meUser.id)}
            disabled={assign.isPending}
            loading={assign.isPending && assign.variables?.assigneeUserId === meUser.id}
            data-testid="obs-issue-assign-me"
          >
            Assign to me
          </Button>
        )}
        {issue.assignee && (
          <Button
            size="sm"
            variant="ghost"
            leftIcon={<UserMinus className="h-3.5 w-3.5" />}
            onClick={() => send("")}
            disabled={assign.isPending}
            loading={assign.isPending && assign.variables?.assigneeUserId === ""}
            data-testid="obs-issue-unassign"
          >
            Unassign
          </Button>
        )}
      </div>
      {members.error && !members.data && (
        <p className="text-xs text-ink-muted" data-testid="obs-issue-assignee-roster-closed">
          The member list isn&apos;t available to you, so only you and the current assignee are listed.
        </p>
      )}
      {conflict ? (
        <p role="alert" className="flex items-start gap-2 rounded-md border border-warning/40 bg-surface-sunken px-3 py-2 text-xs text-ink" data-testid="obs-issue-assignee-conflict">
          <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0 text-warning" aria-hidden="true" />
          <span>
            Someone changed this issue while you were looking, so the assignee was not changed.{" "}
            {refreshed ? `${conflictSummary(readAtClick, issue)} Check it, then choose again.` : "Loading what changed…"}
          </span>
        </p>
      ) : assign.error ? (
        <p role="alert" className="text-xs text-danger-ink" data-testid="obs-issue-assignee-error">
          {discussionErrorText(assign.error, "assign it")}
        </p>
      ) : null}
    </div>
  );
}
