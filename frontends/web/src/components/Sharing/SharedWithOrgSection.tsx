// Copyright (c) 2025 Reliant Labs

/**
 * "Shared with the org": the chats other members of the active org shared
 * (control-plane docs/design/org-sharing.md §5.2). They never appear in the
 * person's own list; this section is the one place they do, read-only.
 *
 * The list is ListSharedChats for the active org, kept current by the org
 * feed (useOrgFeed): a share, a rename, a run starting or ending, an unshare
 * and a deletion each re-read it. A person with no org, or none shared,
 * sees nothing here at all.
 */

import { memo, useState } from "react";
import { ChevronRight, Users } from "lucide-react";

import { ChatActivity, type Chat as ProtoChat } from "@/gen/reliant/v1/chat_pb";
import { convertProtoChat } from "@/hooks/chat-model";
import { useActiveOrg } from "@/hooks/useOrgs";
import { useOrgFeed, useOrgMemberNames, useSharedChats } from "@/hooks/sharing-queries";
import { cn } from "@/lib/utils";
import { openSharedChat } from "@/store/openChat";

interface SharedWithOrgSectionProps {
  activeChatId: string | null;
}

export function SharedWithOrgSection({ activeChatId }: SharedWithOrgSectionProps) {
  const { activeOrgId, active } = useActiveOrg();
  useOrgFeed(activeOrgId);
  const shared = useSharedChats(activeOrgId);
  const names = useOrgMemberNames(activeOrgId);
  const [open, setOpen] = useState(true);

  const chats = shared.data ?? [];
  if (!activeOrgId || chats.length === 0) return null;

  const orgName = active?.name || "your organization";
  return (
    <section className="mt-3 border-t border-border/40 pt-2" aria-label={`Shared with ${orgName}`} data-testid="shared-with-org">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        className="flex w-full items-center gap-1.5 rounded-md px-2 py-1 text-xs font-medium text-muted-foreground hover:bg-foreground/5 hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        <ChevronRight className={cn("h-3 w-3 transition-transform", open && "rotate-90")} aria-hidden />
        <Users className="h-3.5 w-3.5" aria-hidden />
        <span className="flex-1 truncate text-left">Shared with {orgName}</span>
        <span className="tabular-nums">{chats.length}</span>
      </button>
      {open && (
        <ul className="mt-1 space-y-0.5">
          {chats.map((chat) => (
            <SharedChatRow
              key={chat.id}
              chat={chat}
              ownerName={names.get(chat.userId)}
              active={chat.id === activeChatId}
            />
          ))}
        </ul>
      )}
    </section>
  );
}

interface SharedChatRowProps {
  chat: ProtoChat;
  ownerName: string | undefined;
  active: boolean;
}

/** One shared chat: its title, whose it is, and whether it is running. */
export const SharedChatRow = memo(function SharedChatRow({ chat, ownerName, active }: SharedChatRowProps) {
  const title = chat.title || "Untitled chat";
  const running = chat.activity === ChatActivity.RUNNING;
  return (
    <li>
      <button
        type="button"
        onClick={() => openSharedChat(convertProtoChat(chat))}
        aria-current={active ? "true" : undefined}
        data-testid={`shared-chat-${chat.id}`}
        className={cn(
          "flex w-full min-w-0 items-center gap-2 rounded-md border-l-2 px-2.5 py-1.5 text-left text-xs transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring",
          active
            ? "border-primary bg-foreground/5 font-semibold text-foreground"
            : "border-transparent text-foreground/80 hover:bg-foreground/5 hover:text-foreground",
        )}
      >
        <span className="min-w-0 flex-1">
          <span className="block truncate" title={title}>
            {title}
          </span>
          <span className="block truncate text-2xs text-muted-foreground">
            {ownerName ? `Shared by ${ownerName}` : "Shared by a member"}
          </span>
        </span>
        {running && (
          <span
            role="img"
            aria-label="Running"
            className="h-1.5 w-1.5 flex-shrink-0 rounded-full bg-info motion-safe:animate-pulse"
          />
        )}
      </button>
    </li>
  );
});
