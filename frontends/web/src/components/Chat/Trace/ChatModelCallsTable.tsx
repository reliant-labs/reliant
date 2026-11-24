// Copyright (c) 2026 Reliant Labs

/**
 * One row per provider request of the chat (llm_call_usage): when, what for,
 * which model, how it ended, its tokens and timing, and, for the owner, its
 * cost and on what basis. A call has no prompt, reply or error text; a
 * failure is its closed error class only.
 *
 * A viewer's table has no cost column.
 */

import { timestampDate } from "@bufbuild/protobuf/wkt";

import DataTable, { type DataTableColumn } from "@/components/forge-ui/data_table";
import type { ChatModelCall } from "@/gen/reliant/v1/chat_trace_pb";
import { formatDuration } from "@/components/Forge/Observability/format";

import { formatTokenCount, formatUsdNanos } from "./chatTraceModel";

type Row = { call: ChatModelCall; key: string };

const PURPOSE: Record<string, string> = {
  chat: "Reply",
  compaction: "Compaction",
  title: "Title",
  draft_automation: "Draft automation",
};

function time(call: ChatModelCall): string {
  return call.time ? timestampDate(call.time).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" }) : "—";
}

/** The call's cost as the owner reads it: what basis, never zero for unknown. */
export function callCost(call: ChatModelCall): string {
  if (call.costBasis === "unpriced") return "Unpriced";
  if (call.costUsdNanos === undefined) return "—";
  if (call.costBasis === "estimated") return `~${formatUsdNanos(call.costUsdNanos)}`;
  return formatUsdNanos(call.costUsdNanos);
}

export function ChatModelCallsTable({ calls, viewer }: { calls: ChatModelCall[]; viewer: boolean }) {
  const rows: Row[] = calls.map((call, index) => ({ call, key: `${call.id || index}:${call.attempt}` }));
  const columns: DataTableColumn<Row>[] = [
    { key: "time", header: "Time", render: (_, { call }) => <span className="tabular-nums">{time(call)}</span> },
    { key: "purpose", header: "Purpose", render: (_, { call }) => PURPOSE[call.purpose] ?? call.purpose },
    {
      key: "model",
      header: "Model",
      stackTitle: true,
      render: (_, { call }) => <span className="font-mono text-code">{call.model || "—"}</span>,
    },
    {
      key: "outcome",
      header: "Outcome",
      render: (_, { call }) =>
        call.outcome === "error" ? (
          <span className="inline-flex items-center gap-1.5">
            <span className="size-2 shrink-0 rounded-full bg-destructive" aria-hidden="true" />
            Failed{call.errorClass ? ` (${call.errorClass.replace(/_/g, " ")})` : ""}
          </span>
        ) : call.outcome === "cancelled" ? (
          "Cancelled"
        ) : (
          "Succeeded"
        ),
    },
    { key: "in", header: "Input", numeric: true, render: (_, { call }) => formatTokenCount(call.inputTokens) },
    { key: "out", header: "Output", numeric: true, render: (_, { call }) => formatTokenCount(call.outputTokens) },
    { key: "cache", header: "Cache read", numeric: true, render: (_, { call }) => formatTokenCount(call.cacheReadTokens) },
    { key: "latency", header: "Latency", numeric: true, render: (_, { call }) => formatDuration(Number(call.latencyMs)) },
    {
      key: "ttft",
      header: "First token",
      numeric: true,
      render: (_, { call }) => (call.ttftMs === undefined ? "—" : formatDuration(Number(call.ttftMs))),
    },
  ];
  if (!viewer) {
    columns.push({
      key: "cost",
      header: "Cost",
      numeric: true,
      render: (_, { call }) => <span data-testid="chat-trace-call-cost">{callCost(call)}</span>,
    });
  }
  return (
    <div data-testid="chat-trace-calls-table">
      <DataTable
        columns={columns}
        data={rows}
        getRowKey={(row) => row.key}
        compact
        stacked
        ariaLabel="Model calls"
        emptyMessage="No model calls in this window."
      />
    </div>
  );
}
