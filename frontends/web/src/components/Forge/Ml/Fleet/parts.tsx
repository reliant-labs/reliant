// Copyright (c) 2026 Reliant Labs

/**
 * Parts the Fleet pages share: a device field table (value, unit, DCGM
 * source, alarm; not-collected rows name their source and fix), the
 * workloads Gantt, and the demo's refused actions.
 */

import type { ReactNode } from "react";

import Button from "@/components/forge-ui/button";
import { MlCollection, type MlFieldValue, type MlNodeWorkload } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import { cn } from "@/lib/utils";
import type { MlOutcome } from "@/services/controlPlane/ml/types";

import { FindingLink, Gantt, StatusDot, fmtTime } from "../kit";
import { fmtField, workloadRows } from "./fleetModel";

export const DEMO_READ_ONLY = "demo shell: read-only";

/** A read's data when it answered, else undefined (for optional overlays: events on a chart). */
export function dataOf<T>(query: { data?: MlOutcome<T> }): T | undefined {
  return query.data?.kind === "ok" ? query.data.data : undefined;
}

/** Write actions the demo refuses: shown, disabled, saying why. */
export function DemoActions({ actions }: { actions: readonly { label: string; icon?: ReactNode }[] }) {
  return (
    <>
      {actions.map((a) => (
        <Button key={a.label} variant="outline" size="sm" disabled title={DEMO_READ_ONLY} leftIcon={a.icon}>
          {a.label}
        </Button>
      ))}
    </>
  );
}

/** One group of fields: label, value, DCGM / exporter source, alarm dot. */
export function FieldTable({ fields, testId }: { fields: readonly MlFieldValue[]; testId?: string }) {
  return (
    <table className="w-full border-collapse text-ui" data-testid={testId}>
      <tbody>
        {fields.map((f) => {
          const value = fmtField(f);
          const na = f.collection === MlCollection.NOT_APPLICABLE;
          return (
            <tr key={f.key} className="h-8 border-b border-border-subtle last:border-0" data-testid={`ml-field-${f.key}`}>
              <td className="py-1 pl-4 pr-2 text-ink-muted">
                <span className="inline-flex items-center gap-1.5">
                  {f.alarm ? <StatusDot tone="danger" /> : <span className="inline-block h-2 w-2" aria-hidden="true" />}
                  {f.label}
                </span>
              </td>
              <td className={cn("whitespace-nowrap px-2 py-1 text-right tabular-nums", f.alarm ? "font-medium text-danger-ink" : "text-ink")}>
                {value ?? (
                  <span className="text-meta text-ink-muted" title={f.missingSource || undefined}>
                    {na ? "n/a" : "not collected"}
                  </span>
                )}
              </td>
              <td className="max-w-0 truncate py-1 pl-2 pr-4 font-mono text-code text-ink-muted" title={value === null && f.missingSource ? f.missingSource : f.sourceField}>
                {value === null && f.missingSource ? (
                  <span className="font-sans text-meta">
                    {f.missingSource}
                    {f.fixFindingId && (
                      <>
                        {" · "}
                        <FindingLink id={f.fixFindingId}>fix</FindingLink>
                      </>
                    )}
                  </span>
                ) : (
                  f.sourceField
                )}
              </td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}

/** Workloads that held a node's (or one device's) GPUs over the last week. */
export function WorkloadGantt({ workloads, nowMs, days = 7, testId }: { workloads: readonly MlNodeWorkload[]; nowMs: number; days?: number; testId?: string }) {
  const from = nowMs - days * 86_400_000;
  const rows = workloadRows(workloads, from);
  if (!rows.length) return <p className="py-3 text-meta text-ink-muted">Nothing held these GPUs in the last {days} days.</p>;
  return <Gantt rows={rows} from={from} to={nowMs} now={nowMs} formatTick={(v) => fmtTime(v)} labelWidth={240} testId={testId} />;
}
