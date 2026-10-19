// Copyright (c) 2025 Reliant Labs

/**
 * DiskPressureBanner — chat-view banner shown when a volume on the chat's
 * machine is (nearly) full, with what is using it and a way to free space.
 *
 * Why it exists: a cloud machine's build caches, docker data and persisted
 * system packages share one small volume. When it fills, builds, `gh`, apt and
 * docker fail with "no space left on device" while the home volume — the only
 * disk anything used to measure — is mostly empty, and the user is left
 * guessing. Data path: the daemon's diskwatch puts a DiskReport on every
 * heartbeat → the gateway stores it on the daemon row and announces a change
 * on the user stream → the shared `useDaemonList` query refetches it here. No
 * poll of its own.
 *
 * Two tones. Warning while the volume is under pressure; error once a command
 * on the machine has actually failed with ENOSPC (the daemon records it and
 * heartbeats at once), because then the user's work is failing now.
 *
 * A sibling of OomKillBanner and placed beside it. Dismissal is keyed on the
 * machine, the volume, how full it is (each halving of free space), whether
 * commands are failing and whether builds are paused (the daemon's disk guard
 * refusing heavy commands), in sessionStorage, so a worse state comes back.
 */
import { useCallback, useMemo, useState } from "react";
import { HardDrive, X } from "lucide-react";

import { useDaemonList } from "@/hooks/useOnboardingQueries";
import { useDaemonStatus } from "@/hooks/useDaemonStatus";
import { DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";
import { diskNotice, failingRecently, heavyRefused, pressuredVolume } from "@/lib/machineDisk";
import { cn } from "@/lib/utils";
import { FreeDiskSpaceDialog } from "../MachineDisk/FreeDiskSpaceDialog";

const DISMISS_KEY = "reliant.diskPressureBanner.dismissed";

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

/** Each halving of free space below 10% is a new band (mirrors the storage
 *  inbox item's signature), so "dismissed at 8% free" does not hide 1%. */
function freeBand(free: bigint, total: bigint): number {
  if (total <= 0n) return 0;
  const pct = (100 * Number(free)) / Number(total);
  let band = 0;
  for (let p = 10; pct < p && band < 6; p /= 2) band++;
  return band;
}

export function DiskPressureBanner({ daemonId }: { daemonId?: string | null }) {
  const { data: daemons = [] } = useDaemonList();
  const { activeDaemon } = useDaemonStatus();
  const [dismissedSig, setDismissedSig] = useState<string>(() => readDismissed());
  const [dialogOpen, setDialogOpen] = useState(false);

  // The chat's machine when the chat names one, else the one the app is on.
  const targetId = daemonId || activeDaemon?.daemonId;
  const daemon = useMemo(() => daemons.find((d) => d.daemonId === targetId), [daemons, targetId]);
  const volume = pressuredVolume(daemon?.disk);
  const now = Date.now();
  const sig = daemon && volume
    ? `${daemon.daemonId}:${volume.label}:${freeBand(volume.freeBytes, volume.totalBytes)}:${failingRecently(volume, now)}:${heavyRefused(volume)}`
    : "";

  const dismiss = useCallback(() => {
    setDismissedSig(sig);
    writeDismissed(sig);
  }, [sig]);

  if (!daemon || !volume) return null;
  if (dismissedSig && dismissedSig === sig && !dialogOpen) return null;

  const notice = diskNotice(volume, now);
  const machineLabel = daemon.name || daemon.hostname || "this machine";
  const online = daemon.status === DaemonStatus.ACTIVE;

  return (
    <div
      role={notice.tone === "error" ? "alert" : "status"}
      data-testid="disk-pressure-banner"
      data-tone={notice.tone}
      className={cn(
        "flex-shrink-0 border-t px-4 py-3 text-sm",
        notice.tone === "error"
          ? "border-destructive/30 bg-destructive/10 text-destructive-ink"
          : "border-warning/30 bg-warning/10 text-warning-ink",
      )}
    >
      <div className="flex items-center justify-between gap-4">
        <div className="flex min-w-0 items-start gap-2">
          <HardDrive className="mt-0.5 h-4 w-4 flex-shrink-0" aria-hidden="true" />
          <div className="min-w-0">
            <p className="font-medium">{notice.title}</p>
            <p className="text-xs text-foreground/80">
              {notice.usage}
              {notice.using ? ` — ${notice.using}` : ""}
            </p>
            {(notice.paused || notice.reclaimed) && (
              <p className="text-xs text-foreground/80" data-testid="disk-guard-note">
                {[notice.paused, notice.reclaimed && `${notice.reclaimed}.`].filter(Boolean).join(" ")}
              </p>
            )}
          </div>
        </div>
        <div className="flex flex-shrink-0 items-center gap-2">
          <button
            type="button"
            onClick={() => setDialogOpen(true)}
            className={cn(
              "inline-flex items-center gap-1 rounded-md border px-3 py-1 text-xs font-medium transition-colors",
              notice.tone === "error"
                ? "border-destructive/40 hover:bg-destructive/20"
                : "border-warning/40 hover:bg-warning/20",
            )}
          >
            Free up space
          </button>
          <button
            type="button"
            onClick={dismiss}
            aria-label="Dismiss"
            className="rounded-md p-1 transition-colors hover:bg-foreground/10"
          >
            <X className="h-3.5 w-3.5" />
          </button>
        </div>
      </div>
      {dialogOpen && (
        <FreeDiskSpaceDialog
          isOpen
          onClose={() => setDialogOpen(false)}
          daemonId={daemon.daemonId}
          machineLabel={machineLabel}
          volume={volume}
          online={online}
        />
      )}
    </div>
  );
}
