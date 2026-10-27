// Copyright (c) 2026 Reliant Labs
import { useLayoutEffect, useRef, useState } from "react";
import { Code, ConnectError } from "@connectrpc/connect";
import type { GetObservabilityLimitsResponse, UpdateObservabilityLimitsResponse } from "@/gen/controlplane/services/observability_billing/v1/observability_billing_pb";
import type { MessageInitShape } from "@bufbuild/protobuf";
import type { UpdateObservabilityLimitsRequestSchema } from "@/gen/controlplane/services/observability_billing/v1/observability_billing_pb";
import Card, { CardHeader } from "@/components/forge-ui/card";
import { at, dollars, gb } from "./observabilityBillingModel";

export type SaveLimits = (request: MessageInitShape<typeof UpdateObservabilityLimitsRequestSchema>) => Promise<UpdateObservabilityLimitsResponse>;
export function BillingUnavailable({ label, error, retry }: { label: string; error?: unknown; retry?: () => void }) {
  const undeployed = error instanceof ConnectError && error.code === Code.Unimplemented;
  return <div role="status" className="rounded-lg border border-border bg-background p-4 text-sm">
    <p>{label} {undeployed ? "is not available on this server yet." : "is unavailable."}</p>
    {retry && <button type="button" className="mt-2 text-primary underline" onClick={retry}>Try again</button>}
  </div>;
}

/** Drafts are tied to the version read, not reset by background refreshes. */
export function ObservabilityLimits({ limits, orgId, save, reload }: {
  limits: GetObservabilityLimitsResponse; orgId: string; save: SaveLimits;
  reload: () => Promise<unknown>;
}) {
  const [editing, setEditing] = useState(false);
  const [version, setVersion] = useState(limits.version);
  const [noOverage, setNoOverage] = useState(limits.spendCapCents === 0n);
  const [cap, setCap] = useState((Number(limits.spendCapCents) / 100).toFixed(2));
  const [thresholds, setThresholds] = useState(limits.alertThresholdsPercent);
  const [onCap, setOnCap] = useState(limits.alertOnCap);
  const [message, setMessage] = useState("");
  const [conflict, setConflict] = useState(false);
  // Each draft/operation owns its UI continuations, including pending state.
  // Superseding one never cancels its valid server write or cache completion.
  const epoch = useRef(0);
  const [pending, setPending] = useState<"save" | "reload" | null>(null);
  const saving = pending === "save";
  useLayoutEffect(() => {
    setEditing(false); setMessage(""); setConflict(false); setPending(null);
    return () => { epoch.current += 1; };
  }, [orgId]);
  const cancel = () => {
    epoch.current += 1;
    setEditing(false); setMessage(""); setConflict(false); setPending(null);
  };
  const reloadLatest = async () => {
    const requestEpoch = ++epoch.current;
    setPending("reload");
    try {
      await reload();
      if (epoch.current !== requestEpoch) return;
      setEditing(false); setMessage("Latest limits reloaded. Review them before editing again."); setConflict(false);
    } catch {
      if (epoch.current !== requestEpoch) return;
      setMessage("Reload failed. Your edits have not been saved; try reloading again.");
    } finally {
      if (epoch.current === requestEpoch) setPending(null);
    }
  };
  const start = () => {
    epoch.current += 1; setPending(null);
    setVersion(limits.version); setNoOverage(limits.spendCapCents === 0n);
    setCap((Number(limits.spendCapCents) / 100).toFixed(2));
    setThresholds([...limits.alertThresholdsPercent]); setOnCap(limits.alertOnCap);
    setConflict(false); setMessage(""); setEditing(true);
  };
  const submit = async () => {
    const requestEpoch = ++epoch.current;
    setPending(null); setMessage("");
    // Local parsing only: the backend owns bounds and the already-charged floor.
    if (!noOverage && !/^\d+(\.\d{1,2})?$/.test(cap)) { setMessage("Enter a dollar amount with at most two decimal places."); return; }
    setPending("save");
    try {
      const result = await save({ orgId, expectedVersion: version, spendCapCents: noOverage ? 0n : BigInt(Math.round(Number(cap) * 100)),
        setAlertThresholds: true, alertThresholdsPercent: thresholds, alertOnCap: onCap });
      if (epoch.current !== requestEpoch) return;
      setEditing(false);
      setMessage(result.spendCapRaisedToCharged ? "Saved at the amount already charged this month. Lowering the cap cannot undo charges." : "Observability limits saved.");
    } catch (error) {
      if (epoch.current !== requestEpoch) return;
      if (error instanceof ConnectError && error.code === Code.Aborted) {
        setConflict(true);
        setMessage("Someone changed these limits. Your edits were not saved. Reload the latest limits, review them, then edit again.");
      } else setMessage(error instanceof ConnectError ? error.rawMessage : "Could not save limits. Check your connection and try again.");
    } finally {
      if (epoch.current === requestEpoch) setPending(null);
    }
  };
  return <Card>
    <CardHeader title="Spend cap & usage alerts" description="Owners and admins can edit; members can view." />
    <p className="text-sm">{limits.spendCapCents === 0n ? "No overage" : `Cap at ${dollars(limits.spendCapCents)} per month`}</p>
    <p className="mt-2 text-sm text-muted-foreground">At the cap we stop charging and keep only errors and metrics until the 1st. A Stripe subscription is required for overage.</p>
    <p className="mt-2 text-xs text-muted-foreground">Already charged: {dollars(limits.bounds?.chargedThisPeriodCents ?? 0n)}. Lowering the cap cannot undo already charged amounts; the server raises a lower request to that amount this month.</p>
    <p className="mt-2 text-xs text-muted-foreground">Self-serve cap: {dollars(limits.bounds?.minCents ?? 0n)}–{dollars(limits.bounds?.maxCents ?? 100000n)}. Default: {dollars(limits.bounds?.defaultCents ?? 5000n)}. No uncapped overage.</p>
    {!editing && <>
      <p className="mt-3 text-sm">Thresholds: {limits.alertThresholdsPercent.length ? limits.alertThresholdsPercent.map(n => `${n}%`).join(", ") : "None"}; cap reached: {limits.alertOnCap ? "On" : "Off"}.</p>
      {limits.canUpdate && <button type="button" className="mt-3 text-primary underline" onClick={start}>Edit observability limits</button>}
    </>}
    {editing && <form className="mt-4 space-y-4" onSubmit={e => { e.preventDefault(); void submit(); }}>
      <fieldset disabled={saving || conflict || !limits.canUpdate} className="space-y-3">
        <legend className="text-sm font-medium">Monthly overage</legend>
        <label className="flex items-center gap-2 text-sm"><input type="radio" name="obs-cap" checked={noOverage} onChange={() => setNoOverage(true)} />No overage ($0): keep within the included allowance; degrade at the limit</label>
        <label className="flex items-center gap-2 text-sm"><input type="radio" name="obs-cap" checked={!noOverage} onChange={() => setNoOverage(false)} />Cap at a dollar amount</label>
        {!noOverage && <label className="block text-sm">Monthly cap (USD)<input className="ml-3 rounded border border-border bg-background px-3 py-2" aria-label="Monthly cap (USD)" type="number" min="0" max={Number(limits.bounds?.maxCents ?? 100000n) / 100} step="0.01" required value={cap} onChange={e => setCap(e.target.value)} /></label>}
        <div className="space-y-2"><p className="text-sm font-medium">Usage alert thresholds</p>
          {[...new Set([50, 80, 100, ...thresholds])].sort((a, b) => a - b).map(n => <label key={n} className="mr-4 inline-flex items-center gap-2 text-sm"><input type="checkbox" checked={thresholds.includes(n)} onChange={e => setThresholds(e.target.checked ? [...thresholds, n].sort((a, b) => a - b) : thresholds.filter(x => x !== n))} />{n}% of included</label>)}
          <label className="flex items-center gap-2 text-sm"><input type="checkbox" checked={onCap} onChange={e => setOnCap(e.target.checked)} />Cap reached</label>
        </div>
        <button type="submit" className="rounded border border-border px-3 py-2 text-sm">{saving ? "Saving…" : "Save observability limits"}</button>
      </fieldset>
      <button type="button" className="text-sm text-primary underline" onClick={cancel}>Cancel</button>
    </form>}
    <p className="mt-3 text-xs text-muted-foreground">Threshold preferences are saved only. Alert delivery and recipient routing are not yet connected here; saving does not enable email, Slack or webhook delivery.</p>
    {message && <p role="status" className="mt-3 text-sm">{message}</p>}
    {conflict && <button type="button" className="mt-2 text-primary underline" disabled={pending === "reload"} onClick={() => void reloadLatest()}>Reload latest limits</button>}
    {limits.updatedAt && <p className="mt-3 text-xs text-muted-foreground">Last changed {at(limits.updatedAt)} by {limits.updatedByEmail || "system"}.</p>}
  </Card>;
}

export function ObservabilityFairUse({ limits }: { limits: GetObservabilityLimitsResponse }) {
  return <Card><CardHeader title="Queries & retention" description="Read-only fair-use budgets, the same for every organization. Queries are included; these limits keep the shared cluster fast for everyone." />
    <ul className="divide-y divide-border text-sm">{limits.queryLanes.map(lane => <li className="space-y-1 py-3" key={lane.lane}>
      <p className="font-medium">{({ interactive: "Console", automation: "Alerts & automation", agent: "Agent" } as Record<string, string>)[lane.lane] ?? lane.lane}: {lane.queriesPerHour.toLocaleString()} queries/hour</p>
      <p>{lane.usageKnown ? `${lane.queriesUsed.toLocaleString()} queries; ${gb(lane.readBytesUsed)} read; ${lane.executionSecondsUsed.toLocaleString()} execution seconds this hour${lane.exhausted ? " — exhausted" : ""}` : "Current usage unavailable — not measured by billing."}</p>
      <p className="text-xs text-muted-foreground">Budget: {gb(lane.readBytesPerHour)} read/hour · {lane.executionSecondsPerHour.toLocaleString()} execution seconds/hour · resets {at(lane.resetsAt)}</p>
    </li>)}</ul>
    <p className="my-3 text-sm">Per query: {limits.maxQuerySeconds} seconds · {gb(limits.maxQueryMemoryBytes)} memory · {Number(limits.maxQueryRange?.seconds ?? 0n) / 86400}-day range.</p>
    <ul className="text-sm">{limits.retention.map(signal => <li key={signal.signal} className="py-1">{signal.signal}: {signal.tiers.map(t => `${t.label}: ${Number(t.keep?.seconds ?? 0n) / 86400} days`).join(" · ")}</li>)}</ul>
  </Card>;
}
