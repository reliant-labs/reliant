import { useCallback, useEffect, useRef, useState } from "react";
import { useDaemonStatus } from "@/hooks/useDaemonStatus";
import { cn } from "@/lib/utils";
import type { StepProps } from "../types";

type ChecklistItemStatus = "done" | "active" | "pending";

interface ChecklistItem {
  label: string;
  status: ChecklistItemStatus;
}

function StatusIcon({ status }: { status: ChecklistItemStatus }) {
  if (status === "done") {
    return (
      <svg
        className="h-3.5 w-3.5 text-green-500"
        fill="none"
        stroke="currentColor"
        viewBox="0 0 24 24"
      >
        <path
          strokeLinecap="round"
          strokeLinejoin="round"
          strokeWidth={2}
          d="M5 13l4 4L19 7"
        />
      </svg>
    );
  }
  if (status === "active") {
    return (
      <span className="inline-block h-3.5 w-3.5 animate-spin rounded-full border-2 border-primary border-t-transparent" />
    );
  }
  return (
    <span className="inline-block h-3 w-3 rounded-full border-2 border-border/60" />
  );
}

export function DaemonConnectStep({ onNext }: StepProps) {
  const { daemons, activeDaemon, loading } = useDaemonStatus();
  const [manualFeedback, setManualFeedback] = useState<string | null>(null);
  const hasAdvancedRef = useRef(false);
  const connected = Boolean(activeDaemon);

  const advanceOnce = useCallback(() => {
    if (hasAdvancedRef.current) return;
    hasAdvancedRef.current = true;
    onNext();
  }, [onNext]);

  useEffect(() => {
    if (connected) {
      setManualFeedback(null);
      const timer = setTimeout(advanceOnce, 800);
      return () => clearTimeout(timer);
    }
  }, [advanceOnce, connected]);

  const handleManualCheck = () => {
    if (activeDaemon) {
      setManualFeedback("Daemon connected. Moving on...");
      advanceOnce();
      return;
    }

    if (loading) {
      setManualFeedback(
        "Still checking daemon status. Keep this screen open and try again in a moment.",
      );
      return;
    }

    if (daemons.length > 0) {
      setManualFeedback(
        "A daemon was found, but it is not active yet. Make sure it is still running, then try again.",
      );
      return;
    }

    setManualFeedback(
      "No active daemon detected yet. Start the daemon, wait a few seconds, then check again.",
    );
  };

  const checklist: ChecklistItem[] = [
    { label: "Selecting workflow", status: "done" },
    { label: "Preparing environment", status: "done" },
    { label: "Connecting daemon", status: connected ? "done" : "active" },
  ];

  return (
    <div className="space-y-6">
      <div className="space-y-2 text-center">
        <h2 className="text-xl font-semibold text-foreground">
          Connect your daemon
        </h2>
        <p className="text-sm text-muted-foreground">
          {connected
            ? "Daemon connected. Moving on..."
            : loading
              ? "Checking the daemon registry for a local connection..."
              : "Start the daemon so Reliant can create or open your first project."}
        </p>
      </div>

      <div className="space-y-3 rounded-lg border border-border/50 bg-muted/30 px-4 py-3">
        {checklist.map((item) => (
          <div key={item.label} className="flex items-center gap-3">
            <StatusIcon status={item.status} />
            <span
              className={cn(
                "text-sm",
                item.status === "done"
                  ? "text-foreground"
                  : item.status === "active"
                    ? "font-medium text-foreground"
                    : "text-muted-foreground",
              )}
            >
              {item.label}
              {item.status === "active" && (
                <span className="ml-2 text-xs text-muted-foreground">
                  {loading ? "(checking...)" : "(polling...)"}
                </span>
              )}
            </span>
          </div>
        ))}
      </div>

      {!connected && (
        <div className="rounded-lg border border-border/40 bg-background p-3 space-y-1.5">
          <span className="block text-xs text-muted-foreground">
            Paste this in your terminal (with the token from the previous step):
          </span>
          <code className="block select-all font-mono text-xs text-foreground">
            echo "&lt;token&gt;" | reliant daemon start --token
          </code>
        </div>
      )}

      <div className="space-y-2">
        <button
          type="button"
          onClick={handleManualCheck}
          className="w-full rounded-lg bg-zinc-950 py-2.5 text-sm font-medium text-white transition-colors hover:bg-zinc-800 dark:bg-white dark:text-zinc-950 dark:hover:bg-zinc-200"
        >
          I've started the daemon — check connection
        </button>
        {manualFeedback && (
          <p className="text-center text-xs text-muted-foreground">
            {manualFeedback}
          </p>
        )}
      </div>
    </div>
  );
}
