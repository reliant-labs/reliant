/**
 * `/m/daemons` — mobile machine management.
 *
 * The phone surface can now perform the high-value lifecycle work users need
 * away from desktop: add a machine (the same Add machine sheet as desktop —
 * it opens as a bottom sheet on a phone), watch status, resume, suspend,
 * and delete. Port access, tokens, and detailed diagnostics remain desktop
 * settings because they need denser forms and copy-once secrets.
 *
 * Data comes from the same `useDaemonList` / daemon mutation hooks the desktop
 * surfaces use, so every lifecycle action refreshes one shared cache entry. The
 * only mobile-specific behavior is the poll gate — see `useVisibilityPolling`.
 */

import { useState } from "react";
import { Link } from "@tanstack/react-router";
import { ChevronRight, Loader2, Plus, Server } from "lucide-react";
import { Virtuoso } from "react-virtuoso";
import { useDaemonList } from "@/hooks/useOnboardingQueries";
import type { DaemonInfo as Daemon } from "@/gen/reliant/v1/daemon_registry_pb";
import { cn } from "../../lib/utils";
import { lastSeenMs, presentDaemon, sizeLabel } from "./daemonPresentation";
import { relativeTimeFromMs } from "./relativeTime";
import { useVisibilityPolling } from "./useVisibilityPolling";
import { MobileMenuButton } from "./MobileMenuButton";
import {
  MOBILE_PRIMARY_ACTION,
  MOBILE_ROW,
  MobileEmptyState,
  MobileRowIcon,
  MobileScreenHeader,
} from "./MobileChrome";
import { machineDisplayName } from "@/lib/machineName";
import { MachineCost } from "@/components/Billing/MachineCost";
import { AddMachineSheet } from "@/components/Machines/AddMachineSheet";

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

function NewMachineButton({ onCreate }: { onCreate: () => void }) {
  return (
    <button
      type="button"
      onClick={onCreate}
      aria-label="Add machine"
      className="flex min-h-[44px] items-center gap-1 rounded-lg px-3 text-sm font-medium text-primary active:bg-primary/10"
    >
      <Plus className="h-4 w-4" />
      <span>Add</span>
    </button>
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
  const lastSeen = lastSeenMs(daemon);
  const size = sizeLabel(daemon);

  return (
    <div className="px-4 pb-2">
      <Link
        to="/m/daemons/$daemonId"
        params={{ daemonId: daemon.daemonId }}
        className={cn(MOBILE_ROW, "rounded-lg border-b-0 elevation-1")}
      >
        <MobileRowIcon icon={Server} />
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <span className="truncate text-sm font-medium text-foreground">
              {machineDisplayName(daemon)}
            </span>
            {size && (
              <span className="shrink-0 rounded-md bg-primary/10 px-1.5 py-0.5 text-xs font-medium text-primary">
                {size}
              </span>
            )}
          </div>

          <div className="mt-1.5 flex items-center gap-2 text-xs text-muted-foreground">
            <DaemonStatusPill daemon={daemon} />
            {lastSeen !== null && <span>{relativeTimeFromMs(lastSeen)}</span>}
          </div>
        </div>

        <ChevronRight className="h-4 w-4 shrink-0 text-muted-foreground" />
      </Link>
      {daemon.daemonType === "managed" && <MachineCost daemonId={daemon.daemonId} size={daemon.size} />}
    </div>
  );
}

function DaemonListHeader({ onCreate }: { onCreate: () => void }) {
  return (
    <MobileScreenHeader
      title="Machines"
      leading={<MobileMenuButton />}
      trailing={<NewMachineButton onCreate={onCreate} />}
    />
  );
}

export function MobileDaemonList() {
  const refetchInterval = useVisibilityPolling(POLL_INTERVAL_MS);
  const { data: daemons, isLoading } = useDaemonList({ refetchInterval });
  const [createOpen, setCreateOpen] = useState(false);

  const header = <DaemonListHeader onCreate={() => setCreateOpen(true)} />;
  // Always mounted: a machine still starting after "Keep working" reports
  // in when it is ready.
  const createSheet = <AddMachineSheet open={createOpen} onClose={() => setCreateOpen(false)} />;

  if (isLoading) {
    return (
      <div className="flex h-full min-h-0 flex-col">
        {header}
        <div className="flex flex-1 items-center justify-center">
          <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
        </div>
        {createSheet}
      </div>
    );
  }

  if (!daemons || daemons.length === 0) {
    return (
      <div className="flex h-full min-h-0 flex-col">
        {header}
        <MobileEmptyState
          icon={Server}
          title="No machines yet"
          description="Machines run your agents. Start one here, then connect GitHub to clone repos onto it."
          action={
            <button
              type="button"
              onClick={() => setCreateOpen(true)}
              className={MOBILE_PRIMARY_ACTION}
            >
              <Plus className="h-4 w-4" />
              Add machine
            </button>
          }
        />
        {createSheet}
      </div>
    );
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      {header}

      <Virtuoso
        className="min-h-0 flex-1"
        data={daemons}
        // Stable identity across the 5s refetch: without it a daemon changing
        // status can recycle into a neighbour's DOM node mid-scroll.
        computeItemKey={(_, daemon) => daemon.daemonId}
        itemContent={(_, daemon) => <DaemonRow daemon={daemon} />}
        components={{
          Header: () => <div className="h-4" />,
          Footer: () => <div className="h-6" />,
        }}
      />
      {createSheet}
    </div>
  );
}
