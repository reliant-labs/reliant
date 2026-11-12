import { Monitor } from "lucide-react";
import { type CSSProperties } from "react";
import { useNavigate } from "@tanstack/react-router";

import { useDaemonStatus } from "../../hooks/useDaemonStatus";
import { cn } from "../../lib/utils";
import { useGlobalUpdatesStore } from "../../store/globalUpdatesStore";
import Tooltip from "../forge-ui/tooltip";
import { machineDisplayName } from "@/lib/machineName";

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

export function DaemonStatusDot() {
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

  const meta = getStatusMeta(status, activeDaemon ? machineDisplayName(activeDaemon) : undefined, heartbeatAgeSeconds);

  return (
    <Tooltip content={meta.label} side="bottom" delay={300} triggerClassName="block">
      <button
        type="button"
        className="header-icon-btn relative flex h-7 w-7 items-center justify-center rounded text-xs transition-colors focus-visible:ring-2 focus-visible:ring-ring"
        aria-label={`${meta.label}. Open machine settings`}
        onClick={() => {
          if (activeDaemon) {
            void navigate({ to: "/settings/machines/$machineId", params: { machineId: activeDaemon.daemonId } });
          } else {
            void navigate({ to: "/settings/$section", params: { section: "machines" } });
          }
        }}
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
