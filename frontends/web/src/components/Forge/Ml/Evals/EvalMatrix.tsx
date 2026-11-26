// Copyright (c) 2026 Reliant Labs

/**
 * THE MATRIX (02-views §4.1): rows are subjects (versions, checkpoints, an
 * external API model), columns are tasks, then the judge's win rate, the
 * run's cost and the gate. A cell is the score, its delta against the
 * baseline row and the 95% interval; it is coloured by significance only (a
 * significant regression has a danger dot and danger text, a significant
 * improvement a success dot), outlined when it fails a gate, and opens the
 * task's samples.
 */

import { MlSubjectKind } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import { cn } from "@/lib/utils";

import { Chip, fmtAgo, fmtPct, fmtUsd, MlLink, mlHref, StatusDot, SubjectLink, TONE_DOT, tsMs } from "../kit";
import { cellHref, cellTone, fmtCi, fmtDeltaPt, fmtScore, type MatrixVm } from "./evalsModel";

const ALIAS_ORDER: Record<string, number> = { production: 0, staging: 1, candidate: 2 };

export function EvalMatrix({ vm, now }: { vm: MatrixVm; now: number }) {
  const baselineRun = vm.baseline?.evalRunId;
  const rows = [...vm.rows].sort((a, b) => Number(b.isBaseline) - Number(a.isBaseline) || (ALIAS_ORDER[a.row.alias] ?? 9) - (ALIAS_ORDER[b.row.alias] ?? 9));
  return (
    <div className="min-w-0 overflow-x-auto" data-testid="ml-eval-matrix">
      <table className="w-full border-collapse text-ui">
        <thead>
          <tr className="border-b border-border text-left text-meta text-ink-muted">
            <th scope="col" className="h-10 px-3 pl-4 font-medium">
              Subject
            </th>
            {vm.columns.map((t) => (
              <th key={t.key} scope="col" className="h-10 px-2 text-right align-bottom font-medium" title={`${t.metric} · ${t.samples.toLocaleString("en-US")} samples`}>
                <span className="block text-ink">{t.name}</span>
                <span className="block font-normal">{t.setting}</span>
              </th>
            ))}
            <th scope="col" className="h-10 px-2 text-right align-bottom font-medium" title="LLM judge (gpt-4o, rubric v3): pairwise win rate against the baseline on support tickets">
              Judge win
            </th>
            <th scope="col" className="h-10 px-2 text-right align-bottom font-medium">
              Cost / run
            </th>
            <th scope="col" className="h-10 px-3 pr-4 align-bottom font-medium">
              Gate
            </th>
          </tr>
        </thead>
        <tbody>
          {rows.map(({ row, isBaseline, cells }) => (
            <tr key={row.id} className={cn("border-b border-border-subtle last:border-0", isBaseline && "bg-surface-sunken")} data-testid={`ml-eval-row-${row.id}`}>
              <th scope="row" className="whitespace-nowrap px-3 py-1.5 pl-4 text-left font-normal">
                <div className="flex items-center gap-1.5">
                  {row.alias && <Chip>{row.alias}</Chip>}
                  {row.subject && row.subject.kind !== MlSubjectKind.UNSPECIFIED ? <SubjectLink subject={row.subject} /> : <span className="text-ink">{row.label}</span>}
                  {isBaseline && <span className="text-meta text-ink-muted">baseline</span>}
                </div>
                <div className="text-meta text-ink-muted">evaluated {fmtAgo(tsMs(row.evaluatedAt), now)}</div>
              </th>
              {vm.columns.map((t) => {
                const cell = cells.get(t.key);
                if (!cell) {
                  return (
                    <td key={t.key} className="px-2 text-right text-meta text-ink-muted">
                      not run
                    </td>
                  );
                }
                const tone = cellTone(cell);
                return (
                  <td key={t.key} className="px-1 py-1 text-right">
                    <MlLink
                      href={cellHref(row, t.key, baselineRun)}
                      className={cn("block rounded-md border px-1.5 py-0.5 tabular-nums hover:bg-ink/5 hover:no-underline", cell.gateFail ? "border-danger-ink/50" : "border-transparent")}
                      testId={`ml-eval-cell-${row.id}-${t.key}`}
                      title={`${t.name}: ${fmtScore(cell.value)} ${fmtCi(cell.stderr)} (n = ${cell.n.toLocaleString("en-US")})${isBaseline ? "" : `, ${fmtDeltaPt(cell.delta)} pt vs baseline${cell.significant ? ", significant at 95%" : ", not significant"}`}${cell.gateFail ? " — fails a gate" : ""}`}
                    >
                      <span className={cn("block", tone === "danger" ? "text-danger-ink" : "text-ink")}>{fmtScore(cell.value)}</span>
                      <span className="flex items-center justify-end gap-1 text-meta text-ink-muted">
                        {isBaseline ? (
                          fmtCi(cell.stderr)
                        ) : (
                          <>
                            {tone !== "neutral" && <span className={cn("h-1.5 w-1.5 rounded-full", TONE_DOT[tone])} aria-hidden="true" />}
                            <span className={tone === "danger" ? "text-danger-ink" : undefined}>{fmtDeltaPt(cell.delta)}</span>
                            <span>{fmtCi(cell.stderr)}</span>
                          </>
                        )}
                      </span>
                    </MlLink>
                  </td>
                );
              })}
              <td className="px-2 text-right tabular-nums">{Number.isFinite(row.judgeWinRate) ? fmtPct(row.judgeWinRate) : <span className="text-ink-muted">—</span>}</td>
              <td className="px-2 text-right tabular-nums">
                <MlLink href={mlHref("evals", ["runs", row.evalRunId])}>{fmtUsd(row.usd)}</MlLink>
              </td>
              <td className="whitespace-nowrap px-3 pr-4">
                {row.gateStatus ? (
                  <span title={row.gateFailures.join("\n") || "Every gate passed"}>
                    <StatusDot tone={row.gateStatus === "fail" ? "danger" : "success"} label={row.gateStatus === "fail" ? `fail · ${row.gateFailures.length}` : "pass"} />
                  </span>
                ) : (
                  <span className="text-meta text-ink-muted">no gate</span>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      <p className="flex flex-wrap items-center gap-x-4 gap-y-1 border-t border-border-subtle px-4 py-2 text-meta text-ink-muted">
        <span className="inline-flex items-center gap-1.5">
          <span className={cn("h-1.5 w-1.5 rounded-full", TONE_DOT.danger)} aria-hidden="true" />
          significant regression
        </span>
        <span className="inline-flex items-center gap-1.5">
          <span className={cn("h-1.5 w-1.5 rounded-full", TONE_DOT.success)} aria-hidden="true" />
          significant improvement (95%, paired McNemar)
        </span>
        <span className="inline-flex items-center gap-1.5">
          <span className="h-3 w-4 rounded-sm border border-danger-ink/50" aria-hidden="true" />
          fails a gate
        </span>
        <span>Scores in points; delta against the baseline row; ± is the 95% interval. A cell opens its samples.</span>
      </p>
    </div>
  );
}
