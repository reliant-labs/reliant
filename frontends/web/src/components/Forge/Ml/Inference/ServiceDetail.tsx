// Copyright (c) 2026 Reliant Labs

/**
 * Inference › one service (02-views §6.2): are we meeting SLO; is latency
 * queueing, prefill or decode; is the KV cache the limit; did the new
 * revision regress; what does a token cost; are replicas balanced.
 * Tabs (`?tab=`): Overview, Engine, Replicas, Revisions (with the Changes
 * A/B), Traces, Cost, Alerts. Every chart shares one cursor and carries the
 * release, Changes ramp, scale and restart markers.
 */

import { MlSubjectKind, type GetMlInferenceServiceResponse, type MlEvent, type MlSeries } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import { MlFrame, MlRead } from "../MlParts";
import {
  Chip,
  EmptyNote,
  FindingLink,
  fmtCompact,
  fmtMs,
  fmtPct,
  fmtUnit,
  fmtUsd,
  Grid,
  Heatmap,
  KpiStrip,
  MlChart,
  MlTabs,
  Panel,
  StackedBar,
  StatusDot,
  subjectHref,
  SubjectLink,
  tail,
  useActiveTab,
  useMl,
  useMlAlerts,
  useMlEvents,
  verdictOf,
  type HeatmapRow,
  type MlTabDef,
} from "../kit";
import { AlertsTab, CostTab } from "./CostAlertsTabs";
import { byKey, deltaFor, fmtDelta, heatmapRows, kvTone, peakOf, relDelta, sloTone } from "./inferenceModel";
import { serviceSubject, SeriesTile, SYNC, useServingSeries } from "./parts";
import { ReplicasTab } from "./ReplicasTab";
import { RevisionsTab } from "./RevisionsTab";
import { TracesTab } from "./TracesTab";

export function ServiceDetail({ serviceId }: { serviceId: string }) {
  const q = useMl("getMlInferenceService", { serviceId });
  const svc = q.data?.kind === "ok" ? q.data.data.service : undefined;
  const title = svc ? `${svc.name} (${svc.env})` : serviceId;
  return (
    <MlFrame
      view="inference"
      title={title}
      crumbs={[{ label: title }]}
      testId="ml-inference-service"
      meta={
        svc && (
          <span className="inline-flex flex-wrap items-center gap-2 text-ui text-ink-muted">
            <SubjectLink subject={{ kind: MlSubjectKind.REVISION, id: svc.revisionId }} mono>
              {`rev ${svc.release}`}
            </SubjectLink>
            <span>·</span>
            <SubjectLink subject={{ kind: MlSubjectKind.MODEL_VERSION, id: svc.modelVersionId }}>{`model ${tail(svc.modelVersionId)}`}</SubjectLink>
            <span>·</span>
            <span className="inline-flex items-center gap-1.5">
              obs check <StatusDot tone={verdictOf(svc.verdict).tone} label={verdictOf(svc.verdict).label} />
            </span>
            {svc.comparing && <Chip>A/B running</Chip>}
          </span>
        )
      }
    >
      <MlRead query={q} what="this inference service">
        {(d) => <ServiceBody d={d} />}
      </MlRead>
    </MlFrame>
  );
}

function ServiceBody({ d }: { d: GetMlInferenceServiceResponse }) {
  const svc = d.service!;
  const subject = serviceSubject(svc.id, `${svc.name} (${svc.env})`);
  const current = d.revisions.find((r) => r.id === svc.revisionId);
  const ttftDelta = deltaFor(current, "TTFT p95");
  const eventsQ = useMlEvents(subject, { range: "24h" });
  const alertsQ = useMlAlerts(subject);
  const events = eventsQ.data?.kind === "ok" ? eventsQ.data.data.events : [];
  const alerts = alertsQ.data?.kind === "ok" ? alertsQ.data.data.alerts : [];
  const firing = alerts.filter((a) => a.state === "firing").length;
  const slo = sloTone(svc.sloAttainment, svc.sloTarget);
  const generative = Number.isFinite(svc.ttftP95Ms);

  const tabs: MlTabDef[] = [
    { id: "overview", label: "Overview" },
    { id: "engine", label: "Engine", tone: kvTone(svc.kvCacheRatio) === "danger" ? "warning" : undefined },
    { id: "replicas", label: "Replicas", count: d.replicas.length },
    { id: "revisions", label: "Revisions", count: d.revisions.length, ...(d.comparison ? { tone: "info" as const } : {}) },
    { id: "traces", label: "Traces" },
    { id: "cost", label: "Cost" },
    { id: "alerts", label: "Alerts", count: alerts.length || undefined, tone: firing ? "warning" : undefined },
  ];
  const tab = useActiveTab(tabs);

  return (
    <div className="space-y-4">
      <KpiStrip
        items={[
          { label: "SLO attainment · 30d", value: fmtPct(svc.sloAttainment, svc.sloAttainment > 0.999 ? 2 : 1), tone: slo, sub: `target ${fmtPct(svc.sloTarget, 1)} · error ${fmtPct(svc.errorRatio, 2)}`, hero: true },
          { label: "Requests/s now", value: svc.rps >= 10 ? Math.round(svc.rps) : svc.rps.toFixed(1), sub: `${svc.replicas}/${svc.replicasDesired} replicas · ${svc.accelerators} GPUs` },
          generative
            ? {
                label: "TTFT p95 · 24h",
                value: fmtMs(svc.ttftP95Ms),
                sub: ttftDelta ? (
                  <span className={ttftDelta.significant && ttftDelta.regression ? "text-warning-ink" : undefined}>
                    {fmtDelta(relDelta(ttftDelta))} vs {d.revisions.find((r) => r.endedAt && !r.release.includes("+"))?.release ?? "previous"}
                  </span>
                ) : undefined,
              }
            : { label: "e2e p95 · 24h", value: fmtMs(svc.e2eP95Ms), sub: `queue p95 ${fmtMs(svc.queueP95Ms)}` },
          generative ? { label: "ITL p95 · 24h", value: fmtMs(svc.itlP95Ms), sub: `e2e p95 ${fmtMs(svc.e2eP95Ms)}` } : { label: "Busy", value: fmtPct(svc.busyRatio), sub: "GPU utilisation, 24h" },
          generative ? { label: "Output tokens/s", value: fmtCompact(svc.tokensPerSecond), sub: `KV cache ${fmtPct(svc.kvCacheRatio)} avg` } : { label: "Errors", value: fmtPct(svc.errorRatio, 2), sub: "24h" },
          { label: generative ? "$ per 1M tokens" : "Cost 24h", value: generative ? fmtUsd(svc.usdPerMillionTokens) : fmtUsd(svc.cost?.usd), sub: `${fmtUsd(svc.cost?.usd)} · ${Math.round(svc.cost?.gpuHours ?? 0)} GPU-h in 24h` },
        ]}
      />
      {d.findingIds.length > 0 && (
        <p className="flex flex-wrap items-center gap-x-3 gap-y-1 text-meta text-ink-muted" data-testid="ml-inference-findings">
          <span>Findings</span>
          {d.findingIds.map((id) => (
            <FindingLink key={id} id={id}>
              {id}
            </FindingLink>
          ))}
        </p>
      )}
      <MlTabs tabs={tabs} testId="ml-inference-tabs" />
      {tab === "overview" && <OverviewTab d={d} events={events} />}
      {tab === "engine" && <EngineTab d={d} events={events} />}
      {tab === "replicas" && <ReplicasTab d={d} events={events} />}
      {tab === "revisions" && <RevisionsTab d={d} events={events} />}
      {tab === "traces" && <TracesTab d={d} />}
      {tab === "cost" && <CostTab d={d} events={events} />}
      {tab === "alerts" && <AlertsTab alerts={alerts} query={alertsQ} />}
    </div>
  );
}

/** Latency series read as "p50 / p95 / p99" lines (the stat is already the group). */
const asLines = (series: readonly MlSeries[]) => series.map((s) => ({ ...s, stat: "" }) as MlSeries);

const OVERVIEW_KEYS = ["serve.queue_time", "serve.ttft", "serve.itl", "serve.e2e", "serve.time_split", "serve.rps", "serve.tokens_in_per_s", "serve.tokens_out_per_s", "serve.error_ratio"];

function OverviewTab({ d, events }: { d: GetMlInferenceServiceResponse; events: readonly MlEvent[] }) {
  const svc = d.service!;
  const subject = serviceSubject(svc.id);
  const series = useServingSeries(subject, OVERVIEW_KEYS);
  const byFinish = useServingSeries(subject, ["serve.rps"], "finish_reason");
  return (
    <div className="space-y-4">
      <MlRead query={series} what="latency">
        {({ series: all }) => (
          <>
            <Panel title="Latency" note="p50 / p95 / p99 over 24h, with release, Changes and scale markers">
              <Grid cols={2}>
                <SeriesTile title="Queue wait" series={asLines(byKey(all, "serve.queue_time"))} events={events} />
                <SeriesTile title="Time to first token" series={asLines(byKey(all, "serve.ttft"))} events={events} />
                <SeriesTile title="Inter-token latency" series={asLines(byKey(all, "serve.itl"))} events={events} />
                <SeriesTile title="End-to-end" series={asLines(byKey(all, "serve.e2e"))} events={events} />
              </Grid>
            </Panel>
            <Grid cols={2}>
              <Panel title="Where time goes" note="p50 per request: queue wait, prefill, decode (stacked)">
                <MlChart series={byKey(all, "serve.time_split")} events={events} label="Where time goes" area="stacked" syncKey={SYNC} height={180} legend />
              </Panel>
              <Panel title="Traffic" note="Requests/s by finish reason (stacked) and tokens/s">
                <MlRead query={byFinish} what="traffic">
                  {({ series: fr }) => (
                    <div className="space-y-3">
                      {fr.some((s) => s.x.length > 0) ? (
                        <MlChart series={fr} events={events} label="Requests by finish reason" area="stacked" syncKey={SYNC} height={130} legend />
                      ) : (
                        <MlChart series={byKey(all, "serve.rps")} events={events} label="Requests/s" syncKey={SYNC} height={130} />
                      )}
                      <MlChart series={[...byKey(all, "serve.tokens_in_per_s"), ...byKey(all, "serve.tokens_out_per_s")].filter((s) => s.x.length > 0)} events={events} label="Tokens/s" syncKey={SYNC} height={110} legend />
                    </div>
                  )}
                </MlRead>
              </Panel>
            </Grid>
          </>
        )}
      </MlRead>
      <Grid cols={2}>
        <Panel title="Finish reasons" note="Share of requests, 24h">
          {d.finishReasons.length ? (
            <StackedBar segments={d.finishReasons.map((f, i) => ({ label: f.name, value: f.value, ...(f.name === "error" ? { tone: "danger" as const } : f.name === "abort" ? { tone: "warning" as const } : { color: i }) }))} format={fmtCompact} testId="ml-inference-finish" />
          ) : (
            <EmptyNote>No finish reasons: this runtime does not generate tokens.</EmptyNote>
          )}
        </Panel>
        <Panel title="Adapters" note="LoRA adapters on the base model, share of requests">
          {d.adapters.length ? <StackedBar segments={d.adapters.map((a, i) => ({ label: a.name, value: a.value, color: i }))} format={fmtCompact} testId="ml-inference-adapters" /> : <EmptyNote>No LoRA adapters serve on this service.</EmptyNote>}
        </Panel>
      </Grid>
    </div>
  );
}

/** KV cache rarely drops under this; the heatmap's shade starts here so the peaks stand out. */
const KV_FLOOR = 0.4;

/** Rescales cells from [floor, 1] to [0, 1]. */
const stretch = (rows: readonly HeatmapRow[], floor: number): HeatmapRow[] => rows.map((r) => ({ ...r, cells: r.cells.map((c) => (c === null ? null : Math.max(0, (c - floor) / (1 - floor)))) }));

const ENGINE_KEYS = ["engine.running", "engine.waiting", "engine.batch_size", "engine.preemptions", "engine.prefix_cache_hit_ratio", "engine.spec_decode_acceptance", "serve.replicas"];

function EngineTab({ d, events }: { d: GetMlInferenceServiceResponse; events: readonly MlEvent[] }) {
  const svc = d.service!;
  const subject = serviceSubject(svc.id);
  const series = useServingSeries(subject, ENGINE_KEYS);
  const kv = useServingSeries(subject, ["engine.kv_cache_ratio"], "replica");
  const kvFinding = d.findingIds.find((id) => id.includes("kv-cache"));
  return (
    <div className="space-y-4">
      <MlRead query={kv} what="KV cache">
        {({ series: perReplica }) => {
          const peak = peakOf(perReplica);
          const { rows, columns } = heatmapRows(
            perReplica,
            6,
            (s) => (s.subject ? (subjectHref(s.subject) ?? undefined) : undefined),
            (s) => peakOf([s]).value >= 0.9,
          );
          return (
            <Panel
              title="KV cache per replica"
              note={
                Number.isFinite(peak.value) ? (
                  <>
                    Peak {fmtPct(peak.value)} on {peak.series?.group}; above ~90% vLLM preempts and requests wait
                    {kvFinding && (
                      <>
                        {" · "}
                        <FindingLink id={kvFinding}>the fix</FindingLink>
                      </>
                    )}
                  </>
                ) : undefined
              }
            >
              {rows.length === 0 ? (
                <SeriesTile title="KV cache" series={perReplica} />
              ) : (
                <div className="space-y-3">
                  <MlChart series={perReplica} events={events} label="KV cache per replica" syncKey={SYNC} height={150} legend unit="ratio" tones={perReplica.map((s) => (peakOf([s]).value >= 0.9 ? "warning" : undefined))} />
                  <Heatmap rows={stretch(rows, KV_FLOOR)} columns={columns} format={(v) => fmtUnit(KV_FLOOR + v * (1 - KV_FLOOR), "ratio")} labelWidth={96} testId="ml-inference-kv-heatmap" />
                  <p className="text-meta text-ink-muted">Shade runs from {fmtPct(KV_FLOOR)} (lightest) to 100%; hatched: the replica did not exist (scaled down).</p>
                </div>
              )}
            </Panel>
          );
        }}
      </MlRead>
      <MlRead query={series} what="engine metrics">
        {({ series: all }) => (
          <Grid cols={3}>
            <SeriesTile title="Running and waiting requests" series={[...byKey(all, "engine.running"), ...byKey(all, "engine.waiting").map((s) => ({ ...s, group: "waiting" }) as MlSeries)]} events={events} tones={[undefined, "warning"]} note="Across replicas; waiting > 0 means the batch is full" />
            <SeriesTile title="Batch size" series={byKey(all, "engine.batch_size")} events={events} note={svc.runtime.startsWith("vLLM") ? "Requests per engine step · max_num_seqs 256" : "Requests per batch"} />
            <SeriesTile title="KV preemptions" series={byKey(all, "engine.preemptions")} events={events} bars note="vllm:num_preemptions per 5 min" tones={["warning"]} />
            <SeriesTile title="Prefix cache hit rate" series={byKey(all, "engine.prefix_cache_hit_ratio")} events={events} note="prefix_cache_hits / prefix_cache_queries" />
            <SeriesTile title="Speculative decoding acceptance" series={byKey(all, "engine.spec_decode_acceptance")} events={events} note="accepted / draft tokens" />
            <SeriesTile title="Replicas vs desired" series={byKey(all, "serve.replicas")} events={events} note="HPA on vllm:num_requests_waiting" />
          </Grid>
        )}
      </MlRead>
    </div>
  );
}
