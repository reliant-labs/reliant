import { Loader2 } from "lucide-react";
import { cn } from "../lib/utils";
import { DAEMON_CONNECTING_MESSAGE } from "../lib/daemon-errors";

interface DaemonConnectingStateProps {
  /** Extra classes for the outer container (layout/sizing). */
  className?: string;
  /** Optional reassuring sub-line under the headline. */
  detail?: string;
}

/**
 * Shared "Connecting to your environment…" placeholder shown while a cloud
 * daemon is still coming online. Rendered by daemon-RPC consumers (file tree,
 * etc.) IN PLACE OF a red error banner when the failure is the transient
 * `isDaemonConnectingError` class — with a spinner while the caller auto-retries.
 *
 * Mirrors the visual language of the onboarding DaemonConnectingGate so the
 * "your workspace is warming up" state reads consistently across the app.
 */
export function DaemonConnectingState({
  className,
  detail = "Your hosted workspace is coming online — this only takes a moment.",
}: DaemonConnectingStateProps) {
  return (
    <div
      className={cn("flex items-center justify-center h-full p-4", className)}
      role="status"
      aria-live="polite"
    >
      <div className="text-center space-y-2">
        <Loader2 className="w-8 h-8 animate-spin text-muted-foreground mx-auto" />
        <p className="text-sm text-foreground font-mono">
          {DAEMON_CONNECTING_MESSAGE}
        </p>
        {detail && <p className="text-xs text-muted-foreground">{detail}</p>}
      </div>
    </div>
  );
}
