/**
 * `/m/daemons` — machines, read-mostly.
 *
 * The mobile surface declares `daemonView` and `daemonResume` true and
 * `daemonManage` false, and this screen is the whole of that: you can see
 * what your machines are doing and restart one that stopped. Create, suspend,
 * delete and port-access rules live on desktop, because they are slow,
 * destructive, or need a diagnostics pane a phone can't carry.
 *
 * Data comes from the same `useDaemonList` / `useResumeDaemon` pair the
 * desktop resume pill and OOM banner use, so all three share one cache entry
 * and one quota-error routing rule. The only mobile-specific behavior is the
 * poll gate — see `useVisibilityPolling`.
 */

import { Link } from "@tanstack/react-router";
import { ChevronRight, Loader2, Server } from "lucide-react";
import { Virtuoso } from "react-virtuoso";
import { useDaemonList } from "@/hooks/useOnboardingQueries";
import type { Daemon } from "@/services/controlPlane/daemon";
import { cn } from "../../lib/utils";
import { heartbeatMs, presentDaemon, sizeLabel } from "./daemonPresentation";
import { relativeTimeFromMs } from "./relativeTime";
import { useVisibilityPolling } from "./useVisibilityPolling";
import { MobileMenuButton } from "./MobileMenuButton";
import {
  MOBILE_ROW,
  MobileEmptyState,
  MobileRowIcon,
  MobileScreenHeader,
} from "./MobileChrome";

/** Matches the desktop daemon poll so the shared cache entry stays fresh. */
const POLL_INTERVAL_MS = 5_000;

export function DaemonStatusPill({ daemon }: { daemon: Daemon }) {
  const { label, pillClassName } = presentDaemon(daemon);
  return (
    <span
      className={cn(
        "inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ring-1 ring-inset",
        pillClassName,
      )}
    >
      {label}
    </span>
  );
}

/**
 * One machine, as its own card.
 *
 * Card-per-row rather than the shared rounded container the settings and
 * workflow screens use: this list is virtualized, and a group container would
 * have to wrap items Virtuoso renders and recycles independently. A card per
 * item gives the same rounded, raised, separated reading with no fight against
 * the virtualizer — and a machine is a self-contained object, so one card each
 * is arguably the truer mapping anyway.
 */
function DaemonRow({ daemon }: { daemon: Daemon }) {
  const beat = heartbeatMs(daemon);
  const size = sizeLabel(daemon);

  return (
    // The horizontal padding lives here rather than on the scroll container:
    // Virtuoso owns the scroller, so insetting the item is what produces the
    // page margin without the card's shadow being clipped.
    <div className="px-4 pb-2">
      <Link
        to="/m/daemons/$daemonId"
        params={{ daemonId: daemon.id }}
        className={cn(MOBILE_ROW, "rounded-lg border-b-0 elevation-1")}
      >
        <MobileRowIcon icon={Server} />
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <span className="truncate text-sm font-medium text-foreground">
              {daemon.name || "Unnamed machine"}
            </span>
            {size && (
              <span className="shrink-0 rounded-md bg-primary/10 px-1.5 py-0.5 text-xs font-medium text-primary">
                {size}
              </span>
            )}
          </div>

          <div className="mt-1.5 flex items-center gap-2 text-xs text-muted-foreground">
            <DaemonStatusPill daemon={daemon} />
            {beat !== null && <span>{relativeTimeFromMs(beat)}</span>}
          </div>
        </div>

        <ChevronRight className="h-4 w-4 shrink-0 text-muted-foreground" />
      </Link>
    </div>
  );
}

function DaemonListHeader() {
  return <MobileScreenHeader title="Machines" leading={<MobileMenuButton />} />;
}

export function MobileDaemonList() {
  const refetchInterval = useVisibilityPolling(POLL_INTERVAL_MS);
  const { data: daemons, isLoading } = useDaemonList({ refetchInterval });

  if (isLoading) {
    return (
      <div className="flex h-full min-h-0 flex-col">
        <DaemonListHeader />
        <div className="flex flex-1 items-center justify-center">
          <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
        </div>
      </div>
    );
  }

  if (!daemons || daemons.length === 0) {
    return (
      <div className="flex h-full min-h-0 flex-col">
        <DaemonListHeader />
        {/* No action: `daemonManage` is false on this surface, so a button
            here would open nothing. The description carries the next step
            instead — a dead end is more honest than a dead button. */}
        <MobileEmptyState
          icon={Server}
          title="No machines yet"
          description="Machines run your agents. Start one from the desktop app and it will appear here."
        />
      </div>
    );
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      <DaemonListHeader />

      <Virtuoso
        className="min-h-0 flex-1"
        data={daemons}
        // Stable identity across the 5s refetch: without it a daemon changing
        // status can recycle into a neighbour's DOM node mid-scroll.
        computeItemKey={(_, daemon) => daemon.id}
        itemContent={(_, daemon) => <DaemonRow daemon={daemon} />}
        components={{
          Header: () => <div className="h-4" />,
          Footer: () => <div className="h-6" />,
        }}
      />
    </div>
  );
}
