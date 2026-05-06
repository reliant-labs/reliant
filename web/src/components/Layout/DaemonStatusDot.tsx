import { Activity } from "lucide-react";
import { type CSSProperties } from "react";

import { useDaemonStatus } from "../../hooks/useDaemonStatus";
import { cn } from "../../lib/utils";
import { useGlobalUpdatesStore } from "../../store/globalUpdatesStore";
import { Tooltip } from "../ui/Tooltip";

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
        label: daemonName ? `Daemon connected: ${daemonName}` : "Daemon connected",
        dotClassName: "bg-green-500 shadow-[0_0_0_3px_rgba(34,197,94,0.15)]",
        iconClassName: "text-green-500",
      };
    case "loading":
      return {
        label: "Checking daemon status...",
        dotClassName: "bg-muted-foreground/60 animate-pulse",
        iconClassName: "text-muted-foreground",
      };
    case "recent":
      return {
        label: `Daemon heartbeat seen ${heartbeatAgeSeconds ?? "recently"}s ago; reconnecting status...`,
        dotClassName: "bg-yellow-500 shadow-[0_0_0_3px_rgba(234,179,8,0.15)]",
        iconClassName: "text-yellow-500",
      };
    case "offline":
    default:
      return {
        label: "No daemon connected",
        dotClassName: "bg-red-500 shadow-[0_0_0_3px_rgba(239,68,68,0.15)]",
        iconClassName: "text-red-500",
      };
  }
}

export function DaemonStatusDot() {
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

  const meta = getStatusMeta(status, activeDaemon?.hostname || activeDaemon?.daemonId, heartbeatAgeSeconds);

  return (
    <Tooltip content={meta.label} placement="bottom" delay={300}>
      <div
        className="header-icon-btn relative flex h-7 w-7 items-center justify-center rounded text-xs transition-colors"
        role="status"
        aria-label={meta.label}
        style={{ WebkitAppRegion: "no-drag" } as CSSProperties}
      >
        <Activity className={cn("h-4 w-4", meta.iconClassName)} aria-hidden="true" />
        <span
          className={cn(
            "absolute right-1 top-1 h-2 w-2 rounded-full ring-1 ring-background",
            meta.dotClassName,
          )}
          aria-hidden="true"
        />
      </div>
    </Tooltip>
  );
}