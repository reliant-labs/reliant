// Copyright (c) 2025 Reliant Labs

/**
 * One env-runtime check, as ONE TABLE ROW.
 *
 * The row is a real `<tr>` under the `<thead>` DevStackPanel renders, and each
 * fact gets its own `<td>`: the disposition badge, the check's name, the detail,
 * and the duration. It used to be a `<li>` that stacked badge, name, duration,
 * message, explanation and evidence into six vertical blocks, which made every
 * row ~120px of ragged left edge and gave a reader no column to scan down. A
 * table gives them four.
 *
 * The row is painted by DISPOSITION, never by a pass/fail boolean, and a
 * `data-disposition` attribute is emitted alongside the classes. That attribute
 * is what the contract test asserts the semantic half on, so the five-level
 * distinction is pinned to something stable rather than to a Tailwind class list
 * that a restyle would churn — but the tests also compare the rendered
 * treatments against each other, because an honest attribute over an identical
 * appearance is the same bug wearing a disguise.
 *
 * The badge is a DispositionChip: the vocabulary's fill / border-style / ring /
 * opacity / hue on one chip, the same one the legend uses. Those are the axes
 * that survive greyscale, and no library badge has the dashed, unfilled half.
 * See checkVocabulary.ts and DispositionChip.tsx.
 *
 * WHY SOME ROWS ARE TALLER THAN ONE LINE. `undetermined` and `not-applicable`
 * print an explanatory sentence under the detail, and that is deliberate: those
 * two are the pair a reader is most likely to conflate, and the explanation has
 * to be next to the row rather than behind a tooltip. Every other disposition
 * stays on a single line. A row that could not be measured being visibly taller
 * than a row that passed is the correct emphasis, not a layout failure.
 *
 * `evidence` is diagnostic text for a human and is shown on demand, in a native
 * <details>. Nothing branches on it: forge's contract is that `status` carries
 * the verdict and evidence carries the explanation, and parsing prose to second-
 * guess a status is how a UI starts disagreeing with the tool it is reporting.
 */

import {
  checkStatusExplanation,
  checkStatusLabel,
  dispositionOf,
  formatCheckDuration,
  type ForgeCheckResult,
} from "@/services/forge/status";

import { iconForCheckStatus } from "./checkVocabulary";
import { DispositionChip } from "./DispositionChip";

export interface CheckRowProps {
  check: ForgeCheckResult & { name: string };
}

export function CheckRow({ check }: CheckRowProps) {
  const disposition = dispositionOf(check.status);
  const Icon = iconForCheckStatus(check.status);
  const label = checkStatusLabel(check.status);
  const duration = formatCheckDuration(check.duration_ms);
  const explain = disposition === "undetermined" || disposition === "not-applicable";
  const hasDetail = Boolean(check.message || explain || check.evidence);

  return (
    <tr
      data-testid={`forge-check-${check.name}`}
      data-disposition={disposition}
      data-check-status={check.status ?? ""}
      className="border-b border-border/60 last:border-b-0"
    >
      <td className="whitespace-nowrap px-4 py-2 align-top">
        <DispositionChip
          // The chip carries the whole visual verdict: fill, border style,
          // icon and hue. data-* above is its semantic twin.
          data-disposition-badge={disposition}
          disposition={disposition}
        >
          <Icon className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
          <span>{label}</span>
        </DispositionChip>
      </td>

      <td className="px-4 py-2 align-top text-sm text-foreground">{check.name}</td>

      <td className="px-4 py-2 align-top text-sm text-muted-foreground">
        {hasDetail ? (
          <div className="space-y-1">
            {check.message && <p>{check.message}</p>}

            {/* What the status MEANS, spelled out for the two that are easiest
                to misread. A reader who has not internalised the five-level
                vocabulary gets the distinction in words, in the row, without a
                tooltip. */}
            {explain && <p className="text-xs">{checkStatusExplanation(check.status)}</p>}

            {check.evidence && (
              <details>
                <summary className="cursor-pointer text-xs text-muted-foreground hover:text-foreground">
                  Evidence
                </summary>
                <pre className="mt-1 overflow-x-auto whitespace-pre-wrap rounded-md border border-border/60 bg-background px-2 py-1.5 font-mono text-xs text-muted-foreground">
                  {check.evidence}
                </pre>
              </details>
            )}
          </div>
        ) : (
          // An absent detail is stated, never left blank — a blank cell reads
          // as a rendering failure.
          <span aria-hidden="true">—</span>
        )}
      </td>

      {/* A duration is a measurement, not an identifier, so it is not
          monospaced. `tabular-nums` keeps the column aligned without it. */}
      <td className="whitespace-nowrap px-4 py-2 text-right align-top text-xs tabular-nums text-muted-foreground">
        {duration || <span aria-hidden="true">—</span>}
      </td>
    </tr>
  );
}
