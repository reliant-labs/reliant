import { useEffect, useState } from "react";
import { Loader2, RotateCw } from "lucide-react";

import { DaemonStatus, type DaemonInfo } from "@/gen/reliant/v1/daemon_registry_pb";

/** How long a machine connection may take before the wait offers a way out. */
export const CONNECT_WAIT_SLOW_MS = 30_000;

const STATUS_LABEL: Partial<Record<DaemonStatus, string>> = {
  [DaemonStatus.ACTIVE]: "connected",
  [DaemonStatus.PENDING]: "starting",
  [DaemonStatus.SUSPENDED]: "asleep",
  [DaemonStatus.DISCONNECTED]: "disconnected",
  [DaemonStatus.FAILED]: "failed",
};

/** What this app can see about the machine connection, in plain lines. Pure. */
export function connectionFacts(input: {
  machines: ReadonlyArray<Pick<DaemonInfo, "status">>;
  unreachable: boolean;
  desktop: boolean;
  bundledDaemonConnected: boolean | null;
}): string[] {
  const lines: string[] = [];
  lines.push(
    input.unreachable
      ? "Reliant's servers: not answering from this device. Check your network."
      : "Reliant's servers: reachable.",
  );
  if (input.machines.length === 0) {
    lines.push("Machines on your account: none yet.");
  } else {
    const states = input.machines
      .map((m) => STATUS_LABEL[m.status] ?? "unknown")
      .join(", ");
    lines.push(`Machines on your account: ${input.machines.length} (${states}).`);
  }
  if (input.desktop) {
    lines.push(
      input.bundledDaemonConnected === null
        ? "This app's built-in machine: checking…"
        : input.bundledDaemonConnected
          ? "This app's built-in machine: connected."
          : "This app's built-in machine: not connected. Quit and reopen Reliant to restart it.",
    );
  }
  return lines;
}

interface ConnectWaitProps {
  /** What is being waited for, e.g. "Waiting for your computer to connect". */
  label: string;
  /** Ask the registry again now. */
  onRetry: () => void;
  daemons: ReadonlyArray<Pick<DaemonInfo, "status">>;
  unreachable?: boolean;
  /** A way past the wait, offered with the retry once it is slow. */
  skip?: { label: string; onClick: () => void };
}

/**
 * Progress for a wait on a machine: an elapsed counter from the first second,
 * and — once it passes CONNECT_WAIT_SLOW_MS — a retry, a plain-language read of
 * what the app can see, and optionally a way past. One ed4d08 session sat
 * 4m36s on a bare spinner for a machine that was never coming.
 */
export function ConnectWait({ label, onRetry, daemons, unreachable = false, skip }: ConnectWaitProps) {
  const [elapsedMs, setElapsedMs] = useState(0);
  const [bundledConnected, setBundledConnected] = useState<boolean | null>(null);
  const [showFacts, setShowFacts] = useState(false);
  const desktop = typeof window !== "undefined" && !!window.electronAPI;

  useEffect(() => {
    const startedAt = Date.now();
    const id = setInterval(() => setElapsedMs(Date.now() - startedAt), 1000);
    return () => clearInterval(id);
  }, []);

  const slow = elapsedMs >= CONNECT_WAIT_SLOW_MS;

  useEffect(() => {
    // Not on the typed ElectronAPI (electron/src/preload.js exposes it; the
    // same cast is in hooks/useDaemonStatus).
    const ask = (window as unknown as { electronAPI?: { isDaemonConnected?: () => Promise<boolean> } })
      .electronAPI?.isDaemonConnected;
    if (!slow || !desktop || !ask) return;
    let cancelled = false;
    ask()
      .then((connected) => !cancelled && setBundledConnected(Boolean(connected)))
      .catch(() => !cancelled && setBundledConnected(false));
    return () => {
      cancelled = true;
    };
  }, [slow, desktop]);

  const seconds = Math.floor(elapsedMs / 1000);

  return (
    <div className="space-y-3" role="status" aria-live="polite" data-testid="connect-wait">
      <p className="flex items-center gap-2 text-sm text-muted-foreground">
        <Loader2 className="h-4 w-4 animate-spin" aria-hidden />
        <span>
          {label}… <span data-testid="connect-wait-elapsed">{seconds}s</span>
        </span>
      </p>
      {slow && (
        <div className="space-y-3 rounded-lg border border-warning/30 bg-warning/5 p-3" data-testid="connect-wait-slow">
          <p className="text-sm text-warning-ink">This is taking longer than usual.</p>
          <div className="flex flex-wrap gap-2">
            <button
              type="button"
              onClick={onRetry}
              className="inline-flex items-center gap-1.5 rounded-lg border border-border bg-background px-3 py-1.5 text-sm font-medium text-foreground transition-colors hover:bg-muted"
            >
              <RotateCw className="h-3.5 w-3.5" aria-hidden />
              Retry
            </button>
            <button
              type="button"
              aria-expanded={showFacts}
              onClick={() => setShowFacts((open) => !open)}
              className="rounded-lg border border-border bg-background px-3 py-1.5 text-sm font-medium text-foreground transition-colors hover:bg-muted"
            >
              What&apos;s happening?
            </button>
            {skip && (
              <button
                type="button"
                onClick={skip.onClick}
                className="rounded-lg px-3 py-1.5 text-sm text-muted-foreground transition-colors hover:text-foreground"
              >
                {skip.label}
              </button>
            )}
          </div>
          {showFacts && (
            <ul className="list-disc space-y-1 pl-5 text-xs text-muted-foreground" data-testid="connect-wait-facts">
              {connectionFacts({ machines: daemons, unreachable, desktop, bundledDaemonConnected: bundledConnected }).map(
                (line) => (
                  <li key={line}>{line}</li>
                ),
              )}
            </ul>
          )}
        </div>
      )}
    </div>
  );
}
