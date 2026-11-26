// Copyright (c) 2025 Reliant Labs

import { useCallback, useContext, useMemo } from "react";
import { QueryClientContext, useQuery } from "@tanstack/react-query";

import { acrossAllOrgs } from "@/api/orgInterceptor";
import type { ListDaemonsResponse } from "@/gen/controlplane/services/daemon/v1/daemon_pb";
import { queryClient as appQueryClient } from "@/lib/query-client";

import { hasControlPlane } from "@/services/controlPlane/config";
import type { MyOrg } from "@/services/controlPlane/orgs";
import { daemonServiceQueries } from "./controlplane/daemon-service-hooks_gen";
import { useActiveOrg } from "./useOrgs";

/**
 * Which org owns each machine (controlplane.v1.Daemon.org_id, control-plane
 * #2424). The machine list the UI reads is reliant's registry (one list:
 * docs/design/one-daemon-list.md), which knows whether a machine is attached
 * but not who pays for it; the control plane does. This reads only that
 * label, keyed by machine id, to join onto the registry's rows: machine id →
 * the org that owns it, "" for a machine recorded before machines had an
 * owning org (the control plane bills it to its operator's default org, and
 * lists it there).
 */
function orgByMachineOf(res: ListDaemonsResponse): ReadonlyMap<string, string> {
  return new Map(res.daemons.map((d) => [d.id, d.orgId]));
}

/**
 * The read names no org on purpose (acrossAllOrgs): an unset org_id lists
 * every machine the caller runs, in every org, so a machine that belongs to
 * another org is known to, rather than merely absent from, the active org's
 * list. Per-call options are not part of the key, so this is the one reader
 * of ListDaemons({}): an org-scoped list would need a request naming its org.
 */
const ACROSS_ALL_ORGS = () => ({ contextValues: acrossAllOrgs() });

/**
 * The org that owns machine `daemonId`, given the control plane's machine →
 * org map: its recorded org, or the default org for a machine recorded before
 * machines had one (the control plane bills and lists it there). Undefined
 * when the map does not know the machine (not loaded yet, or a machine the
 * control plane has no record of).
 */
export function machineOrgId(
  daemonId: string,
  orgByMachine: ReadonlyMap<string, string> | undefined,
  defaultOrgId: string,
): string | undefined {
  const org = orgByMachine?.get(daemonId);
  if (org === undefined) return undefined;
  return org || defaultOrgId || undefined;
}

/**
 * The machines to offer while `activeOrgId` is active: the ones it owns, the
 * ones whose org is not known (never hidden on a guess), and the ids in
 * `keep` (the current choice, so a select never shows a value it does not
 * list). Order is kept.
 */
export function machinesInOrg<T extends { daemonId: string }>(
  machines: readonly T[],
  orgByMachine: ReadonlyMap<string, string> | undefined,
  activeOrgId: string | null,
  defaultOrgId: string,
  keep: ReadonlyArray<string | null | undefined> = [],
): T[] {
  if (!activeOrgId || !orgByMachine) return [...machines];
  return machines.filter((m) => {
    if (keep.includes(m.daemonId)) return true;
    const org = machineOrgId(m.daemonId, orgByMachine, defaultOrgId);
    return org === undefined || org === activeOrgId;
  });
}

export interface MachineOrgs {
  /**
   * The person belongs to several orgs: machines are labelled with their org
   * and pickers offer the active org's. With one org (or none) this is false,
   * nothing is fetched, and nothing changes.
   */
  multiOrg: boolean;
  /** The org that owns the machine, when known. */
  orgOf: (daemonId: string) => MyOrg | undefined;
  /** `machinesInOrg` for the active org. */
  inActiveOrg: <T extends { daemonId: string }>(machines: readonly T[], keep?: ReadonlyArray<string | null | undefined>) => T[];
}

/**
 * Machine → org labels and the active-org machine filter (see MachineOrgs).
 *
 * The pickers that call it are leaf components, often rendered on their own
 * (tests, previews): outside a QueryClientProvider it reads the app's one
 * client, the one App.tsx provides.
 */
export function useMachineOrgs(): MachineOrgs {
  const client = useContext(QueryClientContext) ?? appQueryClient;
  const { orgs, activeOrgId, defaultOrgId, canSwitch } = useActiveOrg(client);
  const multiOrg = hasControlPlane && canSwitch;
  const { data: orgByMachine } = useQuery(
    daemonServiceQueries.listDaemons({}, {
      select: orgByMachineOf,
      enabled: multiOrg,
      staleTime: 60_000,
      callOptions: ACROSS_ALL_ORGS,
    }),
    client,
  );
  const known = multiOrg ? orgByMachine : undefined;

  const orgOf = useCallback(
    (daemonId: string) => {
      const id = machineOrgId(daemonId, known, defaultOrgId);
      return id ? orgs.find((o) => o.id === id) : undefined;
    },
    [known, defaultOrgId, orgs],
  );
  const inActiveOrg = useCallback(
    <T extends { daemonId: string }>(machines: readonly T[], keep?: ReadonlyArray<string | null | undefined>) =>
      machinesInOrg(machines, known, activeOrgId, defaultOrgId, keep),
    [known, activeOrgId, defaultOrgId],
  );
  return useMemo(() => ({ multiOrg, orgOf, inActiveOrg }), [multiOrg, orgOf, inActiveOrg]);
}
