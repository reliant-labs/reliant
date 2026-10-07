/**
 * "Waking <machine>…" on the composer's status line (research/WORKFLOW_UI.md
 * §9.2), while an attended send wakes the chat's machine — and, when the
 * machine is unavailable, the offer to continue without it
 * (research/NO_MACHINE_CHATS.md §2.3).
 *
 * The signal is facts the web already has, nothing inferred from timing:
 *
 *   1. A send is in flight. The server wakes the daemon INSIDE the send RPC
 *      (wakeDaemonForAttendedTurn, best effort, bounded at 30 s) and only then
 *      answers, so an in-flight send against a sleeping machine is a wake in
 *      progress.
 *   2. The machine that send wakes is the chat's pinned daemon
 *      (chat.activeDaemonId), and the registry says it is asleep or starting.
 *   3. The run is blocked on its machine (ChatActivity.WAITING_FOR_DAEMON), or
 *      the registry says the pinned machine is offline, failed or gone.
 *
 * An unpinned chat wakes whatever default resolution picks on the server,
 * which the web cannot see — so it gets no machine name, only the
 * server-reported "waiting" state. If the wake fails the send still goes
 * through; the tool card then reports the pending error and the run's machine
 * banner offers Wake it.
 *
 * Whenever the machine is unavailable, `continueWithoutMachine` (the
 * "Continue without machine" action) is offered beside the status: a branch
 * with no machine that carries the conversation and leaves this chat alone.
 */

import type { ReactNode } from "react";
import { DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";
import { useDaemonList } from "@/hooks/useOnboardingQueries";
import StatusDot from "../forge-ui/status_dot";
import { machineDisplayName } from "@/lib/machineName";

interface ComposerWakeStatusProps {
  /** A send for this chat is awaiting the server. */
  sending: boolean;
  /** The chat's pinned machine; unset for an unpinned chat. */
  daemonId?: string;
  /** The run is blocked on its machine (ChatActivity.WAITING_FOR_DAEMON). */
  waitingOnMachine?: boolean;
  /** "Continue without machine", shown whenever the machine is unavailable. */
  continueWithoutMachine?: ReactNode;
}

/** The registry states a send has to wake: asleep, or already starting. */
function needsWake(status: DaemonStatus): boolean {
  return status === DaemonStatus.SUSPENDED || status === DaemonStatus.PENDING;
}

/** The registry states nothing on this side can bring back. */
function isOffline(status: DaemonStatus): boolean {
  return status === DaemonStatus.DISCONNECTED || status === DaemonStatus.FAILED;
}

export function ComposerWakeStatus({ sending, daemonId, waitingOnMachine, continueWithoutMachine }: ComposerWakeStatusProps) {
  if (daemonId) {
    // The registry is only read while it could matter: a chat pinned to a
    // machine. (It is a shared, cached query; most reads find it warm.)
    return (
      <WakeLine
        daemonId={daemonId}
        sending={sending}
        waitingOnMachine={!!waitingOnMachine}
        continueWithoutMachine={continueWithoutMachine}
      />
    );
  }
  if (!waitingOnMachine) return null;
  return (
    <StatusLine label="Waiting for your machine…" pulse continueWithoutMachine={continueWithoutMachine}>
      Waiting for your machine…
    </StatusLine>
  );
}

function WakeLine({
  daemonId,
  sending,
  waitingOnMachine,
  continueWithoutMachine,
}: {
  daemonId: string;
  sending: boolean;
  waitingOnMachine: boolean;
  continueWithoutMachine?: ReactNode;
}) {
  const { data: daemons } = useDaemonList();
  // Undefined until the registry has answered: absence is only evidence the
  // machine is gone once there is a list to be absent from.
  if (!daemons) return null;
  const daemon = daemons.find((d) => d.daemonId === daemonId);
  const name = daemon ? machineDisplayName(daemon) : "your machine";

  if (!daemon || isOffline(daemon.status)) {
    const label = daemon ? `${name} is offline` : "This chat's machine is no longer available";
    return (
      <StatusLine label={label} variant="error" continueWithoutMachine={continueWithoutMachine}>
        {daemon ? (
          <>
            <span className="font-medium text-foreground">{name}</span> is offline
          </>
        ) : (
          label
        )}
      </StatusLine>
    );
  }
  if (needsWake(daemon.status) && (sending || waitingOnMachine)) {
    const label = `Waking ${name}…`;
    return (
      <StatusLine label={label} pulse continueWithoutMachine={continueWithoutMachine}>
        Waking <span className="font-medium text-foreground">{name}</span>…
      </StatusLine>
    );
  }
  return null;
}

function StatusLine({
  label,
  pulse,
  variant = "pending",
  continueWithoutMachine,
  children,
}: {
  label: string;
  pulse?: boolean;
  variant?: "pending" | "error";
  continueWithoutMachine?: ReactNode;
  children: ReactNode;
}) {
  return (
    <div className="flex-shrink-0 px-4 sm:px-6 lg:px-8">
      <div className="mx-auto max-w-[1200px]">
        <div className="forge-ui flex flex-wrap items-center gap-x-3 gap-y-0.5 px-1 pb-1.5 text-xs text-muted-foreground">
          <p role="status" aria-label={label} className="flex items-center gap-2" data-testid="composer-wake-status">
            <StatusDot variant={variant} pulse={pulse} size="sm" />
            <span>{children}</span>
          </p>
          {continueWithoutMachine}
        </div>
      </div>
    </div>
  );
}
