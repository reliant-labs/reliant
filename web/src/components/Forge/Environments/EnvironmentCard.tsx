// Copyright (c) 2025 Reliant Labs

/**
 * One environment in the /forge/environments list: a selectable card naming
 * where the env runs, whether it is healthy, and what it serves.
 *
 * ── WHY THE CARD IS NOT ONE <button> ────────────────────────────────────────
 *
 * It used to be, which forced a hosted env's workload URLs to render as inert
 * text — a link nested in a button is invalid and double-fires — so the one
 * screen that exists to answer "what URL does this serve" could not be clicked
 * through to it. The card is now a plain container with a STRETCHED selection
 * button (its `after:` pseudo-element covers the card, so the whole surface is
 * still one click target) and the URLs sit above that layer as real links. Tab
 * order is: select this env, then each of its links.
 */

import StatusDot from "@/components/forge-ui/status_dot";
import { cn } from "@/lib/utils";
import {
  destinationExplanation,
  destinationOf,
  usesKubeContext,
  type ForgeTopologyEnv,
} from "@/services/forge/topology";

import { DestinationBadge, HostedFacts } from "../DestinationBadge";

/** A label/value pair. Values that are IDENTIFIERS render mono; nothing else does. */
function Field({
  label,
  value,
  mono,
  empty = "—",
}: {
  label: string;
  value?: string;
  mono?: boolean;
  empty?: string;
}) {
  return (
    <div className="min-w-0">
      <dt className="text-2xs font-medium uppercase tracking-wide text-muted-foreground">{label}</dt>
      <dd
        className={cn(
          "mt-1 truncate text-sm",
          value ? "text-foreground" : "text-muted-foreground",
          value && mono && "font-mono"
        )}
      >
        {value || empty}
      </dd>
    </div>
  );
}

export function EnvironmentCard({
  env,
  active,
  onSelect,
}: {
  env: ForgeTopologyEnv;
  active: boolean;
  onSelect: (env: string) => void;
}) {
  const hosted = destinationOf(env) === "hosted";

  return (
    <div
      data-testid={`environment-card-${env.env}`}
      data-selected={active}
      className={cn(
        "relative rounded-lg border bg-card px-5 py-4 transition-colors",
        // Focus is drawn on the CARD when its selection button has it, so the
        // ring outlines the thing being selected, not an invisible overlay.
        "has-[button[data-card-select]:focus-visible]:ring-2 has-[button[data-card-select]:focus-visible]:ring-ring",
        active ? "border-primary" : "border-border hover:border-border-strong"
      )}
    >
      <div className="flex items-center justify-between gap-3">
        <span className="inline-flex min-w-0 items-center gap-2">
          <button
            type="button"
            data-card-select
            aria-pressed={active}
            aria-label={`Show what ${env.env} runs`}
            onClick={() => onSelect(env.env)}
            className="font-mono text-sm font-medium text-foreground after:absolute after:inset-0 after:rounded-lg after:content-[''] focus:outline-none"
          >
            {/* An env name is an identifier. */}
            {env.env}
          </button>
          <DestinationBadge env={env} />
        </span>
        <StatusDot
          variant={env.bound ? "active" : "neutral"}
          label={env.bound ? "Promoted" : "Never promoted"}
          size="sm"
        />
      </div>
      {env.note && <p className="mt-2 text-sm text-muted-foreground">{env.note}</p>}
      <dl className="mt-4 grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Field label="Release" value={env.release} mono empty="None promoted" />
        {hosted ? (
          // No cluster or namespace for a hosted env — the control plane
          // runs it. Its "where" is the control plane, and its workloads'
          // health and URLs. `relative z-10` lifts the links above the
          // stretched selection button so they are clickable.
          <div className="relative z-10 min-w-0 sm:col-span-2">
            <dt className="text-2xs font-medium uppercase tracking-wide text-muted-foreground">
              Runs on
            </dt>
            <dd className="mt-1">
              <HostedFacts env={env} />
            </dd>
          </div>
        ) : usesKubeContext(destinationOf(env)) || env.kube_context ? (
          <>
            <Field label="Cluster" value={env.kube_context} mono empty="Not resolved" />
            <Field label="Namespace" value={env.namespace} mono empty="Not resolved" />
          </>
        ) : (
          // compose / host / static / external / unknown: there is no cluster
          // to name, and labelling an empty field "Cluster" would claim one.
          <div className="min-w-0 sm:col-span-2">
            <dt className="text-2xs font-medium uppercase tracking-wide text-muted-foreground">
              Runs on
            </dt>
            <dd className="mt-1 text-sm text-muted-foreground">
              {destinationExplanation(destinationOf(env))}
            </dd>
          </div>
        )}
        <Field
          label="Declared"
          value={env.declared ? "In this checkout" : "Not in this checkout"}
        />
      </dl>
    </div>
  );
}
