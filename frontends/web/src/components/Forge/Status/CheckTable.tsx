// Copyright (c) 2025 Reliant Labs

/**
 * The env-runtime checks, ONE TABLE ROW EACH: forge-ui's DataTable, each fact
 * in its own column — the disposition badge, the check's name, the detail, and
 * the duration. It used to be a `<li>` that stacked badge, name, duration,
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

import DataTable, { type DataTableColumn } from "@/components/forge-ui/data_table";
import { cn } from "@/lib/utils";
import {
  checkStatusExplanation,
  checkStatusLabel,
  dispositionOf,
  formatCheckDuration,
  type ForgeCheckResult,
} from "@/services/forge/status";

import { iconForCheckStatus } from "./checkVocabulary";
import { DispositionChip } from "./DispositionChip";

type Check = ForgeCheckResult & { name: string };
type CheckTableRow = { name: string; check: Check };

/** Detail wraps and rows top-align: an undetermined check's explanation runs long. */
const TOP = "whitespace-normal py-2 align-top";

const CHECK_COLUMNS: DataTableColumn<CheckTableRow>[] = [
  {
    key: "result",
    header: "Result",
    className: "py-2 align-top",
    render: (_, { check }) => {
      const disposition = dispositionOf(check.status);
      const Icon = iconForCheckStatus(check.status);
      return (
        <DispositionChip
          // The chip carries the whole visual verdict: fill, border style,
          // icon and hue. The row's data-* is its semantic twin.
          data-disposition-badge={disposition}
          disposition={disposition}
        >
          <Icon className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
          <span>{checkStatusLabel(check.status)}</span>
        </DispositionChip>
      );
    },
  },
  { key: "check", header: "Check", className: TOP, render: (_, { check }) => check.name },
  {
    key: "detail",
    header: "Detail",
    className: cn(TOP, "w-full text-ink-muted"),
    render: (_, { check }) => {
      const disposition = dispositionOf(check.status);
      const explain = disposition === "undetermined" || disposition === "not-applicable";
      if (!check.message && !explain && !check.evidence) {
        // An absent detail is stated, never left blank — a blank cell reads
        // as a rendering failure.
        return <span aria-hidden="true">—</span>;
      }
      return (
        <div className="space-y-1">
          {check.message && <p>{check.message}</p>}

          {/* What the status MEANS, spelled out for the two that are easiest
              to misread. A reader who has not internalised the five-level
              vocabulary gets the distinction in words, in the row, without a
              tooltip. */}
          {explain && <p className="text-xs">{checkStatusExplanation(check.status)}</p>}

          {check.evidence && (
            <details>
              <summary className="cursor-pointer text-xs text-ink-muted hover:text-ink">Evidence</summary>
              <pre className="mt-1 overflow-x-auto whitespace-pre-wrap rounded-md border border-border/60 bg-surface-sunken px-2 py-1.5 font-mono text-xs text-ink-muted">
                {check.evidence}
              </pre>
            </details>
          )}
        </div>
      );
    },
  },
  {
    // A duration is a measurement, not an identifier, so it is not
    // monospaced. `numeric` keeps the column aligned without it.
    key: "took",
    header: "Took",
    numeric: true,
    className: "py-2 align-top text-xs text-ink-muted",
    render: (_, { check }) => formatCheckDuration(check.duration_ms) || <span aria-hidden="true">—</span>,
  },
];

export function CheckTable({ checks }: { checks: Check[] }) {
  return (
    <DataTable<CheckTableRow>
      flush
      stacked
      ariaLabel="Runtime checks"
      columns={CHECK_COLUMNS}
      data={checks.map((check) => ({ name: check.name, check }))}
      getRowKey={(r) => r.name}
      getRowProps={({ check }) => ({
        "data-testid": `forge-check-${check.name}`,
        "data-disposition": dispositionOf(check.status),
        "data-check-status": check.status ?? "",
      })}
    />
  );
}
