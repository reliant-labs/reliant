// Copyright (c) 2026 Reliant Labs

/**
 * Whether the change's saved revision reached the environment's apps
 * (GetChangesDeliveryStatus; changeModel.ts deliveryStateOf): Saved,
 * Waiting for delivery, or Delivered, and each place the platform writes the
 * environment's snapshot (a hosted site's /_forge/changes.json, the hosted
 * workloads' ConfigMap) with what it has and when it got it.
 *
 * "Delivered" is precise on purpose: written to the CDN origin or the
 * ConfigMap. A page applies it on its next load; a pod after the ConfigMap
 * sync (about a minute) plus the SDK's 10s re-read. Whether an app has SEEN
 * it is not shown here.
 */

import { useId } from "react";

import Card from "@/components/forge-ui/card";
import Status from "@/components/forge-ui/status";
import { useChangesDelivery } from "@/hooks/changes-queries";
import type { Change, ChangeDeliveryTarget, ChangeDeliveryTargetKind, ChangesDeliveryStatus } from "@/services/controlPlane/changes/changesTypes";
import { changeErrorKind, describeChangeError } from "@/services/controlPlane/changes/errors";

import { agoOf, deliveryStateOf, targetDelivered, type ChangeDeliveryState } from "./changeModel";

const KIND_LABELS: Record<ChangeDeliveryTargetKind, string> = {
  "static-site": "Hosted site",
  "config-map": "Hosted workloads",
  unknown: "Target",
};

const STATE_LABELS: Record<ChangeDeliveryState["kind"], string> = {
  saved: "Saved",
  "no-targets": "Saved",
  waiting: "Waiting for delivery",
  delivered: "Delivered",
};

/** How each kind of target applies what was written to it. */
function howAppsApplyIt(targets: ChangeDeliveryTarget[]): string[] {
  const lines: string[] = [];
  if (targets.some((t) => t.kind === "static-site")) lines.push("A page applies it on its next load.");
  if (targets.some((t) => t.kind === "config-map")) lines.push("A running workload picks it up about a minute after it is written, plus the SDK's 10-second re-read.");
  return lines;
}

function summaryOf(state: ChangeDeliveryState, revision: number, envName: string): string {
  const saved = `Revision ${revision} is saved.`;
  switch (state.kind) {
    case "saved":
      return saved;
    case "no-targets":
      return `${saved} No hosted apps in this environment receive it.`;
    case "waiting":
      return `${saved} ${state.delivered} of ${state.targets} delivery targets in ${envName} have the environment's latest changes so far.`;
    case "delivered": {
      const where = state.targets === 1 ? "its one delivery target" : `all ${state.targets} delivery targets`;
      return `Revision ${revision} is saved and written to ${where} in ${envName}${state.at ? `, ${agoOf(state.at)}` : ""}.`;
    }
  }
}

export function ChangeDelivery({ change, envName }: { change: Change; envName: string }) {
  const id = useId();
  const query = useChangesDelivery(change);
  const status = query.data;
  const state = deliveryStateOf(status);
  const errorKind = query.error ? changeErrorKind(query.error) : null;

  return (
    <Card padding="md" aria-labelledby={`${id}-title`} className="space-y-2" data-testid="change-delivery">
      <div className="flex flex-wrap items-baseline gap-x-3">
        <h2 id={`${id}-title`} className="text-section text-ink">
          Delivery
        </h2>
        <span className="text-ui" data-testid="change-delivery-state">
          <Status tone={state.kind === "delivered" ? "success" : "neutral"} label={STATE_LABELS[state.kind]} />
        </span>
      </div>
      <p className="text-ui text-ink">{summaryOf(state, change.revision, envName)}</p>
      {state.kind === "saved" && (
        <p className="text-meta text-ink-muted">
          {query.isLoading
            ? `Checking whether it reached ${envName}'s apps…`
            : errorKind === "not-available"
              ? "This control plane can't report delivery yet."
              : query.error
                ? `Couldn't check whether it reached ${envName}'s apps: ${describeChangeError(query.error)}`
                : null}
        </p>
      )}
      {status && status.targets.length > 0 && (
        <>
          {howAppsApplyIt(status.targets).map((line) => (
            <p key={line} className="text-meta text-ink-muted">
              {line}
            </p>
          ))}
          <TargetList status={status} />
        </>
      )}
    </Card>
  );
}

function TargetList({ status }: { status: ChangesDeliveryStatus }) {
  return (
    <ul className="divide-y divide-border-subtle" aria-label="Delivery targets">
      {status.targets.map((target) => {
        const delivered = targetDelivered(target);
        return (
          <li key={`${target.kind}:${target.name}`} className="flex flex-wrap items-center gap-x-3 gap-y-0.5 py-1.5 text-ui" data-testid={`delivery-target-${target.name}`}>
            <Status tone={delivered ? "success" : "neutral"} label={delivered ? "Delivered" : "Waiting"} />
            <span className="text-ink">{KIND_LABELS[target.kind]}</span>
            <span className="font-mono text-ink">{target.name}</span>
            {target.location && <span className="truncate font-mono text-meta text-ink-muted">{target.location}</span>}
            {target.deliveredAt && <span className="ml-auto text-meta tabular-nums text-ink-muted">{`${delivered ? "Written" : "Last written"} ${agoOf(target.deliveredAt)}`}</span>}
          </li>
        );
      })}
    </ul>
  );
}
