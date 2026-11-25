// Copyright (c) 2026 Reliant Labs

/**
 * The chat's Trace view (dev-docs/design/agent-analytics.md §2): what its
 * runs did and how long each step took, drawn with the observability shell's
 * waterfall, and what each model call cost.
 *
 *   PARTIAL banner   when either half is not the whole window, with reasons
 *   Totals           calls, tokens, time in model; cost for the owner
 *   Timing           the span waterfall, and the selected span's facts
 *   Model calls      one row per provider request
 *
 * One read, ChatTraceService.GetChatTrace, authorized as the chat itself. The
 * owner gets the whole answer; a viewer of a shared chat gets structure,
 * timing, models and tokens (`viewer` on the response), and this view also
 * hides every dollar when the chat says the caller is a viewer, whatever the
 * response carried. Anyone else is NotFound, said as "not available".
 *
 * Mounted only while the Trace view is open, so a chat nobody inspects costs
 * no read.
 */

import { useMemo, useState } from "react";
import { Code, ConnectError } from "@connectrpc/connect";
import { RefreshCw } from "lucide-react";
import { timestampDate } from "@bufbuild/protobuf/wkt";

import Button from "@/components/forge-ui/button";
import { TraceWaterfall } from "@/components/Forge/Observability/waterfall/TraceWaterfall";
import { formatDuration } from "@/components/Forge/Observability/format";
import { ChatTracePartialReason, type GetChatTraceResponse } from "@/gen/reliant/v1/chat_trace_pb";
import { useGetChatTrace } from "@/hooks/chat-trace-service-hooks_gen";
import type { Span } from "@/services/controlPlane/observability/types";

import { ChatModelCallsTable } from "./ChatModelCallsTable";
import { ChatTraceCostSummary } from "./ChatTraceCostSummary";
import { ChatTracePartialBanner } from "./ChatTracePartialBanner";
import { ATTR, formatTokenCount, isEmptyTrace, partialReasons, spanModel, toWaterfallSpan } from "./chatTraceModel";

function isNotFound(error: unknown): boolean {
  return error instanceof ConnectError && (error.code === Code.NotFound || error.code === Code.PermissionDenied);
}

/** Retries after a failed read (an outage, not a refusal); tests shrink the delay. */
export const chatTraceRetry = {
  attempts: 2,
  delayMs: (attempt: number) => Math.min(1000 * 2 ** attempt, 8000),
};

export function ChatTracePanel({ chatId, viewerChat }: { chatId: string; viewerChat: boolean }) {
  const query = useGetChatTrace(
    { chatId },
    {
      staleTime: 30_000,
      // NotFound is an answer (the caller may not read this chat), not an outage.
      retry: (count, error) => !isNotFound(error) && count < chatTraceRetry.attempts,
      retryDelay: (attempt) => chatTraceRetry.delayMs(attempt),
    },
  );

  return (
    <div className="forge-ui min-h-0 flex-1 overflow-y-auto" data-testid="chat-trace-panel">
      <div className="mx-auto max-w-[var(--page-max)] space-y-6 px-4 py-5 sm:px-6">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h2 className="text-section text-foreground">Trace and cost</h2>
          <div className="flex items-center gap-2">
            {query.data?.asOf && (
              <span className="text-meta text-muted-foreground" data-testid="chat-trace-as-of">
                As of {timestampDate(query.data.asOf).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}
              </span>
            )}
            {!query.isLoading && !isNotFound(query.error) && (
              <Button
                variant="ghost"
                size="sm"
                onClick={() => void query.refetch()}
                disabled={query.isFetching}
                leftIcon={<RefreshCw className={query.isFetching ? "animate-spin" : undefined} />}
              >
                Refresh
              </Button>
            )}
          </div>
        </div>

        {query.isLoading ? (
          <p className="text-ui text-muted-foreground" role="status" data-testid="chat-trace-loading">
            Loading this chat's trace…
          </p>
        ) : query.error ? (
          isNotFound(query.error) ? (
            <p className="text-ui text-muted-foreground" role="note" data-testid="chat-trace-unavailable">
              This chat's trace isn't available to you.
            </p>
          ) : (
            <div
              role="alert"
              className="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-border bg-card p-4 text-ui"
              data-testid="chat-trace-error"
            >
              <span className="text-foreground">Couldn't load this chat's trace. Your chat is unaffected.</span>
              <Button variant="outline" size="sm" onClick={() => void query.refetch()}>
                Try again
              </Button>
            </div>
          )
        ) : query.data ? (
          <ChatTraceReport data={query.data} viewer={viewerChat || query.data.viewer} />
        ) : null}
      </div>
    </div>
  );
}

export function ChatTraceReport({ data, viewer }: { data: GetChatTraceResponse; viewer: boolean }) {
  const reasons = partialReasons(data);
  const spans = useMemo(() => data.spans.map((span) => toWaterfallSpan(span, viewer)), [data.spans, viewer]);
  const [selectedSpanId, setSelectedSpanId] = useState<string | undefined>(undefined);
  const selected = spans.find((span) => span.spanId === selectedSpanId);

  if (isEmptyTrace(data) && reasons.length === 0) {
    return (
      <div className="rounded-xl border border-border bg-card px-4 py-8 text-center" data-testid="chat-trace-empty">
        <p className="text-ui font-medium text-foreground">No model calls yet</p>
        <p className="mt-1 text-ui text-muted-foreground">
          When an agent works in this chat, each step's timing and each model call appear here.
        </p>
      </div>
    );
  }

  const spansUnreadable = data.partialReasons.some(
    (reason) =>
      reason === ChatTracePartialReason.SPANS_UNAVAILABLE ||
      reason === ChatTracePartialReason.TRACE_EXPIRED ||
      reason === ChatTracePartialReason.SPANS_MISSING,
  );
  const spansTruncated = data.partialReasons.includes(ChatTracePartialReason.SPANS_TRUNCATED);

  return (
    <div className="space-y-6" data-testid="chat-trace-report" data-viewer={viewer ? "true" : "false"}>
      <ChatTracePartialBanner reasons={reasons} />
      <ChatTraceCostSummary totals={data.totals} viewer={viewer} />

      <section aria-labelledby="chat-trace-timing" className="space-y-3">
        <h3 id="chat-trace-timing" className="text-ui font-medium text-foreground">
          Timing
        </h3>
        {spans.length > 0 ? (
          <>
            <TraceWaterfall spans={spans} truncated={spansTruncated} selectedSpanId={selectedSpanId} onSelect={setSelectedSpanId} />
            {selected && <SpanFacts span={selected} viewer={viewer} />}
          </>
        ) : (
          <p className="text-ui text-muted-foreground" data-testid="chat-trace-no-spans">
            {spansUnreadable ? "No timing to draw for this window." : "No steps were recorded in this window."}
          </p>
        )}
      </section>

      <section aria-labelledby="chat-trace-calls-heading" className="space-y-3">
        <h3 id="chat-trace-calls-heading" className="text-ui font-medium text-foreground">
          Model calls
        </h3>
        <ChatModelCallsTable calls={data.modelCalls} viewer={viewer} />
      </section>
    </div>
  );
}

/**
 * The selected span's facts: what it was, how long, how it ended, and for a
 * model call its model and tokens. The owner also sees its reported cost; a
 * viewer never does.
 */
function SpanFacts({ span, viewer }: { span: Span; viewer: boolean }) {
  const model = spanModel(span);
  const tool = span.attributes[ATTR.tool];
  const input = span.attributes[ATTR.inputTokens];
  const output = span.attributes[ATTR.outputTokens];
  const cost = viewer ? undefined : span.attributes[ATTR.cost];
  const facts: { label: string; value: string }[] = [
    { label: "Step", value: span.name },
    { label: "Service", value: span.service || "—" },
    { label: "Duration", value: formatDuration(span.durationMs) },
    { label: "Status", value: span.status === "error" ? "Failed" : span.status === "ok" ? "Succeeded" : "Not set" },
  ];
  if (model) facts.push({ label: "Model", value: model });
  if (tool) facts.push({ label: "Tool", value: tool });
  if (input) facts.push({ label: "Input tokens", value: formatTokenCount(Number(input)) });
  if (output) facts.push({ label: "Output tokens", value: formatTokenCount(Number(output)) });
  if (cost && Number.isFinite(Number(cost))) {
    facts.push({ label: "Reported cost", value: `$${Number(cost).toFixed(4)}` });
  }
  return (
    <dl
      className="grid grid-cols-1 gap-x-6 gap-y-2 rounded-xl border border-border bg-card p-4 sm:grid-cols-2 lg:grid-cols-4"
      data-testid="chat-trace-span-facts"
    >
      {facts.map((fact) => (
        <div key={fact.label} className="min-w-0">
          <dt className="text-meta text-muted-foreground">{fact.label}</dt>
          <dd className="truncate text-ui text-foreground" title={fact.value}>
            {fact.value}
          </dd>
        </div>
      ))}
    </dl>
  );
}
