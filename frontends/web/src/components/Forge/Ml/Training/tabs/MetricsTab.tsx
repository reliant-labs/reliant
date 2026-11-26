// Copyright (c) 2026 Reliant Labs

/**
 * A run's Metrics tab (02-views §3.4): a W&B-class workspace of small
 * multiples in sections (loss, optimizer, stability, throughput, timers,
 * memory, data, model vitals, evals, MoE, RL, system on step), every chart on
 * one synced cursor, read from ObservabilityService (QueryPanel, filtered to
 * the scope's ml.* attributes). The x axis is the optimizer step (the
 * trainer's ml.train.step), or wall time with `?axis=time`; EMA smoothing
 * draws the raw line faint under the smoothed one; a phase's attempts overlay
 * on the step axis with the steps a resume redid shaded and restarts,
 * preemptions, Xids and loss spikes marked; a family nothing reports says why
 * instead of drawing zeros. `?q` filters tiles by key.
 */

import { useMemo, useState } from "react";
import { ChevronDown, ChevronRight } from "lucide-react";

import { FilterBar, FilterChip, FilterSearch } from "@/components/forge-ui/filter_bar";
import { MlAxis, MlCollection, MlEventKind, type MlSeries } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import { MlRead } from "../../MlParts";
import { useMlEvents, useMlSearch, useMlSeries, xAxisOf } from "../../kit";
import { MetricTile, paletteOf } from "../obs/MetricTile";
import { filterSections, METRIC_SECTIONS, redoneBands, seriesForTile, workspaceKeys, type MetricSection } from "../obs/obsModel";
import { scopeRange, type RunScope } from "../runScope";

const SYNC = "ml-run-metrics";
/** The events worth a marker on a training chart (checkpoints are on the Checkpoints tab: one every 1,000 steps would hide the rest). */
const MARKED: readonly MlEventKind[] = [MlEventKind.RESTART, MlEventKind.PREEMPTION, MlEventKind.NCCL_TIMEOUT, MlEventKind.XID, MlEventKind.HANG, MlEventKind.OOM, MlEventKind.LOSS_SPIKE, MlEventKind.NAN, MlEventKind.REWIND, MlEventKind.STRAGGLER];
const SMOOTHING = [0, 0.6, 0.8, 0.9, 0.95] as const;

export function MetricsTab({ scope }: { scope: RunScope }) {
  const [search, setSearch] = useMlSearch();
  const axis = xAxisOf(search.axis);
  const keys = useMemo(() => workspaceKeys(), []);
  const subject = scope.subject;
  const range = scopeRange(scope);
  const train = useMlSeries(subject, keys.train, { axis, range });
  const system = useMlSeries(subject, keys.system, { axis, range });
  const events = useMlEvents(subject, { kinds: MARKED, range });
  const [smoothing, setSmoothing] = useState(0.6);
  const q = search.q ?? "";

  return (
    <div className="space-y-4" data-testid="ml-run-metrics">
      <FilterBar
        label="Metrics workspace"
        testId="ml-metrics-controls"
        active={q ? 1 : 0}
        onClear={() => setSearch({ q: undefined })}
        trailing={
          <>
            <span className="text-meta text-ink-muted">Smoothing</span>
            {SMOOTHING.map((w) => (
              <FilterChip key={w} on={smoothing === w} onClick={() => setSmoothing(w)} testId={`ml-metrics-smooth-${w}`}>
                {w === 0 ? "Off" : w.toFixed(2)}
              </FilterChip>
            ))}
            <span className="ml-2 text-meta text-ink-muted">x</span>
            {(["step", "time"] as const).map((a) => (
              <FilterChip key={a} on={axis === a} onClick={() => setSearch({ axis: a === "step" ? undefined : a })} testId={`ml-metrics-axis-${a}`}>
                {a === "step" ? "Step" : "Wall time"}
              </FilterChip>
            ))}
          </>
        }
      >
        <FilterSearch value={q} onChange={(v) => setSearch({ q: v || undefined })} placeholder="Filter metrics (loss, mfu, timers/…)" testId="ml-metrics-search" />
      </FilterBar>
      <MlRead query={train} what="training metrics">
        {(t) => (
          <Workspace
            scope={scope}
            series={[...t.series, ...(system.data?.kind === "ok" ? system.data.data.series : [])]}
            events={events.data?.kind === "ok" ? events.data.data.events : []}
            q={q}
            smoothing={smoothing}
          />
        )}
      </MlRead>
    </div>
  );
}

function Workspace({ scope, series, events, q, smoothing }: { scope: RunScope; series: readonly MlSeries[]; events: Parameters<typeof MetricTile>[0]["events"]; q: string; smoothing: number }) {
  const sections = useMemo(() => filterSections(METRIC_SECTIONS, q), [q]);
  // The redone steps are a stretch of the step axis; on wall time the attempts follow one another.
  const stepped = series.some((s) => s.axis === MlAxis.STEP);
  const bands = useMemo(() => (stepped ? redoneBands(scope.attempt ? null : scope.phase) : []), [stepped, scope.attempt, scope.phase]);
  // One colour per attempt on every tile, in attempt order.
  const colors = useMemo(() => {
    const ids = [...new Set(series.filter((s) => s.attempt && /^attempt \d+$/.test(s.group)).map((s) => s.group))].sort();
    const tokens = paletteOf(ids.map((group) => ({ group }) as MlSeries));
    return new Map(ids.map((id, i) => [id, tokens[i]]));
  }, [series]);
  const [collapsed, setCollapsed] = useState<Set<string>>(() => new Set());

  if (sections.length === 0) return <p className="py-6 text-center text-meta text-ink-muted">No metric matches “{q}”.</p>;
  return (
    <div className="space-y-5">
      {colors.size > 1 && (
        <ul className="flex flex-wrap items-center gap-x-4 gap-y-1 text-meta text-ink-muted" data-testid="ml-metrics-legend">
          {[...colors.entries()].map(([id, token]) => (
            <li key={id} className="inline-flex items-center gap-1.5">
              <span className="h-0.5 w-4 rounded-full" style={{ backgroundColor: `hsl(var(${token}))` }} aria-hidden="true" />
              {id}
            </li>
          ))}
          {bands.length > 0 && (
            <li className="inline-flex items-center gap-1.5">
              <span className="h-2.5 w-4 rounded-sm bg-warning/30" aria-hidden="true" />
              steps redone after a failure
            </li>
          )}
          {smoothing > 0 && (
            <li className="inline-flex items-center gap-1.5">
              <span className="h-0.5 w-4 rounded-full bg-border" aria-hidden="true" />
              raw (EMA {smoothing})
            </li>
          )}
        </ul>
      )}
      {sections.map((section) => (
        <Section
          key={section.id}
          section={section}
          series={series}
          events={events}
          bands={bands}
          colors={colors}
          smoothing={smoothing}
          collapsed={collapsed.has(section.id)}
          onToggle={() =>
            setCollapsed((prev) => {
              const next = new Set(prev);
              if (next.has(section.id)) next.delete(section.id);
              else next.add(section.id);
              return next;
            })
          }
        />
      ))}
    </div>
  );
}

function Section({
  section,
  series,
  events,
  bands,
  colors,
  smoothing,
  collapsed,
  onToggle,
}: {
  section: MetricSection;
  series: readonly MlSeries[];
  events: Parameters<typeof MetricTile>[0]["events"];
  bands: Parameters<typeof MetricTile>[0]["bands"];
  colors: ReadonlyMap<string, string>;
  smoothing: number;
  collapsed: boolean;
  onToggle: () => void;
}) {
  const tiles = section.tiles.map((tile) => ({ tile, series: seriesForTile(series, tile) }));
  const all = tiles.flatMap((t) => t.series);
  const absent = all.length > 0 && all.every((s) => s.collection !== MlCollection.COLLECTED);
  const reason = absent ? (all.find((s) => s.missingSource)?.missingSource ?? "") : "";
  const kind = absent && all.every((s) => s.collection === MlCollection.NOT_APPLICABLE) ? "Not applicable" : "Not collected";
  const Icon = collapsed ? ChevronRight : ChevronDown;
  return (
    <section className="space-y-2" data-testid={`ml-metrics-section-${section.id}`}>
      <button type="button" onClick={onToggle} className="flex items-baseline gap-2 text-left" aria-expanded={!collapsed}>
        <Icon className="h-3.5 w-3.5 self-center text-ink-muted" aria-hidden="true" />
        <h2 className="text-section text-ink">{section.title}</h2>
        {absent ? (
          <span className="text-meta text-ink-muted" data-testid={`ml-metrics-section-${section.id}-absent`}>
            {kind}
            {reason ? `: ${reason}` : ""}
          </span>
        ) : (
          section.note && <span className="text-meta text-ink-muted">{section.note}</span>
        )}
        <span className="text-meta tabular-nums text-ink-muted">· {section.tiles.length}</span>
      </button>
      {!collapsed && !absent && (
        <div className="grid min-w-0 gap-3 sm:grid-cols-2 xl:grid-cols-3">
          {tiles.map(({ tile, series: s }) => (
            <MetricTile key={tile.title} title={tile.title} series={s} events={events} bands={bands} syncKey={SYNC} smoothing={smoothing} colors={colors} testId={`ml-metric-${tile.keys[0]}`} />
          ))}
        </div>
      )}
    </section>
  );
}
