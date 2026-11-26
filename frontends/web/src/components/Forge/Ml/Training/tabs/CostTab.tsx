// Copyright (c) 2026 Reliant Labs

/**
 * A run's Cost tab (02-views §3.13): GPU-hours and dollars by phase ×
 * attempt × category, dollars per billion tokens, what it will cost to
 * finish, and how it compares with the experiment's median run.
 */

import { MlCostCategory } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import { costCategoryOf, fmtGpuHours, fmtPct, fmtUsd, has, KpiStrip, MlLink, MlTable, mlHref, Panel, StackedBar } from "../../kit";
import type { RunScope } from "../runScope";
import { costSegments, costTable, type CostRow } from "../trainingModel";

export function CostTab({ scope }: { scope: RunScope }) {
  const { detail, run } = scope;
  const cost = detail.cost;
  const table = costTable(cost?.cells ?? [], run.phases.map((p) => p.key));
  const segs = costSegments(run.cost);
  const waste = segs.filter((s) => s.category !== MlCostCategory.PRODUCTIVE).reduce((s, x) => s + x.usd, 0);
  const vsMedian = cost && has(cost.experimentMedianUsd) && cost.experimentMedianUsd > 0 ? (run.cost?.usd ?? 0) / cost.experimentMedianUsd : Number.NaN;
  const base = run.name || run.id;
  return (
    <div className="space-y-4" data-testid="ml-run-cost">
      <KpiStrip
        testId="ml-cost-kpis"
        items={[
          { label: "Spent", value: fmtUsd(run.cost?.usd), sub: fmtGpuHours(run.cost?.gpuHours), hero: true },
          { label: "Waste", value: fmtUsd(waste), sub: `${fmtPct(1 - run.goodput)} of GPU time` },
          { label: "$ per 1B tokens", value: cost && has(cost.usdPerBillionTokens) ? fmtUsd(cost.usdPerBillionTokens) : "—" },
          { label: "To finish", value: cost?.projectedUsdToFinish ? fmtUsd(cost.projectedUsdToFinish) : "—", sub: cost?.projectedUsdToFinish ? `total ≈ ${fmtUsd((run.cost?.usd ?? 0) + cost.projectedUsdToFinish)}` : "nothing left to run" },
          { label: "vs experiment median", value: has(vsMedian) ? `${vsMedian.toFixed(1)}×` : "—", sub: cost && has(cost.experimentMedianUsd) ? `median full run ${fmtUsd(cost.experimentMedianUsd)}` : undefined },
        ]}
      />
      <Panel title="By category" note="goodput vs badput by cause">
        <StackedBar segments={segs.map((s) => ({ label: s.label, value: s.usd, color: s.color }))} format={fmtUsd} height={12} />
      </Panel>
      <Panel title="Phase × attempt" note="GPU-hours · $" flush>
        <MlTable<CostRow>
          rows={table.rows}
          rowKey={(r) => r.key}
          rowHref={(r) => mlHref("runs", [base, "phases", r.phaseKey], { attempt: r.attempt, tab: "cost" })}
          testId="ml-cost-table"
          columns={[
            { key: "phase", header: "Phase", render: (r) => <MlLink href={mlHref("runs", [base, "phases", r.phaseKey], { tab: "cost" })}>{r.phaseKey}</MlLink> },
            { key: "attempt", header: "Attempt", render: (r) => `a${r.attempt}` },
            ...table.categories.map((c) => ({
              key: `c${c}`,
              header: costCategoryOf(c).label,
              numeric: true,
              render: (r: CostRow) => {
                const v = r.byCategory[c];
                return v ? (
                  <span title={fmtGpuHours(v.gpuHours)} className={c === MlCostCategory.PRODUCTIVE ? "text-ink" : "text-ink-muted"}>
                    {fmtUsd(v.usd)}
                  </span>
                ) : (
                  <span className="text-ink-muted">—</span>
                );
              },
            })),
            { key: "total", header: "Total", numeric: true, sort: (r) => r.usd, render: (r) => <span className="font-medium">{fmtUsd(r.usd)}</span> },
            { key: "gpuh", header: "GPU-h", numeric: true, render: (r) => <span className="text-ink-muted">{fmtGpuHours(r.gpuHours)}</span> },
          ]}
        />
        <div className="flex justify-end gap-6 border-t border-border px-4 py-2 text-ui tabular-nums">
          <span className="text-ink-muted">Total</span>
          <span className="font-medium text-ink">{fmtUsd(table.total.usd)}</span>
          <span className="text-ink-muted">{fmtGpuHours(table.total.gpuHours)}</span>
        </div>
      </Panel>
    </div>
  );
}
