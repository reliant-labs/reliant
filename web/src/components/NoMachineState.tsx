/**
 * What a machine-backed surface (files, terminal, search, changes, the editor)
 * shows when the account has no machine at all — see lib/daemon-errors isNoMachineError.
 *
 * Not an error and not a wait: nothing is broken, and nothing will arrive on
 * its own. It says what the surface needs and offers the one thing that
 * provides it. When a machine does connect (from here or anywhere else), the
 * surface is told to load again, so it never needs a manual refresh.
 *
 * Layout follows DaemonWaitState's variants, so swapping one state for the
 * other does not move the surface around.
 */

import { useEffect, useRef, useState } from "react";
import { MonitorOff, Plus } from "lucide-react";

import { cn } from "../lib/utils";
import { useDaemonStatus } from "../hooks/useDaemonStatus";
import { ConnectDaemonModal } from "./Layout/ConnectDaemonModal";

export interface NoMachineStateProps {
  /** What the machine is for here, completing "Connect a machine to …". */
  purpose: string;
  /** `panel` fills an empty region; `overlay` is a card the host floats. */
  variant?: "panel" | "overlay";
  /** Load the surface again — called once a machine is connected. */
  onMachineConnected?: () => void;
  className?: string;
}

export function NoMachineState({
  purpose,
  variant = "panel",
  onMachineConnected,
  className,
}: NoMachineStateProps) {
  const [connecting, setConnecting] = useState(false);
  const { activeDaemon } = useDaemonStatus();

  // A machine appearing is the signal to retry, however it was connected.
  const hadMachine = useRef(!!activeDaemon);
  useEffect(() => {
    const hasMachine = !!activeDaemon;
    if (hasMachine && !hadMachine.current) onMachineConnected?.();
    hadMachine.current = hasMachine;
  }, [activeDaemon, onMachineConnected]);

  const body = (
    <>
      <MonitorOff
        className={cn("text-muted-foreground", variant === "overlay" ? "h-4 w-4" : "h-7 w-7")}
        aria-hidden="true"
      />
      <div className="space-y-1">
        <p className="text-sm font-medium text-foreground">No machine connected</p>
        <p className="text-xs text-muted-foreground">
          {`Connect a machine to ${purpose}. Chats work without one.`}
        </p>
      </div>
      <button
        type="button"
        onClick={() => setConnecting(true)}
        className="inline-flex items-center gap-1.5 rounded-md bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground transition-opacity hover:opacity-90"
      >
        <Plus className="h-3.5 w-3.5" aria-hidden="true" />
        Connect a machine
      </button>
      <ConnectDaemonModal isOpen={connecting} onClose={() => setConnecting(false)} />
    </>
  );

  if (variant === "overlay") {
    return (
      <div
        role="status"
        data-testid="no-machine-state"
        className={cn(
          "flex max-w-sm flex-col items-center gap-2 rounded-lg border border-border bg-card px-4 py-3 text-center shadow-lg",
          className,
        )}
      >
        {body}
      </div>
    );
  }

  return (
    <div
      role="status"
      data-testid="no-machine-state"
      className={cn("flex h-full items-center justify-center p-6", className)}
    >
      <div className="flex max-w-sm flex-col items-center gap-3 text-center">{body}</div>
    </div>
  );
}
