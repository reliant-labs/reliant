// Copyright (c) 2025 Reliant Labs

/**
 * The states every observability view shares: loading, "not set up yet",
 * "nothing in this window", and failures — each worded for what it means to
 * the reader rather than for the error code behind it (errors.ts).
 *
 * "Not set up" is the one a new organization meets first, so it is written as
 * an explanation, not an error: nothing is wrong, nothing needs retrying, and
 * the telemetry starts arriving on its own once the store exists.
 */

import type { ReactNode } from "react";
import { Activity, AlertTriangle, Clock, Lock, RefreshCw, SearchX } from "lucide-react";

import { Button } from "@/components/ui/Button";
import {
  obsErrorKind,
  obsErrorMessage,
  type ObsErrorKind,
} from "@/services/controlPlane/observability/errors";
import type { Provenance } from "@/services/controlPlane/observability/types";
import { RANGE_LABELS, type RangePreset } from "@/services/controlPlane/observability/window";

function StateFrame({
  icon,
  title,
  children,
  actions,
  testId,
  tone = "neutral",
}: {
  icon: ReactNode;
  title: string;
  children?: ReactNode;
  actions?: ReactNode;
  testId: string;
  tone?: "neutral" | "error";
}) {
  return (
    <div
      data-testid={testId}
      role={tone === "error" ? "alert" : "status"}
      className="flex items-start gap-3 rounded-lg border border-dashed border-border bg-background px-4 py-5"
    >
      <span
        className={tone === "error" ? "mt-0.5 shrink-0 text-destructive" : "mt-0.5 shrink-0 text-muted-foreground"}
        aria-hidden="true"
      >
        {icon}
      </span>
      <div className="min-w-0 space-y-1.5">
        <p className="text-sm font-medium text-foreground">{title}</p>
        {children && <div className="space-y-1 text-xs text-muted-foreground">{children}</div>}
        {actions && <div className="flex flex-wrap gap-2 pt-1">{actions}</div>}
      </div>
    </div>
  );
}

export function ObsNotSetUp({ envName }: { envName: string }) {
  return (
    <StateFrame
      testId="obs-not-set-up"
      icon={<Activity className="h-4 w-4" />}
      title="Observability isn't set up for this organization yet"
    >
      <p>
        Reliant keeps each organization&apos;s logs, traces, metrics and profiles in a store of its own. Yours
        hasn&apos;t been created yet, so there is nothing to show for <span className="font-mono">{envName}</span>.
      </p>
      <p>
        Once it exists, telemetry from this environment appears here on its own — services built with forge send it
        without any setup.
      </p>
    </StateFrame>
  );
}

export function ObsNoData({
  what,
  range,
  filtered = false,
  onWiden,
  onClearFilters,
}: {
  /** "logs", "traces", "requests"… */
  what: string;
  range: RangePreset;
  /** A filter or query is narrowing the result — say so, and offer to clear it. */
  filtered?: boolean;
  onWiden?: () => void;
  onClearFilters?: () => void;
}) {
  const window = RANGE_LABELS[range].toLowerCase();
  return (
    <StateFrame
      testId="obs-no-data"
      icon={filtered ? <SearchX className="h-4 w-4" /> : <Clock className="h-4 w-4" />}
      title={filtered ? `No ${what} match in the ${window}` : `No ${what} in the ${window}`}
      actions={
        <>
          {filtered && onClearFilters && (
            <Button variant="outline" size="sm" onClick={onClearFilters} data-testid="obs-clear-filters">
              Clear filters
            </Button>
          )}
          {onWiden && range !== "7d" && (
            <Button variant="ghost" size="sm" onClick={onWiden} data-testid="obs-widen">
              Look at the last 7 days
            </Button>
          )}
        </>
      }
    >
      {!filtered && (
        <p>
          This environment has sent none in that window. A service that is idle, or was deployed after it began, looks
          exactly like this.
        </p>
      )}
    </StateFrame>
  );
}

const ERROR_COPY: Record<Exclude<ObsErrorKind, "not-set-up">, { title: string; body: string }> = {
  denied: {
    title: "You can't read this environment's telemetry",
    body: "Your role in this organization doesn't include observability. An organization admin can grant it.",
  },
  invalid: {
    title: "That search couldn't be run",
    body: "The server couldn't parse the query. Check its syntax, or clear it.",
  },
  "too-broad": {
    title: "That query asked for too much",
    body: "It hit this organization's query limit. A shorter time range, or a narrower filter, usually fits.",
  },
  unavailable: {
    title: "Telemetry couldn't be loaded",
    body: "The observability service didn't answer. This is usually brief.",
  },
};

/**
 * The state for a failed query. `not-set-up` is handed to the area (it is true
 * of every view at once, so the area says it once, above them); every other
 * kind is drawn here.
 */
export function ObsError({
  error,
  envName,
  onRetry,
  retrying,
}: {
  error: unknown;
  envName: string;
  onRetry?: () => void;
  retrying?: boolean;
}) {
  const kind = obsErrorKind(error);
  if (kind === "not-set-up") return <ObsNotSetUp envName={envName} />;
  const copy = ERROR_COPY[kind];
  const detail = obsErrorMessage(error);
  return (
    <StateFrame
      testId={`obs-error-${kind}`}
      tone="error"
      icon={kind === "denied" ? <Lock className="h-4 w-4" /> : <AlertTriangle className="h-4 w-4" />}
      title={copy.title}
      actions={
        onRetry && kind !== "denied" && kind !== "invalid" ? (
          <Button
            variant="outline"
            size="sm"
            onClick={onRetry}
            loading={retrying}
            disabled={retrying}
            leftIcon={<RefreshCw className="h-3 w-3" />}
            data-testid="obs-retry"
          >
            Try again
          </Button>
        ) : undefined
      }
    >
      <p>{copy.body}</p>
      {detail && <p className="break-words font-mono text-2xs">{detail}</p>}
    </StateFrame>
  );
}

/**
 * Marks Reliant's own operator telemetry (daemon and platform logs, which live
 * only in Reliant's org) so it is never mistaken for the app's.
 */
export function OperatorBadge({ provenance }: { provenance: Provenance }) {
  if (!provenance.operatorData && !provenance.source) return null;
  const label = provenance.source || "operator";
  return (
    <span
      className="shrink-0 rounded border border-border px-1 py-px font-sans text-2xs uppercase tracking-wide text-muted-foreground"
      title="Reliant operator data: platform telemetry, not this app's"
      data-testid="obs-operator-badge"
    >
      {label}
    </span>
  );
}

/** Rows of placeholder bars, for a table or list that is loading. */
export function ObsLoadingRows({ rows = 6, testId = "obs-loading" }: { rows?: number; testId?: string }) {
  return (
    <div data-testid={testId} aria-busy="true" aria-label="Loading" className="space-y-2 p-4">
      {Array.from({ length: rows }, (_, index) => (
        <div
          key={index}
          className="h-5 animate-pulse rounded bg-border/60 motion-reduce:animate-none"
          style={{ width: `${92 - ((index * 13) % 30)}%` }}
        />
      ))}
    </div>
  );
}
