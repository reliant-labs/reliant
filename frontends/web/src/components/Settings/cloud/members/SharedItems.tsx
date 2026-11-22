// Copyright (c) 2025 Reliant Labs

/**
 * Settings › Members › Shared (admins and owners): the chats and projects
 * other members shared with the org, each with "Make private"
 * (control-plane docs/design/org-sharing.md: "an admin list in Settings ›
 * Organization of shared items with Make private").
 *
 * The lists are ListSharedChats and ListSharedProjects for this org, which
 * never include the caller's own shares, so an admin's own are not here.
 * Making one private is SetChatVisibility / SetProjectVisibility PRIVATE by an
 * admin who is a viewer, which the server records as an admin unshare in the
 * org's share history; the owner keeps the item and may share it again.
 */

import { useState, type ReactNode } from "react";

import Button from "@/components/forge-ui/button";
import ConfirmationDialog from "@/components/forge-ui/confirmation_dialog";
import type { Chat } from "@/gen/reliant/v1/chat_pb";
import type { Project } from "@/gen/reliant/v1/project_pb";
import { useListSharedChats, useListSharedProjects } from "@/hooks/sharing-service-hooks_gen";
import { useOrgMemberNames, useSetChatShared, useSetProjectShared } from "@/hooks/sharing-queries";
import { formatAbsoluteTime, formatRelativeTime } from "@/lib/relativeTime";
import { toast } from "@/lib/toast-manager";
import type { MyOrg } from "@/services/controlPlane/orgs";

import { shareErrorMessage } from "../../../Sharing/shareErrors";

/** A shared list's page: the servers' default and cap. */
export const SHARED_PAGE_SIZE = 100;

/** The item a "Make private" waits to confirm. */
interface Target {
  kind: "chat" | "project";
  id: string;
  label: string;
  ownerId: string;
}

export function SharedItems({ org }: { org: MyOrg }) {
  const names = useOrgMemberNames(org.id);
  const ownerName = (userId: string) => names.get(userId) ?? "A former member";
  const [chatOffset, setChatOffset] = useState(0);
  const [projectOffset, setProjectOffset] = useState(0);
  const chats = useListSharedChats(
    { orgId: org.id, limit: SHARED_PAGE_SIZE, offset: chatOffset },
    { enabled: !!org.id, meta: { silenceErrorToast: true } },
  );
  const projects = useListSharedProjects(
    { orgId: org.id, limit: SHARED_PAGE_SIZE, offset: projectOffset },
    { enabled: !!org.id, meta: { silenceErrorToast: true } },
  );
  const chatUnshare = useSetChatShared();
  const projectUnshare = useSetProjectShared();
  const [confirming, setConfirming] = useState<Target | null>(null);
  const [error, setError] = useState<string | null>(null);

  const makePrivate = async () => {
    const target = confirming;
    if (!target) return;
    setError(null);
    try {
      if (target.kind === "chat") await chatUnshare.unshare(target.id);
      else await projectUnshare.unshare(target.id);
      toast.success(`Made ${target.label} private`);
    } catch (err) {
      setError(shareErrorMessage(err, `Could not make ${target.label} private.`));
    } finally {
      setConfirming(null);
    }
  };

  const ask = (target: Target) => {
    setError(null);
    setConfirming(target);
  };

  return (
    <div className="space-y-6" data-testid="shared-items">
      <p className="text-meta text-muted-foreground">
        What other members of {org.name} shared with it, read-only. Your own shares are not listed here: stop sharing them
        from the chat or the project.
      </p>
      {error && (
        <p role="alert" className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-meta text-destructive-ink">
          {error}
        </p>
      )}

      <SharedList
        id="shared-chats"
        title="Shared chats"
        query={chats}
        items={chats.data?.chats ?? []}
        empty="No member has shared a chat."
        loadError="Could not load the shared chats."
        offset={chatOffset}
        onOffset={setChatOffset}
        render={(chat: Chat) => {
          const label = chat.title || "Untitled chat";
          return (
            <SharedRow
              key={chat.id}
              testId={`shared-chat-row-${chat.id}`}
              label={label}
              owner={ownerName(chat.userId)}
              sharedAt={chat.sharedAt}
              onMakePrivate={() => ask({ kind: "chat", id: chat.id, label, ownerId: chat.userId })}
            />
          );
        }}
      />

      <SharedList
        id="shared-projects"
        title="Shared projects"
        query={projects}
        items={projects.data?.projects ?? []}
        empty="No member has shared a project."
        loadError="Could not load the shared projects."
        offset={projectOffset}
        onOffset={setProjectOffset}
        render={(project: Project) => (
          <SharedRow
            key={project.id}
            testId={`shared-project-row-${project.id}`}
            label={project.name}
            owner={ownerName(project.userId)}
            sharedAt={project.sharedAt}
            onMakePrivate={() => ask({ kind: "project", id: project.id, label: project.name, ownerId: project.userId })}
          />
        )}
      />

      <ConfirmationDialog
        open={confirming !== null}
        onCancel={() => setConfirming(null)}
        onConfirm={() => void makePrivate()}
        loading={chatUnshare.isPending || projectUnshare.isPending}
        variant="warning"
        title={confirming?.kind === "project" ? "Make this project private?" : "Make this chat private?"}
        description={
          confirming
            ? `Members of ${org.name} will no longer see ${confirming.label}, and neither will you. ${ownerName(confirming.ownerId)} keeps it and can share it again. This is recorded in the organization's share history.${
                confirming.kind === "project" ? " Chats in it that were shared stay shared." : ""
              }`
            : undefined
        }
        confirmLabel="Make private"
      />
    </div>
  );
}

interface SharedListProps<T> {
  id: string;
  title: string;
  query: { isPending: boolean; isError: boolean; error: unknown };
  items: T[];
  empty: string;
  loadError: string;
  offset: number;
  onOffset: (offset: number) => void;
  render: (item: T) => ReactNode;
}

function SharedList<T>({ id, title, query, items, empty, loadError, offset, onOffset, render }: SharedListProps<T>) {
  // A full page may have more behind it; the servers cap a page at 100.
  const full = items.length >= SHARED_PAGE_SIZE;
  return (
    <section className="rounded-xl border border-border bg-card" aria-labelledby={`${id}-heading`} data-testid={id}>
      <div className="border-b border-border px-4 py-3">
        <h3 id={`${id}-heading`} className="text-ui font-semibold text-foreground">
          {title}
        </h3>
      </div>
      {query.isPending ? (
        <p className="px-4 py-3 text-ui text-muted-foreground" role="status">
          Loading…
        </p>
      ) : query.isError ? (
        <p className="px-4 py-3 text-ui text-muted-foreground" role="alert" data-testid={`${id}-error`}>
          {shareErrorMessage(query.error, loadError)}
        </p>
      ) : items.length === 0 ? (
        <p className="px-4 py-3 text-ui text-muted-foreground" data-testid={`${id}-empty`}>
          {offset > 0 ? "Nothing more." : empty}
        </p>
      ) : (
        <ul className="divide-y divide-border-subtle">{items.map(render)}</ul>
      )}
      {(offset > 0 || full) && (
        <div className="flex items-center justify-between gap-2 border-t border-border px-4 py-2 text-meta text-muted-foreground">
          <span data-testid={`${id}-page`}>
            Showing {offset + 1}–{offset + items.length}
            {full ? `; a page holds ${SHARED_PAGE_SIZE}` : ""}
          </span>
          <span className="flex gap-2">
            {offset > 0 && (
              <Button variant="ghost" size="sm" onClick={() => onOffset(Math.max(0, offset - SHARED_PAGE_SIZE))}>
                Previous
              </Button>
            )}
            {full && (
              <Button variant="ghost" size="sm" onClick={() => onOffset(offset + SHARED_PAGE_SIZE)} data-testid={`${id}-next`}>
                Next {SHARED_PAGE_SIZE}
              </Button>
            )}
          </span>
        </div>
      )}
    </section>
  );
}

function SharedRow({
  testId,
  label,
  owner,
  sharedAt,
  onMakePrivate,
}: {
  testId: string;
  label: string;
  owner: string;
  sharedAt: string | undefined;
  onMakePrivate: () => void;
}) {
  return (
    <li className="flex flex-wrap items-center gap-3 px-4 py-3" data-testid={testId}>
      <div className="min-w-0 flex-1">
        <p className="truncate text-ui font-medium text-foreground">{label}</p>
        <p className="truncate text-meta text-muted-foreground">
          Shared by {owner}
          {sharedAt && (
            <>
              {" · "}
              <time dateTime={sharedAt} title={formatAbsoluteTime(sharedAt)}>
                {formatRelativeTime(sharedAt)}
              </time>
            </>
          )}
        </p>
      </div>
      <Button variant="outline" size="sm" onClick={onMakePrivate} aria-label={`Make ${label} private`}>
        Make private
      </Button>
    </li>
  );
}
