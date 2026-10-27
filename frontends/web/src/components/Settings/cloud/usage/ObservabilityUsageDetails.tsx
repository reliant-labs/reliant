// Copyright (c) 2026 Reliant Labs
import { useState } from "react";
import Card, { CardHeader } from "@/components/forge-ui/card";
import type { GetObservabilityStorageResponse, UsageBucket } from "@/gen/controlplane/services/observability_billing/v1/observability_billing_pb";
import { useObsBillingDays, useObsBillingLimits, useObsBillingStorage, useObsLimitHistory, useUpdateObsLimits } from "@/hooks/observability-billing-queries";
import { at, dailyTotals, day, dollars, gb, monthNow, usageCsv } from "./observabilityBillingModel";
import { BillingUnavailable, ObservabilityFairUse, ObservabilityLimits } from "./ObservabilityLimits";
import { ObservabilityCollectionPolicy } from "./ObservabilityCollectionPolicy";

export function ObservabilityUsageDetails({ orgId }: { orgId: string }) {
  const storage = useObsBillingStorage(orgId);
  const limits = useObsBillingLimits(orgId);
  const update = useUpdateObsLimits(orgId);
  const history = useObsLimitHistory(orgId);
  return <>
    <DailyUsage orgId={orgId} />
    {storage.isPending ? <Loading label="Loading stored-now snapshot…" /> : storage.error || !storage.data ? <BillingUnavailable label="Stored-now snapshot" error={storage.error} retry={() => void storage.refetch()} /> : <StorageSnapshot storage={storage.data} />}
    {limits.isPending ? <Loading label="Loading observability limits…" /> : limits.error || !limits.data ? <BillingUnavailable label="Observability limits" error={limits.error} retry={() => void limits.refetch()} /> : <>
      <ObservabilityLimits key={orgId} orgId={orgId} limits={limits.data} save={request => update.mutateAsync(request)} reload={async () => { const result = await limits.refetch(); if (result.error) throw result.error; }} />
      <ObservabilityCollectionPolicy orgId={orgId} />
      <ObservabilityFairUse limits={limits.data} />
    </>}
    <Card><CardHeader title="Limit change history" description="Organization-wide changes, newest first." />
      {history.isPending ? <Loading label="Loading change history…" /> : history.error || !history.data ? <BillingUnavailable label="Change history" error={history.error} retry={() => void history.refetch()} /> : <>
        {!history.data.pages.some(page => page.changes.length) && <p className="text-sm text-muted-foreground">No limit changes recorded yet.</p>}
        <ul className="divide-y divide-border text-sm">{history.data.pages.flatMap(page => page.changes).map(change => <li className="space-y-1 py-3" key={change.id}>
          <p>{change.resource} · {change.field}: <span className="break-words">{historyValue(change.resource, change.oldValue)} → {historyValue(change.resource, change.newValue)}</span></p>
          <p className="text-xs text-muted-foreground">{change.actorEmail || "System"} · {at(change.at)}</p>
        </li>)}</ul>
        {history.hasNextPage && <button type="button" className="text-primary underline" disabled={history.isFetchingNextPage} onClick={() => void history.fetchNextPage()}>Load older changes</button>}
      </>}
    </Card>
  </>;
}
function historyValue(resource: string, value: string) {
  if (!value) return "Default / not set";
  if (resource.endsWith("spend_cap") && /^\d+$/.test(value)) return dollars(BigInt(value));
  return value;
}
export function Loading({ label }: { label: string }) { return <p role="status" className="py-4 text-sm text-muted-foreground">{label}</p>; }
function DailyUsage({ orgId }: { orgId: string }) {
  const [month, setMonth] = useState(monthNow);
  const days = useObsBillingDays(orgId, month);
  const buckets = days.data?.pages.flatMap(page => page.buckets) ?? [];
  const exportCsv = () => {
    // Do not export a partial invoice-shaped report. All pages must be loaded.
    if (days.hasNextPage || days.isFetching || days.error) return;
    const url = URL.createObjectURL(new Blob([usageCsv(buckets)], { type: "text/csv;charset=utf-8" }));
    const anchor = document.createElement("a"); anchor.href = url; anchor.download = `observability-${month}.csv`; anchor.click();
    setTimeout(() => URL.revokeObjectURL(url), 0);
  };
  return <Card><CardHeader title="Daily ingestion" description="Selected month's UTC ingestion by signal and service. Billable bytes include data kept free above the spend cap; the included allowance is deducted before charging." />
    <div className="mb-4 flex flex-wrap items-center gap-3 text-sm"><label>Month <input aria-label="Usage month" type="month" className="ml-2 rounded border border-border bg-background px-2 py-1" min={earliestMonth()} max={monthNow()} value={month} onChange={e => { if (/^\d{4}-\d{2}$/.test(e.target.value)) setMonth(e.target.value); }} /></label>
      <button type="button" className="text-primary underline disabled:opacity-50" disabled={days.isPending || !!days.error || days.hasNextPage || days.isFetching} onClick={exportCsv}>Download CSV</button>
    </div>
    {days.isPending ? <Loading label="Loading daily ingestion…" /> : days.error ? <BillingUnavailable label="Daily ingestion" error={days.error} retry={() => void days.refetch()} /> : <>
      <DailyUsageTable buckets={buckets} />
      {days.hasNextPage && <div className="mt-3 text-sm"><p>Showing part of this month. Load all rows to enable CSV export.</p><button type="button" className="text-primary underline" disabled={days.isFetchingNextPage} onClick={() => void days.fetchNextPage()}>Load more usage</button></div>}
    </>}
  </Card>;
}
function earliestMonth() { const date = new Date(); date.setUTCMonth(date.getUTCMonth() - 12); return date.toISOString().slice(0, 7); }
const SIGNAL_CLASSES: Record<string, string> = { logs: "bg-primary", traces: "bg-warning", metrics: "bg-success", profiles: "bg-destructive" };
export function DailyUsageTable({ buckets }: { buckets: UsageBucket[] }) {
  if (!buckets.length) return <p className="text-sm text-muted-foreground">No ingestion recorded in this month. The meter updates hourly; recently sent telemetry may not appear yet.</p>;
  const days = dailyTotals(buckets);
  const max = Math.max(1, ...days.map(d => Number(d.total)));
  return <>
    <figure aria-label="Daily billable ingestion by signal" className="mb-4 space-y-2">
      <figcaption className="text-xs text-muted-foreground">Daily billable GB (including bytes kept free above the cap)</figcaption>
      {days.map(d => <div key={d.date} className="grid grid-cols-[6rem_1fr] items-center gap-3 text-xs"><span>{d.date}</span><div className="flex h-5 overflow-hidden rounded bg-background" aria-label={`${d.date}: ${gb(d.total)}`}>
        {d.signals.map(([signal, bytes]) => <div key={signal} className={SIGNAL_CLASSES[signal] ?? "bg-primary"} style={{ width: `${Number(bytes) / max * 100}%` }} title={`${signal}: ${gb(bytes)}`} />)}
      </div></div>)}
      <div className="flex flex-wrap gap-4">{Object.entries(SIGNAL_CLASSES).map(([signal, color]) => <span key={signal} className="flex items-center gap-1"><span className={`inline-block h-2 w-2 rounded ${color}`} />{signal}</span>)}</div>
    </figure>
    <div className="overflow-x-auto"><table className="w-full text-left text-xs tabular-nums"><caption className="sr-only">Daily ingestion by signal and service</caption><thead><tr className="border-b border-border">{["UTC day", "Signal", "Service", "Ingested", "Billable", "Kept free above cap", "Rows"].map(h => <th scope="col" className="whitespace-nowrap px-2 py-2" key={h}>{h}</th>)}</tr></thead><tbody>
      {buckets.map((b, index) => <tr className="border-b border-border/60" key={`${day(b.bucketStart)}/${b.signal}/${b.service}/${index}`}><td className="whitespace-nowrap px-2 py-2">{day(b.bucketStart)}</td><td className="px-2 py-2">{b.signal}</td><td className="max-w-48 break-words px-2 py-2">{b.service || "(no service name)"}</td><td className="whitespace-nowrap px-2 py-2">{gb(b.ingestedBytes)}</td><td className="whitespace-nowrap px-2 py-2">{gb(b.billedBytes)}</td><td className="whitespace-nowrap px-2 py-2">{gb(b.unchargedBytes)}</td><td className="px-2 py-2">{b.rows.toLocaleString()}</td></tr>)}
    </tbody><tfoot><tr><th scope="row" colSpan={3} className="px-2 py-2">Total shown</th><td className="px-2 py-2">{gb(buckets.reduce((n, b) => n + b.ingestedBytes, 0n))}</td><td className="px-2 py-2">{gb(buckets.reduce((n, b) => n + b.billedBytes, 0n))}</td><td className="px-2 py-2">{gb(buckets.reduce((n, b) => n + b.unchargedBytes, 0n))}</td><td className="px-2 py-2">{buckets.reduce((n, b) => n + b.rows, 0n).toLocaleString()}</td></tr></tfoot></table></div>
    <p className="mt-3 text-xs text-muted-foreground">Dollar charges are organization-wide, after the monthly allowance and cap; this API does not allocate invoice dollars to individual service rows. CSV exports exact bytes, not rounded GB.</p>
  </>;
}
export function StorageSnapshot({ storage }: { storage: GetObservabilityStorageResponse }) {
  return <Card><CardHeader title="What's stored now" description={storage.asOf ? `Snapshot as of ${at(storage.asOf)} · ${gb(storage.totalStoredBytes)} stored` : "No stored-now snapshot available yet."} />
    {storage.asOf && (!storage.signals.length ? <p className="text-sm text-muted-foreground">The snapshot contains no stored telemetry.</p> : <div className="overflow-x-auto"><table className="w-full text-left text-sm"><thead><tr>{["Signal", "Stored now", "Rows", "Oldest data"].map(h => <th scope="col" className="py-2 pr-4" key={h}>{h}</th>)}</tr></thead><tbody>{storage.signals.map(s => <tr className="border-t border-border" key={s.signal}><th scope="row" className="py-3 pr-4 text-left font-medium">{s.signal}</th><td className="py-3 pr-4">{gb(s.storedBytes)}</td><td className="py-3 pr-4">{s.rows.toLocaleString()}</td><td className="py-3">{s.oldestAt ? at(s.oldestAt) : "No data"}</td></tr>)}</tbody></table></div>)}
    {storage.signals.some(s => s.services.length > 0) && <details className="mt-3 text-sm"><summary>Per-service shares (estimates)</summary><p className="my-2 text-xs text-muted-foreground">Estimated from what arrived within each signal's retention, not exact current service storage.</p><ul>{storage.signals.flatMap(s => s.services.map(service => <li key={`${s.signal}/${service.service}`}>{s.signal} · {service.service || "(no service name)"}: {gb(service.estimatedBytes)} estimated</li>))}</ul></details>}
    <p className="mt-3 text-xs text-muted-foreground">Storage isn't billed separately. You pay once, when data arrives, and retention is included. Stored data shrinks as it ages out. See retention below.</p>
  </Card>;
}
