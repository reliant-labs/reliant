import { Monitor } from "lucide-react";
import { type CSSProperties } from "react";
import { useNavigate } from "@tanstack/react-router";

import { useDaemonStatus } from "../../hooks/useDaemonStatus";
import { cn } from "../../lib/utils";
import { useGlobalUpdatesStore } from "../../store/globalUpdatesStore";
import Tooltip from "../forge-ui/tooltip";
import { machineDisplayName } from "@/lib/machineName";
import { machineSettingsPath, settingsPath } from "@/lib/paths";

const RECENT_HEARTBEAT_SECONDS = 60;

type DaemonConnectionStatus = "connected" | "loading" | "recent" | "offline";

function getHeartbeatAgeSeconds(lastSeen: number | null): number | null {
  if (!lastSeen) return null;
  return Math.max(0, Math.floor(Date.now() / 1000) - lastSeen);
}

function getStatusMeta(status: DaemonConnectionStatus, daemonName?: string, heartbeatAgeSeconds?: number | null) {
  switch (status) {
    case "connected":
      return {
        label: daemonName ? `Machine connected: ${daemonName}` : "Machine connected",
        dotClassName: "bg-success ring-2 ring-success/15",
        iconClassName: "text-success-ink",
      };
    case "loading":
      return {
        label: "Checking your machine...",
        dotClassName: "bg-muted-foreground/60 animate-pulse",
        iconClassName: "text-muted-foreground",
      };
    case "recent":
      return {
        label: `Last seen ${heartbeatAgeSeconds ?? "a moment"}s ago — reconnecting...`,
        dotClassName: "bg-warning ring-2 ring-warning/15",
        iconClassName: "text-warning-ink",
      };
    case "offline":
    default:
      return {
        label: "No machine connected",
        dotClassName: "bg-destructive ring-2 ring-destructive/15",
        iconClassName: "text-destructive-ink",
      };
  }
}

/**
 * `chip` (the top bar proposal, topBar.ts): a status dot and the machine's
 * name instead of a monitor icon, so the bar says WHICH machine. The name
 * shows from the xl breakpoint; narrower, the dot alone (its tooltip names it).
 */
export function DaemonStatusDot({ chip = false }: { chip?: boolean } = {}) {
  const navigate = useNavigate();
  const { daemons, activeDaemon, loading } = useDaemonStatus();
  const daemonLastSeen = useGlobalUpdatesStore((state) => state.daemonLastSeen);

  const heartbeatAgeSeconds = getHeartbeatAgeSeconds(daemonLastSeen);
  const hasRecentHeartbeat =
    heartbeatAgeSeconds !== null && heartbeatAgeSeconds <= RECENT_HEARTBEAT_SECONDS;
  const status: DaemonConnectionStatus = activeDaemon
    ? "connected"
    : loading
      ? "loading"
      : daemons.length === 0 && hasRecentHeartbeat
        ? "recent"
        : "offline";

  const name = activeDaemon ? machineDisplayName(activeDaemon) : undefined;
  const meta = getStatusMeta(status, name, heartbeatAgeSeconds);
  const open = () => {
    if (activeDaemon) {
      void navigate({ to: machineSettingsPath(activeDaemon.daemonId) });
    } else {
      void navigate({ to: settingsPath("machines") });
    }
  };

  if (chip) {
    return (
      <Tooltip content={meta.label} side="bottom" delay={300} triggerClassName="block">
        <button
          type="button"
          data-testid="header-machine-chip"
          className="inline-flex h-7 min-w-7 max-w-[12rem] items-center justify-center gap-2 rounded-md px-2 text-meta text-muted-foreground transition-colors hover:bg-foreground/5 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          aria-label={`${meta.label}. Open machine settings`}
          onClick={open}
          style={{ WebkitAppRegion: "no-drag" } as CSSProperties}
        >
          <span className={cn("size-2 shrink-0 rounded-full", meta.dotClassName)} aria-hidden="true" />
          <span className="hidden truncate xl:inline">{name ?? (status === "loading" ? "Checking…" : "No machine")}</span>
        </button>
      </Tooltip>
    );
  }

  return (
    <Tooltip content={meta.label} side="bottom" delay={300} triggerClassName="block">
      <button
        type="button"
        className="header-icon-btn relative flex h-7 w-7 items-center justify-center rounded text-xs transition-colors focus-visible:ring-2 focus-visible:ring-ring"
        aria-label={`${meta.label}. Open machine settings`}
        onClick={open}
        style={{ WebkitAppRegion: "no-drag" } as CSSProperties}
      >
        <Monitor className={cn("h-4 w-4", meta.iconClassName)} aria-hidden="true" />
        <span
          className={cn(
            "absolute right-1 top-1 h-2 w-2 rounded-full ring-1 ring-background",
            meta.dotClassName,
          )}
          aria-hidden="true"
        />
      </button>
    </Tooltip>
  );
}
