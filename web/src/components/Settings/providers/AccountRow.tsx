import { useEffect, useId, useRef, useState, type DragEvent, type KeyboardEvent } from "react";
import { GripVertical, MoreHorizontal } from "lucide-react";
import { cn } from "../../../lib/utils";
import type { AccountStatus, ProviderAccount } from "../../../api/provider-account-grpc";
import { PlanBadge } from "./PlanBadge";
import { UsageBars } from "./UsageBars";
import { formatResetsIn, limitedLabel } from "./accountFormat";

const PILL: Record<AccountStatus, string> = {
  available: "border-success/20 bg-success/10 text-success-ink",
  limited: "border-warning/30 bg-warning/10 text-warning-ink",
  paused: "border-border bg-foreground/5 text-muted-foreground",
  needs_sign_in: "border-destructive/30 bg-destructive/10 text-destructive-ink",
};

export function StatusPill({ account, now }: { account: ProviderAccount; now: Date }) {
  const text =
    account.status === "available"
      ? "Available"
      : account.status === "limited"
        ? limitedLabel(account, now)
        : account.status === "paused"
          ? "Paused"
          : "Needs sign-in";
  const countdown =
    account.status === "limited" ? formatResetsIn(account.limitedUntil, now) : "";
  const title =
    account.status === "needs_sign_in" ? account.needsSignInReason : countdown || undefined;
  return (
    <span
      data-testid="status-pill"
      data-status={account.status}
      title={title}
      className={cn(
        "inline-flex shrink-0 items-center rounded-full border px-2 py-0.5 text-2xs font-medium leading-none",
        PILL[account.status],
      )}
    >
      {text}
    </span>
  );
}

export interface AccountRowProps {
  account: ProviderAccount;
  index: number;
  count: number;
  now: Date;
  /** "mobile" has no drag handle (reorder is Move up / Move down) and touch-sized controls. */
  variant: "desktop" | "mobile";
  onRename: (label: string) => void;
  onTogglePause: () => void;
  onMove: (direction: -1 | 1) => void;
  onReconnect: () => void;
  onRemove: () => void;
  onRefresh: () => void;
  refreshing: boolean;
  refreshNote?: string;
  drag?: {
    onDragStart: (id: string) => void;
    onDragEnd: () => void;
    onDropOn: (id: string) => void;
    dragging: boolean;
  };
}

export function AccountRow({
  account,
  index,
  count,
  now,
  variant,
  onRename,
  onTogglePause,
  onMove,
  onReconnect,
  onRemove,
  onRefresh,
  refreshing,
  refreshNote,
  drag,
}: AccountRowProps) {
  const mobile = variant === "mobile";
  const [menuOpen, setMenuOpen] = useState(false);
  const [renaming, setRenaming] = useState(false);
  const [draft, setDraft] = useState(account.label);
  const menuRef = useRef<HTMLDivElement>(null);
  const menuId = useId();
  const hintId = useId();

  useEffect(() => {
    if (!menuOpen) return;
    const onDown = (e: MouseEvent) => {
      if (!menuRef.current?.contains(e.target as Node)) setMenuOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, [menuOpen]);

  const commitRename = () => {
    setRenaming(false);
    // Unchanged is a no-op; an empty label is sent so the server restores the default (the email).
    if (draft.trim() !== account.label) onRename(draft.trim());
  };

  const onHandleKey = (e: KeyboardEvent<HTMLButtonElement>) => {
    if (e.key === "ArrowUp" && index > 0) {
      e.preventDefault();
      onMove(-1);
    } else if (e.key === "ArrowDown" && index < count - 1) {
      e.preventDefault();
      onMove(1);
    }
  };

  const onMenuKey = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.key === "Escape") {
      e.stopPropagation();
      setMenuOpen(false);
    }
  };

  const canReconnect = account.authKind === "oauth" || account.authKind === "device";
  const secondary = [
    account.email && account.email !== account.label ? account.email : null,
    account.organizationName,
    account.maskedKey && account.maskedKey !== account.label ? account.maskedKey : null,
  ].filter(Boolean) as string[];

  const itemClass = cn(
    "flex w-full items-center rounded-md px-3 text-left text-sm text-foreground hover:bg-foreground/5 disabled:opacity-40 disabled:hover:bg-transparent",
    mobile ? "min-h-[44px]" : "py-1.5",
  );
  const choose = (fn: () => void) => () => {
    setMenuOpen(false);
    fn();
  };

  const dragProps = drag
    ? {
        onDragOver: (e: DragEvent) => e.preventDefault(),
        onDrop: (e: DragEvent) => {
          e.preventDefault();
          drag.onDropOn(account.id);
        },
      }
    : {};

  return (
    <li
      data-testid="account-row"
      data-account-id={account.id}
      aria-label={account.label}
      className={cn(
        "rounded-lg border border-border/60 bg-background p-3",
        drag?.dragging && "opacity-50",
        account.status === "paused" && "opacity-80",
      )}
      {...dragProps}
    >
      <div className="flex items-start gap-2">
        {!mobile && (
          <>
            <button
              type="button"
              draggable
              aria-label={`Reorder ${account.label}`}
              aria-describedby={hintId}
              onKeyDown={onHandleKey}
              onDragStart={(e) => {
                e.dataTransfer?.setData("text/plain", account.id);
                if (e.dataTransfer) e.dataTransfer.effectAllowed = "move";
                drag?.onDragStart(account.id);
              }}
              onDragEnd={() => drag?.onDragEnd()}
              className="mt-0.5 shrink-0 cursor-grab rounded-md p-1 text-muted-foreground hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/40 active:cursor-grabbing"
            >
              <GripVertical className="h-4 w-4" />
            </button>
            <span id={hintId} className="sr-only">
              Position {index + 1} of {count}. Press the up or down arrow key to move.
            </span>
          </>
        )}

        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
            {renaming ? (
              <input
                autoFocus
                aria-label={`Name for ${account.label}`}
                value={draft}
                placeholder={account.email ?? "Account name"}
                onChange={(e) => setDraft(e.target.value)}
                onBlur={commitRename}
                onKeyDown={(e) => {
                  if (e.key === "Enter") commitRename();
                  if (e.key === "Escape") {
                    setDraft(account.label);
                    setRenaming(false);
                  }
                }}
                className={cn(
                  "min-w-0 rounded-md border border-border bg-background px-2 text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-ring/40",
                  mobile ? "min-h-[44px] w-full" : "py-1",
                )}
              />
            ) : (
              <span
                data-sentry-mask
                className="min-w-0 truncate text-sm font-medium text-foreground"
              >
                {account.label}
              </span>
            )}
            <PlanBadge account={account} />
            <StatusPill account={account} now={now} />
            {account.status === "needs_sign_in" && canReconnect && (
              <button
                type="button"
                onClick={onReconnect}
                className={cn(
                  "rounded-md border border-border px-2 text-xs font-medium text-foreground hover:bg-foreground/5",
                  mobile ? "min-h-[44px]" : "py-0.5",
                )}
              >
                Reconnect
              </button>
            )}
          </div>
          {secondary.length > 0 && (
            <p data-sentry-mask className="mt-0.5 truncate font-mono text-xs text-muted-foreground">
              {secondary.join(" · ")}
            </p>
          )}
        </div>

        <div ref={menuRef} className="relative shrink-0" onKeyDown={onMenuKey}>
          <button
            type="button"
            aria-label={`Actions for ${account.label}`}
            aria-haspopup="menu"
            aria-expanded={menuOpen}
            aria-controls={menuOpen ? menuId : undefined}
            onClick={() => setMenuOpen((v) => !v)}
            className={cn(
              "flex items-center justify-center rounded-md text-muted-foreground hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/40",
              mobile ? "min-h-[44px] min-w-[44px]" : "h-7 w-7",
            )}
          >
            <MoreHorizontal className="h-4 w-4" />
          </button>
          {menuOpen && (
            <div
              id={menuId}
              role="menu"
              aria-label={`${account.label} actions`}
              className="absolute right-0 z-20 mt-1 min-w-[10rem] rounded-lg border border-border bg-card p-1 shadow-lg"
            >
              <button
                type="button"
                role="menuitem"
                className={itemClass}
                onClick={choose(() => {
                  setDraft(account.label);
                  setRenaming(true);
                })}
              >
                Rename
              </button>
              <button type="button" role="menuitem" className={itemClass} onClick={choose(onTogglePause)}>
                {account.status === "paused" ? "Resume" : "Pause"}
              </button>
              <button
                type="button"
                role="menuitem"
                className={itemClass}
                disabled={index === 0}
                onClick={choose(() => onMove(-1))}
              >
                Move up
              </button>
              <button
                type="button"
                role="menuitem"
                className={itemClass}
                disabled={index >= count - 1}
                onClick={choose(() => onMove(1))}
              >
                Move down
              </button>
              {canReconnect && (
                <button type="button" role="menuitem" className={itemClass} onClick={choose(onReconnect)}>
                  Reconnect
                </button>
              )}
              <button
                type="button"
                role="menuitem"
                className={cn(itemClass, "text-destructive-ink")}
                onClick={choose(onRemove)}
              >
                Remove
              </button>
            </div>
          )}
        </div>
      </div>

      {account.status === "needs_sign_in" && account.needsSignInReason && (
        <p className="mt-2 text-xs text-destructive-ink">{account.needsSignInReason}</p>
      )}

      <div className={cn("mt-3", !mobile && "pl-7")}>
        <UsageBars
          account={account}
          now={now}
          onRefresh={onRefresh}
          refreshing={refreshing}
          refreshNote={refreshNote}
          compact={mobile}
        />
      </div>
    </li>
  );
}
