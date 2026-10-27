// Copyright (c) 2026 Reliant Labs
import { timestampDate, type Timestamp } from "@bufbuild/protobuf/wkt";
import type { UsageBucket, GetObservabilityUsageResponse } from "@/gen/controlplane/services/observability_billing/v1/observability_billing_pb";

export const gb = (bytes: bigint | number) => `${(Number(bytes) / 2 ** 30).toLocaleString(undefined, { maximumFractionDigits: 3 })} GB`;
export const dollars = (cents: bigint | number) => new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" }).format(Number(cents) / 100);
export const at = (time?: Timestamp) => time ? timestampDate(time).toISOString().replace("T", " ").replace(".000Z", " UTC") : "Unavailable";
export const day = (time?: Timestamp) => time ? timestampDate(time).toISOString().slice(0, 10) : "Unknown date";
export const monthNow = () => new Date().toISOString().slice(0, 7);
export function usageStateSentence(u: GetObservabilityUsageResponse) {
  if (u.spendCapReached) return `${u.capReachedAt ? `Since ${at(u.capReachedAt)}, we` : "We"} keep only errors and metrics: you reached your ${dollars(u.spendCapCents)} cap. Nothing above the cap is charged.`;
  if (u.state === 2 || u.state === 3) return "Debug logs and routine traces are dropped; errors and metrics are kept. A Stripe subscription is required for overage.";
  if (u.state === 1) return "Everything you send is being kept.";
  return "Collection state is unavailable.";
}
// Quote every cell AND neutralize formula prefixes, including leading controls/whitespace.
export function csvCell(value: string) {
  // Control characters are intentional here: a leading tab/CR or a formula
  // hidden behind whitespace must not become executable in a spreadsheet.
  // eslint-disable-next-line no-control-regex
  const safe = /^[\s\u0000-\u001f]*[=+@-]/.test(value) || /^[\t\r\n]/.test(value) ? `'${value}` : value;
  return `"${safe.replaceAll('"', '""')}"`;
}
export function usageCsv(buckets: UsageBucket[]) {
  const rows = [["UTC day", "Signal", "Service", "Ingested bytes", "Billable bytes", "Uncharged above cap bytes", "Rows", "Browser bytes", "Browser billable bytes", "Metadata bytes"],
    ...buckets.map(b => [day(b.bucketStart), b.signal, b.service, String(b.ingestedBytes), String(b.billedBytes), String(b.unchargedBytes), String(b.rows), String(b.browserBytes), String(b.browserBilledBytes), String(b.metadataBytes)])];
  return rows.map(row => row.map(csvCell).join(",")).join("\r\n");
}
export function dailyTotals(buckets: UsageBucket[]) {
  const days = new Map<string, Map<string, bigint>>();
  for (const b of buckets) {
    const date = day(b.bucketStart);
    const signals = days.get(date) ?? new Map<string, bigint>();
    signals.set(b.signal, (signals.get(b.signal) ?? 0n) + b.billedBytes);
    days.set(date, signals);
  }
  return [...days].sort(([a], [b]) => a.localeCompare(b)).map(([date, signals]) => ({ date, signals: [...signals], total: [...signals.values()].reduce((a, b) => a + b, 0n) }));
}
