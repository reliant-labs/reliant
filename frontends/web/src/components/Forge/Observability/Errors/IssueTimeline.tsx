// Copyright (c) 2025 Reliant Labs

/**
 * One timeline per issue: status changes, comments, assignments and the chats
 * started from it, oldest first, with a composer under it.
 *
 * The server merges them (ListIssueTimeline) and, for a merged issue, answers
 * with the target's timeline including every merged-away issue's entries; each
 * entry says which issue it happened on, and one from another issue is labelled
 * so (issueTimelineModel.entryOrigin).
 *
 * Writes never retry and never re-send. A comment that was refused stays in the
 * box so nothing typed is lost.
 */

import { useMemo, useRef, useState } from "react";
import { useRouter } from "@tanstack/react-router";
import { CheckCircle2, CircleDot, MessageSquare, MessageSquareText, Pencil, Trash2, UserRound } from "lucide-react";

import Button from "@/components/forge-ui/button";
import {
  useAddIssueComment,
  useCurrentIssueUser,
  useDeleteIssueComment,
  useIssueTimeline,
  useOrgMembers,
  useUpdateIssueComment,
} from "@/hooks/observability-queries";
import { issuesErrorKind } from "@/services/controlPlane/observability/errors";
import { keepsLastResult } from "@/services/controlPlane/observability/quota";
import type { IssueComment, IssueTimelineEntry } from "@/services/controlPlane/observability/issuesTypes";
import { loadRunContext } from "@/components/runs/loadRunContext";
import { toast } from "@/lib/toast-manager";

import { formatAgo, formatWhen } from "../format";
import { ObsLoadingRows } from "../ObsStates";
import { CommentBody } from "./CommentBody";
import {
  actorName,
  canDeleteComment,
  canEditComment,
  chatLabel,
  describeAssignment,
  describeStatus,
  discussionErrorText,
  entryOrigin,
  isEdited,
  mergeTimelinePages,
} from "./issueTimelineModel";

/** The server's limit on a comment (AddIssueCommentRequest.body). */
export const COMMENT_MAX_LENGTH = 10_000;

const TEXTAREA =
  "w-full rounded-md border border-border bg-surface-sunken px-3 py-2 text-sm text-ink placeholder:text-ink-muted focus:outline-none focus-visible:ring-2 focus-visible:ring-ring";

export function IssueTimeline({ issueId, orgId }: { issueId: string; orgId: string }) {
  const timeline = useIssueTimeline(issueId);
  const me = useCurrentIssueUser();
  const members = useOrgMembers(orgId);
  const refused = !!timeline.error && !keepsLastResult(timeline.error, timeline.data);
  const pages = refused ? undefined : timeline.data?.pages;
  const entries = useMemo(() => mergeTimelinePages(pages ?? []), [pages]);
  // The issue the timeline is of: the one asked for, or the issue it was merged into.
  const timelineIssueId = pages?.[0]?.issueId ?? issueId;
  const myRole = members.data?.callerRole ?? undefined;
  const now = Date.now();

  return (
    <div data-testid="obs-issue-timeline" data-issue={timelineIssueId}>
      {timeline.isLoading ? (
        <ObsLoadingRows rows={3} testId="obs-issue-timeline-loading" />
      ) : refused ? (
        <div className="space-y-2 px-4 py-3" data-testid="obs-issue-timeline-error">
          <p className="text-xs text-danger-ink">
            {issuesErrorKind(timeline.error) === "not-configured" ? "This control plane doesn't keep an issue timeline yet." : "The timeline didn't load."}
          </p>
          <Button variant="ghost" size="sm" onClick={() => void timeline.refetch()} loading={timeline.isFetching}>
            Try again
          </Button>
        </div>
      ) : entries.length === 0 ? (
        <p className="px-4 py-3 text-xs text-ink-muted" data-testid="obs-issue-timeline-empty">
          Nothing yet. Be the first to say something about it.
        </p>
      ) : (
        <ol className="divide-y divide-border/40" data-testid="obs-issue-timeline-list">
          {entries.map((entry) => (
            <TimelineRow
              key={entry.id}
              entry={entry}
              timelineIssueId={timelineIssueId}
              viewedIssueId={issueId}
              now={now}
              canEdit={entry.body.kind === "comment" && canEditComment(entry.body.comment, me.data)}
              canDelete={entry.body.kind === "comment" && canDeleteComment(entry.body.comment, me.data, myRole)}
            />
          ))}
        </ol>
      )}
      {!refused && timeline.hasNextPage && (
        <div className="border-t border-border/60 px-4 py-2">
          <Button variant="ghost" size="sm" onClick={() => void timeline.fetchNextPage()} loading={timeline.isFetchingNextPage} data-testid="obs-issue-timeline-more">
            Show later activity
          </Button>
        </div>
      )}
      <div hidden={refused}><CommentComposer key={timelineIssueId} issueId={timelineIssueId} disabled={refused} /></div>
    </div>
  );
}

const ICON = "mt-0.5 h-3.5 w-3.5 shrink-0 text-ink-muted";

function TimelineRow({
  entry,
  timelineIssueId,
  viewedIssueId,
  now,
  canEdit,
  canDelete,
}: {
  entry: IssueTimelineEntry;
  timelineIssueId: string;
  viewedIssueId: string;
  now: number;
  canEdit: boolean;
  canDelete: boolean;
}) {
  const origin = entryOrigin(entry, timelineIssueId, viewedIssueId);
  const body = entry.body;
  const when = (
    <span className="whitespace-nowrap text-ink-muted" title={formatWhen(entry.timeMs, now)}>
      {formatAgo(entry.timeMs, now)}
    </span>
  );
  const originNote = origin && (
    <span className="rounded border border-border/60 px-1.5 text-2xs text-ink-muted" data-testid="obs-timeline-origin" data-origin-issue={origin.issueId} title={`Issue ${origin.issueId}`}>
      {origin.label}
    </span>
  );
  const frame = (icon: React.ReactNode, children: React.ReactNode) => (
    <li className="flex gap-2.5 px-4 py-2.5 text-xs" data-testid={`obs-timeline-${entry.id}`} data-kind={body.kind} data-issue={entry.issueId}>
      {icon}
      <div className="min-w-0 flex-1 space-y-1">{children}</div>
    </li>
  );

  switch (body.kind) {
    case "status":
      return frame(
        <CircleDot className={ICON} aria-hidden="true" />,
        <p className="flex flex-wrap items-center gap-x-2 gap-y-1 text-ink">
          <span>
            <strong className="font-medium">{actorName(entry.actor)}</strong> {describeStatus(body, now)}
          </span>
          {originNote}
          {when}
        </p>
      );
    case "assignment":
      return frame(
        <UserRound className={ICON} aria-hidden="true" />,
        <p className="flex flex-wrap items-center gap-x-2 gap-y-1 text-ink">
          <span>
            <strong className="font-medium">{actorName(entry.actor)}</strong> {describeAssignment(entry.actor, body.from, body.to)}
          </span>
          {originNote}
          {when}
        </p>
      );
    case "chat":
      return frame(
        <MessageSquareText className={ICON} aria-hidden="true" />,
        <p className="flex flex-wrap items-center gap-x-2 gap-y-1 text-ink">
          <span>
            <strong className="font-medium">{actorName(entry.actor ?? (body.link.linkedBy ? { kind: "user", id: body.link.linkedBy.id, name: body.link.linkedBy.name } : null))}</strong> started a chat:{" "}
            <ChatLink chatId={body.link.chatId} label={chatLabel(body.link)} />
          </span>
          {originNote}
          {when}
        </p>
      );
    case "comment":
      return frame(
        <MessageSquare className={ICON} aria-hidden="true" />,
        <CommentEntry comment={body.comment} actor={actorName(entry.actor)} when={when} originNote={originNote} canEdit={canEdit} canDelete={canDelete} />
      );
  }
}

function ChatLink({ chatId, label }: { chatId: string; label: string }) {
  const router = useRouter({ warn: false });
  const open = async () => {
    try {
      const chat = await loadRunContext(chatId);
      await router?.navigate({ to: "/project/$projectId", params: { projectId: chat.projectId }, search: {} });
    } catch (error) {
      void toast.error(error);
    }
  };
  return (
    <button
      type="button"
      onClick={() => void open()}
      className="font-medium text-ink underline underline-offset-2 hover:no-underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      title={`Chat ${chatId}`}
      data-testid={`obs-timeline-chat-${chatId}`}
    >
      {label}
    </button>
  );
}

function CommentEntry({
  comment,
  actor,
  when,
  originNote,
  canEdit,
  canDelete,
}: {
  comment: IssueComment;
  actor: string;
  when: React.ReactNode;
  originNote: React.ReactNode;
  canEdit: boolean;
  canDelete: boolean;
}) {
  const update = useUpdateIssueComment();
  const remove = useDeleteIssueComment();
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(comment.body);
  const [confirming, setConfirming] = useState(false);
  const who = comment.author?.name || actor;

  const saving = useRef(false);
  const save = () => {
    if (saving.current || !draft.trim() || draft === comment.body) return;
    saving.current = true;
    const sent = draft;
    update.mutate(
      { issueId: comment.issueId, commentId: comment.id, body: sent },
      { onSuccess: () => setEditing(false), onSettled: () => { saving.current = false; } }
    );
  };

  return (
    <>
      <p className="flex flex-wrap items-center gap-x-2 gap-y-1 text-ink">
        <strong className="font-medium">{comment.author ? who : "A deleted account"}</strong>
        <span className="text-ink-muted">commented</span>
        {originNote}
        {when}
        {isEdited(comment) && <span className="text-ink-muted">(edited)</span>}
        {!editing && (canEdit || canDelete) && (
          <span className="ml-auto inline-flex items-center gap-1">
            {canEdit && (
              <Button
                size="sm"
                variant="ghost"
                icon={<Pencil className="h-3 w-3" />}
                aria-label="Edit comment"
                onClick={() => {
                  setDraft(comment.body);
                  setEditing(true);
                }}
                data-testid={`obs-comment-edit-${comment.id}`}
              />
            )}
            {canDelete && !confirming && (
              <Button size="sm" variant="ghost" icon={<Trash2 className="h-3 w-3" />} aria-label="Delete comment" onClick={() => setConfirming(true)} data-testid={`obs-comment-delete-${comment.id}`} />
            )}
          </span>
        )}
      </p>
      {editing ? (
        <div className="space-y-2" data-testid={`obs-comment-editor-${comment.id}`}>
          <label htmlFor={`obs-comment-edit-box-${comment.id}`} className="sr-only">
            Edit comment
          </label>
          <textarea
            id={`obs-comment-edit-box-${comment.id}`}
            value={draft}
            onChange={(event) => setDraft(event.target.value)}
            rows={4}
            maxLength={COMMENT_MAX_LENGTH}
            className={TEXTAREA}
            data-testid={`obs-comment-edit-box-${comment.id}`}
          />
          <div className="flex items-center gap-2">
            <Button size="sm" variant="primary" onClick={save} disabled={!draft.trim() || draft === comment.body} loading={update.isPending} data-testid={`obs-comment-save-${comment.id}`}>
              Save
            </Button>
            <Button
              size="sm"
              variant="ghost"
              onClick={() => {
                setEditing(false);
                update.reset();
              }}
              disabled={update.isPending}
            >
              Cancel
            </Button>
          </div>
          {update.error ? (
            <p role="alert" className="text-xs text-danger-ink" data-testid="obs-comment-error">
              {discussionErrorText(update.error, "edited")}
            </p>
          ) : null}
        </div>
      ) : (
        <CommentBody body={comment.body} />
      )}
      {confirming && (
        <div role="alertdialog" aria-label="Delete this comment?" className="flex flex-wrap items-center gap-2 rounded-md border border-border/60 bg-surface-sunken px-3 py-2" data-testid={`obs-comment-confirm-${comment.id}`}>
          <span className="text-ink">Delete this comment? It leaves the timeline for everyone.</span>
          <Button
            size="sm"
            variant="primary"
            onClick={() => remove.mutate({ issueId: comment.issueId, commentId: comment.id }, { onSuccess: () => setConfirming(false) })}
            loading={remove.isPending}
            data-testid={`obs-comment-confirm-delete-${comment.id}`}
          >
            Delete
          </Button>
          <Button size="sm" variant="ghost" onClick={() => setConfirming(false)} disabled={remove.isPending}>
            Keep it
          </Button>
          {remove.error ? (
            <p role="alert" className="w-full text-xs text-danger-ink" data-testid="obs-comment-error">
              {discussionErrorText(remove.error, "deleted")}
            </p>
          ) : null}
        </div>
      )}
    </>
  );
}

function CommentComposer({ issueId, disabled }: { issueId: string; disabled: boolean }) {
  const add = useAddIssueComment();
  const [body, setBody] = useState("");
  const sending = useRef(false);
  const empty = !body.trim();
  const submit = () => {
    if (empty || disabled || sending.current) return;
    // Lock before mutate: a second keyboard/form event can arrive before React renders pending.
    sending.current = true;
    const sent = body;
    add.mutate({ issueId, body: sent }, {
      // Keep anything typed while this request was in flight, and keep the draft on refusal.
      onSuccess: () => setBody((current) => current === sent ? "" : current),
      onSettled: () => { sending.current = false; },
    });
  };
  return (
    <form
      onSubmit={(event) => {
        event.preventDefault();
        submit();
      }}
      className="space-y-2 border-t border-border/60 px-4 py-3"
      data-testid="obs-comment-composer"
    >
      <label htmlFor="obs-comment-box" className="sr-only">
        Add a comment
      </label>
      <textarea
        id="obs-comment-box"
        value={body}
        onChange={(event) => setBody(event.target.value)}
        onKeyDown={(event) => {
          if ((event.metaKey || event.ctrlKey) && event.key === "Enter") {
            event.preventDefault();
            submit();
          }
        }}
        rows={3}
        maxLength={COMMENT_MAX_LENGTH}
        placeholder="Add a comment. Markdown works; raw HTML is ignored."
        className={TEXTAREA}
        data-testid="obs-comment-box"
      />
      <div className="flex items-center gap-2">
        <Button type="submit" size="sm" variant="primary" leftIcon={<CheckCircle2 className="h-3.5 w-3.5" />} disabled={empty || disabled || add.isPending} loading={add.isPending} data-testid="obs-comment-submit">
          Comment
        </Button>
        <span className="text-2xs text-ink-muted">⌘/Ctrl+Enter sends</span>
      </div>
      {add.error ? (
        <p role="alert" className="text-xs text-danger-ink" data-testid="obs-comment-add-error">
          {discussionErrorText(add.error, "commented on")}
        </p>
      ) : null}
    </form>
  );
}
