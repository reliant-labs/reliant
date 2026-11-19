// Copyright (c) 2025 Reliant Labs

/**
 * The app a chat is about (Chat.app_name, control-plane
 * docs/design/unified-shell-and-projects.md §8.5, D4), as a small tag.
 *
 *   ChatAppTag      a chat's own tag: the chat header (a link into the app's
 *                   console) and a chat row (plain: the row is the control).
 *   NewChatAppTag   the composer's: "About <app>", with a remove button, for
 *                   a new chat a screen started from an app.
 *
 * A tag, not an AppChip: a chat knows only the app's name, not its state, so
 * it has no state dot. Neutral, like AppChip's border chip.
 */

import { Link } from "@tanstack/react-router";
import { Boxes, X } from "lucide-react";

import Tooltip from "@/components/forge-ui/tooltip";
import { appPath } from "@/lib/paths";
import { cn } from "@/lib/utils";

const TAG = "inline-flex h-5 min-w-0 max-w-[10rem] shrink-0 items-center gap-1 rounded-md border border-border px-1.5 text-meta leading-none text-muted-foreground";

export interface ChatAppTagProps {
  name: string;
  /** Link into the app's console (default). False inside a row that is itself the control. */
  interactive?: boolean;
  className?: string;
}

export function ChatAppTag({ name, interactive = true, className }: ChatAppTagProps) {
  const body = (
    <>
      <Boxes aria-hidden className="h-3 w-3 shrink-0" />
      <span className="truncate">{name}</span>
    </>
  );
  if (!interactive) {
    return (
      <span className={cn(TAG, className)} title={`About ${name}`} aria-label={`About the app ${name}`} role="img" data-testid="chat-app-tag">
        {body}
      </span>
    );
  }
  return (
    <Tooltip content={`Started from ${name}: open the app`} side="bottom" delay={300} focusable={false}>
      <Link
        to={appPath(name)}
        aria-label={`About the app ${name}: open it`}
        data-testid="chat-app-tag"
        className={cn(TAG, "hover:bg-foreground/5 hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring", className)}
      >
        {body}
      </Link>
    </Tooltip>
  );
}

export function NewChatAppTag({ name, onRemove, disabled = false }: { name: string; onRemove: () => void; disabled?: boolean }) {
  return (
    <span
      className="inline-flex h-9 min-w-0 items-center gap-1.5 rounded-md border border-border pl-3 pr-1 text-ui text-foreground"
      data-testid="new-chat-app-tag"
    >
      <Boxes aria-hidden className="h-4 w-4 shrink-0 text-muted-foreground" />
      <span className="text-muted-foreground">About</span>
      <span className="max-w-[12rem] truncate font-medium">{name}</span>
      <button
        type="button"
        onClick={onRemove}
        disabled={disabled}
        aria-label={`Don't tag this chat with ${name}`}
        title="Not about this app"
        className="inline-flex h-7 w-7 items-center justify-center rounded-md text-muted-foreground hover:bg-foreground/5 hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50"
      >
        <X aria-hidden className="h-3.5 w-3.5" />
      </button>
    </span>
  );
}
