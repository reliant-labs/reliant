// Copyright (c) 2025 Reliant Labs
import { useGetRun } from "@/hooks/run-service-hooks_gen";
import type { RunNodeExecution } from "@/gen/reliant/v1/run_pb";
import { WorkflowState, WorkflowStopReason } from "@/gen/reliant/v1/chat_pb";
import Button from "@/components/forge-ui/button";

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
            </div>}
            <RunNodeList nodes={query.data?.nodes ?? []} truncated={query.data?.nodesTruncated ?? false} failed={failure !== ""} />
          </>}
      </div>
    </details>
  );
}

export function RunNodeList({ nodes, truncated, failed = false }: { nodes: RunNodeExecution[]; truncated: boolean; failed?: boolean }) {
  return <>
    {nodes.length === 0 && <p className="text-sm text-muted-foreground">{failed ? "No node ran: the run failed before its first node." : "No recorded node executions yet."}</p>}
    <ul className="divide-y divide-border/60" aria-label="Node attempts (newest first)">
      {nodes.map((node) => <li key={node.id} className="py-2 text-sm">
        <details open={node.status === "failed"}>
          <summary className="cursor-pointer break-words text-foreground">
            <span className="font-medium">{node.nodePath || node.nodeId}</span>{" — "}
            <span className={node.status === "failed" ? "text-destructive" : "text-muted-foreground"}>{node.status}</span>
            {node.durationMs !== undefined && <span className="ml-2 text-muted-foreground">{String(node.durationMs)} ms</span>}
            {node.exitCode !== undefined && <span className="ml-2 text-muted-foreground">exit {node.exitCode}</span>}
          </summary>
          <p className="mt-1 break-all text-xs text-muted-foreground">Workflow {node.workflowId} · attempt {node.attempt}{node.loopIteration !== undefined ? ` · iteration ${node.loopIteration}` : ""}</p>
          {node.startedAt && <p className="text-xs text-muted-foreground">{node.startedAt} — {node.completedAt}</p>}
          {node.error && <pre className="mt-2 whitespace-pre-wrap break-words text-destructive">{node.error}</pre>}
          {[["stdout", node.stdoutTail], ["stderr", node.stderrTail]].map(([label, text]) => text ? <div key={label} className="mt-2">
            <h4 className="text-xs font-medium text-muted-foreground">{label} (last 50 lines, bounded)</h4>
            <pre className="mt-1 overflow-auto whitespace-pre-wrap break-words rounded border border-border/60 bg-background p-3 text-xs text-foreground">{text}</pre>
          </div> : null)}
        </details>
      </li>)}
    </ul>
    {truncated && <p className="mt-2 text-xs text-muted-foreground">Older node attempts omitted (response limit).</p>}
  </>;
}
