// Copyright (c) 2025 Reliant Labs
import { useGetRun } from "@/hooks/run-service-hooks_gen";
import type { RunNodeExecution } from "@/gen/reliant/v1/run_pb";
import type { NodeObservation, ObservedEvent } from "@/gen/reliant/v1/common_pb";
import type { JsonObject, JsonValue } from "@bufbuild/protobuf";
import { WorkflowState, WorkflowStopReason } from "@/gen/reliant/v1/chat_pb";
import Button from "@/components/forge-ui/button";
import { MissingConnectionLink } from "@/components/workflow/run/MissingConnectionLink";

/** Reads the same bounded projection get_run uses; never raw node inputs. */
export function RunNodes({ workflowId }: { workflowId: string }) {
  const query = useGetRun({ runId: workflowId }, {
    enabled: !!workflowId,
    refetchInterval: (query) => {
      const run = query.state.data?.run;
      return run?.state === WorkflowState.STOPPED && run.stopReason !== WorkflowStopReason.PAUSED ? false : 5000;
    },
  });
  // Why the run failed, as recorded on the run. A run that failed before any
  // node ran (input validation, a definition that would not load) has no node
  // attempt to show an error on, so this is the only place it is said.
  const failure = query.data?.run?.failureDetail ?? "";
  return (
    <details className="border-b border-border/60 bg-card px-6 py-3" open>
      <summary className="cursor-pointer text-sm font-medium text-foreground">Node executions</summary>
      <div className="mt-2 max-h-72 overflow-auto">
        {query.isLoading ? <p role="status" className="text-sm text-muted-foreground">Loading node executions…</p> :
          query.isError ? <div role="alert" className="text-sm text-destructive">Could not load node executions. <Button variant="outline" onClick={() => void query.refetch()}>Try again</Button></div> :
          <>
            {failure && <div role="alert" className="mb-2 text-sm">
              <h4 className="font-medium text-destructive">Why the run failed</h4>
              <pre className="mt-1 whitespace-pre-wrap break-words text-destructive">{failure}</pre>
              <MissingConnectionLink message={failure} className="mt-1 inline-block text-sm font-medium text-foreground underline-offset-2 hover:underline" />
            </div>}
            <RunNodeList nodes={query.data?.nodes ?? []} truncated={query.data?.nodesTruncated ?? false} failed={failure !== ""}
              observations={query.data?.observations ?? []} observationsTruncated={query.data?.observationsTruncated ?? false} />
          </>}
      </div>
    </details>
  );
}

/** Which node attempt an observation belongs to: same workflow, node, loop scope and attempt. */
function attemptKey(o: { workflowId: string; nodePath: string; nodeId: string; loopNodeId: string; loopIteration?: number; attempt: number }): string {
  return [o.workflowId, o.nodePath || o.nodeId, o.loopNodeId, o.loopIteration ?? "", o.attempt || 1].join("\u0000");
}

const timeOf = (iso: string) => {
  const t = Date.parse(iso);
  return Number.isNaN(t) ? 0 : t;
};

type RunEntry =
  | { kind: "node"; node: RunNodeExecution; observations: NodeObservation[]; at: number }
  | { kind: "observation"; observation: NodeObservation; at: number };

/**
 * One newest-first list: each node attempt with what it recorded, and the
 * observations of nodes the workflow completed itself (compute, join, router,
 * loop, workflow), which have no attempt row, as entries of their own.
 */
export function runEntries(nodes: RunNodeExecution[], observations: NodeObservation[]): RunEntry[] {
  const byAttempt = new Map<string, NodeObservation[]>();
  for (const o of observations) {
    const key = attemptKey(o);
    byAttempt.set(key, [...(byAttempt.get(key) ?? []), o]);
  }
  const entries: RunEntry[] = nodes.map((node) => {
    const key = attemptKey(node);
    const matched = byAttempt.get(key) ?? [];
    byAttempt.delete(key);
    return { kind: "node", node, observations: matched, at: timeOf(node.completedAt) };
  });
  for (const rest of byAttempt.values()) {
    for (const observation of rest) entries.push({ kind: "observation", observation, at: timeOf(observation.createdAt) });
  }
  // Stable: attempts keep the server's order among themselves.
  return entries.map((e, i) => ({ e, i })).sort((a, b) => b.e.at - a.e.at || a.i - b.i).map(({ e }) => e);
}

function text(value: JsonValue | undefined): string {
  if (value === undefined || value === null) return "";
  return typeof value === "string" ? value : JSON.stringify(value);
}

/** "name: value (reason)" for a decision event, "" for any other. */
export function decisionLine(event: ObservedEvent): string {
  if (event.name !== "decision") return "";
  const attrs: JsonObject = event.attributes ?? {};
  const name = text(attrs.name);
  const reason = text(attrs.reason);
  return `${name ? `${name}: ` : ""}${text(attrs.value)}${reason ? ` (${reason})` : ""}`;
}

function Pairs({ attrs }: { attrs: JsonObject }) {
  return <ul className="mt-1 space-y-0.5">
    {Object.entries(attrs).map(([k, v]) => <li key={k} className="break-words text-meta">
      <span className="text-muted-foreground">{k}</span> <span className="font-mono text-code text-foreground">{text(v)}</span>
    </li>)}
  </ul>;
}

/** What one node attempt recorded with observe:: decisions as one-liners, the rest behind "Observed". */
export function NodeObservations({ observations }: { observations: NodeObservation[] }) {
  if (observations.length === 0) return null;
  const decisions = observations.flatMap((o) => o.events.map(decisionLine).filter(Boolean));
  const others = observations.flatMap((o) => o.events.filter((e) => e.name !== "decision"));
  const attributes = Object.assign({}, ...observations.map((o) => o.attributes ?? {})) as JsonObject;
  const errors = observations.flatMap((o) => o.errors);
  const truncated = observations.some((o) => o.truncated);
  const hasMore = others.length > 0 || Object.keys(attributes).length > 0 || errors.length > 0;
  return <div className="mt-2">
    {decisions.length > 0 && <ul aria-label="Decisions" className="space-y-0.5">
      {decisions.map((line, i) => <li key={i} className="text-ui text-foreground">
        <span className="text-muted-foreground">Decided </span>{line}
      </li>)}
    </ul>}
    {hasMore && <details className="mt-1">
      <summary className="cursor-pointer text-meta font-medium text-muted-foreground">Observed</summary>
      {Object.keys(attributes).length > 0 && <Pairs attrs={attributes} />}
      {others.map((e, i) => <div key={i} className="mt-1">
        <p className="text-ui font-medium text-foreground">{e.name}</p>
        {e.attributes && <Pairs attrs={e.attributes} />}
      </div>)}
      {errors.map((msg, i) => <p key={i} className="mt-1 break-words text-meta text-muted-foreground">Expression failed (the node was unaffected): {msg}</p>)}
      {truncated && <p className="mt-1 text-meta text-muted-foreground">Cut to its size limit.</p>}
    </details>}
  </div>;
}

export function RunNodeList({ nodes, truncated, failed = false, observations = [], observationsTruncated = false }: {
  nodes: RunNodeExecution[]; truncated: boolean; failed?: boolean; observations?: NodeObservation[]; observationsTruncated?: boolean;
}) {
  const entries = runEntries(nodes, observations);
  return <>
    {entries.length === 0 && <p className="text-sm text-muted-foreground">{failed ? "No node ran: the run failed before its first node." : "No recorded node executions yet."}</p>}
    <ul className="divide-y divide-border/60" aria-label="Node attempts (newest first)">
      {entries.map((entry, i) => entry.kind === "observation" ? <li key={`obs-${i}-${attemptKey(entry.observation)}`} className="py-2 text-sm">
        <p className="break-words text-foreground"><span className="font-medium">{entry.observation.nodePath || entry.observation.nodeId}</span>{" — "}<span className="text-muted-foreground">recorded</span></p>
        <NodeObservations observations={[entry.observation]} />
      </li> : <li key={entry.node.id} className="py-2 text-sm">
        <NodeAttempt node={entry.node} observations={entry.observations} />
      </li>)}
    </ul>
    {truncated && <p className="mt-2 text-xs text-muted-foreground">Older node attempts omitted (response limit).</p>}
    {observationsTruncated && <p className="mt-2 text-meta text-muted-foreground">Older observations omitted (response limit).</p>}
  </>;
}

function NodeAttempt({ node, observations }: { node: RunNodeExecution; observations: NodeObservation[] }) {
  return <details open={node.status === "failed"}>
    <summary className="cursor-pointer break-words text-foreground">
      <span className="font-medium">{node.nodePath || node.nodeId}</span>{" — "}
      <span className={node.status === "failed" ? "text-destructive" : "text-muted-foreground"}>{node.status}</span>
      {node.durationMs !== undefined && <span className="ml-2 text-muted-foreground">{String(node.durationMs)} ms</span>}
      {node.exitCode !== undefined && <span className="ml-2 text-muted-foreground">exit {node.exitCode}</span>}
    </summary>
    <p className="mt-1 break-all text-xs text-muted-foreground">Workflow {node.workflowId} · attempt {node.attempt}{node.loopIteration !== undefined ? ` · iteration ${node.loopIteration}` : ""}</p>
    {node.startedAt && <p className="text-xs text-muted-foreground">{node.startedAt} — {node.completedAt}</p>}
    {node.error && <pre className="mt-2 whitespace-pre-wrap break-words text-destructive">{node.error}</pre>}
    <MissingConnectionLink message={node.error} className="mt-1 inline-block text-sm font-medium text-foreground underline-offset-2 hover:underline" />
    <NodeObservations observations={observations} />
    {[["stdout", node.stdoutTail], ["stderr", node.stderrTail]].map(([label, text]) => text ? <div key={label} className="mt-2">
      <h4 className="text-xs font-medium text-muted-foreground">{label} (last 50 lines, bounded)</h4>
      <pre className="mt-1 overflow-auto whitespace-pre-wrap break-words rounded border border-border/60 bg-background p-3 text-xs text-foreground">{text}</pre>
    </div> : null)}
  </details>;
}
