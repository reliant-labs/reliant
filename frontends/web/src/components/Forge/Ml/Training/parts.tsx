// Copyright (c) 2026 Reliant Labs

/** Small parts the Training pages share: a demo-disabled action, a run's state with its badges, a cost pair. */

import { useState, type ReactNode } from "react";
import { AlertTriangle, Check, Clock, X } from "lucide-react";

import Button from "@/components/forge-ui/button";
import Tooltip from "@/components/forge-ui/tooltip";
import { MlAttemptEndReason, MlRunState, type MlCost, type MlRun } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import { cn } from "@/lib/utils";

import { Chip, StatusDot, endReasonOf, fmtGpuHours, fmtUsd, runBadgeOf, runStateOf } from "../kit";

/** A write the demo shell cannot do: a disabled button that says why on hover. */
export function DemoAction({ children, icon, reason = "Demo shell: read-only", testId }: { children: ReactNode; icon?: ReactNode; reason?: string; testId?: string }) {
  return (
    <Tooltip content={reason}>
      <span className="inline-flex" data-testid={testId}>
        <Button variant="outline" size="sm" disabled leftIcon={icon}>
          {children}
        </Button>
      </span>
    </Tooltip>
  );
}

/** A run's state as a dot and word, then its badges. */
export function RunState({ run, compact }: { run: MlRun; compact?: boolean }) {
  const state = runStateOf(run.state);
  return (
    <span className="inline-flex flex-wrap items-center gap-1.5">
      <StatusDot tone={state.tone} label={state.label} pulse={run.state === MlRunState.RUNNING || run.state === MlRunState.DEGRADED} />
      {run.badges.map((b) => {
        const badge = runBadgeOf(b);
        return compact ? (
          <span key={b} className="text-meta text-ink-muted" title={badge.label}>
            · {badge.label.toLowerCase()}
          </span>
        ) : (
          <Chip key={b} tone={badge.tone}>
            {badge.label}
          </Chip>
        );
      })}
    </span>
  );
}

/** GPU-hours and money on one line: "1,204 GPU-h · $5.1k". */
export function CostPair({ cost, className }: { cost: MlCost | undefined; className?: string }) {
  if (!cost) return <span className="text-ink-muted">—</span>;
  return (
    <span className={cn("tabular-nums", className)}>
      {fmtGpuHours(cost.gpuHours)}
      <span className="text-ink-muted"> · {cost.priced ? fmtUsd(cost.usd) : "unpriced"}</span>
    </span>
  );
}

/** An attempt's end as an icon (and its word for screen readers). */
export function EndIcon({ reason, className }: { reason: MlAttemptEndReason; className?: string }) {
  const end = endReasonOf(reason);
  const cls = cn("h-3.5 w-3.5 shrink-0", className);
  const icon =
    reason === MlAttemptEndReason.UNSPECIFIED ? (
      <Clock className={cn(cls, "text-primary-ink")} />
    ) : reason === MlAttemptEndReason.COMPLETED ? (
      <Check className={cn(cls, "text-ink-muted")} />
    ) : end.tone === "danger" ? (
      <X className={cn(cls, "text-danger-ink")} />
    ) : (
      <AlertTriangle className={cn(cls, "text-warning-ink")} />
    );
  return (
    <span className="inline-flex items-center" title={end.label}>
      {icon}
      <span className="sr-only">{end.label}</span>
    </span>
  );
}

/** A collapsed block: a one-line summary that opens to its body. */
export function Disclosure({ summary, children, testId }: { summary: ReactNode; children: ReactNode; testId?: string }) {
  const [open, setOpen] = useState(false);
  return (
    <div data-testid={testId}>
      <button type="button" className="text-meta text-ink-muted hover:text-ink" aria-expanded={open} onClick={() => setOpen((v) => !v)}>
        {open ? "▾" : "▸"} {summary}
      </button>
      {open && <div className="mt-1.5">{children}</div>}
    </div>
  );
}

/** "phase · attempt n" as a person reads it. */
export const attemptLabel = (phaseKey: string, n: number) => `${phaseKey} · a${n}`;
