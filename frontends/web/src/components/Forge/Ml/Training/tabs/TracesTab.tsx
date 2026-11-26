// Copyright (c) 2026 Reliant Labs

/**
 * A run's Traces tab (02-views §3.7): the traces the step tracer kept for the
 * scope — every slow step (rank lanes 0, the straggler and its neighbours, so
 * you see who arrived late at the collective), checkpoint saves, data
 * pipeline batches and an attempt's start from Kueue admission to its first
 * step. The slow-step list opens a trace below it (`?sel=<trace id>`), drawn
 * with Observability's waterfall; every span's run, rank, node and GPU are
 * links.
 */

import { MlSubjectKind, MlTraceKind, type MlTraceSummary } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import { FilterBar, FilterChip } from "@/components/forge-ui/filter_bar";

import { MlRead } from "../../MlParts";
import { Chip, EmptyNote, fmtMs, fmtStep, fmtTime, KpiStrip, MlTable, MlTraceView, NotCollected, num, Panel, StatusDot, SubjectLink, tsMs, useMl, useMlSearch, type MlColumn } from "../../kit";
import { TRACE_KINDS, traceKindLabel } from "../obs/obsModel";
import type { RunScope } from "../runScope";

export function TracesTab({ scope }: { scope: RunScope }) {
  const [search, setSearch] = useMlSearch();
  const kind = TRACE_KINDS.find((k) => k.id === search.filter) ?? TRACE_KINDS[0];
  const traces = useMl("listMlTraces", { subject: scope.subject, ...(kind.kind ? { kind: kind.kind } : {}), limit: 100 });
  return (
    <div className="space-y-4" data-testid="ml-run-traces">
      <FilterBar label="Trace kind" testId="ml-traces-kinds">
        {TRACE_KINDS.map((k) => (
          <FilterChip key={k.id} on={k.id === kind.id} onClick={() => setSearch({ filter: k.id === "all" ? undefined : k.id, sel: undefined })} testId={`ml-traces-kind-${k.id}`}>
            {k.label}
          </FilterChip>
        ))}
      </FilterBar>
      <MlRead query={traces} what="traces">
        {(data) =>
          data.traces.length === 0 ? (
            data.missingFindingId ? (
              <NotCollected what="Step traces" source="training instrumentation (the ml/instrument skill emits ml.train.step spans)" findingId={data.missingFindingId} height={160} />
            ) : (
              <Panel title="Traces">
                <EmptyNote>No {kind.id === "all" ? "" : `${kind.label.toLowerCase()} `}traces were kept for this scope. Steps are sampled 1 in 250, plus every step over p99.</EmptyNote>
              </Panel>
            )
          ) : (
            <TraceList scope={scope} traces={data.traces} selected={search.sel} onSelect={(id) => setSearch({ sel: id === search.sel ? undefined : id })} />
          )
        }
      </MlRead>
    </div>
  );
}

function TraceList({ scope, traces, selected, onSelect }: { scope: RunScope; traces: readonly MlTraceSummary[]; selected?: string; onSelect: (id: string) => void }) {
  const steps = traces.filter((t) => t.kind === MlTraceKind.TRAIN_STEP && !t.error);
  const slow = steps.filter((t) => t.vsMedian >= 1.1);
  const worst = steps[0];
  const current = traces.find((t) => t.traceId === selected);
  const columns: MlColumn<MlTraceSummary>[] = [
    { key: "kind", header: "Kind", render: (t) => <span className="text-ink-muted">{traceKindLabel(t.kind)}</span> },
    {
      key: "title",
      header: "Trace",
      render: (t) => (
        <span className="inline-flex items-center gap-2">
          {t.error && <StatusDot tone="danger" hideLabel label="error" />}
          <span className="text-ink">{t.title}</span>
        </span>
      ),
    },
    { key: "step", header: "Step", numeric: true, render: (t) => (num(t.step) ? fmtStep(t.step) : "—"), sort: (t) => num(t.step) },
    { key: "rank", header: "Rank", numeric: true, render: (t) => (t.rank >= 0 && t.subject ? <SubjectLink subject={{ kind: MlSubjectKind.RANK, id: `${t.subject.id}/${t.rank}` }}>{t.rank}</SubjectLink> : "—") },
    { key: "duration", header: "Duration", numeric: true, render: (t) => fmtMs(t.durationMs), sort: (t) => t.durationMs },
    {
      key: "vs",
      header: "vs median",
      numeric: true,
      render: (t) => <span className={t.vsMedian >= 1.1 ? "text-warning-ink" : undefined}>{t.vsMedian ? `${t.vsMedian.toFixed(2)}×` : "—"}</span>,
      sort: (t) => t.vsMedian,
    },
    { key: "dominant", header: "Dominant span", render: (t) => <span className="font-mono text-code text-ink-muted">{t.dominantSpan}</span> },
    { key: "at", header: "Started", render: (t) => <span className="text-ink-muted">{fmtTime(tsMs(t.start))}</span>, sort: (t) => tsMs(t.start) ?? 0 },
  ];
  return (
    <>
      <KpiStrip
        testId="ml-traces-kpis"
        items={[
          { label: "Traces kept", value: traces.length.toLocaleString(), sub: `${steps.length} steps · ${traces.length - steps.length} other` },
          { label: "Slow steps", value: slow.length.toLocaleString(), sub: "over 1.1× the median step", tone: slow.length ? "warning" : undefined },
          { label: "Slowest step", value: worst ? fmtMs(worst.durationMs) : "—", sub: worst ? `step ${fmtStep(worst.step)} · ${worst.vsMedian.toFixed(2)}×` : undefined },
          { label: "Dominant span", value: <span className="font-mono text-ui">{worst?.dominantSpan.split(" ")[0] ?? "—"}</span>, sub: worst?.dominantSpan.includes("(") ? worst.dominantSpan.slice(worst.dominantSpan.indexOf("(")) : undefined },
        ]}
      />
      <Panel title="Kept traces" note="slowest first · select one to open it" flush testId="ml-traces-list">
        <MlTable columns={columns} rows={traces} rowKey={(t) => t.traceId} onRowClick={(t) => onSelect(t.traceId)} selected={selected} maxRows={15} testId="ml-traces-table" />
      </Panel>
      {selected && (
        <section className="space-y-3 border-t border-border pt-4" data-testid="ml-traces-selected">
          <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
            <h2 className="text-section text-ink">{current?.title ?? "Trace"}</h2>
            <span className="inline-flex flex-wrap items-center gap-2 text-meta text-ink-muted">
              <span className="font-mono text-code">{selected}</span>
              {current && <Chip>{traceKindLabel(current.kind)}</Chip>}
              {current?.subject && <SubjectLink subject={current.subject}>{current.subject.label}</SubjectLink>}
              {(current?.attributes.ranks ?? "")
                .split(",")
                .filter(Boolean)
                .map((r) => (
                  <SubjectLink key={r} subject={{ kind: MlSubjectKind.RANK, id: `${current?.subject?.id ?? scope.subject.id}/${r}` }}>
                    rank {r}
                  </SubjectLink>
                ))}
            </span>
          </div>
          <MlTraceView traceId={selected} testId="ml-run-trace" />
        </section>
      )}
    </>
  );
}
