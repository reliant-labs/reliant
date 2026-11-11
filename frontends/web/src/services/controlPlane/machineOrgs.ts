// Copyright (c) 2025 Reliant Labs

/**
 * Which org owns each machine (controlplane.v1.Daemon.org_id, control-plane
 * #2424). The machine list the UI reads is reliant's registry (one list:
 * docs/design/one-daemon-list.md), which knows whether a machine is attached
 * but not who pays for it; the control plane does. This reads only that
 * label, keyed by machine id, to join onto the registry's rows.
 *
 * It names no org on purpose (acrossAllOrgs): an unset org_id lists every
 * machine the caller runs, in every org, so a machine that belongs to another
 * org is known to, rather than merely absent from the active org's list.
 */

import { DaemonService } from "@/gen/controlplane/services/daemon/v1/daemon_pb";
import { acrossAllOrgs } from "@/api/orgInterceptor";
import { getControlPlaneClient } from "./client";

export const machineOrgKeys = {
  // Under the org-scoped "controlPlane" root: a switch re-reads it too.
  all: ["controlPlane", "machine-orgs"] as const,
};

/**
 * Machine id → the org that owns it. "" for a machine recorded before machines
 * had an owning org: the control plane bills it to its operator's default
 * org, and lists it there.
 */
export async function listMachineOrgs(): Promise<ReadonlyMap<string, string>> {
  const res = await getControlPlaneClient(DaemonService).listDaemons({}, { contextValues: acrossAllOrgs() });
  return new Map(res.daemons.map((d) => [d.id, d.orgId]));
}
