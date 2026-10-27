import { useMemo, useState } from "react";
import { Loader2, Plus } from "lucide-react";
import { useMutation } from "@tanstack/react-query";

import Status from "@/components/forge-ui/status";
import {
  DaemonStatus,
  type DaemonInfo as CloudDaemon,
} from "@/gen/reliant/v1/daemon_registry_pb";
import { useDaemonList, useResumeDaemon } from "@/hooks/useOnboardingQueries";
import { capabilities } from "@/services/controlPlane/capabilities";
import { deleteDaemon } from "@/services/controlPlane/daemon";
import { toast } from "@/lib/toast-manager";
import { cn } from "@/lib/utils";

import { AddMachineSheet } from "../../Machines";
import { failureReason, isFailedDaemon } from "../cloneTargets";
import { cloudDaemonStatusLabel } from "../cloudDaemonStatusLabel";
import { isCloudDaemon } from "./format";
import { pickerButton } from "./buttonStyles";
import { machineDisplayName } from "@/lib/machineName";

/**
 * Machine ("daemon" internally) status, shown only where having no active
 * machine blocks adding a project (the first-run state):
 *
 *   - checking       a pending strip while the registry first loads
 *   - none active    a bordered panel listing every cloud machine with the
 *                    action it actually supports (resume / delete-failed),
 *                    plus a link to add a machine in Settings → Machines
 *
 * There is deliberately no "connected" strip: a project is not tied to the
 * machine that happens to be connected (it can be checked out on several,
 * and each chat picks its own), so a strip saying so was false.
 *
 * The panel coexists with the add-project actions rather than replacing
 * them, so a web user whose only machine FAILED still has a way to add a
 * project — see docs/findings/add-github-project-2026-09-30.md.
 */

type StatusTone = "success" | "warning" | "danger" | "neutral";

// cloudDaemonStatusLabel's vocabulary → Status tones. Colour carries the
// meaning; the label names it.
const STATUS_DOT_VARIANT: Record<string, StatusTone> = {
  active: "success",
  starting: "neutral",
  resuming: "neutral",
  suspended: "neutral",
  disconnected: "warning",
  failed: "danger",
};

const STRIP = "flex flex-wrap items-center gap-x-3 gap-y-2 rounded-lg border border-border bg-card px-3 py-2";

/** Shown while the machine list is loading, or the local machine is starting. */
export function MachineStatusPending({ message }: { message: string }) {
  return (
    <div className={STRIP} role="status">
      <Status tone="neutral" size="md" pulse />
      <span className="text-sm text-ink-muted">{message}</span>
    </div>
  );
}

/**
 * No active machine. For a local-only deployment this is a single "connect"
 * call to action; for a cloud account it lists every cloud machine with the
 * one action its state supports.
 */
export function NoActiveMachinePanel() {
  const [error, setError] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);
  const hasCloud = capabilities.cloudDaemons;

  // One list, from the registry — the service that knows both whether a
  // machine has actually attached AND what it is doing (see
  // docs/design/one-daemon-list.md). It is the shared daemon list, not a copy
  // polled every 8s: a machine waking up reaches it as a lifecycle event and
  // then an attach, and both are pushed.
  const { data: allDaemons, isLoading: listLoading, refetch } = useDaemonList();
  const cloudDaemons = useMemo<CloudDaemon[] | undefined>(
    () => allDaemons?.filter((d) => isCloudDaemon(d.daemonType)),
    [allDaemons],
  );
  const isLoading = hasCloud && listLoading;

  // The hook owns the ResourceExhausted-with-reason suppression (the global
  // upgradeInterceptor already opened the modal); only non-reasoned errors
  // reach here.
  const resumeDaemonMutation = useResumeDaemon({
    onSuccess: async () => {
      // The page flips to the connected strip once useDaemonStatus sees the
      // machine go ACTIVE; refetching shows the transition in the meantime.
      await refetch();
    },
    onError: (err) => {
      const msg = err instanceof Error ? err.message : "Failed to resume machine";
      setError(msg);
      toast.error(msg);
    },
  });
  const resumingId =
    resumeDaemonMutation.isPending && typeof resumeDaemonMutation.variables === "string"
      ? resumeDaemonMutation.variables
      : null;

  // A FAILED machine cannot be resumed — provisioning never completed, so
  // there is nothing to wake. The only recovery is to delete it and create a
  // new one. Delete is offered for FAILED cloud machines only; a working
  // machine is never removable from here.
  const deleteDaemonMutation = useMutation({
    mutationFn: (daemonId: string) => deleteDaemon(daemonId),
    onSuccess: async () => {
      setError(null);
      await refetch();
    },
    onError: (err) => {
      const msg = err instanceof Error ? err.message : "Failed to delete machine";
      setError(msg);
      toast.error(msg);
    },
  });
  const deletingId =
    deleteDaemonMutation.isPending && typeof deleteDaemonMutation.variables === "string"
      ? deleteDaemonMutation.variables
      : null;

  const handleResume = (daemon: CloudDaemon) => {
    if (daemon.status !== DaemonStatus.SUSPENDED) return;
    setError(null);
    resumeDaemonMutation.mutate(daemon.daemonId);
  };

  const handleDeleteFailed = (daemon: CloudDaemon) => {
    if (!isFailedDaemon(daemon)) return;
    if (
      !window.confirm(
        `Delete ${machineDisplayName(daemon)}? It failed to start and can't be recovered. You can create a new one afterwards.`,
      )
    ) {
      return;
    }
    setError(null);
    deleteDaemonMutation.mutate(daemon.daemonId);
  };

  const daemons = hasCloud ? (cloudDaemons ?? []) : [];
  const hasAnyCloudDaemon = daemons.length > 0;
  // "Resume a machine" is a lie when every machine failed — there is nothing
  // resumable, and the user's next step is to replace one.
  const allFailed = hasAnyCloudDaemon && daemons.every(isFailedDaemon);

  const title = !hasCloud
    ? "No machine connected"
    : allFailed
      ? "Your machine needs attention"
      : hasAnyCloudDaemon
        ? "Resume a machine"
        : "Start a machine";
  const description = !hasCloud
    ? "Reliant runs tools on a machine that can see your files. Add one to open and work on projects."
    : allFailed
      ? "Every machine on your account failed to start. Delete the failed one, then add a new one."
      : hasAnyCloudDaemon
        ? "Your machines are asleep. Resume one to work on its projects; this page updates when it connects."
        : "You don't have a machine yet. Start one in the cloud or add your own computer.";

  return (
    <section
      className="rounded-lg border border-border bg-card"
      aria-labelledby="no-active-machine-title"
      data-testid="no-active-machine"
    >
      <div className="flex flex-wrap items-start justify-between gap-3 p-4">
        <div className="flex min-w-0 items-start gap-3">
          <Status
            tone={allFailed ? "danger" : "warning"}
            size="md"
            className="mt-1.5"
          />
          <div className="min-w-0">
            <h2 id="no-active-machine-title" className="text-sm font-semibold text-ink">
              {title}
            </h2>
            <p className="mt-0.5 max-w-prose text-pretty text-sm text-ink-muted">{description}</p>
          </div>
        </div>
        <button
          type="button"
          onClick={() => setAdding(true)}
          className={pickerButton(hasAnyCloudDaemon && !allFailed ? "secondary" : "primary", "sm")}
          data-testid="no-active-machine-add"
        >
          <Plus className="h-3.5 w-3.5" aria-hidden="true" />
          Add a machine
        </button>
        {/* Kept mounted, so a machine still starting after "Keep working" reports in. */}
        <AddMachineSheet open={adding} onClose={() => setAdding(false)} onReady={() => void refetch()} />
      </div>

      {hasCloud && isLoading && (
        <div className="flex items-center gap-2 border-t border-border px-4 py-3 text-sm text-ink-muted">
          <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
          Checking your machines…
        </div>
      )}

      {hasAnyCloudDaemon && (
        <ul className="divide-y divide-border border-t border-border">
          {daemons.map((daemon) => {
            const isResuming = resumingId === daemon.daemonId;
            const failed = isFailedDaemon(daemon);
            const isSuspended = daemon.status === DaemonStatus.SUSPENDED;
            const isDeleting = deletingId === daemon.daemonId;
            const statusLabel = cloudDaemonStatusLabel(daemon, isResuming);
            const name = machineDisplayName(daemon);
            // A failed machine's reason is the most useful thing on its row
            // ("Storage request exceeds your plan's limit"), so it is shown in
            // full in the danger tone. Anything else shows its last message,
            // muted and truncated.
            const detail = failed
              ? (() => {
                  const reason = failureReason(daemon);
                  return reason ? `Failed to start: ${reason}` : "Failed to start. No reason was reported.";
                })()
              : daemon.lastStatusMessage || null;

            return (
              <li
                key={daemon.daemonId}
                className="flex items-center gap-3 px-4 py-2.5"
                data-testid={failed ? `failed-daemon-${daemon.daemonId}` : `machine-row-${daemon.daemonId}`}
              >
                <Status
                  tone={STATUS_DOT_VARIANT[statusLabel] ?? "neutral"}
                  pulse={statusLabel === "starting" || statusLabel === "resuming"}
                />
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2">
                    <span className="truncate text-sm font-medium text-ink">{name}</span>
                    <span className="text-xs capitalize text-ink-muted">{statusLabel}</span>
                  </div>
                  {detail && (
                    <p
                      className={cn(
                        "mt-0.5 text-xs",
                        failed ? "text-pretty text-danger" : "truncate text-ink-muted",
                      )}
                    >
                      {detail}
                    </p>
                  )}
                </div>
                {failed ? (
                  <button
                    type="button"
                    onClick={() => handleDeleteFailed(daemon)}
                    disabled={isDeleting}
                    aria-label={`Delete ${name}`}
                    className={pickerButton("danger", "sm")}
                  >
                    {isDeleting ? "Deleting…" : "Delete"}
                  </button>
                ) : isSuspended || isResuming ? (
                  <button
                    type="button"
                    onClick={() => handleResume(daemon)}
                    disabled={isResuming}
                    aria-label={`Resume ${name}`}
                    className={pickerButton("secondary", "sm")}
                  >
                    {isResuming && <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />}
                    {isResuming ? "Resuming…" : "Resume"}
                  </button>
                ) : null}
              </li>
            );
          })}
        </ul>
      )}

      {error && (
        <p className="border-t border-border px-4 py-2.5 text-xs text-danger" role="alert">
          {error}
        </p>
      )}

    </section>
  );
}
