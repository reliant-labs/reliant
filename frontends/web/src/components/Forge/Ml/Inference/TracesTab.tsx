// Copyright (c) 2026 Reliant Labs

/**
 * A service's slowest requests (24h), by bucket (`?filter=`), each opening
 * its full request trace in a side panel (`?sel=<trace id>`): app → gen_ai
 * client → gateway and endpoint picker → vLLM queue / prefill / decode, with
 * the replica, node and GPU every span ran on.
 */

import type { ReactNode } from "react";

import type { GetMlInferenceServiceResponse, MlTraceSummary } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import { cn } from "@/lib/utils";

import { MlRead } from "../MlParts";
import { EmptyNote, fmtAgo, fmtMs, mlHref, MlLink, MlTable, MlTraceView, NotCollected, Panel, SidePanel, StatusDot, SubjectLink, tsMs, useMl, useMlSearch, type MlColumn } from "../kit";
import { filterTraces, traceBuckets } from "./inferenceModel";
import { serviceSubject } from "./parts";

export function TracesTab({ d }: { d: GetMlInferenceServiceResponse }) {
  const svc = d.service!;
  const [search, setSearch] = useMlSearch();
  const traces = useMl("listMlTraces", { subject: serviceSubject(svc.id), limit: 50 });
  const now = Date.now();
  return (
    <MlRead query={traces} what="request traces">
      {(data) => {
        if (data.traces.length === 0) {
          return data.missingFindingId ? (
            <Panel title="Request traces" note="Latency on this service comes from its runtime histograms only">
              <NotCollected what="Request traces" source="OpenTelemetry tracing on the inference runtime (Triton --trace-config mode=opentelemetry)" findingId={data.missingFindingId} height={160} />
            </Panel>
          ) : (
            <EmptyNote>No slow requests in the last 24h.</EmptyNote>
          );
        }
        const buckets = traceBuckets(data.traces);
        const rows = filterTraces(data.traces, search.filter);
        const columns: MlColumn<MlTraceSummary>[] = [
          { key: "when", header: "When", render: (t) => <span className="tabular-nums text-ink-muted">{fmtAgo(tsMs(t.start), now)}</span>, sort: (t) => tsMs(t.start) ?? 0 },
          { key: "dur", header: "Duration", numeric: true, render: (t) => fmtMs(t.durationMs), sort: (t) => t.durationMs },
          { key: "vs", header: "vs median", numeric: true, render: (t) => `${t.vsMedian.toFixed(1)}×`, sort: (t) => t.vsMedian },
          { key: "ttft", header: "TTFT", numeric: true, render: (t) => fmtMs(Number(t.attributes.ttft_ms)), sort: (t) => Number(t.attributes.ttft_ms) },
          { key: "tok", header: "Tokens in / out", numeric: true, render: (t) => `${Number(t.attributes.prompt_tokens).toLocaleString("en-US")} / ${t.attributes.output_tokens}` },
          { key: "dom", header: "Dominant", render: (t) => <span className="font-mono text-code">{t.dominantSpan}</span> },
          { key: "finish", header: "Finish", render: (t) => <StatusDot tone={t.error ? "danger" : t.attributes.finish_reason === "stop" ? "neutral" : "warning"} label={t.attributes.finish_reason} /> },
          { key: "adapter", header: "Adapter", render: (t) => t.attributes.adapter },
          { key: "tenant", header: "Tenant", render: (t) => <span className="text-ink-muted">{t.attributes.tenant}</span> },
          { key: "rev", header: "Revision", render: (t) => <span className="font-mono text-code text-ink-muted">{t.attributes.revision}</span> },
          { key: "replica", header: "Replica", render: (t) => (t.subject ? <SubjectLink subject={{ kind: t.subject.kind, id: t.subject.id }} mono>{t.subject.label}</SubjectLink> : "—") },
          { key: "bucket", header: "Bucket", render: (t) => <span className="text-ink-muted">{t.attributes.bucket}</span> },
        ];
        return (
          <div className="space-y-3">
            <div className="flex flex-wrap items-center gap-1.5" role="group" aria-label="Filter by bucket">
              <FilterChip on={!search.filter} onClick={() => setSearch({ filter: undefined })}>
                All {data.traces.length}
              </FilterChip>
              {buckets.map((b) => (
                <FilterChip key={b.bucket} on={search.filter === b.bucket} onClick={() => setSearch({ filter: b.bucket })}>
                  {b.bucket} {b.count}
                </FilterChip>
              ))}
            </div>
            <Panel title="Slowest requests" note="24h, tail-sampled: errors, length / abort, and anything over the SLO" flush>
              <MlTable columns={columns} rows={rows} rowKey={(t) => t.traceId} selected={search.sel} onRowClick={(t) => setSearch({ sel: t.traceId })} initialSort={{ key: "dur", desc: true }} testId="ml-inference-traces" />
            </Panel>
            {search.sel && rows.some((t) => t.traceId === search.sel) && (
              <SidePanel
                title="Request trace"
                meta={
                  <MlLink href={mlHref("inference", ["traces", search.sel])} className="text-primary-ink">
                    Open full page
                  </MlLink>
                }
                onClose={() => setSearch({ sel: undefined })}
                width="min(960px, 100vw)"
                testId="ml-inference-trace-panel"
              >
                <MlTraceView traceId={search.sel} />
              </SidePanel>
            )}
          </div>
        );
      }}
    </MlRead>
  );
}

export function FilterChip({ on, onClick, children }: { on: boolean; onClick: () => void; children: ReactNode }) {
  return (
    <button
      type="button"
      aria-pressed={on}
      onClick={onClick}
      className={cn("rounded-md border px-2 py-0.5 text-meta tabular-nums", on ? "border-primary text-ink" : "border-border text-ink-muted hover:text-ink")}
    >
      {children}
    </button>
  );
}
