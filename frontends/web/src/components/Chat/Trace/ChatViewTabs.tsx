// Copyright (c) 2026 Reliant Labs

/**
 * The chat header's view switch: the conversation, or its Trace (timing and
 * cost). One quiet two-segment control in the bar; narrower than xl it shares
 * the bar with the title, so the labels fold to icons and the tab keeps its
 * accessible name.
 *
 * Why a header tab and not the execution sidebar (agent-analytics.md §3.7,
 * the choice it left open): that sidebar is the workflow viewer's side
 * panel, which mounts only for a chat with a workflow execution and only on
 * desktop, and is 300–800px wide. A trace exists for every chat with a model
 * call, viewers' included, and a waterfall beside a timeline needs the
 * column's width. The tab is one control and one piece of state; the panel
 * would have needed a tab system of its own inside the workflow viewer.
 */

import { Activity, MessagesSquare } from "lucide-react";

import { cn } from "@/lib/utils";

export type ChatView = "chat" | "trace";

const VIEWS: { id: ChatView; label: string; icon: typeof Activity }[] = [
  { id: "chat", label: "Chat", icon: MessagesSquare },
  { id: "trace", label: "Trace", icon: Activity },
];

export function ChatViewTabs({ view, onChange }: { view: ChatView; onChange: (view: ChatView) => void }) {
  return (
    <div
      role="tablist"
      aria-label="Chat view"
      className="inline-flex h-8 shrink-0 items-center gap-0.5 rounded-lg border border-border-subtle p-0.5"
      data-testid="chat-view-tabs"
    >
      {VIEWS.map(({ id, label, icon: Icon }) => {
        const active = id === view;
        return (
          <button
            key={id}
            type="button"
            role="tab"
            aria-selected={active}
            aria-label={label}
            title={label}
            data-testid={`chat-view-tab-${id}`}
            onClick={() => onChange(id)}
            className={cn(
              "touch-target inline-flex h-6 items-center gap-1.5 rounded-md px-1.5 text-ui transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring xl:px-2",
              active ? "bg-foreground/5 text-foreground" : "text-muted-foreground hover:text-foreground",
            )}
          >
            <Icon className="size-3.5" aria-hidden="true" />
            <span className="hidden xl:inline">{label}</span>
          </button>
        );
      })}
    </div>
  );
}
