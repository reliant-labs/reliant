// Copyright (c) 2025 Reliant Labs

/**
 * A step's Run tab: what this step did in the test run. Its resolved inputs
 * (what the {{ }} expressions evaluated to), its output, its error with the
 * full detail, how long it took, how many attempts it needed, the tool calls
 * an agent made, and what ran inside it (an Agent step's turns, a loop's
 * iterations). Long values are cut, with Show all and Copy.
 */

import { useState } from "react";
import { Link } from "@tanstack/react-router";
import { Check, ChevronDown, ChevronRight, Copy } from "lucide-react";

import { cn } from "../../../lib/utils";
import type { StepRecord } from "../../../api/step-executions-grpc";
import { humanizeField } from "../workflowFindings";
import { runErrorFieldKey, type RunNodeStatus } from "./builderRun";
import { useStepRun, type StepRun } from "./BuilderRunContext";

const STATUS_TEXT: Record<RunNodeStatus, string> = {
  running: "Running",
  completed: "Done",
  failed: "Failed",
  skipped: "Skipped",
  waiting: "Waiting for you",
};

const STATUS_CLASS: Record<RunNodeStatus, string> = {
  running: "border-info/40 bg-info/10 text-info",
  completed: "border-success/40 bg-success/10 text-success-ink",
  failed: "border-destructive/40 bg-destructive/10 text-destructive-ink",
  skipped: "border-border bg-background text-muted-foreground",
  waiting: "border-warning/50 bg-warning/10 text-foreground",
};

/** A value cut to this many characters (or lines) until Show all. */
const PREVIEW_CHARS = 600;
const PREVIEW_LINES = 14;

function formatDuration(ms: number | undefined): string | undefined {
  if (ms === undefined) return undefined;
  if (ms < 1000) return `${ms} ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)} s`;
  return `${(ms / 60_000).toFixed(1)} min`;
}

function valueText(value: unknown): string {
  if (typeof value === "string") return value;
  return JSON.stringify(value, null, 2) ?? String(value);
}

function CopyValueButton({ text, label }: { text: string; label: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      className="inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-xs text-muted-foreground hover:bg-muted hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
      aria-label={`Copy ${label}`}
      onClick={() => {
        void navigator.clipboard?.writeText(text);
        setCopied(true);
        setTimeout(() => setCopied(false), 1500);
      }}
    >
      {copied ? <Check className="h-3 w-3" aria-hidden /> : <Copy className="h-3 w-3" aria-hidden />}
      {copied ? "Copied" : "Copy"}
    </button>
  );
}

/** A value from the run: JSON pretty-printed, long ones cut until Show all. */
export function ValueView({ value, label }: { value: unknown; label: string }) {
  const [expanded, setExpanded] = useState(false);
  const text = valueText(value);
  const lines = text.split("\n");
  const long = text.length > PREVIEW_CHARS || lines.length > PREVIEW_LINES;
  const shown = expanded || !long ? text : `${lines.slice(0, PREVIEW_LINES).join("\n").slice(0, PREVIEW_CHARS)}…`;
  return (
    <div className="rounded-md border border-border/60 bg-background">
      <pre
        data-testid="run-value"
        className={cn(
          "overflow-auto whitespace-pre-wrap break-words px-2.5 py-2 font-mono text-xs text-foreground",
          expanded && "max-h-[28rem]",
        )}
      >
        {shown}
      </pre>
      <div className="flex items-center justify-end gap-1 border-t border-border/60 px-1.5 py-1">
        {long && (
          <button
            type="button"
            className="rounded px-1.5 py-0.5 text-xs text-muted-foreground hover:bg-muted hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
            aria-expanded={expanded}
            onClick={() => setExpanded((open) => !open)}
          >
            {expanded ? "Show less" : `Show all (${text.length.toLocaleString()} characters)`}
          </button>
        )}
        <CopyValueButton text={text} label={label} />
      </div>
    </div>
  );
}

function Section({ title, children, aside }: { title: string; children: React.ReactNode; aside?: React.ReactNode }) {
  return (
    <section className="space-y-1.5">
      <div className="flex items-baseline justify-between gap-2">
        <h4 className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">{title}</h4>
        {aside}
      </div>
      {children}
    </section>
  );
}

interface ToolCall {
  name: string;
  args: unknown;
}

function parseMaybeJson(value: unknown): unknown {
  if (typeof value !== "string") return value;
  try {
    return JSON.parse(value);
  } catch {
    return value;
  }
}

/** The tool calls the step's model asked for: from call_llm outputs, else the calls execute_tools ran. */
export function toolCallsOf(records: readonly StepRecord[]): ToolCall[] {
  const read = (list: unknown): ToolCall[] =>
    Array.isArray(list)
      ? list.map((call) => {
          const item = (call ?? {}) as Record<string, unknown>;
          const fn = (item.function ?? {}) as Record<string, unknown>;
          return {
            name: String(item.name ?? fn.name ?? "tool"),
            args: parseMaybeJson(item.input ?? item.arguments ?? item.args ?? fn.arguments ?? {}),
          };
        })
      : [];
  const fromModel = records.flatMap((record) =>
    record.activityName === "CallLLM" ? read((record.output as Record<string, unknown> | undefined)?.tool_calls) : [],
  );
  if (fromModel.length > 0) return fromModel;
  return records.flatMap((record) =>
    record.activityName === "ExecuteTools"
      ? read((record.input as Record<string, unknown> | undefined)?.resolved_tool_calls)
      : [],
  );
}

function ErrorBlock({ message, fieldKey, onFocusField }: { message: string; fieldKey?: string; onFocusField: (key: string) => void }) {
  const [showDetails, setShowDetails] = useState(false);
  const [summary, ...rest] = message.split("\n");
  const hasDetails = rest.join("").trim().length > 0 || summary.length > 180;
  return (
    <div role="alert" className="space-y-2 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive-ink">
      <p className="break-words font-medium">{summary.length > 180 && !showDetails ? `${summary.slice(0, 180)}…` : summary}</p>
      <div className="flex flex-wrap items-center gap-2">
        {fieldKey && (
          <button
            type="button"
            onClick={() => onFocusField(fieldKey)}
            className="rounded border border-destructive/40 bg-background px-2 py-0.5 text-xs font-medium text-foreground hover:bg-muted focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
          >
            Go to {humanizeField(fieldKey)}
          </button>
        )}
        {hasDetails && (
          <button
            type="button"
            aria-expanded={showDetails}
            onClick={() => setShowDetails((open) => !open)}
            className="text-xs font-medium underline-offset-2 hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
          >
            {showDetails ? "Hide details" : "Show details"}
          </button>
        )}
      </div>
      {showDetails && <ValueView value={message} label="error details" />}
    </div>
  );
}

function recordStatus(record: StepRecord): RunNodeStatus {
  if (record.error || record.success === false) return "failed";
  return "completed";
}

/** One execution's inputs, output and error. */
function RecordDetails({ record, onFocusField }: { record: StepRecord; onFocusField: (key: string) => void }) {
  return (
    <div className="space-y-3">
      {record.error && <ErrorBlock message={record.error} fieldKey={runErrorFieldKey(record.error)} onFocusField={onFocusField} />}
      <Section title="Inputs (resolved)">
        {record.input !== undefined ? (
          <ValueView value={record.input} label="inputs" />
        ) : (
          <p className="text-xs text-muted-foreground">No inputs were recorded for this step.</p>
        )}
      </Section>
      <Section title="Output">
        {record.output !== undefined ? (
          <ValueView value={record.output} label="output" />
        ) : (
          <p className="text-xs text-muted-foreground">{record.error ? "It failed before producing output." : "No output."}</p>
        )}
      </Section>
    </div>
  );
}

function InnerStep({ record, onFocusField }: { record: StepRecord; onFocusField: (key: string) => void }) {
  const [open, setOpen] = useState(false);
  const status = recordStatus(record);
  const name = record.nodePath || record.stepId;
  return (
    <li className="rounded-md border border-border/60 bg-background">
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
        className="flex w-full items-center gap-2 px-2.5 py-1.5 text-left text-xs hover:bg-muted focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
      >
        {open ? <ChevronDown className="h-3 w-3 flex-shrink-0" aria-hidden /> : <ChevronRight className="h-3 w-3 flex-shrink-0" aria-hidden />}
        <code className="min-w-0 flex-1 truncate font-mono">{name}</code>
        {record.loopIteration !== undefined && <span className="text-muted-foreground">#{record.loopIteration + 1}</span>}
        <span className={cn("rounded-full border px-1.5 text-xs", STATUS_CLASS[status])}>{STATUS_TEXT[status]}</span>
        <span className="text-muted-foreground">{formatDuration(record.durationMs)}</span>
      </button>
      {open && (
        <div className="border-t border-border/60 p-2.5">
          <RecordDetails record={record} onFocusField={onFocusField} />
        </div>
      )}
    </li>
  );
}

function StepRunBody({ stepRun }: { stepRun: StepRun }) {
  const { state, own, inner } = stepRun;
  const [attemptIndex, setAttemptIndex] = useState<number | null>(null);
  const selected = own.length > 0 ? own[attemptIndex ?? own.length - 1] : undefined;
  const status: RunNodeStatus | undefined = state?.status ?? (selected ? recordStatus(selected) : undefined);
  const error = selected?.error ?? state?.error;
  const toolCalls = toolCallsOf(own.length > 0 && own.some((r) => r.activityName === "CallLLM") ? own : inner);
  const duration =
    selected?.durationMs ??
    (inner.length > 0
      ? inner[inner.length - 1]!.createdAtMs + (inner[inner.length - 1]!.durationMs ?? 0) - inner[0]!.createdAtMs
      : undefined);
  const loop = state?.loop;

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2 text-xs" data-testid="step-run-summary">
        {status && (
          <span className={cn("rounded-full border px-2 py-0.5 font-medium", STATUS_CLASS[status])}>{STATUS_TEXT[status]}</span>
        )}
        {duration !== undefined && duration > 0 && <span className="text-muted-foreground">{formatDuration(duration)}</span>}
        {own.length > 1 && (
          <span className="text-muted-foreground">
            {own.length} attempts
          </span>
        )}
        {loop && (
          <span className="text-muted-foreground">
            {loop.currentIteration !== undefined
              ? `Iteration ${loop.currentIteration + 1}`
              : `${loop.completedIterations} iteration${loop.completedIterations === 1 ? "" : "s"}`}
          </span>
        )}
        {selected && <span className="text-muted-foreground">{new Date(selected.createdAtMs).toLocaleTimeString()}</span>}
      </div>

      {own.length > 1 && (
        <div className="flex flex-wrap items-center gap-1" role="group" aria-label="Attempts">
          {own.map((record, index) => (
            <button
              key={record.id}
              type="button"
              aria-pressed={record === selected}
              onClick={() => setAttemptIndex(index)}
              className={cn(
                "rounded border px-2 py-0.5 text-xs focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40",
                record === selected ? "border-foreground/50 bg-muted text-foreground" : "border-border text-muted-foreground hover:bg-muted",
              )}
            >
              Attempt {record.attempt || index + 1}
              {recordStatus(record) === "failed" ? " · failed" : ""}
            </button>
          ))}
        </div>
      )}

      {status === "waiting" && (
        <p className="rounded-md border border-warning/50 bg-warning/10 px-3 py-2 text-sm text-foreground">
          This step is waiting for a person. Answer it in the run to let the workflow continue.
        </p>
      )}
      {status === "skipped" && (
        <p className="text-sm text-muted-foreground">Its condition was false, so this step did not run.</p>
      )}

      {selected ? (
        <RecordDetails record={selected} onFocusField={stepRun.focusField} />
      ) : (
        error && <ErrorBlock message={error} fieldKey={runErrorFieldKey(error)} onFocusField={stepRun.focusField} />
      )}

      {toolCalls.length > 0 && (
        <Section title={`Tool calls (${toolCalls.length})`}>
          <ul className="space-y-1.5">
            {toolCalls.map((call, index) => (
              <li key={index} className="space-y-1">
                <code className="font-mono text-xs text-foreground">{call.name}</code>
                <ValueView value={call.args} label={`${call.name} arguments`} />
              </li>
            ))}
          </ul>
        </Section>
      )}

      {inner.length > 0 && (
        <Section title={`Steps inside (${inner.length})`}>
          <ul className="space-y-1">
            {inner.map((record) => (
              <InnerStep key={record.id} record={record} onFocusField={stepRun.focusField} />
            ))}
          </ul>
        </Section>
      )}

      {!selected && inner.length === 0 && !error && status !== "skipped" && status !== "waiting" && (
        <p className="text-sm text-muted-foreground">
          {stepRun.recordsLoading || status === "running" ? "Waiting for this step's record…" : "Nothing was recorded for this step."}
        </p>
      )}
      {stepRun.truncated && (
        <p className="text-xs text-muted-foreground">This run is long; only its newest steps are shown here.</p>
      )}

      <Link
        to="/workflows/runs/$runId"
        params={{ runId: stepRun.chatId }}
        className="inline-block text-sm font-medium text-foreground hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
      >
        Open the full run
      </Link>
    </div>
  );
}

/** The Run tab's content for one step. */
export function StepRunPanel({ nodeId }: { nodeId: string }) {
  const stepRun = useStepRun(nodeId);
  if (!stepRun) {
    return <p className="cpv2-section text-sm text-muted-foreground">This step has not run in the test run.</p>;
  }
  return (
    <div className="cpv2-section" data-testid="step-run-panel">
      <StepRunBody stepRun={stepRun} />
    </div>
  );
}

/** On a step's other tabs: it failed in the test run, and why. */
export function StepRunFailureBanner({ nodeId, onShowRun }: { nodeId: string; onShowRun: () => void }) {
  const stepRun = useStepRun(nodeId);
  if (stepRun?.state?.status !== "failed") return null;
  const error = stepRun.state.error ?? stepRun.own[stepRun.own.length - 1]?.error;
  return (
    <div className="cpv2-section" data-testid="step-run-failure">
      <div role="status" className="cpv2-step-problems cpv2-step-problems--error">
        <p className="font-medium">This step failed in the test run</p>
        {error && <p className="mt-1 line-clamp-3 break-words">{error}</p>}
        <button type="button" onClick={onShowRun} className="mt-1 text-xs font-medium underline underline-offset-2">
          See what it was given
        </button>
      </div>
    </div>
  );
}
