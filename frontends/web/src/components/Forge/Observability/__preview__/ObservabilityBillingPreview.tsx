// Copyright (c) 2026 Reliant Labs
// Prop-driven fixtures for happy/empty/conflict/read-only billing states. No live API.
import { useState } from "react";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";
import { GetObservabilityLimitsResponseSchema, GetObservabilityUsageResponseSchema, GetObservabilityStorageResponseSchema, UpdateObservabilityLimitsResponseSchema, UsageBucketSchema } from "@/gen/controlplane/services/observability_billing/v1/observability_billing_pb";
import { ObservabilityMonth } from "../UsageView";
import { ObservabilityLimits, ObservabilityFairUse, BillingUnavailable } from "@/components/Settings/cloud/usage/ObservabilityLimits";
import { DailyUsageTable, StorageSnapshot } from "@/components/Settings/cloud/usage/ObservabilityUsageDetails";
import Card, { CardHeader } from "@/components/forge-ui/card";

const stamp = (value: string) => timestampFromDate(new Date(value));
const GB = 1073741824n;
export default function ObservabilityBillingPreview() {
  const initial = new URLSearchParams(window.location.search).get("billingCase") ?? "happy";
  const [which, setWhich] = useState(initial);
  const [version, setVersion] = useState(0n);
  const [cap, setCap] = useState(5000n);
  const empty = which === "empty";
  const limits = create(GetObservabilityLimitsResponseSchema, { version, canUpdate: which !== "member", spendCapCents: cap, bounds: { minCents: 0n, maxCents: 100000n, defaultCents: 5000n, chargedThisPeriodCents: empty ? 0n : 325n }, alertThresholdsPercent: [50, 80, 100], alertOnCap: true,
    queryLanes: [{ lane: "interactive", queriesPerHour: 3000n, usageKnown: false, readBytesPerHour: 50n * GB, executionSecondsPerHour: 1800n }, { lane: "automation", queriesPerHour: 9000n, usageKnown: false }, { lane: "agent", queriesPerHour: 1500n, usageKnown: false }], maxQueryMemoryBytes: GB, maxQuerySeconds: 30, maxQueryRange: { seconds: 2592000n }, retention: [{ signal: "logs", tiers: [{ label: "debug", keep: { seconds: 259200n } }, { label: "info", keep: { seconds: 1209600n } }, { label: "warn and above", keep: { seconds: 2592000n } }] }] });
  const usage = create(GetObservabilityUsageResponseSchema, { periodStart: stamp("2026-10-01T00:00:00Z"), periodEnd: stamp("2026-11-01T00:00:00Z"), measuredThrough: empty ? undefined : stamp("2026-10-26T14:00:00Z"), usedBytes: empty ? 0n : 23n * GB, allowanceBytes: 10n * GB, chargedCents: empty ? 0n : 325n, spendCapCents: cap, rateCentsPerGb: "25.0000", state: 1, funded: true, browserBytes: empty ? 0n : 5n * GB, browserBilledBytes: empty ? 0n : 2n * GB, browserCapBytes: 2n * GB, projectedBytes: empty ? 0n : 28n * GB, projectedOverageCents: 450n });
  return <main className="forge-ui mx-auto max-w-5xl space-y-5 bg-surface-sunken p-6 text-ink">
    <header><h1 className="text-2xl font-semibold">Observability billing preview</h1><label className="mt-3 block text-sm">Preview case <select className="ml-2 rounded border border-border bg-surface-sunken px-3 py-2" value={which} onChange={e => { setWhich(e.target.value); setVersion(0n); setCap(5000n); }}><option value="happy">Happy</option><option value="empty">Empty</option><option value="conflict">Conflict on save</option><option value="member">Member read-only</option><option value="unavailable">Unavailable</option></select></label></header>
    {which === "unavailable" ? <BillingUnavailable label="Observability billing management" error={new ConnectError("Not deployed", Code.Unimplemented)} /> : <>
      <ObservabilityMonth usage={usage} />
      <Card><CardHeader title="Daily ingestion" /><DailyUsageTable buckets={empty ? [] : [create(UsageBucketSchema, { bucketStart: stamp("2026-10-25T00:00:00Z"), signal: "logs", service: "api", ingestedBytes: 3n * GB, billedBytes: 3n * GB, rows: 92348n }), create(UsageBucketSchema, { bucketStart: stamp("2026-10-25T00:00:00Z"), signal: "traces", service: "checkout", ingestedBytes: GB, billedBytes: GB, rows: 2349n })]} /></Card>
      <StorageSnapshot storage={create(GetObservabilityStorageResponseSchema, empty ? {} : { asOf: stamp("2026-10-26T14:00:00Z"), totalStoredBytes: 12n * GB, signals: [{ signal: "logs", storedBytes: 12n * GB, rows: 9234888n, oldestAt: stamp("2026-10-12T00:00:00Z"), services: [{ service: "api", estimatedBytes: 12n * GB }] }] })} />
      <ObservabilityLimits key={which} orgId="fixture" limits={limits} save={async request => { if (which === "conflict") throw new ConnectError("Changed by a teammate", Code.Aborted); setVersion(version + 1n); setCap(request.spendCapCents ?? cap); return create(UpdateObservabilityLimitsResponseSchema, { limits: { ...limits, version: version + 1n, spendCapCents: request.spendCapCents ?? cap } }); }} reload={async () => { setVersion(version + 1n); setCap(7500n); }} />
      <ObservabilityFairUse limits={limits} />
    </>}
  </main>;
}
