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
 * An unpinned chat wakes whatever default resolution picks on the server.
 * Once its run is held for that machine, the line names the machine the
 * server's default resolves to (`defaultMachineDaemon`), the same rule the
 * new-chat picker preselects with. If the wake fails the send still goes
 * through; the tool card then reports the pending error and the run's machine
 * banner offers Wake it.
 *
 * A machine that FAILED to start is not "offline" and not "waking": nothing
 * will bring it back by itself, and a run held for it waits for nothing. The
 * line says so in the shared wait vocabulary (`classifyDaemonWait`, "Your
 * machine failed to start", with the control plane's reason) and offers the
 * ways forward: Try again (resume, which the control plane treats as a retry
 * that rebuilds the machine), Manage machines, or continue without it.
 *
 * Whenever the machine is unavailable, `continueWithoutMachine` (the
 * "Continue without machine" action) is offered beside the status: a branch
 * with no machine that carries the conversation and leaves this chat alone.
 *
 * When the chat has work waiting on that machine, the transcript footer states
 * the machine's status (ChatMachineNoticeLine) whatever the run is doing, and
 * this line keeps only the way out.
 */

import type { ReactNode } from "react";
import { DaemonStatus, type DaemonInfo } from "@/gen/reliant/v1/daemon_registry_pb";
import { useDaemonList, useResumeDaemon } from "@/hooks/useOnboardingQueries";
import StatusDot from "../forge-ui/status_dot";
import { DaemonWaitState } from "../DaemonWaitState";
import { machineDisplayName } from "@/lib/machineName";
import { defaultMachineDaemon } from "@/lib/chatMachine";
import { classifyDaemonWait, WAITING_FOR_MACHINE } from "@/lib/daemon-wait";

interface ComposerWakeStatusProps {
  /** A send for this chat is awaiting the server. */
  sending: boolean;
  /** The chat's pinned machine; unset for an unpinned chat. */
  daemonId?: string;
  /** The run is blocked on its machine (ChatActivity.WAITING_FOR_DAEMON). */
  waitingOnMachine?: boolean;
  /**
   * The transcript's footer is saying what the machine is doing, with its
   * Try again / Start it (ChatMachineNoticeLine). The composer then offers
   * only the way out, rather than a second copy of the status.
   */
  machineStatusInTranscript?: boolean;
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

export function ComposerWakeStatus({
  sending,
  daemonId,
  waitingOnMachine,
  machineStatusInTranscript,
  continueWithoutMachine,
}: ComposerWakeStatusProps) {
  if (machineStatusInTranscript) {
    if (!continueWithoutMachine) return null;
    return (
      <div className="flex-shrink-0 px-4 sm:px-6 lg:px-8" data-testid="composer-machine-exit">
        <div className="mx-auto max-w-[1200px]">
          <div className="forge-ui flex flex-wrap items-center gap-x-3 px-1 pb-1.5 text-xs text-muted-foreground">
            {continueWithoutMachine}
          </div>
        </div>
      </div>
    );
  }
  if (daemonId) {
    // The registry is only read while it could matter: a chat pinned to a
    // machine, or a run held for one. (It is a shared, cached query; most
    // reads find it warm.)
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
  return <UnpinnedWaitLine continueWithoutMachine={continueWithoutMachine} />;
}

const WAITING_LABEL = `${WAITING_FOR_MACHINE}…`;

function UnpinnedWaitLine({ continueWithoutMachine }: { continueWithoutMachine?: ReactNode }) {
  const { data: daemons } = useDaemonList();
  const daemon = daemons ? defaultMachineDaemon(daemons) : undefined;
  if (daemon?.status === DaemonStatus.FAILED) {
    return <FailedMachineLine daemon={daemon} continueWithoutMachine={continueWithoutMachine} />;
  }
  if (daemon && needsWake(daemon.status)) {
    return <WakingLine name={machineDisplayName(daemon)} continueWithoutMachine={continueWithoutMachine} />;
  }
  return (
    <StatusLine label={WAITING_LABEL} pulse continueWithoutMachine={continueWithoutMachine}>
      {WAITING_LABEL}
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

  if (daemon?.status === DaemonStatus.FAILED) {
    return <FailedMachineLine daemon={daemon} continueWithoutMachine={continueWithoutMachine} />;
  }
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
    return <WakingLine name={name} continueWithoutMachine={continueWithoutMachine} />;
  }
  return null;
}

function WakingLine({ name, continueWithoutMachine }: { name: string; continueWithoutMachine?: ReactNode }) {
  const label = `Waking ${name}…`;
  return (
    <StatusLine label={label} pulse continueWithoutMachine={continueWithoutMachine}>
      Waking <span className="font-medium text-foreground">{name}</span>…
    </StatusLine>
  );
}

/**
 * The machine failed to start. Said with the shared wait vocabulary and its
 * exits, because this is the state where the user has to do something: Try
 * again resumes the machine, which the control plane treats as a retry that
 * rebuilds it on the current image.
 */
function FailedMachineLine({
  daemon,
  continueWithoutMachine,
}: {
  daemon: DaemonInfo;
  continueWithoutMachine?: ReactNode;
}) {
  const resume = useResumeDaemon();
  const state = classifyDaemonWait({ daemon, elapsedMs: 0, isCloud: true });
  return (
    <div className="flex-shrink-0 px-4 sm:px-6 lg:px-8" data-testid="composer-machine-failed">
      <div className="mx-auto max-w-[1200px]">
        <DaemonWaitState
          variant="inline"
          state={state}
          onRetry={resume.isPending ? undefined : () => resume.mutate(daemon.daemonId)}
          className="rounded-md border border-border/60"
        />
        {continueWithoutMachine && <div className="px-1 pt-1 pb-1.5">{continueWithoutMachine}</div>}
      </div>
    </div>
  );
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
