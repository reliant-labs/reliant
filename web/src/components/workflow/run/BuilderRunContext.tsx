// Copyright (c) 2025 Reliant Labs

/**
 * The builder's test run, for the parts of the builder that show it beside
 * the thing it is about: a step's Run tab (what this step was given, produced
 * and failed with) and the sample value next to each field in the Outputs tab
 * and the Insert data picker.
 *
 * The run's record — every step's resolved inputs, output and error — is
 * read once for the whole run (ListStepExecutions) and fetched again as steps
 * settle. A test run is small, and one read serves every panel. Without a
 * provider every lookup is empty, which keeps the shared panels unchanged
 * everywhere else they are used.
 */

import { createContext, useContext, useMemo, type ReactNode } from "react";
import { keepPreviousData, useQuery } from "@tanstack/react-query";

import { listStepExecutions, type StepRecord } from "../../../api/step-executions-grpc";
import type { BuilderRun } from "../hooks/useBuilderTestRun";
import type { RunNodeState } from "./builderRun";

/** "Show me this step's run": open its Run tab. Bumped per request. */
export interface RunFocus {
  nodeId: string;
  seq: number;
}

interface BuilderRunContextValue {
  run: BuilderRun;
  records: StepRecord[];
  recordsLoading: boolean;
  /** Older steps were left out of the record to bound it. */
  truncated: boolean;
  /** The run's inputs, as it was started with them. */
  inputs: Record<string, unknown>;
  focus: RunFocus | null;
  /** Select a field of a step and focus it (a run error that names a field). */
  focusField: (nodeId: string, fieldKey: string) => void;
}

const BuilderRunContext = createContext<BuilderRunContextValue | null>(null);

export function BuilderRunProvider({
  run,
  inputs,
  focus,
  focusField,
  children,
}: {
  run: BuilderRun | null;
  inputs: Record<string, unknown>;
  focus: RunFocus | null;
  focusField: (nodeId: string, fieldKey: string) => void;
  children: ReactNode;
}) {
  const chatId = run?.chatId ?? null;
  const query = useQuery({
    queryKey: ["builder-run-records", chatId, run?.settledVersion ?? 0],
    queryFn: () => listStepExecutions(chatId!),
    enabled: !!chatId,
    // Keep showing the last record while the next one loads, so the Run tab
    // does not blank out each time a step settles — but never a previous
    // run's record against this one.
    placeholderData: (previous, previousQuery) =>
      previousQuery?.queryKey[1] === chatId ? keepPreviousData(previous) : undefined,
  });
  const current = query.data;
  const value = useMemo<BuilderRunContextValue | null>(
    () =>
      run
        ? {
            run,
            records: current?.records ?? [],
            recordsLoading: query.isLoading,
            truncated: current?.truncated ?? false,
            inputs,
            focus,
            focusField,
          }
        : null,
    [run, current, query.isLoading, inputs, focus, focusField],
  );
  return <BuilderRunContext.Provider value={value}>{children}</BuilderRunContext.Provider>;
}

/** Whether a record belongs to a step: the step itself, or something it ran (its path below it). */
export function recordIsWithin(record: StepRecord, nodeId: string): boolean {
  if (record.nodePath) return record.nodePath === nodeId || record.nodePath.startsWith(`${nodeId}.`);
  return record.stepId === nodeId || record.stepId === `${nodeId}-save`;
}

/** Whether a record is the step's own execution, not something it ran or its message save. */
export function recordIsOwn(record: StepRecord, nodeId: string): boolean {
  if (record.activityName === "SaveMessage") return false;
  return record.nodePath ? record.nodePath === nodeId : record.stepId === nodeId;
}

export interface StepRun {
  chatId: string;
  state: RunNodeState | undefined;
  /** The step's own attempts, oldest first. */
  own: StepRecord[];
  /** What ran inside the step (an Agent step's turns, a loop's body), oldest first. */
  inner: StepRecord[];
  recordsLoading: boolean;
  truncated: boolean;
  runLive: boolean;
  focus: RunFocus | null;
  focusField: (fieldKey: string) => void;
}

/** A step's part of the test run; null when there is no run, or the step had no part in it. */
export function useStepRun(nodeId: string | undefined): StepRun | null {
  const ctx = useContext(BuilderRunContext);
  return useMemo(() => {
    if (!ctx || !nodeId) return null;
    const state = ctx.run.view.nodes[nodeId];
    const within = ctx.records.filter((record) => recordIsWithin(record, nodeId) && record.activityName !== "SaveMessage");
    if (!state && within.length === 0) return null;
    const oldestFirst = [...within].sort((a, b) => a.createdAtMs - b.createdAtMs);
    return {
      chatId: ctx.run.chatId,
      state,
      own: oldestFirst.filter((record) => recordIsOwn(record, nodeId)),
      inner: oldestFirst.filter((record) => !recordIsOwn(record, nodeId)),
      recordsLoading: ctx.recordsLoading,
      truncated: ctx.truncated,
      runLive: !ctx.run.view.ended,
      focus: ctx.focus && ctx.focus.nodeId === nodeId ? ctx.focus : null,
      focusField: (fieldKey: string) => ctx.focusField(nodeId, fieldKey),
    };
  }, [ctx, nodeId]);
}

const PATH_SEGMENT = /[^.[\]]+|\[(\d+)\]/g;

/** `nodes.x.tool_calls[0].name` → ["nodes", "x", "tool_calls", 0, "name"]. */
function pathSegments(path: string): Array<string | number> {
  const segments: Array<string | number> = [];
  for (const match of path.matchAll(PATH_SEGMENT)) {
    segments.push(match[1] !== undefined ? Number(match[1]) : match[0]);
  }
  return segments;
}

function lookup(value: unknown, segments: Array<string | number>): unknown {
  let current = value;
  for (const segment of segments) {
    if (current === null || current === undefined) return undefined;
    if (typeof segment === "number") {
      if (!Array.isArray(current)) return undefined;
      current = current[segment];
    } else {
      if (typeof current !== "object" || Array.isArray(current)) return undefined;
      current = (current as Record<string, unknown>)[segment];
    }
  }
  return current;
}

/**
 * The value a CEL path had in the test run: `nodes.<id>.…` from that step's
 * newest successful output, `inputs.…` from what the run was started with.
 * Undefined when there is no run, or the run never produced it.
 */
export function sampleValue(
  path: string,
  records: readonly StepRecord[],
  inputs: Record<string, unknown>,
): unknown {
  const [root, ...rest] = pathSegments(path);
  if (root === "inputs") return rest.length > 0 ? lookup(inputs, rest) : undefined;
  if (root !== "nodes" || typeof rest[0] !== "string") return undefined;
  const nodeId = rest[0];
  let newest: StepRecord | undefined;
  for (const record of records) {
    if (!recordIsOwn(record, nodeId) || record.success === false || record.output === undefined) continue;
    if (!newest || record.createdAtMs > newest.createdAtMs) newest = record;
  }
  return newest ? lookup(newest.output, rest.slice(1)) : undefined;
}

/** A field's value in the test run, for showing beside it. */
export function useRunSample(path: string): unknown {
  const ctx = useContext(BuilderRunContext);
  return useMemo(() => (ctx ? sampleValue(path, ctx.records, ctx.inputs) : undefined), [ctx, path]);
}

/** A sample value in one line, for beside a field. */
export function formatSample(value: unknown, max = 60): string {
  const text = JSON.stringify(value) ?? String(value);
  return text.length > max ? `${text.slice(0, max - 1)}…` : text;
}

/** "In the test run: …" beside a field whose value the test run produced; nothing otherwise. */
export function RunSample({ path, className }: { path: string; className?: string }) {
  const value = useRunSample(path);
  if (value === undefined) return null;
  const full = JSON.stringify(value, null, 2) ?? String(value);
  return (
    <span data-testid="run-sample" title={full} className={className ?? "block truncate text-xs text-muted-foreground"}>
      In the test run: <code className="font-mono text-foreground/80">{formatSample(value)}</code>
    </span>
  );
}
