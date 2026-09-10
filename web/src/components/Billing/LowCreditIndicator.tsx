/**
 * The ambient "your credit is running out" indicator.
 *
 * ── Why this exists in the header ─────────────────────────────────────
 *
 * The runway estimate was already built, already tested, and already rendered —
 * inside billing settings, which is the one place a person running out of
 * credit has no reason to be. This is a PLACEMENT fix, not new arithmetic: the
 * same `estimateCreditRunwayDays` the settings page uses, surfaced where the
 * user actually is.
 *
 * ── The pattern it follows ────────────────────────────────────────────
 *
 * `ConfigHealthIndicator`, sitting a few pixels away in the same header row:
 * an icon that is ABSENT when there is nothing to say, a click-through for
 * detail, and no interruption. Adopting its shape rather than inventing a
 * second visual language for ambient status is most of the value of putting it
 * here.
 *
 * ── Ignorable by construction ─────────────────────────────────────────
 *
 * It renders nothing at all above the threshold — not a green tick, not a
 * neutral balance chip. A user who is fine must never see it, because an
 * indicator that is always present is one nobody reads, and it would take the
 * genuine warning down with it. There is no dismiss button for the same reason
 * there is no toast: a persistent indicator does not need one, and a dismissed
 * warning about money is a warning that will be missed.
 *
 * ── Who never sees it ─────────────────────────────────────────────────
 *
 * Anyone not spending Reliant credit. A user on their own API key has no
 * balance to run out of, and the wallet query is simply not enabled for them —
 * see `useLowCreditStatus`.
 */

import { Wallet } from "lucide-react";
import { useNavigate } from "@tanstack/react-router";

import { Tooltip } from "@/components/ui/Tooltip";
import { cn } from "@/lib/utils";

import { lowCreditLabel } from "./lowCredit";
import { useLowCreditStatus } from "./useLowCreditStatus";

export function LowCreditIndicator({ className }: { className?: string }) {
  const navigate = useNavigate();
  const { status, formattedBalance, available } = useLowCreditStatus();

  // Nothing to say, or nothing to say it about. Both render absolutely nothing.
  if (!available || status.urgency === "healthy") return null;

  const empty = status.urgency === "empty";
  const label = lowCreditLabel(status, formattedBalance);

  return (
    <Tooltip
      content={
        empty
          ? "Your balance is empty — add credit to keep working"
          : `${label} — click to add credit`
      }
      placement="bottom"
      delay={300}
    >
      <button
        onClick={() =>
          void navigate({
            to: "/settings/$section",
            params: { section: "billing" },
          })
        }
        className={cn(
          "flex items-center gap-1.5 rounded px-2 py-1 text-xs font-medium transition-colors",
          // Empty is a failure state and reads as one. Low is a warning, and
          // deliberately not red: red for a week's notice is the crying-wolf
          // that makes the empty state unreadable when it arrives.
          empty
            ? "text-destructive hover:bg-destructive/10"
            : "text-yellow-600 hover:bg-yellow-500/10 dark:text-yellow-500",
          className,
        )}
        style={{ WebkitAppRegion: "no-drag" } as React.CSSProperties}
        aria-label={label}
        data-testid="low-credit-indicator"
      >
        <Wallet className="h-3.5 w-3.5 shrink-0" aria-hidden />
        {/* The QUANTITY, not the word "low". "~3 days of credit left" tells a
            user whether to act now or after lunch; "low balance" does not. */}
        <span className="whitespace-nowrap">{label}</span>
      </button>
    </Tooltip>
  );
}
