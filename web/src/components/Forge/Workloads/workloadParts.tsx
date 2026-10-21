// Copyright (c) 2025 Reliant Labs

/**
 * The small pieces the Overview and the workload page share: the health
 * verdict as a dot and a word, the release a workload runs (and how that is
 * known), a usage meter against the limit, and the toolbar chips.
 */

import { useState, type ReactNode } from "react";
import { ChevronDown } from "lucide-react";

import Link from "@/components/forge-ui/link";
import Status from "@/components/forge-ui/status";
import { useEscapeLayer } from "@/hooks/useEscapeLayer";
import type { ResourceUsage, Workload } from "@/services/controlPlane/observability/types";
import { cn } from "@/lib/utils";

import { formatPercent } from "../Observability/format";
import { INK_LINK } from "../Releases/releaseParts";
import { shortVersion } from "../Releases/releaseModel";
import { HEALTH_STATUS } from "./health";

/** The share of the limit at which a meter turns to the warning tone (the mockup's 85%). */
export const USAGE_HOT = 0.85;

/** The server's verdict, as a dot and a word; the reason on hover. The word is tinted only when it is not fine (D8). */
export function HealthStatus({ workload, withReason = false }: { workload: Pick<Workload, "health" | "healthReason">; withReason?: boolean }) {
  const status = HEALTH_STATUS[workload.health];
  return (
    <span className="inline-flex min-w-0 items-center gap-2" title={workload.healthReason || undefined} data-health={workload.health}>
      <Status tone={status.tone} label={status.label} />
      {withReason && workload.healthReason && <span className="truncate text-xs text-ink-muted">{workload.healthReason}</span>}
    </span>
  );
}

/**
 * The release a workload runs, linked to it. A release established from the
 * newest promotion rather than an observed digest is DECLARED, not verified,
 * and says so on hover — the two must never read alike.
 */
export function WorkloadRelease({
  workload,
  releaseHref,
}: {
  workload: Pick<Workload, "releaseVersion" | "releaseBasis" | "serviceVersion">;
  releaseHref: (version: string) => string;
}) {
  if (!workload.releaseVersion) {
    return (
      <span className="text-ink-muted" title="Reliant could not tell which release this runs">
        {workload.serviceVersion || "unknown"}
      </span>
    );
  }
  const declared = workload.releaseBasis === "promotion";
  return (
    <span className="inline-flex items-center gap-1.5">
      <Link
        href={releaseHref(workload.releaseVersion)}
        className={cn(INK_LINK, "relative z-10")}
        title={
          declared
            ? `${workload.releaseVersion}: the newest promotion that moved this workload. No image digest was observed, so it is declared, not verified.`
            : `${workload.releaseVersion}: the running image digest is the one this release pinned.`
        }
      >
        {shortVersion(workload.releaseVersion)}
      </Link>
      {declared && <span className="text-xs text-ink-muted">declared</span>}
    </span>
  );
}

/** Which bound a usage is measured against: the limit, else the request. */
function boundOf(usage: ResourceUsage): { share: number | null; bound: number | null; against: "limit" | "request" | null } {
  if (usage.limitUtilizationLatest !== null || usage.limit !== null) return { share: usage.limitUtilizationLatest, bound: usage.limit, against: "limit" };
  if (usage.requestUtilizationLatest !== null || usage.request !== null) return { share: usage.requestUtilizationLatest, bound: usage.request, against: "request" };
  return { share: null, bound: null, against: null };
}

/**
 * Latest use against the limit (or the request): a short bar and the share,
 * in ink, turning to the warning tone at 85%. Without a limit or a request
 * there is nothing to measure against, and it says so instead of drawing an
 * empty bar.
 */
export function UsageMeter({ usage, format, what }: { usage: ResourceUsage | null; format: (value: number) => string; what: string }) {
  if (!usage) return <span className="text-xs text-ink-muted">not reported</span>;
  const { share, bound, against } = boundOf(usage);
  const hot = share !== null && share >= USAGE_HOT;
  const text = bound !== null ? `${format(usage.latest)} of ${format(bound)} ${against}` : format(usage.latest);
  const label = against ? `${what}: ${text}${share !== null ? `, ${formatPercent(share)}` : ""}; p95 ${format(usage.p95)}` : `${what}: ${text}, no limit or request set; p95 ${format(usage.p95)}`;
  if (share === null) {
    return (
      <span className="text-xs tabular-nums text-ink-muted" title={label} data-testid="usage-meter" data-hot="false">
        {format(usage.latest)}
        {against === null && " · no limit"}
      </span>
    );
  }
  return (
    <span className="inline-flex items-center gap-2" title={label} data-testid="usage-meter" data-hot={hot ? "true" : "false"}>
      <span className="relative block h-1 w-14 overflow-hidden rounded-full bg-border/70" aria-hidden="true">
        <span className={cn("absolute inset-y-0 left-0 rounded-full", hot ? "bg-warning" : "bg-ink-muted")} style={{ width: `${Math.min(share, 1) * 100}%` }} />
      </span>
      <span className={cn("text-xs tabular-nums", hot ? "text-warning-ink" : "text-ink-muted")}>{formatPercent(share)}</span>
      <span className="sr-only">{label}</span>
    </span>
  );
}

// ── Toolbar chips (the Releases list's, in the same shape) ─────────────────

const CHIP =
  "inline-flex h-7 items-center gap-1 rounded-full border bg-surface px-2.5 text-xs transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent";
const chipState = (on: boolean) => (on ? "border-ink-subtle text-ink" : "border-border text-ink-muted hover:border-ink-subtle hover:text-ink");

export function Chip({ on, onClick, children, title, testId }: { on: boolean; onClick: () => void; children: ReactNode; title?: string; testId?: string }) {
  return (
    <button type="button" onClick={onClick} aria-pressed={on} title={title} data-testid={testId} className={cn(CHIP, chipState(on))}>
      {children}
    </button>
  );
}

export function ChipMenu({
  label,
  value,
  options,
  onChange,
  testId,
}: {
  label: string;
  value?: string;
  options: { value: string; label: string }[];
  onChange: (value: string | undefined) => void;
  testId?: string;
}) {
  const [open, setOpen] = useState(false);
  const escapeLayer = useEscapeLayer(open, () => setOpen(false));
  const current = options.find((option) => option.value === value);
  return (
    <div
      className="relative"
      onBlur={(event) => {
        if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setOpen(false);
      }}
    >
      <button type="button" aria-haspopup="menu" aria-expanded={open} onClick={() => setOpen((prev) => !prev)} data-testid={testId} className={cn(CHIP, chipState(!!current))}>
        {label}: {current ? current.label : "all"}
        <ChevronDown className="h-3 w-3" aria-hidden="true" />
      </button>
      {open && (
        <div role="menu" {...escapeLayer} className="absolute left-0 top-8 z-50 min-w-44 rounded-lg border border-border bg-surface p-1 shadow-md">
          {[{ value: "", label: "All" }, ...options].map((option) => (
            <button
              key={option.value || "all"}
              type="button"
              role="menuitemradio"
              aria-checked={(option.value || undefined) === value}
              onClick={() => {
                setOpen(false);
                onChange(option.value || undefined);
              }}
              className={cn("flex h-8 w-full items-center rounded-md px-2 text-left text-sm text-ink hover:bg-ink/5", (option.value || undefined) === value && "font-medium")}
            >
              {option.label}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

/** A table's loading rows, at the table's own row height so nothing shifts when the data lands. */
export function SkeletonRows({ columns, rows = 4 }: { columns: number; rows?: number }) {
  return (
    <>
      {Array.from({ length: rows }).map((_, i) => (
        <tr key={i} className="border-t border-border/70 first:border-t-0">
          <td colSpan={columns} className="p-0">
            <span className="sr-only">Loading…</span>
            <div aria-hidden="true" className="flex h-10 items-center gap-4 px-4">
              <div className="h-3 w-40 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" />
              <div className="h-3 w-20 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" />
              <div className="h-3 flex-1 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" />
            </div>
          </td>
        </tr>
      ))}
    </>
  );
}
