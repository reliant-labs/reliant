// Copyright (c) 2026 Reliant Labs

/**
 * Inference › services (02-views §6.1): one row per serving endpoint with its
 * SLIs, saturation, cost per token and the newest revision's verdict, under
 * a header of the numbers that say whether serving is healthy.
 */

import { MlAlertState, MlSubjectKind, type MlAlertRule, type MlEvent, type MlInferenceService } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import { MlFrame, MlRead } from "../MlParts";
import {
  Chip,
  EmptyNote,
  eventKindOf,
  fmtAgo,
  fmtMs,
  fmtPct,
  fmtUsd,
  Grid,
  KpiStrip,
  Meter,
  mlHref,
  MlTable,
  Panel,
  severityOf,
  Sparkline,
  StatusDot,
  SubjectLink,
  tail,
  tsMs,
  useMl,
  verdictOf,
  type MlColumn,
} from "../kit";
import { kvTone, servicesKpis, sloTone } from "./inferenceModel";

const SERVING_KINDS = new Set([MlSubjectKind.INFERENCE_SERVICE, MlSubjectKind.REVISION, MlSubjectKind.REPLICA]);
const isServing = (kind: MlSubjectKind | undefined) => kind !== undefined && SERVING_KINDS.has(kind);
const gpuName = (product: string) => product.replace(/^NVIDIA\s+/, "").replace(/\s+\d+GB.*$/, "");

export function ServicesList() {
  const list = useMl("listMlInferenceServices", {});
  return (
    <MlFrame view="inference">
      <MlRead query={list} what="inference services">
        {(data) => <ServicesBody services={data.services} />}
      </MlRead>
    </MlFrame>
  );
}

function ServicesBody({ services }: { services: readonly MlInferenceService[] }) {
  const k = servicesKpis(services);
  const columns: MlColumn<MlInferenceService>[] = [
    {
      key: "service",
      header: "Service",
      render: (s) => (
        <span className="inline-flex items-center gap-2">
          <SubjectLink subject={{ kind: MlSubjectKind.INFERENCE_SERVICE, id: s.id }}>{s.name}</SubjectLink>
          {s.comparing && (
            <Chip title="A Changes comparison is running on this service" tone="neutral">
              A/B
            </Chip>
          )}
        </span>
      ),
      sort: (s) => s.name,
    },
    { key: "env", header: "Env", render: (s) => <span className="text-ink-muted">{s.env}</span>, sort: (s) => s.env },
    {
      key: "model",
      header: "Model · rev",
      render: (s) => (
        <span className="inline-flex items-center gap-1.5">
          <SubjectLink subject={{ kind: MlSubjectKind.MODEL_VERSION, id: s.modelVersionId }} className="max-w-40 truncate">{`${s.modelId === s.name ? "" : `${tail(s.modelId)} `}${tail(s.modelVersionId)}`}</SubjectLink>
          <span className="text-ink-muted">·</span>
          <SubjectLink subject={{ kind: MlSubjectKind.REVISION, id: s.revisionId }} mono>
            {s.release}
          </SubjectLink>
        </span>
      ),
    },
    { key: "runtime", header: "Runtime", render: (s) => <span className="text-ink-muted">{s.runtime.replace(/^(vLLM \d+\.\d+)\.\d+$/, "$1")}</span> },
    {
      key: "rps",
      header: "rps",
      numeric: true,
      render: (s) => (
        <span className="inline-flex items-center justify-end gap-2">
          <Sparkline values={s.rpsSeries?.y ?? []} width={44} height={16} />
          <span className="w-7">{s.rps >= 10 ? Math.round(s.rps) : s.rps.toFixed(1)}</span>
        </span>
      ),
      sort: (s) => s.rps,
    },
    { key: "ttft", header: "TTFT p95", numeric: true, render: (s) => <NaCell value={s.ttftP95Ms} format={fmtMs} why="no token generation" />, sort: (s) => s.ttftP95Ms || 0 },
    { key: "itl", header: "ITL p95", numeric: true, render: (s) => <NaCell value={s.itlP95Ms} format={fmtMs} why="no token generation" />, sort: (s) => s.itlP95Ms || 0 },
    { key: "e2e", header: "e2e p95", numeric: true, render: (s) => fmtMs(s.e2eP95Ms), sort: (s) => s.e2eP95Ms },
    { key: "err", header: "Err", numeric: true, render: (s) => fmtPct(s.errorRatio, s.errorRatio < 0.01 ? 2 : 1), sort: (s) => s.errorRatio },
    {
      key: "slo",
      header: "SLO",
      numeric: true,
      render: (s) => {
        const tone = sloTone(s.sloAttainment, s.sloTarget);
        return (
          <span title={`Target ${fmtPct(s.sloTarget, 1)}`} className={tone === "danger" ? "text-danger-ink" : tone === "warning" ? "text-warning-ink" : undefined}>
            {fmtPct(s.sloAttainment, s.sloAttainment > 0.999 ? 2 : 1)}
          </span>
        );
      },
      sort: (s) => s.sloAttainment - s.sloTarget,
    },
    { key: "kv", header: "KV", numeric: true, render: (s) => <Meter ratio={s.kvCacheRatio} tone={kvTone(s.kvCacheRatio)} width={28} />, sort: (s) => s.kvCacheRatio || 0 },
    { key: "gpus", header: "GPUs", numeric: true, render: (s) => `${s.accelerators}×${gpuName(s.acceleratorProduct)}`, sort: (s) => s.accelerators },
    { key: "util", header: "Busy", numeric: true, render: (s) => fmtPct(s.busyRatio), sort: (s) => s.busyRatio },
    { key: "usd", header: "$/1M tok", numeric: true, render: (s) => <NaCell value={s.usdPerMillionTokens} format={fmtUsd} why="not priced per token" />, sort: (s) => s.usdPerMillionTokens || 0 },
    {
      key: "verdict",
      header: "Verdict",
      render: (s) => {
        const v = verdictOf(s.verdict);
        return <StatusDot tone={v.tone} label={v.label} />;
      },
    },
  ];

  return (
    <div className="space-y-4">
      <KpiStrip
        items={[
          { label: "Services", value: k.services, sub: `${services.filter((s) => s.env === "prod").length} in prod` },
          { label: "Requests/s now", value: k.rps >= 10 ? Math.round(k.rps) : k.rps.toFixed(1), sub: "across every service" },
          {
            label: "Worst SLO attainment",
            value: fmtPct(k.worstSlo?.sloAttainment, 1),
            tone: k.worstSlo ? sloTone(k.worstSlo.sloAttainment, k.worstSlo.sloTarget) : undefined,
            sub: k.worstSlo ? `${k.worstSlo.name} (${k.worstSlo.env}) · target ${fmtPct(k.worstSlo.sloTarget, 1)}` : undefined,
            href: k.worstSlo ? mlHref("inference", [k.worstSlo.id]) : undefined,
          },
          { label: "Worst TTFT p95 · 24h", value: fmtMs(k.worstTtft?.ttftP95Ms), sub: k.worstTtft ? `${k.worstTtft.name} (${k.worstTtft.env})` : undefined, href: k.worstTtft ? mlHref("inference", [k.worstTtft.id]) : undefined },
          { label: "Blended $/1M tokens", value: fmtUsd(k.blendedUsdPerMillion), sub: `${fmtUsd(k.usd24h)} in 24h` },
          { label: "GPUs serving", value: k.gpus, sub: `${fmtPct(k.busy)} busy` },
        ]}
      />
      <Panel title="Services" note="24h SLIs; rps now. Click a row for its latency, engine, replicas and revisions." flush testId="ml-inference-services">
        <MlTable columns={columns} rows={services} rowKey={(s) => s.id} rowHref={(s) => mlHref("inference", [s.id])} testId="ml-inference-services-table" />
      </Panel>
      <Grid cols={2}>
        <ServingAlerts />
        <ServingEvents />
      </Grid>
    </div>
  );
}

function NaCell({ value, format, why }: { value: number; format: (v: number) => string; why: string }) {
  if (Number.isFinite(value)) return <>{format(value)}</>;
  return (
    <span className="text-ink-muted" title={`Not applicable: ${why}`}>
      n/a
    </span>
  );
}

function ServingAlerts() {
  const alerts = useMl("listMlAlerts", { subject: { kind: MlSubjectKind.ORG, id: "", label: "" } });
  return (
    <Panel title="Alerts on serving" note="Firing and pending; a regression halts promotion and notifies — Reliant never rolls back on its own" flush>
      <MlRead query={alerts} what="alerts">
        {(data) => {
          const rows = data.alerts.filter((a) => isServing(a.subject?.kind) && a.state !== MlAlertState.OK);
          if (rows.length === 0) return <div className="px-4"><EmptyNote>Nothing firing on serving.</EmptyNote></div>;
          const cols: MlColumn<MlAlertRule>[] = [
            { key: "state", header: "State", render: (a) => <StatusDot tone={a.state === MlAlertState.FIRING ? severityOf(a.severity).tone : "info"} label={a.state === MlAlertState.FIRING ? "Firing" : "Pending"} /> },
            { key: "name", header: "Alert", render: (a) => a.name },
            { key: "on", header: "On", render: (a) => (a.subject ? <SubjectLink subject={{ kind: a.subject.kind, id: a.subject.id }}>{a.subject.label || a.subject.id}</SubjectLink> : "—") },
            { key: "to", header: "Routed to", render: (a) => <span className="text-ink-muted">{a.routedTo || "—"}</span> },
          ];
          return <MlTable columns={cols} rows={rows} rowKey={(a) => a.id} rowHref={(a) => (a.subject ? mlHref("inference", [a.subject.id], { tab: "alerts" }) : null)} />;
        }}
      </MlRead>
    </Panel>
  );
}

function ServingEvents() {
  const events = useMl("listMlEvents", { subject: { kind: MlSubjectKind.ORG, id: "", label: "" } });
  return (
    <Panel title="Recent serving events" note="Releases, Changes ramps, restarts and findings" flush>
      <MlRead query={events} what="events">
        {(data) => {
          const now = Date.now();
          const rows = data.events.filter((e) => isServing(e.subject?.kind)).sort((a, b) => (tsMs(b.at) ?? 0) - (tsMs(a.at) ?? 0));
          if (rows.length === 0) return <div className="px-4"><EmptyNote>No serving events.</EmptyNote></div>;
          const cols: MlColumn<MlEvent>[] = [
            { key: "when", header: "When", render: (e) => <span className="tabular-nums text-ink-muted">{fmtAgo(tsMs(e.at), now)}</span> },
            { key: "kind", header: "Kind", render: (e) => <StatusDot tone={eventKindOf(e.kind).tone} label={eventKindOf(e.kind).label} /> },
            { key: "title", header: "Event", render: (e) => <span className="block max-w-md truncate" title={e.detail}>{e.title}</span> },
          ];
          return <MlTable columns={cols} rows={rows} rowKey={(e) => e.id} rowHref={(e) => (e.subject ? mlHref("inference", [e.subject.id.split("/")[0]]) : null)} maxRows={6} />;
        }}
      </MlRead>
    </Panel>
  );
}
