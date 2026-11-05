// Copyright (c) 2025 Reliant Labs

/**
 * The composer's account chip: in the composer's control row, and only when
 * the account this chat is running on is at or above 80% in a window, or
 * limited.
 *
 *   82% of 5h · alice@            (amber)
 *   Limited · next account bob@   (amber: another account can take over)
 *   Limited until 3:40 PM         (red: none can)
 *
 * Hover (or focus, or a tap on a phone) opens the account's usage bars, the
 * same ones Settings → Providers shows. Absent otherwise: an account with room
 * to spare is not news.
 *
 * The ⊕ sheet shows the same thing as a row and a page
 * (`AccountUsageDetails`), under the same "only when it matters" rule —
 * `useAccountUsageChip` is that rule, shared.
 */

import { COMPOSER_CONTROL } from "./composerControl";
import { useCallback, useEffect, useId, useRef, useState } from "react";
import { cn } from "@/lib/utils";
import { ComposerSheet } from "./ComposerSheet";
import type { ReactNode } from "react";
import { useSurface } from "@/lib/surfaceContext";
import { useChatAccountUsage } from "@/hooks/useChatAccountUsage";
import { PlanBadge } from "@/components/Settings/providers/PlanBadge";
import { UsageBars } from "@/components/Settings/providers/UsageBars";
import { useNow } from "@/components/Settings/providers/useNow";
import { limitedLabel } from "@/components/Settings/providers/accountFormat";
import {
  accountChipState,
  accountChipText,
  isLimitedNow,
  type AccountChipState,
} from "@/lib/accountUsageChip";
import type { ProviderAccount } from "@/types/providerAccount";

function UsagePanel({ mobile, title, id, onClose, children }: { mobile: boolean; title: string; id: string; onClose: () => void; children: ReactNode }) {
  if (mobile) return <ComposerSheet title={title} onClose={onClose}><div id={id} data-testid="account-usage-popover" className="space-y-2">{children}</div></ComposerSheet>;
  return <div id={id} role="dialog" aria-label={title} data-testid="account-usage-popover" className="absolute bottom-[calc(100%+8px)] left-0 z-[100] w-[min(20rem,calc(100vw-2rem))] space-y-2 rounded-xl border border-border bg-popover p-3 shadow-xl">{children}</div>;
}

const TONE: Record<AccountChipState["kind"], string> = {
  usage: "border-warning/30 bg-warning/10 text-warning-ink",
  limited_next: "border-warning/30 bg-warning/10 text-warning-ink",
  limited_until: "border-destructive/30 bg-destructive/10 text-destructive-ink",
};

interface AccountUsage {
  active: ProviderAccount;
  accounts: ProviderAccount[];
  state: AccountChipState;
  now: Date;
}

/** The chat's account usage when it is worth showing (≥ 80% or limited), else null. */
export function useAccountUsageChip(chatId?: string): AccountUsage | null {
  const { active, accounts } = useChatAccountUsage(chatId);
  const now = useNow();
  const state = active ? accountChipState(active, accounts, now) : null;
  if (!active || !state) return null;
  return { active, accounts, state, now };
}

/** The account's name, plan and usage bars: the chip's popover body, and the ⊕ sheet's page. */
export function AccountUsageDetails({ usage, compact }: { usage: AccountUsage; compact: boolean }) {
  const { active, state, now } = usage;
  return (
    <>
      <div className="flex items-center gap-2">
        <div className="min-w-0 flex-1">
          <div className="truncate text-xs font-medium text-foreground">{active.label}</div>
          <div className="truncate text-2xs text-muted-foreground">
            {active.providerDisplayName}
            {isLimitedNow(active, now) && ` · ${limitedLabel(active, now)}`}
          </div>
        </div>
        <PlanBadge account={active} />
      </div>
      <UsageBars account={active} now={now} compact={compact} />
      {state.kind === "limited_next" && (
        <p className="text-2xs text-muted-foreground">
          The next request continues on {state.next.label}.
        </p>
      )}
    </>
  );
}

/** The usage line for a sheet row: "82% of 5h · alice@". */
export function accountUsageLabel(usage: AccountUsage): string {
  return accountChipText(usage.state, usage.active, usage.accounts, usage.now, false).label;
}

/** The tone classes of a usage state, for a row that echoes the chip. */
export function accountUsageTone(usage: AccountUsage): string {
  return TONE[usage.state.kind];
}

export function AccountUsageChip({ chatId }: { chatId?: string }) {
  const usage = useAccountUsageChip(chatId);
  const compact = useSurface() === "mobile";
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);
  const popoverId = useId();

  useEffect(() => {
    if (!open || compact) return;
    const onPointerDown = (e: PointerEvent) => {
      if (!rootRef.current?.contains(e.target as Node)) setOpen(false);
    };
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    document.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [open, compact]);

  const show = useCallback(() => setOpen(true), []);
  const hide = useCallback(() => setOpen(false), []);

  if (!usage) return null;
  const { active, accounts, state, now } = usage;
  const text = accountChipText(state, active, accounts, now, compact);

  return (
    <div
      ref={rootRef}
      className="relative"
      onMouseEnter={compact ? undefined : show}
      onMouseLeave={compact ? undefined : hide}
    >
      <button
        type="button"
        data-testid="account-usage-chip"
        data-state={state.kind}
        aria-label={text.full}
        aria-expanded={open}
        aria-controls={open ? popoverId : undefined}
        onClick={() => setOpen((v) => !v)}
        onFocus={compact ? undefined : show}
        className={cn(
          "flex max-w-[16rem] items-center rounded-full border px-2.5 text-2xs font-medium transition-colors",
          COMPOSER_CONTROL,
          compact && "min-h-[44px]",
          TONE[state.kind],
        )}
      >
        <span className="truncate">{text.label}</span>
      </button>
      {open && (
        <UsagePanel mobile={compact} id={popoverId} title={`${active.label} usage`} onClose={hide}>
          <AccountUsageDetails usage={usage} compact={compact} />
        </UsagePanel>
      )}
    </div>
  );
}
