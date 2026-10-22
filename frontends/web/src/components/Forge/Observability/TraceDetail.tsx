// Copyright (c) 2025 Reliant Labs

/**
 * One trace: its waterfall, and the selected span's details — attributes,
 * events, and the ways out to that span's logs and to the flamegraph of the
 * CPU its service spent serving the trace (profiles link samples to traces,
 * not to individual spans).
 *
 * The selected span is in the URL (`span`), so a link can point at the one
 * slow call in a trace of four hundred.
 */

import { ArrowLeft, Flame, FileText } from "lucide-react";

import Card, { CardHeader } from "@/components/forge-ui/card";
import { Button } from "@/components/ui/Button";
import { useTrace } from "@/hooks/observability-queries";
import type { ObsScope, Span } from "@/services/controlPlane/observability/types";

import { AttributeTable } from "./AttributeTable";
import { formatDuration, formatTimestamp } from "./format";
import { shortId } from "./LogsView";
import type { ObsNavigate } from "./obsSearch";
import { ObsError, ObsLoadingRows, OperatorBadge } from "./ObsStates";
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
  const spans = trace.data?.spans ?? [];
  const root = spans.find((span) => !span.parentSpanId) ?? spans[0];
  const selected = spans.find((span) => span.spanId === spanId) ?? null;
  const extent = trace.data ? traceExtent(spans) : null;

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
  if (trace.error) {
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
          className="rounded-lg border border-dashed border-border bg-background px-4 py-5 text-sm text-muted-foreground"
        >
          No spans were found for trace <span className="font-mono text-foreground">{traceId}</span>. It may be older than
          this environment keeps traces (7 days), or it was dropped by sampling.
        </p>
      </div>
    );
  }

  return (
    <div className="space-y-4" data-testid="trace-detail">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex min-w-0 items-center gap-2">
          {back}
          <h3 className="truncate text-sm font-semibold text-foreground">
            <span className="text-muted-foreground">{root?.service}</span>{" "}
            <span className="font-mono">{root?.name}</span>
          </h3>
          {root && <OperatorBadge provenance={root} />}
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <span className="font-mono text-xs text-muted-foreground" title={traceId}>
            {shortId(traceId)}
          </span>
          {extent && <span className="text-xs text-muted-foreground">{formatTimestamp(extent.startMs)}</span>}
          <Button
            variant="outline"
            size="sm"
            onClick={() => navigate({ view: "logs", trace: traceId, span: undefined, q: undefined, service: undefined })}
            leftIcon={<FileText className="h-3.5 w-3.5" />}
            data-testid="trace-logs"
          >
            Logs for this trace
          </Button>
          <Button
            variant="outline"
            size="sm"
            onClick={() => navigate({ view: "profiles", trace: traceId, span: undefined, service: root?.service })}
            leftIcon={<Flame className="h-3.5 w-3.5" />}
            data-testid="trace-flamegraph"
          >
            Flamegraph
          </Button>
        </div>
      </div>

      <div className="grid gap-4 xl:grid-cols-[minmax(0,3fr)_minmax(20rem,1fr)]">
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
            onFlamegraph={() => navigate({ view: "profiles", trace: traceId, span: undefined, service: selected.service })}
            onClose={() => navigate({ span: undefined }, { replace: true })}
          />
        ) : (
          <p className="self-start rounded-lg border border-dashed border-border bg-background px-4 py-5 text-xs text-muted-foreground">
            Select a span to see its attributes, events and logs.
          </p>
        )}
      </div>
    </div>
  );
}

function SpanDetails({
  span,
  originMs,
  onLogs,
  onFlamegraph,
  onClose,
}: {
  span: Span;
  originMs: number;
  onLogs: () => void;
  onFlamegraph: () => void;
  onClose: () => void;
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
        <p className="mb-3 break-words rounded-md border border-destructive/40 bg-background px-3 py-2 font-mono text-xs text-destructive">
          {span.statusMessage}
        </p>
      )}
      <div className="mb-3 flex flex-wrap gap-2">
        <Button variant="outline" size="xs" onClick={onLogs} leftIcon={<FileText className="h-3 w-3" />} data-testid="span-logs">
          Logs for this span
        </Button>
        <Button variant="outline" size="xs" onClick={onFlamegraph} leftIcon={<Flame className="h-3 w-3" />} data-testid="span-flamegraph">
          {span.service} CPU in this trace
        </Button>
      </div>
      <div className="space-y-3">
        <AttributeTable title="Attributes" attributes={span.attributes} />
        {span.events.length > 0 && (
          <section aria-label="Events">
            <h4 className="mb-1 text-xs font-semibold uppercase tracking-wide text-muted-foreground">Events</h4>
            <ul className="space-y-2">
              {span.events.map((event, index) => (
                <li key={`${event.name}-${index}`} className="rounded-md border border-border/60 bg-background px-3 py-2 text-xs">
                  <p className="flex justify-between gap-2">
                    <span className="font-mono text-foreground">{event.name}</span>
                    <span className="tabular-nums text-muted-foreground">+{formatDuration(event.timeMs - originMs)}</span>
                  </p>
                  {Object.keys(event.attributes).length > 0 && (
                    <dl className="mt-1 space-y-0.5 font-mono text-muted-foreground">
                      {Object.entries(event.attributes).map(([key, value]) => (
                        <div key={key} className="flex gap-2">
                          <dt className="shrink-0">{key}</dt>
                          <dd className="break-all text-foreground">{value}</dd>
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
      <dt className="text-xs font-medium uppercase tracking-wide text-muted-foreground">{label}</dt>
      <dd className={`${mono ? "font-mono " : ""}${tone === "error" ? "text-destructive" : "text-foreground"}`}>{value}</dd>
    </div>
  );
}
