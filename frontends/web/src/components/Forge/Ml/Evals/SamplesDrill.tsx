// Copyright (c) 2026 Reliant Labs

/**
 * A task's samples, candidate against baseline (02-views §4.3): the paired
 * wins / losses / ties and McNemar's p over every sample, then the logged
 * samples, filtered to what flipped (?filter) and to a slice (?group). A
 * selected sample (?sel) opens side by side: the input, the reference, both
 * answers with their scores, the judge's rationale, tokens and latency.
 */

import { FilterBar, FilterMenu } from "@/components/forge-ui/filter_bar";
import type { MlEvalRun, MlEvalSample, MlEvalSuite } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import { cn } from "@/lib/utils";

import { MlRead } from "../MlParts";
import { EmptyNote, FactList, fmtInt, fmtMs, MlLink, mlHref, MlTable, Panel, SidePanel, StatusDot, useMl, useMlSearch } from "../kit";
import { flipTone, fmtDeltaPt, fmtP, fmtSampleScore, fmtScore, isJudged, sampleFilterOf, sliceOptions, taskTitle } from "./evalsModel";

function Text({ label, children, tone }: { label: string; children: string; tone?: "danger" | "success" }) {
  return (
    <section className="min-w-0 space-y-1">
      <h3 className="flex items-center gap-1.5 text-meta font-medium text-ink-muted">
        {tone && <StatusDot tone={tone} />}
        {label}
      </h3>
      <p className="whitespace-pre-wrap break-words rounded-md border border-border-subtle bg-surface-sunken px-3 py-2 text-ui text-ink">{children}</p>
    </section>
  );
}

function SampleDetail({ sample, judged, candidate, baseline, onClose }: { sample: MlEvalSample; judged: boolean; candidate: string; baseline: string; onClose: () => void }) {
  const better = sample.score > sample.baselineScore;
  const worse = sample.score < sample.baselineScore;
  return (
    <SidePanel
      title={sample.id}
      meta={
        <span className="inline-flex flex-wrap items-center gap-2">
          <StatusDot tone={flipTone(sample.flip)} label={sample.flip} />
          {sample.slice && <span>{sample.slice.replaceAll(",", " · ")}</span>}
        </span>
      }
      onClose={onClose}
      width="min(880px, 100vw)"
      testId="ml-eval-sample-panel"
    >
      <Text label="Input">{sample.input}</Text>
      {sample.reference && <Text label={judged ? "What a good answer does (rubric)" : "Reference"}>{sample.reference}</Text>}
      <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
        <Text label={`${candidate} · ${fmtSampleScore(sample.score, judged)}`} tone={worse ? "danger" : better ? "success" : undefined}>
          {sample.output}
        </Text>
        <Text label={`${baseline} · ${fmtSampleScore(sample.baselineScore, judged)}`}>{sample.baselineOutput}</Text>
      </div>
      {sample.judgeRationale && <Text label="Judge rationale (gpt-4o, rubric v3)">{sample.judgeRationale}</Text>}
      <FactList
        facts={[
          { label: "Tokens", value: <span className="tabular-nums">{fmtInt(sample.tokens)}</span> },
          { label: "Latency", value: fmtMs(sample.latencyMs) },
        ]}
      />
    </SidePanel>
  );
}

export function SamplesDrill({ run, baseline, task, suite }: { run: MlEvalRun; baseline: MlEvalRun | undefined; task: string; suite: MlEvalSuite | undefined }) {
  const [search, setSearch] = useMlSearch();
  const filter = sampleFilterOf(search.filter);
  const slice = search.group;
  const meta = suite?.tasks.find((t) => t.key === task);
  const samples = useMl("listMlEvalSamples", {
    evalRunId: run.id,
    taskKey: task,
    // Only an explicit ?baseline: unset, the server compares with the suite's production run, and
    // the request (and its cache key) does not change when the page learns which run that is.
    ...(search.baseline && search.baseline !== run.id ? { baselineEvalRunId: search.baseline } : {}),
    filter,
    ...(slice ? { slice } : {}),
    limit: 200,
  });
  const value = run.results.find((r) => r.taskKey === task)?.value;
  const base = baseline?.results.find((r) => r.taskKey === task)?.value;
  const candidate = run.subject?.label ?? run.id;
  const baselineLabel = baseline?.subject?.label ?? "baseline";
  const slices = sliceOptions(run, task);

  return (
    <Panel
      title={`${taskTitle(meta, task)} · ${candidate} vs ${baselineLabel}`}
      note={meta ? `${fmtInt(meta.samples)} samples · ${meta.metric}` : undefined}
      testId="ml-eval-samples"
      actions={
        <MlLink href={mlHref("evals", ["runs", run.id], search.baseline ? { baseline: search.baseline } : undefined)} className="text-meta">
          Close
        </MlLink>
      }
      flush
    >
      <MlRead query={samples} what="samples">
        {(d) => {
          const judged = isJudged(d.samples);
          const selected = d.samples.find((s) => s.id === search.sel);
          const delta = value !== undefined && base !== undefined ? value - base : undefined;
          const significant = d.pValue < 0.05;
          return (
            <div className="space-y-3">
              <dl className="grid grid-cols-2 gap-x-6 gap-y-2 border-y border-border-subtle px-4 py-3 sm:grid-cols-3 lg:grid-cols-5" data-testid="ml-eval-samples-header">
                <div>
                  <dt className="text-meta text-ink-muted">Score</dt>
                  <dd className="tabular-nums text-ink">
                    <span className="text-xl font-semibold">{fmtScore(value)}</span> <span className="text-ink-muted">vs {fmtScore(base)}</span>
                  </dd>
                </div>
                <div>
                  <dt className="text-meta text-ink-muted">Difference</dt>
                  <dd className={cn("tabular-nums text-xl font-semibold", significant && delta !== undefined && delta < 0 ? "text-danger-ink" : "text-ink")}>
                    {significant && delta !== undefined ? <StatusDot tone={delta < 0 ? "danger" : "success"} label={`${fmtDeltaPt(delta)} pt`} /> : `${fmtDeltaPt(delta)} pt`}
                  </dd>
                  <dd className="text-meta text-ink-muted">
                    {fmtP(d.pValue)} · {significant ? "significant" : "not significant"} (McNemar)
                  </dd>
                </div>
                <div>
                  <dt className="text-meta text-ink-muted">Improved</dt>
                  <dd className="tabular-nums text-xl font-semibold text-ink">{fmtInt(d.wins)}</dd>
                </div>
                <div>
                  <dt className="text-meta text-ink-muted">Regressed</dt>
                  <dd className="tabular-nums text-xl font-semibold text-ink">{fmtInt(d.losses)}</dd>
                </div>
                <div>
                  <dt className="text-meta text-ink-muted">Same</dt>
                  <dd className="tabular-nums text-xl font-semibold text-ink">{fmtInt(d.ties)}</dd>
                  <dd className="text-meta text-ink-muted">of {fmtInt(d.total)}</dd>
                </div>
              </dl>

              <div className="px-4">
                <FilterBar label="Samples" active={(filter !== "all" ? 1 : 0) + (slice ? 1 : 0)} onClear={() => setSearch({ filter: undefined, group: undefined, sel: undefined })} testId="ml-eval-samples-filters">
                  <FilterMenu
                    label="Show"
                    value={filter === "all" ? undefined : filter}
                    options={[
                      { value: "regressed", label: "regressed only" },
                      { value: "improved", label: "improved only" },
                      { value: "same", label: "unchanged" },
                    ]}
                    onChange={(v) => setSearch({ filter: v, sel: undefined })}
                    testId="ml-eval-samples-filter"
                  />
                  {slices.length > 0 && <FilterMenu label="Slice" value={slice} options={slices.map((s) => ({ value: s, label: s }))} onChange={(v) => setSearch({ group: v, sel: undefined })} testId="ml-eval-samples-slice" />}
                </FilterBar>
              </div>

              {d.samples.length === 0 ? (
                <div className="px-4 pb-3">
                  <EmptyNote>
                    {filter !== "all" || slice
                      ? "No logged sample matches these filters."
                      : "No per-sample records for this pair: the harness kept samples (log_samples) only for the gate's latest candidates. The counts above come from the paired scores."}
                  </EmptyNote>
                </div>
              ) : (
                <MlTable
                  testId="ml-eval-samples-table"
                  rows={d.samples}
                  rowKey={(s) => s.id}
                  selected={search.sel}
                  onRowClick={(s) => setSearch({ sel: s.id })}
                  columns={[
                    { key: "id", header: "Sample", render: (s) => <span className="font-mono text-code text-ink">{s.id}</span> },
                    { key: "slice", header: "Slice", render: (s) => <span className="text-meta text-ink-muted">{s.slice.replaceAll(",", " · ")}</span> },
                    { key: "base", header: baselineLabel, numeric: true, render: (s) => <span className="text-ink-muted">{fmtSampleScore(s.baselineScore, judged)}</span>, sort: (s) => s.baselineScore },
                    {
                      key: "cand",
                      header: candidate,
                      numeric: true,
                      sort: (s) => s.score,
                      render: (s) => <span className={s.flip === "regressed" ? "text-danger-ink" : "text-ink"}>{fmtSampleScore(s.score, judged)}</span>,
                    },
                    { key: "flip", header: "Flip", render: (s) => <StatusDot tone={flipTone(s.flip)} label={s.flip} />, sort: (s) => s.flip },
                    { key: "input", header: "Input", render: (s) => <span className="block max-w-sm truncate text-ink-muted" title={s.input}>{s.input}</span> },
                    { key: "out", header: `${candidate} output`, render: (s) => <span className="block max-w-xs truncate text-ink-muted" title={s.output}>{s.output}</span> },
                  ]}
                />
              )}
              {selected && <SampleDetail sample={selected} judged={judged} candidate={candidate} baseline={baselineLabel} onClose={() => setSearch({ sel: undefined })} />}
            </div>
          );
        }}
      </MlRead>
    </Panel>
  );
}
