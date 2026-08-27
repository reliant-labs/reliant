import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ExternalLink, Globe, Lock } from "lucide-react";
import { type CSSProperties } from "react";

import { useDaemonStatus } from "../../hooks/useDaemonStatus";
import { openExternalLink } from "../../lib/open-link";
import {
  getDaemon,
  listPortAccessRules,
  portAccessRulesQueryKey,
  removePortAccess,
  setDefaultPortAccess,
  setPortAccess,
  PortAccessMode,
} from "../../services/controlPlane/environments";
import { useGlobalUpdatesStore } from "../../store/globalUpdatesStore";
import { Dropdown } from "../ui/Dropdown";

/**
 * DetectedPortsChip — a small header affordance next to DaemonStatusDot that
 * appears when the active daemon reports listening dev-server ports
 * (heartbeat `detected_ports`, detected from the workspace pod's
 * /proc/net/tcp). Clicking a port opens the workspace preview URL served by
 * the workspace-proxy → in-pod preview forwarder chain.
 *
 * Preview URLs come from the control-plane `GetDaemon.workspaceBaseDomain`
 * `{port}` template — the subdomain edge carrier
 * (`https://{port}-<daemonId>.<baseDomain>`) so origin-absolute app fetches
 * (`/favicon.ico`, HMR) round-trip. If the template can't be fetched (e.g. a
 * self-hosted daemon unknown to the control-plane) fall back to plain
 * localhost, matching the local process-ports behavior.
 *
 * Access model (authenticated-by-default): the workspace owner can already
 * open any detected port with zero provisioning. This chip surfaces the
 * one-click "Make public" share action per port — it writes/removes a
 * `public` daemon_port_access rule via the same DaemonService RPCs the
 * Settings → Environments panel uses. The toggle only renders for cloud
 * daemons the control-plane knows about (i.e. when a URL template resolved).
 */
export function DetectedPortsChip() {
  const { activeDaemon } = useDaemonStatus();
  const qc = useQueryClient();
  const livePorts = useGlobalUpdatesStore((state) =>
    activeDaemon ? state.daemonDetectedPorts[activeDaemon.daemonId] : undefined,
  );

  // Live heartbeat data wins (sub-15s fresh); the 5s registry poll seeds the
  // chip on page load before the first streamed heartbeat arrives.
  const ports = livePorts ?? activeDaemon?.detectedPorts?.map(Number) ?? [];

  const daemonId = activeDaemon?.daemonId ?? "";
  const daemonPreviewQueryKey = ["controlPlane", "daemonPreview", daemonId] as const;
  const { data: daemonInfo } = useQuery({
    queryKey: daemonPreviewQueryKey,
    queryFn: () => getDaemon(daemonId),
    enabled: Boolean(daemonId) && ports.length > 0,
    staleTime: 5 * 60_000, // the URL shape is static per daemon
    retry: 1,
  });
  const urlTemplate = daemonInfo?.workspaceBaseDomain;

  // The port-access surface only exists for control-plane-managed (cloud)
  // daemons — the same signal the preview URL template gives us.
  const canManageAccess = Boolean(urlTemplate);

  // Workspace-level default applied to ports with no explicit rule. Anything
  // other than an explicit PUBLIC (unset/AUTHENTICATED) is the safe owner-only
  // default.
  const isPublicDefault = daemonInfo?.daemon?.defaultPortAccess === PortAccessMode.PUBLIC;
  const defaultMut = useMutation({
    mutationFn: (mode: PortAccessMode) => setDefaultPortAccess({ daemonId, defaultAccessMode: mode }),
    onSettled: () => qc.invalidateQueries({ queryKey: daemonPreviewQueryKey }),
  });

  const { data: rules } = useQuery({
    queryKey: portAccessRulesQueryKey(daemonId),
    queryFn: () => listPortAccessRules(daemonId),
    enabled: canManageAccess && ports.length > 0,
    staleTime: 30_000,
    retry: 0,
  });
  const publicPorts = new Set(
    (rules ?? []).filter((r) => r.accessMode === PortAccessMode.PUBLIC).map((r) => r.port),
  );

  const toggleMut = useMutation({
    mutationFn: async ({ port, makePublic }: { port: number; makePublic: boolean }) => {
      if (makePublic) {
        await setPortAccess({ daemonId, port, accessMode: PortAccessMode.PUBLIC });
      } else {
        await removePortAccess(daemonId, port);
      }
    },
    onSettled: () => qc.invalidateQueries({ queryKey: portAccessRulesQueryKey(daemonId) }),
  });

  if (!activeDaemon || ports.length === 0) return null;

  const previewUrl = (port: number): string =>
    urlTemplate ? urlTemplate.replace("{port}", String(port)) : `http://localhost:${port}`;

  const openPort = (port: number) => {
    void openExternalLink(previewUrl(port));
  };

  return (
    <div style={{ WebkitAppRegion: "no-drag" } as CSSProperties}>
      <Dropdown
        tooltip={
          ports.length === 1
            ? `Port ${ports[0]} detected — open preview`
            : `${ports.length} ports detected — open preview`
        }
        align="right"
        trigger={
          <span className="header-icon-btn relative flex h-7 items-center gap-1 rounded px-1.5 text-xs text-muted-foreground transition-colors hover:text-foreground">
            <Globe className="h-4 w-4 text-primary" aria-hidden="true" />
            <span className="font-mono">
              {ports.length === 1 ? ports[0] : ports.length}
            </span>
          </span>
        }
      >
        <div className="min-w-[16rem] py-1">
          {canManageAccess && (
            <div className="border-b border-border px-3 pb-2 pt-1">
              <div className="mb-1.5 text-2xs font-medium uppercase tracking-wide text-muted-foreground">
                Default access for new ports
              </div>
              <div className="flex overflow-hidden rounded border border-border">
                <button
                  type="button"
                  disabled={defaultMut.isPending}
                  onClick={() => {
                    if (isPublicDefault) defaultMut.mutate(PortAccessMode.AUTHENTICATED);
                  }}
                  title="Only you (and your org) can open a detected port unless you make it public."
                  className={
                    "flex flex-1 items-center justify-center gap-1 px-2 py-1 text-xs font-medium transition-colors disabled:opacity-50 " +
                    (!isPublicDefault
                      ? "bg-primary/10 text-primary"
                      : "text-muted-foreground hover:bg-accent hover:text-foreground")
                  }
                >
                  <Lock className="h-3 w-3" aria-hidden="true" />
                  Only you
                </button>
                <button
                  type="button"
                  disabled={defaultMut.isPending}
                  onClick={() => {
                    if (!isPublicDefault) defaultMut.mutate(PortAccessMode.PUBLIC);
                  }}
                  title="Anyone with the link can open every detected port on this workspace."
                  className={
                    "flex flex-1 items-center justify-center gap-1 border-l border-border px-2 py-1 text-xs font-medium transition-colors disabled:opacity-50 " +
                    (isPublicDefault
                      ? "bg-primary/10 text-primary"
                      : "text-muted-foreground hover:bg-accent hover:text-foreground")
                  }
                >
                  <Globe className="h-3 w-3" aria-hidden="true" />
                  Public
                </button>
              </div>
              {isPublicDefault && (
                <p className="mt-1.5 text-2xs leading-tight text-amber-600 dark:text-amber-500">
                  Every listening port is reachable by anyone with its URL unless you set it private.
                </p>
              )}
            </div>
          )}
          <div className="px-3 py-1 text-2xs font-medium uppercase tracking-wide text-muted-foreground">
            Detected ports
          </div>
          {ports.map((port) => {
            const isPublic = publicPorts.has(port);
            const pending = toggleMut.isPending && toggleMut.variables?.port === port;
            return (
              <div
                key={port}
                className="flex w-full items-center justify-between gap-2 px-3 py-1.5 text-xs hover:bg-accent"
              >
                <button
                  type="button"
                  onClick={() => openPort(port)}
                  className="flex flex-1 items-center gap-1.5 text-left"
                  title={previewUrl(port)}
                >
                  <span className="font-mono">Port {port}</span>
                  <ExternalLink className="h-3 w-3 text-muted-foreground" aria-hidden="true" />
                </button>
                {canManageAccess && (
                  <button
                    type="button"
                    disabled={pending}
                    onClick={() => toggleMut.mutate({ port, makePublic: !isPublic })}
                    title={
                      isPublic
                        ? "Public — anyone with the link can open this port. Click to make private."
                        : "Only you can open this port. Click to make it public."
                    }
                    className={
                      "inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-2xs font-medium transition-colors disabled:opacity-50 " +
                      (isPublic
                        ? "bg-primary/10 text-primary hover:bg-primary/20"
                        : "text-muted-foreground hover:bg-accent hover:text-foreground")
                    }
                  >
                    {isPublic ? (
                      <>
                        <Globe className="h-3 w-3" aria-hidden="true" />
                        {pending ? "…" : "Public"}
                      </>
                    ) : (
                      <>
                        <Lock className="h-3 w-3" aria-hidden="true" />
                        {pending ? "…" : "Private"}
                      </>
                    )}
                  </button>
                )}
              </div>
            );
          })}
        </div>
      </Dropdown>
    </div>
  );
}
