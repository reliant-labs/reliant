// Copyright (c) 2025 Reliant Labs

/**
 * The organization's hourly observability QUERY quota, as the console sees it.
 *
 * Every ClickHouse read an org makes (this console, the CLI, alert evaluation)
 * draws on a budget that refills at the top of the hour. When it runs out,
 * control-plane answers ResourceExhausted with reason `obs_query_quota` and a
 * QueryQuotaExhausted detail naming the limit, the lane and the reset. A
 * background poll refused early, to keep a reserve for a person's own query,
 * carries `obs_query_quota_soft` and the same detail. This module is the ONE
 * place that reads those answers, so the rest of the console asks "is this the
 * quota, and when does it end" and never matches prose.
 *
 * ResourceExhausted is not always the quota: the console's in-flight cap and
 * the scan caps use the same code. Only an error carrying the reason, the
 * detail, or (from a control plane that predates them) the quota message is a
 * quota error; the others must not pause the console.
 */

import { create } from "@bufbuild/protobuf";
import { timestampFromMs, timestampMs } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";

import {
  QueryQuotaExhaustedSchema,
  QueryQuotaLimit,
} from "@/gen/controlplane/services/observability/v1/observability_pb";
import { errorReason } from "@/lib/errorReasons";

export type QuotaLimit = "queries" | "read_bytes" | "execution_time" | "unknown";

export interface QuotaExhaustion {
  limit: QuotaLimit;
  /** The query lane that ran out ("interactive", "automation", "agent"); "" before lanes exist. */
  lane: string;
  /** When the quota interval ends and the budget refills. */
  resetsAtMs: number;
  /** A poll refused early: explicit reads still go through. False when the quota is gone. */
  soft: boolean;
}

const HOUR_MS = 60 * 60_000;
const REASON_HEADER = "x-forge-error-reason";
export const QUOTA_REASON = "obs_query_quota";
export const QUOTA_SOFT_REASON = "obs_query_quota_soft";

/** The quota interval is one clock hour (control-plane obsread.QuotaInterval). */
export function nextQuotaResetMs(nowMs: number): number {
  return Math.floor(nowMs / HOUR_MS) * HOUR_MS + HOUR_MS;
}

function limitOf(wire: QueryQuotaLimit): QuotaLimit {
  switch (wire) {
    case QueryQuotaLimit.QUERIES:
      return "queries";
    case QueryQuotaLimit.READ_BYTES:
      return "read_bytes";
    case QueryQuotaLimit.EXECUTION_TIME:
      return "execution_time";
    default:
      return "unknown";
  }
}

function wireLimit(limit: QuotaLimit): QueryQuotaLimit {
  switch (limit) {
    case "queries":
      return QueryQuotaLimit.QUERIES;
    case "read_bytes":
      return QueryQuotaLimit.READ_BYTES;
    case "execution_time":
      return QueryQuotaLimit.EXECUTION_TIME;
    default:
      return QueryQuotaLimit.UNSPECIFIED;
  }
}

/**
 * The exhaustion an error reports, or null when it is not the org's query
 * quota. The reset comes from the typed detail; otherwise the fixed hourly
 * quota resets at the top of the next hour. No cross-origin response header
 * is needed to interpret it.
 */
export function quotaOf(error: unknown, nowMs: number = Date.now()): QuotaExhaustion | null {
  if (!(error instanceof ConnectError) || error.code !== Code.ResourceExhausted) return null;
  const soft = errorReason(error) === QUOTA_SOFT_REASON;

  const [detail] = error.findDetails(QueryQuotaExhaustedSchema);
  if (detail) {
    return {
      limit: limitOf(detail.limit),
      lane: detail.lane,
      resetsAtMs: detail.resetsAt ? timestampMs(detail.resetsAt) : nextQuotaResetMs(nowMs),
      soft,
    };
  }

  const reason = errorReason(error);
  // A control plane from before the detail carried only the message.
  if (reason !== QUOTA_REASON && !soft && !/query quota/i.test(error.rawMessage)) return null;
  return {
    limit: "unknown",
    lane: "",
    resetsAtMs: nextQuotaResetMs(nowMs),
    soft,
  };
}

/**
 * The error a read gets while the quota is known to be out, without asking the
 * server: the same shape the server answers with, so every view reads it the
 * same way.
 */
export function quotaPausedError(exhaustion: QuotaExhaustion): ConnectError {
  return new ConnectError(
    "this organization's hourly observability query quota is used up",
    Code.ResourceExhausted,
    { [REASON_HEADER]: exhaustion.soft ? QUOTA_SOFT_REASON : QUOTA_REASON },
    [
      {
        desc: QueryQuotaExhaustedSchema,
        value: create(QueryQuotaExhaustedSchema, {
          limit: wireLimit(exhaustion.limit),
          lane: exhaustion.lane,
          resetsAt: timestampFromMs(exhaustion.resetsAtMs),
        }),
      },
    ]
  );
}

/** A wall-clock time in the reader's zone: "19:00". */
export function formatClock(ms: number): string {
  return new Date(ms).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

/** The limit in words a reader recognises. */
export function limitWords(limit: QuotaLimit): string {
  switch (limit) {
    case "queries":
      return "number of queries";
    case "read_bytes":
      return "amount of data read";
    case "execution_time":
      return "query time";
    default:
      return "query allowance";
  }
}

/**
 * True when a query failed with the quota answer but still holds its last good
 * result: the view keeps drawing that result (marked paused) instead of the
 * failure. Any other failure still replaces the view, as before.
 */
export function keepsLastResult(error: unknown, data: unknown): boolean {
  return !!data && quotaOf(error) !== null;
}
