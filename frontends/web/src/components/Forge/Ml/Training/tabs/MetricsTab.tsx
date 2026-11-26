// Copyright (c) 2026 Reliant Labs

/**
 * A run's Metrics tab (02-views §3.4): a W&B-class workspace of small
 * multiples in sections (loss, optimizer, stability, throughput, timers,
 * memory, data, model vitals, evals, MoE, RL, system on step), every chart on
 * one synced cursor, read from ObservabilityService (QueryPanel, filtered to
 * the scope's ml.* attributes) on the time axis. EMA smoothing draws the raw
 * line faint under the smoothed one; restarts, preemptions, Xids and loss
 * spikes are marked; a family nothing reports says why instead of drawing
 * zeros. `?q` filters tiles by key.
 */

import { useMemo, useState } from "react";
import { ChevronDown, ChevronRight } from "lucide-react";

import { FilterBar, FilterChip, FilterSearch } from "@/components/forge-ui/filter_bar";
import { MlCollection, MlEventKind, type MlSeries } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import { MlRead } from "../../MlParts";
import { useMlEvents, useMlSearch, useMlSeries } from "../../kit";
import { MetricTile, paletteOf } from "../obs/MetricTile";
import { filterSections, METRIC_SECTIONS, seriesForTile, workspaceKeys, type MetricSection } from "../obs/obsModel";
import { scopeRange, type RunScope } from "../runScope";

const SYNC = "ml-run-metrics";
/** The events worth a marker on a training chart (checkpoints are on the Checkpoints tab: one every 1,000 steps would hide the rest). */
const MARKED: readonly MlEventKind[] = [MlEventKind.RESTART, MlEventKind.PREEMPTION, MlEventKind.NCCL_TIMEOUT, MlEventKind.XID, MlEventKind.HANG, MlEventKind.OOM, MlEventKind.LOSS_SPIKE, MlEventKind.NAN, MlEventKind.REWIND, MlEventKind.STRAGGLER];
const SMOOTHING = [0, 0.6, 0.8, 0.9, 0.95] as const;

export function MetricsTab({ scope }: { scope: RunScope }) {
  const [search, setSearch] = useMlSearch();
  const keys = useMemo(() => workspaceKeys(), []);
  const subject = scope.subject;
  const range = scopeRange(scope);
  const train = useMlSeries(subject, keys.train, { range });
  const system = useMlSeries(subject, keys.system, { range });
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
          </>
        }
      >
        <FilterSearch value={q} onChange={(v) => setSearch({ q: v || undefined })} placeholder="Filter metrics (loss, mfu, timers/…)" testId="ml-metrics-search" />
      </FilterBar>
      <MlRead query={train} what="training metrics">
        {(t) => (
          <Workspace
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

function Workspace({ series, events, q, smoothing }: { series: readonly MlSeries[]; events: Parameters<typeof MetricTile>[0]["events"]; q: string; smoothing: number }) {
  const sections = useMemo(() => filterSections(METRIC_SECTIONS, q), [q]);
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
  colors,
  smoothing,
  collapsed,
  onToggle,
}: {
  section: MetricSection;
  series: readonly MlSeries[];
  events: Parameters<typeof MetricTile>[0]["events"];
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
            <MetricTile key={tile.title} title={tile.title} series={s} events={events} syncKey={SYNC} smoothing={smoothing} colors={colors} testId={`ml-metric-${tile.keys[0]}`} />
          ))}
        </div>
      )}
    </section>
  );
}
