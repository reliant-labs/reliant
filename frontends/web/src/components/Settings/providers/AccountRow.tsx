import { useState, type KeyboardEvent } from "react";
import { ChevronDown, GripVertical, MoreHorizontal } from "lucide-react";
import Button from "@/components/forge-ui/button";
import DropdownMenu, { type MenuItem } from "@/components/forge-ui/dropdown_menu";
import { cn } from "../../../lib/utils";
import type { AccountStatus, ProviderAccount } from "../../../types/providerAccount";
import { UsageBars, UsageMeters } from "./UsageBars";
import { limitedSentence, planLabel } from "./accountFormat";

/**
 * One account as the cells of its provider's accounts table (AccountList), so
 * every account lines up under the same columns: its place in the rotation,
 * the account itself, its usage, and its actions — with the full usage bars
 * and identity line in the row's expanded detail. Exception-first: a healthy
 * account is one line; a limited, nearly limited or signed-out one opens by
 * itself (AccountList decides) to say what is wrong and when it ends.
 */

const STATUS_INK: Record<Exclude<AccountStatus, "available">, string> = {
  limited: "text-warning-ink",
  paused: "text-muted-foreground",
  needs_sign_in: "text-destructive-ink",
};

/**
 * The account's state in words, or nothing when it is healthy. An "Available"
 * pill on every row said nothing that the absence of a problem does not.
 */
export function AccountStatusText({ account, now }: { account: ProviderAccount; now: Date }) {
  if (account.status === "available") return null;
  const text =
    account.status === "limited"
      ? limitedSentence(account, now)
      : account.status === "paused"
        ? "Paused"
        : "Needs sign-in";
  return (
    <span
      data-testid="account-status"
      data-status={account.status}
      className={cn("shrink-0 text-xs font-medium", STATUS_INK[account.status])}
    >
      {text}
    </span>
  );
}

/** The id of an account's expanded detail, for its toggle's aria-controls. */
export function accountDetailId(account: ProviderAccount): string {
  return `account-detail-${account.id}`;
}

export interface AccountDrag {
  onDragStart: (id: string) => void;
  onDragEnd: () => void;
}

/**
 * The account's place in the rotation: its number and, on desktop, the drag
 * handle — which also reorders from the keyboard (arrow keys).
 */
export function AccountOrderCell({
  account,
  index,
  count,
  drag,
  onMove,
}: {
  account: ProviderAccount;
  index: number;
  count: number;
  /** Absent on mobile: reorder is Move up / Move down there. */
  drag?: AccountDrag;
  onMove: (direction: -1 | 1) => void;
}) {
  const hintId = `account-reorder-hint-${account.id}`;
  const onHandleKey = (e: KeyboardEvent<HTMLButtonElement>) => {
    if (e.key === "ArrowUp" && index > 0) {
      e.preventDefault();
      onMove(-1);
    } else if (e.key === "ArrowDown" && index < count - 1) {
      e.preventDefault();
      onMove(1);
    }
  };
  return (
    <span className="flex items-center gap-1">
      {drag && (
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
              drag.onDragStart(account.id);
            }}
            onDragEnd={() => drag.onDragEnd()}
            className="shrink-0 cursor-grab rounded-md p-0.5 text-muted-foreground hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/40 active:cursor-grabbing"
          >
            <GripVertical className="h-4 w-4" />
          </button>
          <span id={hintId} className="sr-only">
            Position {index + 1} of {count}. Press the up or down arrow key to move.
          </span>
        </>
      )}
      <span className="w-4 text-center text-xs tabular-nums text-muted-foreground" aria-hidden="true">
        {index + 1}
      </span>
    </span>
  );
}

/** The account's name (or its rename field), plan, and state. */
export function AccountNameCell({
  account,
  now,
  renaming,
  mobile,
  onRename,
  onRenameEnd,
}: {
  account: ProviderAccount;
  now: Date;
  renaming: boolean;
  mobile: boolean;
  onRename: (label: string) => void;
  onRenameEnd: () => void;
}) {
  const plan = planLabel(account);
  return (
    <span className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5 whitespace-normal py-2">
      {renaming ? (
        <RenameField account={account} mobile={mobile} onRename={onRename} onDone={onRenameEnd} />
      ) : (
        <span data-sentry-mask className="min-w-0 truncate text-sm font-medium text-foreground">
          {account.label}
        </span>
      )}
      {plan && (
        <span data-testid="account-plan" className="shrink-0 text-xs text-muted-foreground">
          {plan}
        </span>
      )}
      <AccountStatusText account={account} now={now} />
    </span>
  );
}

function RenameField({
  account,
  mobile,
  onRename,
  onDone,
}: {
  account: ProviderAccount;
  mobile: boolean;
  onRename: (label: string) => void;
  onDone: () => void;
}) {
  const [draft, setDraft] = useState(account.label);
  const commit = () => {
    onDone();
    // Unchanged is a no-op; an empty label is sent so the server restores the default (the email).
    if (draft.trim() !== account.label) onRename(draft.trim());
  };
  return (
    <input
      autoFocus
      aria-label={`Name for ${account.label}`}
      value={draft}
      placeholder={account.email ?? "Account name"}
      onChange={(e) => setDraft(e.target.value)}
      onBlur={commit}
      onKeyDown={(e) => {
        if (e.key === "Enter") commit();
        if (e.key === "Escape") onDone();
      }}
      className={cn(
        "min-w-0 rounded-md border border-border bg-background px-2 text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-ring/40",
        mobile ? "min-h-[44px] w-full" : "py-1",
      )}
    />
  );
}

/** Small meters while the row is collapsed; the expanded detail has the full bars. */
export function AccountUsageCell({ account, now, open }: { account: ProviderAccount; now: Date; open: boolean }) {
  return open ? null : <UsageMeters account={account} now={now} />;
}

export interface AccountActionsProps {
  account: ProviderAccount;
  index: number;
  count: number;
  mobile: boolean;
  open: boolean;
  onToggleOpen: () => void;
  onStartRename: () => void;
  onTogglePause: () => void;
  onMove: (direction: -1 | 1) => void;
  onReconnect: () => void;
  onRemove: () => void;
}

/** Reconnect when signed out, the detail toggle, and the account's menu. */
export function AccountActionsCell({
  account,
  index,
  count,
  mobile,
  open,
  onToggleOpen,
  onStartRename,
  onTogglePause,
  onMove,
  onReconnect,
  onRemove,
}: AccountActionsProps) {
  const canReconnect = account.authKind === "oauth" || account.authKind === "device";
  const several = count > 1;
  const items: MenuItem[] = [
    { label: "Rename", onClick: onStartRename },
    { label: account.status === "paused" ? "Resume" : "Pause", onClick: onTogglePause },
    ...(several
      ? [
          { label: "Move up", onClick: () => onMove(-1), disabled: index === 0 },
          { label: "Move down", onClick: () => onMove(1), disabled: index >= count - 1 },
        ]
      : []),
    ...(canReconnect ? [{ label: "Reconnect", onClick: onReconnect }] : []),
    { label: "Remove", onClick: onRemove, variant: "danger" as const },
  ];
  const iconButton = cn(
    "flex shrink-0 items-center justify-center rounded-md text-muted-foreground hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/40",
    mobile ? "min-h-[44px] min-w-[44px]" : "h-7 w-7",
  );
  return (
    <span className="flex items-center justify-end gap-1">
      {account.status === "needs_sign_in" && canReconnect && (
        // secondary, not outline: inside .forge-ui `accent` is the primary colour.
        <Button
          variant="secondary"
          size={mobile ? "xl" : "xs"}
          className={mobile ? "min-h-[44px]" : undefined}
          onClick={onReconnect}
        >
          Reconnect
        </Button>
      )}
      <button
        type="button"
        aria-expanded={open}
        aria-controls={open ? accountDetailId(account) : undefined}
        aria-label={`Details for ${account.label}`}
        onClick={onToggleOpen}
        className={iconButton}
      >
        <ChevronDown className={cn("h-4 w-4 transition-transform", open && "rotate-180")} />
      </button>
      <DropdownMenu
        align="right"
        label={`${account.label} actions`}
        menuClassName={mobile ? "[&_[role=menuitem]]:min-h-[44px]" : undefined}
        trigger={(props) => (
          <button type="button" {...props} aria-label={`Actions for ${account.label}`} className={iconButton}>
            <MoreHorizontal className="h-4 w-4" />
          </button>
        )}
        groups={[{ items }]}
      />
    </span>
  );
}

/** The expanded detail: who the account is, why it is signed out, and its usage bars. */
export function AccountDetail({
  account,
  now,
  mobile,
  indent,
  onRefresh,
  refreshing,
  refreshNote,
}: {
  account: ProviderAccount;
  now: Date;
  mobile: boolean;
  /** Line the detail up under the account column when an order column sits before it. */
  indent: boolean;
  onRefresh: () => void;
  refreshing: boolean;
  refreshNote?: string;
}) {
  const secondary = [
    account.email && account.email !== account.label ? account.email : null,
    account.organizationName,
    account.maskedKey && account.maskedKey !== account.label ? account.maskedKey : null,
  ].filter(Boolean) as string[];
  return (
    <div id={accountDetailId(account)} className={cn("space-y-2", indent && (mobile ? "pl-10" : "pl-16"))}>
      {secondary.length > 0 && (
        <p data-sentry-mask className="truncate font-mono text-xs text-muted-foreground">
          {secondary.join(" · ")}
        </p>
      )}
      {account.status === "needs_sign_in" && account.needsSignInReason && (
        <p className="text-xs text-destructive-ink">{account.needsSignInReason}</p>
      )}
      <UsageBars
        account={account}
        now={now}
        onRefresh={onRefresh}
        refreshing={refreshing}
        refreshNote={refreshNote}
        compact={mobile}
      />
    </div>
  );
}
