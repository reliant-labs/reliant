/**
 * Renderer for the start_run and get_run tools.
 *
 * Both name a top-level run the agent started or read. Before this renderer
 * the run id appeared only inside the tool's text output, and a run an agent
 * started had no other way in from the UI (WORKFLOW_UI.md L1). The renderer's
 * job is to turn that id into a link to the run's page.
 *
 * The id is the run's chat id — the tools document `run_id` as "the run id
 * (chat id)" — which is exactly what /runs/$runId takes. It is read from the
 * result metadata once the call has finished, and for get_run from the input
 * before then, so the link exists while the call is still in flight.
 */

import { memo } from "react";
import { Link } from "@tanstack/react-router";
import { ArrowRight, Bot } from "lucide-react";

import type { ToolContentProps } from "./types";
import { formatErrorMessage } from "../../../lib/utils";

interface RunToolMetadata {
  run_id?: string;
  chat_id?: string;
  already_started?: boolean;
  title?: string;
  state?: string;
  workflow?: string;
}

function parseMetadata(metadata?: string): RunToolMetadata {
  if (!metadata) return {};
  try {
    const parsed: unknown = JSON.parse(metadata);
    return parsed && typeof parsed === "object" ? (parsed as RunToolMetadata) : {};
  } catch {
    return {};
  }
}

function stringField(input: unknown, key: string): string | undefined {
  if (!input || typeof input !== "object") return undefined;
  const value = (input as Record<string, unknown>)[key];
  return typeof value === "string" && value !== "" ? value : undefined;
}

function RunToolRendererComponent({ ctx }: ToolContentProps) {
  const { input, result } = ctx;
  const isStart = ctx.toolName.toLowerCase().endsWith("start_run");
  const metadata = parseMetadata(result?.metadata);
  const isError = result?.is_error ?? false;

  const runId =
    (!isError && (metadata.chat_id || metadata.run_id)) || (isStart ? undefined : stringField(input, "run_id"));
  const title = metadata.title || stringField(input, "title");
  const workflow = metadata.workflow || stringField(input, "workflow");

  return (
    <div className="tool-content-run px-2 py-1.5 text-xs" data-testid="run-tool-renderer">
      <div className="flex min-w-0 items-center gap-2">
        <Bot className="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
        <span className="min-w-0 truncate">
          {title ? (
            <span className="font-medium text-foreground">{title}</span>
          ) : (
            <span className="text-muted-foreground">{isStart ? "New run" : "Run"}</span>
          )}
          {workflow && <span className="text-muted-foreground"> · {workflow}</span>}
          {metadata.state && (
            <>
              <span className="text-muted-foreground"> · </span>
              <span className="text-muted-foreground">{metadata.state}</span>
            </>
          )}
        </span>
        {runId && (
          <Link
            to="/workflows/runs/$runId"
            params={{ runId }}
            className="ml-auto inline-flex shrink-0 items-center gap-1 rounded-sm font-medium text-primary hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
          >
            Open run
            <ArrowRight className="h-3 w-3" aria-hidden="true" />
          </Link>
        )}
      </div>
      {isStart && metadata.already_started && (
        <p className="mt-1 text-muted-foreground">
          This run was already started by an earlier attempt; nothing new was started.
        </p>
      )}
      {isError && result?.content && (
        <p className="mt-1 text-warning-ink">{formatErrorMessage(result.content)}</p>
      )}
    </div>
  );
}

export const RunToolRenderer = memo(RunToolRendererComponent);
