// Copyright (c) 2025 Reliant Labs

/**
 * The composer's account chip: next to the model pill, and only when the
 * account this chat is running on is at or above 80% in a window, or limited.
 *
 *   82% of 5h · alice@            (amber)
 *   Limited · next account bob@   (amber: another account can take over)
 *   Limited until 3:40 PM         (red: none can)
 *
 * Hover (or focus, or a tap on a phone) opens the account's usage bars, the
 * same ones Settings → Providers shows. Absent otherwise: an account with room
 * to spare is not news.
 */

import { useCallback, useEffect, useId, useRef, useState } from "react";
import { cn } from "@/lib/utils";
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

const TONE: Record<AccountChipState["kind"], string> = {
  usage: "border-warning/30 bg-warning/10 text-warning-ink",
  limited_next: "border-warning/30 bg-warning/10 text-warning-ink",
  limited_until: "border-destructive/30 bg-destructive/10 text-destructive-ink",
};

export function AccountUsageChip({ chatId }: { chatId?: string }) {
  const { active, accounts } = useChatAccountUsage(chatId);
  const now = useNow();
  const compact = useSurface() === "mobile";
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);
  const popoverId = useId();

  const state = active ? accountChipState(active, accounts, now) : null;
  const visible = !!active && !!state;

  useEffect(() => {
    if (!open) return;
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
  }, [open]);

  const show = useCallback(() => setOpen(true), []);
  const hide = useCallback(() => setOpen(false), []);

  if (!visible || !active || !state) return null;
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
          "flex h-6 max-w-[16rem] items-center rounded-full border px-2.5 text-2xs font-medium transition-colors",
          compact && "min-h-[28px]",
          TONE[state.kind],
        )}
      >
        <span className="truncate">{text.label}</span>
      </button>
      {open && (
        <div
          id={popoverId}
          role="dialog"
          aria-label={`${active.label} usage`}
          data-testid="account-usage-popover"
          className="absolute bottom-[calc(100%+8px)] left-0 z-[100] w-[min(20rem,calc(100vw-2rem))] space-y-2 rounded-xl border border-border bg-popover p-3 shadow-xl"
        >
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
        </div>
      )}
    </div>
  );
}
