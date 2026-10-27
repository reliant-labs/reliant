// Copyright (c) 2025 Reliant Labs

/**
 * One trace: its waterfall, and the selected span's details — attributes,
 * events, and the ways out to that span's logs and to the flamegraph of the
 * CPU its service spent serving the trace (profiles link samples to traces,
 * not to individual spans).
 *
 * The selected span is in the URL (`span`), so a link can point at the one
 * slow call in a trace of four hundred.
 *
 * A trace that began in a browser carries the session it ran in
 * (`rum.sessionId` on its browser spans), and opens that session — its
 * timeline and, when recorded, its masked replay — at the trace's start.
 */

import { ArrowLeft, Flame, FileText, LineChart, MonitorPlay } from "lucide-react";

import Card, { CardHeader } from "@/components/forge-ui/card";
import Button from "@/components/forge-ui/button";
import { useTrace } from "@/hooks/observability-queries";
import type { ObsScope, Span } from "@/services/controlPlane/observability/types";
import { keepsLastResult } from "@/services/controlPlane/observability/quota";

import { AskReliantButton } from "./AskReliant/AskReliant";
import { useObsAskContext } from "./AskReliant/askReliantContext";
import { traceEnvName } from "./AskReliant/askReliantPrompt";
import { AttributeTable } from "./AttributeTable";
import { ExploreLink } from "./Metrics/ExploreLink";
import { tracePivot, useExploreHref } from "./Metrics/pivots";
import { formatDuration, formatTimestamp } from "./format";
import { shortId } from "./LogsView";
import type { ObsNavigate } from "./obsSearch";
import { ObsAsOf, ObsError, ObsLoadingRows, OperatorBadge } from "./ObsStates";
import { signalState, useSignalsIndex, type SignalState } from "./serviceSignals";
import { SessionName } from "./Sessions/SessionName";
import { rumScopeOf, rumSessionIdOf, sessionLink } from "./Sessions/sessionModel";
import { TraceWaterfall } from "./waterfall/TraceWaterfall";
import { traceExtent } from "./waterfall/traceTree";

export function TraceDetail({
  scope,
  subject,
  traceId,
  spanId,
  navigate,
}: {
  scope: ObsScope;
  subject: string;
  traceId: string;
  spanId: string | undefined;
  navigate: ObsNavigate;
}) {
  const trace = useTrace(scope, traceId);
  const { environments } = useObsAskContext();
  const spans = trace.data?.spans ?? [];
  const root = spans.find((span) => !span.parentSpanId) ?? spans[0];
  const selected = spans.find((span) => span.spanId === spanId) ?? null;
  const extent = trace.data ? traceExtent(spans) : null;
  const exploreHref = useExploreHref();
  const metricsHref = (span: Span | undefined) => (span ? (exploreHref?.(tracePivot(span), { range: "1h" }) ?? null) : null);
  const sessionId = rumSessionIdOf(spans);
  // A flamegraph only for a service that sends (or declares) profiles. Judged
  // over a week: trace-linked samples are kept 72 hours, and a trace opened
  // from the 7-day list must not lose its flamegraph to a one-day probe.
  const signals = useSignalsIndex(scope, "7d");
  const profilesOf = (service: string | undefined): SignalState => signalState(signals, service, "profiles");
  const rootProfiles = profilesOf(root?.service);

  const back = (
    <Button
      variant="ghost"
      size="sm"
      onClick={() => navigate({ trace: undefined, span: undefined })}
      leftIcon={<ArrowLeft className="h-3.5 w-3.5" />}
      data-testid="trace-back"
    >
      All traces
    </Button>
  );

  if (trace.isLoading) {
    return (
      <div className="space-y-3">
        {back}
        <Card padding="none">
          <ObsLoadingRows testId="trace-loading" rows={8} />
        </Card>
      </div>
    );
  }
  if (trace.error && !keepsLastResult(trace.error, trace.data)) {
    return (
      <div className="space-y-3">
        {back}
        <ObsError error={trace.error} subject={subject} onRetry={() => void trace.refetch()} retrying={trace.isFetching} />
      </div>
    );
  }
  if (spans.length === 0) {
    return (
      <div className="space-y-3">
        {back}
        <p
          role="status"
          data-testid="trace-empty"
          className="rounded-lg border border-dashed border-border bg-surface-sunken px-4 py-5 text-sm text-ink-muted"
        >
          No spans were found for trace <span className="font-mono text-ink">{traceId}</span>. It may be older than
          this environment keeps traces (7 days), or it was dropped by sampling.
        </p>
      </div>
    );
  }

  return (
    <div className="space-y-4" data-testid="trace-detail">
      <ObsAsOf updatedAt={trace.dataUpdatedAt} />
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex min-w-0 items-center gap-2">
          {back}
          <h3 className="truncate text-sm font-semibold text-ink">
            <span className="text-ink-muted">{root?.service}</span>{" "}
            <span className="font-mono">{root?.name}</span>
          </h3>
          {root && <OperatorBadge provenance={root} />}
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <span className="font-mono text-xs text-ink-muted" title={traceId}>
            {shortId(traceId)}
          </span>
          {extent && <span className="text-xs text-ink-muted">{formatTimestamp(extent.startMs)}</span>}
          <AskReliantButton subject={{ kind: "trace", traceId, env: traceEnvName(scope.envIds, environments, root?.resource) }} />
          <Button
            variant="outline"
            size="sm"
            onClick={() => navigate({ view: "logs", trace: traceId, span: undefined, q: undefined, service: undefined })}
            leftIcon={<FileText className="h-3.5 w-3.5" />}
            data-testid="trace-logs"
          >
            Logs for this trace
          </Button>
          {metricsHref(selected ?? root) && (
            <ExploreLink href={metricsHref(selected ?? root)!} testId="trace-metrics">
              <LineChart className="h-3.5 w-3.5" aria-hidden="true" />
              Metrics for {(selected ?? root)!.service}
            </ExploreLink>
          )}
          {rootProfiles.kind !== "absent" && (
            <Button
              variant="outline"
              size="sm"
              disabled={rootProfiles.kind === "idle"}
              title={rootProfiles.kind === "idle" ? rootProfiles.hint : undefined}
              onClick={() => navigate({ view: "profiles", trace: traceId, span: undefined, service: root?.service })}
              leftIcon={<Flame className="h-3.5 w-3.5" />}
              data-testid="trace-flamegraph"
            >
              Flamegraph
            </Button>
          )}
          {sessionId && (
            <Button
              variant="outline"
              size="sm"
              onClick={() => navigate(sessionLink(sessionId, extent ? extent.startMs - SESSION_LEAD_MS : undefined))}
              leftIcon={<MonitorPlay className="h-3.5 w-3.5" />}
              data-testid="trace-session"
              title={`Browser session ${sessionId}`}
            >
              <SessionName scope={rumScopeOf(scope)} sessionId={sessionId} />
            </Button>
          )}
        </div>
      </div>

      <section aria-label="Related traces" className="space-y-2 rounded-lg border border-border bg-background p-3 text-xs">
        <h4 className="font-semibold text-foreground">Related traces</h4>
        <p className="text-muted-foreground">Links preserve causality across trace boundaries; they are not parent/child spans. Results use the current environment and time range.</p>
        {(trace.data?.relatedTraces?.length ?? 0) > 0 ? (
          <ul className="space-y-1">
            {trace.data?.relatedTraces?.map((ref) => (
              <li key={`${ref.traceId}-${ref.spanId}-${ref.linkedSpanId}`}>
                <Button variant="ghost" size="xs" onClick={() => navigate({ view: "traces", trace: ref.traceId, span: ref.spanId, service: undefined, q: undefined })}>
                  Trace {shortId(ref.traceId)} · span {shortId(ref.spanId)} links to {shortId(ref.linkedSpanId)}
                </Button>
              </li>
            ))}
          </ul>
        ) : <p className="text-muted-foreground">No incoming links found. Related spans may be outside this range, sampled out, or past retention.</p>}
        {trace.data?.relatedTracesTruncated && <p role="status" className="text-muted-foreground">Showing the first 100 related spans. Narrow the time range to find more.</p>}
      </section>

      <div className="grid grid-cols-[minmax(0,1fr)] gap-4 xl:grid-cols-[minmax(0,3fr)_minmax(20rem,1fr)]">
        <Card padding="md">
          <TraceWaterfall
            spans={spans}
            truncated={trace.data?.truncated ?? false}
            selectedSpanId={selected?.spanId}
            onSelect={(next) => navigate({ span: next }, { replace: true })}
          />
        </Card>
        {selected ? (
          <SpanDetails
            span={selected}
            originMs={extent?.startMs ?? selected.startMs}
            onLogs={() => navigate({ view: "logs", trace: traceId, span: selected.spanId, q: undefined, service: undefined })}
            profiles={profilesOf(selected.service)}
            onFlamegraph={() => navigate({ view: "profiles", trace: traceId, span: undefined, service: selected.service })}
            onClose={() => navigate({ span: undefined }, { replace: true })}
            onLinkedSpan={(trace, span) => navigate({ view: "traces", trace, span, service: undefined, q: undefined })}
          />
        ) : (
          <p className="self-start rounded-lg border border-dashed border-border bg-surface-sunken px-4 py-5 text-xs text-ink-muted">
            Select a span to see its attributes, events and logs.
          </p>
        )}
      </div>
    </div>
  );
}

/** How far before the trace its session opens, to show what led to it. */
const SESSION_LEAD_MS = 2_000;

function SpanDetails({
  span,
  originMs,
  onLogs,
  profiles,
  onFlamegraph,
  onClose,
  onLinkedSpan,
}: {
  span: Span;
  originMs: number;
  onLogs: () => void;
  /** Whether the span's service has profiles to open (serviceSignals.ts). */
  profiles: SignalState;
  onFlamegraph: () => void;
  onClose: () => void;
  onLinkedSpan: (traceId: string, spanId: string) => void;
}) {
  return (
    <Card padding="md" className="self-start" data-testid="span-details">
      <CardHeader
        title={<span className="break-all font-mono">{span.name}</span>}
        description={`${span.service} · ${span.kind || "internal"}`}
        actions={
          <Button variant="ghost" size="xs" onClick={onClose} aria-label="Close span details">
            Close
          </Button>
        }
      />
      <dl className="mb-3 grid grid-cols-2 gap-2 text-xs">
        <Fact label="Duration" value={formatDuration(span.durationMs)} />
        <Fact label="Starts at" value={`+${formatDuration(span.startMs - originMs)}`} />
        <Fact
          label="Status"
          value={span.status === "error" ? "Error" : span.status === "ok" ? "OK" : "Unset"}
          tone={span.status === "error" ? "error" : undefined}
        />
        <Fact label="Span" value={shortId(span.spanId)} mono />
      </dl>
      {span.status === "error" && span.statusMessage && (
        <p className="mb-3 break-words rounded-md border border-danger-border bg-surface-sunken px-3 py-2 font-mono text-xs text-danger-ink">
          {span.statusMessage}
        </p>
      )}
      <div className="mb-3 flex flex-wrap gap-2">
        <Button variant="outline" size="xs" onClick={onLogs} leftIcon={<FileText className="h-3 w-3" />} data-testid="span-logs">
          Logs for this span
        </Button>
        {profiles.kind !== "absent" && (
          <Button
            variant="outline"
            size="xs"
            disabled={profiles.kind === "idle"}
            title={profiles.kind === "idle" ? profiles.hint : undefined}
            onClick={onFlamegraph}
            leftIcon={<Flame className="h-3 w-3" />}
            data-testid="span-flamegraph"
          >
            {span.service} CPU in this trace
          </Button>
        )}
      </div>
      <div className="space-y-3">
        {(span.links?.length ?? 0) > 0 && (
          <section aria-label="Span links">
            <h4 className="mb-1 text-xs font-semibold uppercase tracking-wide text-muted-foreground">Span links</h4>
            <p className="mb-1 text-xs text-muted-foreground">Causal references, not parent/child edges. Linked spans may be outside this range, sampled out, or past retention.</p>
            <ul className="space-y-1">
              {span.links?.map((link, index) => (
                <li key={`${link.traceId}-${link.spanId}-${index}`}>
                  <Button variant="ghost" size="xs" onClick={() => onLinkedSpan(link.traceId, link.spanId)}>
                    Trace {shortId(link.traceId)} · span {shortId(link.spanId)}
                  </Button>
                  {Object.keys(link.attributes).length > 0 && <AttributeTable title="Link attributes" attributes={link.attributes} />}
                </li>
              ))}
            </ul>
          </section>
        )}
        <AttributeTable title="Attributes" attributes={span.attributes} />
        {span.events.length > 0 && (
          <section aria-label="Events">
            <h4 className="mb-1 text-xs font-semibold uppercase tracking-wide text-ink-muted">Events</h4>
            <ul className="space-y-2">
              {span.events.map((event, index) => (
                <li key={`${event.name}-${index}`} className="rounded-md border border-border/60 bg-surface-sunken px-3 py-2 text-xs">
                  <p className="flex justify-between gap-2">
                    <span className="font-mono text-ink">{event.name}</span>
                    <span className="tabular-nums text-ink-muted">+{formatDuration(event.timeMs - originMs)}</span>
                  </p>
                  {Object.keys(event.attributes).length > 0 && (
                    <dl className="mt-1 space-y-0.5 font-mono text-ink-muted">
                      {Object.entries(event.attributes).map(([key, value]) => (
                        <div key={key} className="flex gap-2">
                          <dt className="shrink-0">{key}</dt>
                          <dd className="break-all text-ink">{value}</dd>
                        </div>
                      ))}
                    </dl>
                  )}
                </li>
              ))}
            </ul>
          </section>
        )}
        <AttributeTable title="Resource" attributes={span.resource} />
      </div>
    </Card>
  );
}

function Fact({ label, value, tone, mono }: { label: string; value: string; tone?: "error"; mono?: boolean }) {
  return (
    <div>
      <dt className="text-xs font-medium uppercase tracking-wide text-ink-muted">{label}</dt>
      <dd className={`${mono ? "font-mono " : ""}${tone === "error" ? "text-danger-ink" : "text-ink"}`}>{value}</dd>
    </div>
  );
}
