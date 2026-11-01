import { useQuery } from "@tanstack/react-query";

import { DaemonStatus, type DaemonInfo } from "../gen/reliant/v1/daemon_registry_pb";
import { isManagedDaemon } from "../lib/daemonType";
import { logger } from "../lib/logger";
import { queryClient } from "../lib/query-client";
import { getDaemon } from "../services/controlPlane/environments";
import { cachedDaemonList } from "./daemon-registry-queries";
import { useDaemonStatus } from "./useDaemonStatus";

/** The control-plane GetDaemon entry that carries a daemon's preview URL template. */
export function daemonPreviewQuery(daemonId: string) {
  return {
    queryKey: ["controlPlane", "daemonPreview", daemonId] as const,
    queryFn: () => getDaemon(daemonId),
    staleTime: 5 * 60_000, // the URL shape is static per daemon
    retry: 1,
  };
}

/**
 * Only a managed (cloud) daemon's dev servers need a preview URL: a dev server
 * on a self-hosted daemon (the desktop app's own) is on this machine's
 * localhost already, and the control plane would still return a template for
 * it that leads nowhere.
 */
function previewDaemonId(daemon: DaemonInfo | undefined): string {
  return daemon && isManagedDaemon(daemon.daemonType) ? daemon.daemonId : "";
}

/**
 * The active daemon's preview URL template (`https://{port}-<id>.<domain>`),
 * when the active daemon is a cloud machine; undefined otherwise.
 */
export function useActiveDaemonPreviewTemplate(): string | undefined {
  const { activeDaemon } = useDaemonStatus();
  const daemonId = previewDaemonId(activeDaemon);
  const { data } = useQuery({ ...daemonPreviewQuery(daemonId), enabled: Boolean(daemonId) });
  return data?.workspaceBaseDomain || undefined;
}

/**
 * useActiveDaemonPreviewTemplate for a non-React caller (a link click): the
 * daemon list as the app holds it, and the template from the shared cache
 * entry, fetched if it is not there. Undefined on any failure — the caller
 * then treats localhost as this machine's, as it did before cloud daemons.
 */
export async function readActiveDaemonPreviewTemplate(): Promise<string | undefined> {
  const activeDaemon = cachedDaemonList(queryClient)?.find((d) => d.status === DaemonStatus.ACTIVE);
  const daemonId = previewDaemonId(activeDaemon);
  if (!daemonId) return undefined;
  try {
    // No retry: a click waits on this, and a miss only means localhost stays localhost.
    return (await queryClient.fetchQuery({ ...daemonPreviewQuery(daemonId), retry: 0 })).workspaceBaseDomain || undefined;
  } catch (error) {
    logger.warn("[preview] Could not read the active daemon's preview URL:", error);
    return undefined;
  }
}
