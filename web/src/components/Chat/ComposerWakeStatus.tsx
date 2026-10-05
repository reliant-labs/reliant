/**
 * "Waking <machine>…" on the composer's status line (research/WORKFLOW_UI.md
 * §9.2), while an attended send wakes the chat's machine.
 *
 * The signal is two facts the web already has, nothing inferred from timing:
 *
 *   1. A send is in flight. The server wakes the daemon INSIDE the send RPC
 *      (wakeDaemonForAttendedTurn, best effort, bounded at 30 s) and only then
 *      answers, so an in-flight send against a sleeping machine is a wake in
 *      progress.
 *   2. The machine that send wakes is the chat's pinned daemon
 *      (chat.activeDaemonId), and the registry says it is asleep or starting.
 *
 * An unpinned chat wakes whatever default resolution picks on the server,
 * which the web cannot see — so it gets no line rather than a guessed name.
 * If the wake fails the send still goes through; the tool card then reports
 * the pending error and the run's machine banner offers Wake it.
 */

import { DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";
import { useDaemonList } from "@/hooks/useOnboardingQueries";
import StatusDot from "../forge-ui/status_dot";

interface ComposerWakeStatusProps {
  /** A send for this chat is awaiting the server. */
  sending: boolean;
  /** The chat's pinned machine; unset for an unpinned chat. */
  daemonId?: string;
}

/** The registry states a send has to wake: asleep, or already starting. */
function needsWake(status: DaemonStatus): boolean {
  return status === DaemonStatus.SUSPENDED || status === DaemonStatus.PENDING;
}

export function ComposerWakeStatus({ sending, daemonId }: ComposerWakeStatusProps) {
  // The registry is only read while it could matter: a send in flight for a
  // pinned chat. (It is a shared, cached query; most sends find it warm.)
  if (!sending || !daemonId) return null;
  return <WakeLine daemonId={daemonId} />;
}

function WakeLine({ daemonId }: { daemonId: string }) {
  const { data: daemons } = useDaemonList();
  const daemon = daemons?.find((d) => d.daemonId === daemonId);
  if (!daemon || !needsWake(daemon.status)) return null;

  const label = `Waking ${daemon.hostname || "your machine"}…`;
  return (
    <div className="flex-shrink-0 px-4 sm:px-6 lg:px-8">
      <div className="mx-auto max-w-[1200px]">
        <p
          role="status"
          aria-label={label}
          className="forge-ui flex items-center gap-2 px-1 pb-1.5 text-xs text-muted-foreground"
          data-testid="composer-wake-status"
        >
          <StatusDot variant="pending" pulse size="sm" />
          <span>
            Waking <span className="font-medium text-foreground">{daemon.hostname || "your machine"}</span>…
          </span>
        </p>
      </div>
    </div>
  );
}
