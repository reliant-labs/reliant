// Copyright (c) 2026 Reliant Labs

/**
 * MachineHealthBanner — chat-view banner shown when the chat's machine is
 * connected but cannot do work: its daemon reports it degraded (today: its
 * process table is full, so no command can start).
 *
 * Why it exists: on 2026-10-30 a machine's process table filled and every
 * command on it failed for 24 minutes, while the app showed it connected and
 * healthy and agents retried in a loop (reliant#1919). Data path: the daemon's
 * fork watchdog reports MachineHealth on its heartbeat → the gateway stores it
 * on the attachment lease → the registry returns it as DaemonInfo.health, and
 * a heartbeat whose health disagrees with the cached list re-reads it. No poll
 * of its own.
 *
 * A sibling of DiskPressureBanner, placed beside it. Dismissal is keyed on the
 * machine and when it became degraded, in sessionStorage, so a new episode
 * comes back.
 */
import { useCallback, useMemo, useState } from "react";
import { Cpu, X } from "lucide-react";

import { useDaemonList } from "@/hooks/useOnboardingQueries";
import { useDaemonStatus } from "@/hooks/useDaemonStatus";
import { degradedReason } from "@/lib/machineHealth";
import { statusBannerFrame, useStatusSlotCollapsed, useStatusSlotSummary } from "./ComposerStatusSlot";

const DISMISS_KEY = "reliant.machineHealthBanner.dismissed";

const TITLE = "This machine can't start new processes";

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

export function MachineHealthBanner({ daemonId }: { daemonId?: string | null }) {
  const { data: daemons = [] } = useDaemonList();
  const { activeDaemon } = useDaemonStatus();
  const [dismissedSig, setDismissedSig] = useState<string>(() => readDismissed());

  // The chat's machine when the chat names one, else the one the app is on.
  const targetId = daemonId || activeDaemon?.daemonId;
  const daemon = useMemo(() => daemons.find((d) => d.daemonId === targetId), [daemons, targetId]);
  const reason = degradedReason(daemon);
  const sig = daemon && reason ? `${daemon.daemonId}:${daemon.health?.sinceMs ?? 0n}` : "";

  const dismiss = useCallback(() => {
    setDismissedSig(sig);
    writeDismissed(sig);
  }, [sig]);

  const visible = !!reason && dismissedSig !== sig;
  useStatusSlotSummary(visible ? { label: TITLE, tone: "danger" } : null);
  const inSheet = useStatusSlotCollapsed();

  if (!visible) return null;

  return (
    <div
      role="alert"
      data-testid="machine-health-banner"
      className={statusBannerFrame(
        inSheet,
        "flex-shrink-0 border-t border-destructive/30 px-4 py-3",
        "bg-destructive/10 text-sm text-destructive-ink",
      )}
    >
      <div className="flex items-center justify-between gap-4">
        <div className="flex min-w-0 items-start gap-2">
          <Cpu className="mt-0.5 h-4 w-4 flex-shrink-0" aria-hidden="true" />
          <div className="min-w-0">
            <p className="font-medium">{TITLE}</p>
            <p className="text-xs text-foreground/80">{reason}</p>
          </div>
        </div>
        <button
          type="button"
          onClick={dismiss}
          aria-label="Dismiss"
          className="inline-flex flex-shrink-0 items-center justify-center rounded-md p-1 transition-colors hover:bg-foreground/10 pointer-coarse:min-h-11 pointer-coarse:min-w-11"
        >
          <X className="h-3.5 w-3.5" />
        </button>
      </div>
    </div>
  );
}
