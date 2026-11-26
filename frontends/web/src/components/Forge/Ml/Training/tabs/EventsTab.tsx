// Copyright (c) 2026 Reliant Labs

/**
 * A run's Events tab (02-views §3.12): one merged timeline for the scope —
 * Kueue, scheduler, pods, devices, the trainer and people — filtered by
 * kind, each event linking what it happened to and everything it touched.
 */

import { useMemo, useState } from "react";

import { FilterBar, FilterChip } from "@/components/forge-ui/filter_bar";
import type { MlEvent, MlEventKind } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import { MlRead } from "../../MlParts";
import { Chip, EmptyNote, eventKindOf, fmtAgo, fmtStep, fmtTime, num, Panel, StatusDot, SubjectLink, tsMs, useMl } from "../../kit";
import type { RunScope } from "../runScope";
import { mergeEvents } from "../trainingModel";

export function EventsTab({ scope, now }: { scope: RunScope; now: number }) {
  const read = useMl("listMlEvents", { subject: scope.subject, limit: 500 });
  return (
    <MlRead query={read} what="this run's events">
      {(data) => <EventsBody events={data.events} now={now} scopeLabel={scope.subject.label} />}
    </MlRead>
  );
}

function EventsBody({ events, now, scopeLabel }: { events: readonly MlEvent[]; now: number; scopeLabel: string }) {
  const merged = useMemo(() => mergeEvents(events), [events]);
  const [kinds, setKinds] = useState<MlEventKind[]>([]);
  const counts = useMemo(() => {
    const m = new Map<MlEventKind, number>();
    for (const e of merged) m.set(e.kind, (m.get(e.kind) ?? 0) + 1);
    return [...m.entries()].sort((a, b) => b[1] - a[1]);
  }, [merged]);
  const shown = kinds.length ? merged.filter((e) => kinds.includes(e.kind)) : merged;
  const toggle = (k: MlEventKind) => setKinds((ks) => (ks.includes(k) ? ks.filter((x) => x !== k) : [...ks, k]));
  return (
    <div className="space-y-3" data-testid="ml-run-events">
      <FilterBar label="Filter events by kind" active={kinds.length} onClear={() => setKinds([])} testId="ml-run-events-kinds">
        {counts.map(([k, n]) => (
          <FilterChip key={k} on={kinds.includes(k)} onClick={() => toggle(k)} count={n} testId={`ml-event-kind-${k}`}>
            {eventKindOf(k).label}
          </FilterChip>
        ))}
      </FilterBar>
      <Panel title="Events" note={`${shown.length} · ${scopeLabel} · newest first`} flush>
        {shown.length === 0 ? (
          <div className="px-4">
            <EmptyNote>No events.</EmptyNote>
          </div>
        ) : (
          <ol className="divide-y divide-border-subtle">
            {shown.map((e) => {
              const kind = eventKindOf(e.kind);
              const at = tsMs(e.at);
              const end = tsMs(e.endAt);
              return (
                <li key={e.id || `${e.kind}-${at}`} className="grid grid-cols-[8.5rem_7rem_minmax(0,1fr)] gap-x-3 px-4 py-2 text-ui" data-testid={`ml-event-${e.id}`}>
                  <div className="text-meta tabular-nums text-ink-muted">
                    <div className="text-ink">{fmtTime(at)}</div>
                    <div>
                      {fmtAgo(at, now)}
                      {end ? ` · ${Math.round((end - at!) / 60_000)} min` : ""}
                    </div>
                  </div>
                  <div className="space-y-0.5">
                    <StatusDot tone={kind.tone} label={kind.label} />
                    {num(e.step) > 0 && <div className="text-meta tabular-nums text-ink-muted">step {fmtStep(e.step)}</div>}
                  </div>
                  <div className="min-w-0 space-y-0.5">
                    <div className="flex flex-wrap items-baseline gap-x-2">
                      <span className="font-medium text-ink">{e.title}</span>
                      {e.subject && <SubjectLink subject={e.subject} className="text-meta" />}
                      {e.hookEventType && <Chip title="Emits a hook event workflows can subscribe to">{e.hookEventType}</Chip>}
                    </div>
                    {e.detail && <p className="text-meta text-ink-muted">{e.detail}</p>}
                    {(e.related.length > 0 || e.source) && (
                      <p className="flex flex-wrap gap-x-2 text-meta text-ink-muted">
                        {e.source && <span>via {e.source}</span>}
                        {e.related.map((r) => (
                          <SubjectLink key={`${r.kind}-${r.id}`} subject={r} />
                        ))}
                      </p>
                    )}
                  </div>
                </li>
              );
            })}
          </ol>
        )}
      </Panel>
    </div>
  );
}
