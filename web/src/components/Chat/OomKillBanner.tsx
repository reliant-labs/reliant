/**
 * OomKillBanner — persistent, dismissible chat-view banner shown when the
 * current machine's daemon pod was recently OOM-killed.
 *
 * Data path: the workspace operator records OOMKilled container terminations
 * on the Workspace CR status → the control-plane state reconciler mirrors
 * them onto the daemon row → `controlplane.v1.DaemonService.ListDaemons`
 * exposes `lastOomKilledAt` / `oomKillCount` → the shared `useDaemonList`
 * query (same source as ResumeDaemonPill) delivers them here.
 *
 * Without this, an OOM kill looks like a silent disconnect/reconnect blip:
 * the kubelet restarts the container in place, so nothing else in the UI
 * ever says "you ran out of memory".
 *
 * Dismissal is keyed on `${daemonId}:${lastOomKilledAt}` in sessionStorage
 * (mirroring ResumeDaemonPill) so a NEW kill re-surfaces the banner even
 * after the user dismissed an earlier one.
 */
import { useCallback, useMemo, useState } from "react";
import { ArrowUpRight, MemoryStick, X } from "lucide-react";
import { useNavigate } from "@tanstack/react-router";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import { useDaemonList } from "@/hooks/useOnboardingQueries";
import { useDaemonStatus } from "@/hooks/useDaemonStatus";
import { DaemonSize } from "@/gen/controlplane/v1/public/shared_pb";
import type { Daemon } from "@/services/controlPlane/daemon";

const DISMISS_KEY = "reliant.oomKillBanner.dismissed";

/** Only kills newer than this count as "recent" — an old kill on a machine
 *  that has been healthy since shouldn't nag forever. The control-plane sync
 *  runs every ~5 minutes, so the window comfortably covers propagation lag. */
const RECENT_WINDOW_MS = 30 * 60 * 1000;

/** Poll faster than useDaemonList's default (mount/focus only) while the
 *  banner is mounted so a fresh kill surfaces without a tab refocus. */
const POLL_INTERVAL_MS = 60_000;

const SIZE_NAMES: Record<number, string> = {
  [DaemonSize.SMALL]: "small",
  [DaemonSize.MEDIUM]: "medium",
  [DaemonSize.LARGE]: "large",
  [DaemonSize.XL]: "xl",
};

function readDismissed(): string {
  if (typeof window === "undefined") return "";
  try {
    return sessionStorage.getItem(DISMISS_KEY) ?? "";
  } catch {
    return "";
  }
}

function writeDismissed(sig: string): void {
  try {
    sessionStorage.setItem(DISMISS_KEY, sig);
  } catch {
    // sessionStorage can throw in private modes; non-fatal.
  }
}

function lastOomKillMs(d: Daemon): number {
  return d.lastOomKilledAt ? timestampDate(d.lastOomKilledAt).getTime() : 0;
}

/** "small · 4Gi", falling back gracefully when either half is unknown. */
function machineDescriptor(d: Daemon): string {
  const parts = [SIZE_NAMES[d.size], d.resources?.memoryLimit].filter(Boolean);
  return parts.join(" · ");
}

export function OomKillBanner() {
  const navigate = useNavigate();
  const { data: daemons = [] } = useDaemonList({
    refetchInterval: POLL_INTERVAL_MS,
  });
  const { activeDaemon } = useDaemonStatus();
  const [dismissedSig, setDismissedSig] = useState<string>(() => readDismissed());

  const oomDaemon = useMemo(() => {
    const cutoff = Date.now() - RECENT_WINDOW_MS;
    const recent = daemons.filter(
      (d) => d.oomKillCount > 0 && lastOomKillMs(d) > cutoff,
    );
    if (recent.length === 0) return null;
    // Prefer the daemon the app is currently attached to; otherwise surface
    // the most recently killed one.
    return (
      recent.find((d) => d.id === activeDaemon?.daemonId) ??
      recent.reduce((a, b) => (lastOomKillMs(a) >= lastOomKillMs(b) ? a : b))
    );
  }, [daemons, activeDaemon?.daemonId]);

  const sig = oomDaemon ? `${oomDaemon.id}:${lastOomKillMs(oomDaemon)}` : "";

  const dismiss = useCallback(() => {
    setDismissedSig(sig);
    writeDismissed(sig);
  }, [sig]);

  if (!oomDaemon) return null;
  if (dismissedSig && dismissedSig === sig) return null;

  const descriptor = machineDescriptor(oomDaemon);

  return (
    <div className="flex-shrink-0 border-t border-amber-500/30 bg-amber-500/10 px-4 py-3 text-sm text-amber-600 dark:text-amber-400">
      <div className="flex items-center justify-between gap-4">
        <div className="flex items-center gap-2">
          <MemoryStick className="h-4 w-4 flex-shrink-0" />
          <span>
            Your machine ran out of memory
            {descriptor ? ` (${descriptor})` : ""} — {oomDaemon.oomKillCount}
            &times; recently. Upgrade the machine size or reduce memory-heavy
            commands.
          </span>
        </div>
        <div className="flex flex-shrink-0 items-center gap-2">
          <button
            type="button"
            onClick={() =>
              navigate({
                to: "/settings/$section",
                params: { section: "environments" },
              })
            }
            className="inline-flex items-center gap-1 rounded-md border border-amber-500/40 bg-amber-500/20 px-3 py-1 text-xs font-medium transition-colors hover:bg-amber-500/30"
          >
            Upgrade machine size
            <ArrowUpRight className="h-3 w-3" />
          </button>
          <button
            type="button"
            onClick={dismiss}
            aria-label="Dismiss"
            className="rounded-md p-1 transition-colors hover:bg-amber-500/20"
          >
            <X className="h-3.5 w-3.5" />
          </button>
        </div>
      </div>
    </div>
  );
}
