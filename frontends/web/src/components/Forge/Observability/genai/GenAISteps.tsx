// Copyright (c) 2026 Reliant Labs

/**
 * The GenAI lens: a trace's agent, model and tool steps as a transcript-style
 * outline (genaiSteps.ts), each with its latency against the trace, tokens,
 * time to first token and cost. Selecting a step selects its span; the
 * adjacent panel hides raw attributes, events and status text in this lens.
 *
 * Metadata only: what the spans carry. Prompts, completions and tool
 * arguments are a later, opt-in capture with its own access grant; the lens
 * says so rather than leaving a reader to wonder where they are.
 */

import { useMemo } from "react";
import {
  Bot,
  Hammer,
  MessageSquare,
  Workflow,
  GitBranch,
  Circle,
} from "lucide-react";

import { cn } from "@/lib/utils";
import type { Span } from "@/services/controlPlane/observability/types";

import { formatCount, formatDuration } from "../format";
import { traceExtent } from "../waterfall/traceTree";
import {
  buildGenAITrace,
  formatUsd,
  type Cost,
  type Step,
  type StepKind,
  type StepTotals,
} from "./genaiSteps";

const KIND_ICON: Record<StepKind, typeof Bot> = {
  workflow: Workflow,
  node: GitBranch,
  agent: Bot,
  model: MessageSquare,
  tool: Hammer,
  other: Circle,
};

const KIND_LABEL: Record<StepKind, string> = {
  workflow: "Workflow",
  node: "Node",
  agent: "Agent",
  model: "Model call",
  tool: "Tool call",
  other: "AI step",
};

function CostText({ cost }: { cost: Cost | undefined }) {
  if (!cost) return null;
  return (
    <span
      className="tabular-nums"
      title={
        cost.source === "reported"
          ? "Cost the provider or gateway reported"
          : `Cost (${cost.source})`
      }
    >
      {formatUsd(cost.usd)}
      {cost.source !== "reported" && (
        <span className="ml-0.5 text-ink-muted">
          {cost.source === "estimated" ? " est." : " incl. est."}
        </span>
      )}
    </span>
  );
}

function Totals({
  totals,
  durationMs,
}: {
  totals: StepTotals;
  durationMs?: number;
}) {
  const tokens = totals.tokens.input + totals.tokens.output;
  return (
    <dl
      className="flex flex-wrap gap-x-4 gap-y-1 text-xs"
      data-testid="genai-totals"
    >
      {durationMs !== undefined && (
        <div className="flex gap-1">
          <dt className="text-ink-muted">Duration</dt>
          <dd className="tabular-nums text-ink">
            {formatDuration(durationMs)}
          </dd>
        </div>
      )}
      <div className="flex gap-1">
        <dt className="text-ink-muted">Model calls</dt>
        <dd className="tabular-nums text-ink">
          {formatCount(totals.modelCalls)}
        </dd>
      </div>
      <div className="flex gap-1">
        <dt className="text-ink-muted">Tool calls</dt>
        <dd className="tabular-nums text-ink">
          {formatCount(totals.toolCalls)}
        </dd>
      </div>
      <div className="flex gap-1">
        <dt className="text-ink-muted">Tokens</dt>
        <dd
          className="tabular-nums text-ink"
          title={`${totals.tokens.input} in (${totals.tokens.cacheRead} from cache), ${totals.tokens.output} out`}
        >
          {formatCount(tokens)}
        </dd>
      </div>
      <div className="flex gap-1">
        <dt className="text-ink-muted">Cost</dt>
        <dd className="text-ink" data-testid="genai-total-cost">
          {totals.cost ? (
            <CostText cost={totals.cost} />
          ) : (
            <span className="text-ink-muted">not reported</span>
          )}
        </dd>
      </div>
      {totals.failed > 0 && (
        <div className="flex gap-1">
          <dt className="text-ink-muted">Failed</dt>
          <dd className="tabular-nums text-danger-ink">
            {formatCount(totals.failed)}
          </dd>
        </div>
      )}
    </dl>
  );
}

interface Row {
  step: Step;
  depth: number;
}

function flatten(steps: Step[], depth = 0, out: Row[] = []): Row[] {
  for (const step of steps) {
    out.push({ step, depth });
    flatten(step.children, depth + 1, out);
  }
  return out;
}

export function GenAISteps({
  spans,
  selectedSpanId,
  onSelect,
}: {
  spans: Span[];
  selectedSpanId: string | undefined;
  onSelect: (spanId: string) => void;
}) {
  const trace = useMemo(() => buildGenAITrace(spans), [spans]);
  const extent = useMemo(() => traceExtent(spans), [spans]);
  const rows = useMemo(() => flatten(trace.roots), [trace]);
  const origin = extent?.startMs ?? 0;
  const total = Math.max(extent?.totalDurationMs ?? 0, 1);

  return (
    <section
      aria-label="AI steps"
      className="space-y-3"
      data-testid="genai-steps"
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <Totals totals={trace.totals} durationMs={extent?.totalDurationMs} />
        <div className="flex flex-wrap gap-3 text-xs text-ink-muted">
          {trace.conversationId && (
            <span data-testid="genai-conversation">
              Conversation{" "}
              <span className="font-mono text-ink">{trace.conversationId}</span>
            </span>
          )}
          {trace.userId && (
            <span>
              User <span className="font-mono text-ink">{trace.userId}</span>
            </span>
          )}
          {trace.conventions.length > 0 && (
            <span title="forge.ai.convention: the SDK convention the spans arrived in">
              From {trace.conventions.join(", ")}
            </span>
          )}
        </div>
      </div>

      <ol
        role="tree"
        aria-label="AI steps"
        className="divide-y divide-border/60 rounded-lg border border-border"
      >
        {rows.map(({ step, depth }) => {
          const Icon = KIND_ICON[step.kind];
          const selected = step.span.spanId === selectedSpanId;
          const left = ((step.span.startMs - origin) / total) * 100;
          const width = Math.max((step.durationMs / total) * 100, 0.5);
          const tokens = step.tokens
            ? step.tokens.input + step.tokens.output
            : 0;
          return (
            <li
              key={step.id}
              role="treeitem"
              aria-level={depth + 1}
              aria-selected={selected}
            >
              <button
                type="button"
                onClick={() => onSelect(step.span.spanId)}
                data-testid="genai-step"
                data-kind={step.kind}
                className={cn(
                  "grid w-full grid-cols-[minmax(0,2fr)_minmax(6rem,1fr)] items-center gap-3 px-3 py-1.5 text-left text-xs hover:bg-surface-sunken focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                  selected ? "bg-surface-sunken" : "",
                )}
              >
                <span
                  className="flex min-w-0 items-center gap-2"
                  style={{ paddingLeft: `${depth * 0.875}rem` }}
                >
                  <Icon
                    className={cn(
                      "h-3.5 w-3.5 shrink-0",
                      step.failed ? "text-danger-ink" : "text-ink-muted",
                    )}
                    aria-label={KIND_LABEL[step.kind]}
                  />
                  <span
                    className={cn(
                      "truncate font-mono",
                      step.failed ? "text-danger-ink" : "text-ink",
                    )}
                    title={step.span.name}
                  >
                    {step.label}
                  </span>
                  {step.provider && (
                    <span className="shrink-0 text-ink-muted">
                      {step.provider}
                    </span>
                  )}
                  {step.failed && (
                    <span
                      className="shrink-0 rounded border border-danger-border px-1 text-danger-ink"
                      data-testid="genai-step-failed"
                    >
                      {step.errorType || "failed"}
                    </span>
                  )}
                  {!step.failed && step.errorType === "canceled" && (
                    <span className="shrink-0 text-ink-muted">cancelled</span>
                  )}
                  {step.retries !== undefined && (
                    <span className="shrink-0 text-ink-muted">
                      {step.retries} retries
                    </span>
                  )}
                </span>
                <span className="flex items-center justify-end gap-3 tabular-nums text-ink-muted">
                  {step.kind === "model" && tokens > 0 && (
                    <span
                      title={`${step.tokens!.input} in, ${step.tokens!.output} out`}
                      data-testid="genai-step-tokens"
                    >
                      {formatCount(tokens)} tok
                    </span>
                  )}
                  {step.ttftMs !== undefined && (
                    <span title="Time to first token">
                      TTFT {formatDuration(step.ttftMs)}
                    </span>
                  )}
                  {step.cost && <CostText cost={step.cost} />}
                  <span className="text-ink">
                    {formatDuration(step.durationMs)}
                  </span>
                </span>
                <span
                  className="relative col-span-2 h-1 rounded bg-surface-sunken"
                  aria-hidden="true"
                >
                  <span
                    className={cn(
                      "absolute inset-y-0 rounded",
                      step.failed ? "bg-danger" : "bg-primary/50",
                    )}
                    style={{
                      left: `${Math.min(left, 99.5)}%`,
                      width: `${Math.min(width, 100 - Math.min(left, 99.5))}%`,
                    }}
                  />
                </span>
              </button>
            </li>
          );
        })}
      </ol>

      <p className="text-xs text-ink-muted" data-testid="genai-no-content">
        Metadata from the spans: names, models, tokens, timings, cost and
        errors. Prompts, completions and tool arguments are never shown here;
        content capture is a separate, opt-in feature.
      </p>
    </section>
  );
}
