// Copyright (c) 2025 Reliant Labs

/**
 * One env-runtime check.
 *
 * The row is painted by DISPOSITION, never by a pass/fail boolean, and a
 * `data-disposition` attribute is emitted alongside the classes. That attribute
 * is what the contract test asserts the semantic half on, so the five-level
 * distinction is pinned to something stable rather than to a Tailwind class list
 * that a restyle would churn — but the tests also compare the rendered
 * treatments against each other, because an honest attribute over an identical
 * appearance is the same bug wearing a disguise.
 *
 * `evidence` is diagnostic text for a human and is shown on demand, in a native
 * <details>. Nothing branches on it: forge's contract is that `status` carries
 * the verdict and evidence carries the explanation, and parsing prose to second-
 * guess a status is how a UI starts disagreeing with the tool it is reporting.
 */

import { cn } from "@/lib/utils";
import {
  checkStatusExplanation,
  checkStatusLabel,
  dispositionOf,
  formatCheckDuration,
  type ForgeCheckResult,
} from "@/services/forge/status";

import { DISPOSITION_STYLES, iconForCheckStatus } from "./checkVocabulary";

export interface CheckRowProps {
  check: ForgeCheckResult & { name: string };
}

export function CheckRow({ check }: CheckRowProps) {
  const disposition = dispositionOf(check.status);
  const style = DISPOSITION_STYLES[disposition];
  const Icon = iconForCheckStatus(check.status);
  const label = checkStatusLabel(check.status);
  const duration = formatCheckDuration(check.duration_ms);

  return (
    <li
      data-testid={`forge-check-${check.name}`}
      data-disposition={disposition}
      data-check-status={check.status ?? ""}
      className="border-b border-border px-3 py-2 last:border-b-0"
    >
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <span
          // The badge carries the whole visual verdict: fill, border style,
          // icon and hue. data-* above is its semantic twin.
          data-disposition-badge={disposition}
          className={cn(
            "inline-flex shrink-0 items-center gap-1.5 rounded-md px-2 py-0.5 text-xs",
            style.container,
            style.foreground
          )}
        >
          <Icon className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
          <span className="font-mono">{label}</span>
        </span>

        <span className="min-w-0 flex-1 truncate text-sm text-foreground">{check.name}</span>

        {duration && (
          <span className="shrink-0 font-mono text-2xs text-muted-foreground">{duration}</span>
        )}
      </div>

      {check.message && (
        <p className="mt-1 pl-1 text-xs text-muted-foreground">{check.message}</p>
      )}

      {/* What the status MEANS, spelled out for the two that are easiest to
          misread. A reader who has not internalised the five-level vocabulary
          gets the distinction in words, next to the row, without a tooltip. */}
      {(disposition === "undetermined" || disposition === "not-applicable") && (
        <p className="mt-1 pl-1 text-2xs text-muted-foreground">
          {checkStatusExplanation(check.status)}
        </p>
      )}

      {check.evidence && (
        <details className="mt-1.5 pl-1">
          <summary className="cursor-pointer text-2xs text-muted-foreground hover:text-foreground">
            Evidence
          </summary>
          <pre className="mt-1 overflow-x-auto whitespace-pre-wrap rounded-md border border-border bg-muted/40 px-2 py-1.5 font-mono text-2xs text-muted-foreground">
            {check.evidence}
          </pre>
        </details>
      )}
    </li>
  );
}
