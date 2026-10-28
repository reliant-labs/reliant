// Copyright (c) 2025 Reliant Labs

import { useQueries } from "@tanstack/react-query";
import Card from "@/components/forge-ui/card";
import Link from "@/components/forge-ui/link";
import { managedSecretsQueryOptions } from "@/hooks/forge-queries";
import { CONTROL_PLANE_API_URL } from "@/services/controlPlane/config";
import type { RosterEnv } from "@/services/forge/roster";
import { availabilityExplanation, managedSecretState, normalizeEndpoint, type ManagedStoreTarget } from "@/services/forge/secretStore";
import { Ago } from "../Releases/releaseParts";
import { StackedTable } from "../StackedTable";

/** Metadata only. This uses the same listing/cache as the single-environment
 * screen, never version history or a value/reveal endpoint. Unknown is not unset. */
export function SecretsMatrix({ envs, projectId, secretHref }: {
  envs: RosterEnv[];
  projectId: string | null;
  secretHref: (env: string, name?: string) => string;
}) {
  const listings = useQueries({ queries: envs.map((env) => {
    const target: ManagedStoreTarget = !CONTROL_PLANE_API_URL
      ? { kind: "none", availability: "no-control-plane" }
      : env.live
        ? { kind: "lookup", environmentId: env.live.id, endpoint: normalizeEndpoint(CONTROL_PLANE_API_URL) }
        : { kind: "none", availability: "provider-unknown" };
    return managedSecretsQueryOptions(projectId, env.name, target);
  }) });
  const names = [...new Set(envs.flatMap((env, i) => [
    ...(env.live?.declaredShape?.secrets.map((secret) => secret.name) ?? []),
    ...(listings[i].data?.secrets.map((secret) => secret.name) ?? []),
  ]))].sort();

  if (!envs.length) return <Card>No environments yet. Register an environment to see its secret metadata.</Card>;
  return (
    <div className="space-y-3" data-testid="secrets-matrix">
      <p className="text-sm text-ink-muted">Compare secret names and status across environments. Values are never fetched. Open a cell to manage that secret.</p>
      <Card padding="none" className="overflow-hidden">
        <div className="overflow-x-auto">
          <StackedTable className="w-full border-collapse text-sm" aria-label="Secrets by environment">
            <thead><tr className="border-b border-border">
              <th scope="col" className="px-4 py-3 text-left font-medium text-ink">Secret</th>
              {envs.map((env, i) => {
                const listing = listings[i];
                const note = listing.isLoading ? "Loading…" : listing.isFetching ? "Refreshing…" : listing.data?.availability === "available" ? null
                  : availabilityExplanation(listing.data?.availability ?? "unreachable");
                return <th key={env.name} scope="col" className="min-w-40 px-4 py-3 text-left font-medium text-ink">
                  <Link href={secretHref(env.name)} className="hover:underline">{env.name}</Link>
                  {note && <div className="mt-1 max-w-60 text-xs font-normal text-ink-muted">{note}</div>}
                </th>;
              })}
            </tr></thead>
            <tbody>
              {names.map((name) => <tr key={name} className="border-b border-border/60 last:border-0">
                <th scope="row" className="px-4 py-3 text-left font-mono font-normal text-ink">{name}</th>
                {envs.map((env, i) => {
                  const listing = listings[i];
                  // Cached names keep the comparison stable, but their old status
                  // and timestamp are not authoritative while a refresh is pending.
                  const summary = listing.isFetching ? undefined : listing.data?.secrets.find((secret) => secret.name === name);
                  const declared = env.live?.declaredShape?.secrets.find((secret) => secret.name === name);
                  const readable = listing.data?.availability === "available";
                  const provider = declared?.provider;
                  const status = listing.isLoading ? "Loading…"
                    : listing.isFetching ? "Refreshing…"
                    : declared && provider && provider !== "hosted" ? (provider === "file" || provider === "rendered" ? "Local file store" : "External store")
                    : summary ? ({ set: "Set", deleted: "Deleted", destroyed: "Destroyed", tombstoned: "No versions" }[managedSecretState(summary)])
                    : !readable ? "Unknown"
                    : declared ? "Not set"
                    : env.live?.declaredShape ? "Not declared" : "Not in managed store";
                  return <td key={env.name} className="px-4 py-3 text-xs text-ink-muted">
                    <Link href={secretHref(env.name, name)} className="inline-flex flex-col gap-1 hover:text-ink hover:underline" aria-label={`${name} in ${env.name}: ${status}`}>
                      <span>{status}</span>
                      {summary?.updatedAt && <Ago at={summary.updatedAt} />}
                    </Link>
                  </td>;
                })}
              </tr>)}
              {!names.length && <tr><td colSpan={envs.length + 1} className="px-4 py-6 text-ink-muted">
                {listings.some((listing) => listing.isLoading) ? "Loading secret metadata…"
                  : listings.some((listing) => listing.isFetching) ? "Refreshing secret metadata…"
                  : listings.every((listing) => listing.data?.availability === "available") ? "No secrets declared or stored in these environments."
                  : "No secret names could be listed. Some environments are unavailable; this does not mean their secrets are unset."}
              </td></tr>}
            </tbody>
          </StackedTable>
        </div>
      </Card>
    </div>
  );
}
