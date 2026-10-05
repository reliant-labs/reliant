import { useState } from "react";
import { Loader2, Plus } from "lucide-react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { create } from "@bufbuild/protobuf";

import StatusDot from "@/components/forge-ui/status_dot";
import Badge from "@/components/forge-ui/badge";
import { grpcClient } from "@/api/grpc-client";
import {
  DaemonStatus,
  ListDaemonsRequestSchema,
  type DaemonInfo as CloudDaemon,
} from "@/gen/reliant/v1/daemon_registry_pb";
import { useResumeDaemon } from "@/hooks/useOnboardingQueries";
import { capabilities } from "@/services/controlPlane/capabilities";
import { deleteDaemon } from "@/services/controlPlane/daemon";
import { toast } from "@/lib/toast-manager";
import { cn } from "@/lib/utils";

import { ConnectDaemonModal } from "../ConnectDaemonModal";
import { failureReason, isFailedDaemon } from "../cloneTargets";
import { cloudDaemonStatusLabel } from "../cloudDaemonStatusLabel";
import { isLocalDaemonType } from "../addProjectActions";
import { isCloudDaemon } from "./format";
import { pickerButton } from "./buttonStyles";

/**
 * The picker's machine ("daemon" internally) status, in one of three shapes:
 *
 *   - connected      a one-line strip: dot, machine name, kind, add-machine
 *   - checking       the same strip, pending, while the registry first loads
 *   - none active    a bordered panel listing every cloud machine with the
 *                    action it actually supports (resume / delete-failed),
 *                    plus "connect a machine"
 *
 * The no-active panel coexists with the add-project actions rather than
 * replacing them. They used to be mutually exclusive, so a web user whose
 * only machine had FAILED lost every add-project entry point — see
 * docs/findings/add-github-project-2026-09-30.md.
 */

type StatusDotVariant = "active" | "paused" | "pending" | "error" | "warning" | "neutral";

// cloudDaemonStatusLabel's vocabulary → StatusDot's. Colour carries the
// meaning; the label names it.
const STATUS_DOT_VARIANT: Record<string, StatusDotVariant> = {
  active: "active",
  starting: "pending",
  resuming: "pending",
  suspended: "neutral",
  disconnected: "warning",
  failed: "error",
};

function machineKindLabel(daemonType: string | undefined): string {
  if (isCloudDaemon(daemonType)) return "Cloud";
  if (isLocalDaemonType(daemonType)) return "Self-hosted";
  return "Machine";
}

// ConnectDaemonModal pulls eligibility and billing hooks on mount, so it is
// only mounted while open. That also resets its step whenever it is reopened.
function ConnectMachineModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  if (!open) return null;
  return <ConnectDaemonModal isOpen onClose={onClose} />;
}

const STRIP = "flex flex-wrap items-center gap-x-3 gap-y-2 rounded-lg border border-border bg-card px-3 py-2";

/** The connected machine, in one line. */
export function MachineStatusStrip({
  hostname,
  daemonType,
}: {
  hostname: string;
  daemonType: string | undefined;
}) {
  const [connectOpen, setConnectOpen] = useState(false);
  return (
    <div className={STRIP} data-testid="machine-status-strip">
      <StatusDot variant="active" size="md" />
      <div className="flex min-w-0 flex-1 items-center gap-2 text-sm">
        <span className="truncate font-medium text-ink">{hostname || "Machine"}</span>
        <Badge label={machineKindLabel(daemonType)} variant="neutral" size="sm" />
        <span className="hidden text-xs text-ink-muted sm:inline">
          Connected. Projects open on this machine.
        </span>
      </div>
      <button
        type="button"
        onClick={() => setConnectOpen(true)}
        className={pickerButton("ghost", "sm")}
      >
        <Plus className="h-3.5 w-3.5" aria-hidden="true" />
        Add machine
      </button>
      <ConnectMachineModal open={connectOpen} onClose={() => setConnectOpen(false)} />
    </div>
  );
}

/** Shown while the machine list is loading, or the local machine is starting. */
export function MachineStatusPending({ message }: { message: string }) {
  return (
    <div className={STRIP} role="status">
      <StatusDot variant="pending" size="md" pulse />
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
  const [connectOpen, setConnectOpen] = useState(false);
  const hasCloud = capabilities.cloudDaemons;

  // One list, from the registry — the service that knows both whether a
  // machine has actually attached AND what it is doing (see
  // docs/design/one-daemon-list.md).
  const {
    data: cloudDaemons,
    isLoading,
    refetch,
  } = useQuery<CloudDaemon[]>({
    queryKey: ["projectPicker", "cloudDaemons"],
    queryFn: async () => {
      const resp = await grpcClient.daemonRegistry().listDaemons(create(ListDaemonsRequestSchema));
      return resp.daemons.filter((d) => isCloudDaemon(d.daemonType));
    },
    enabled: hasCloud,
    refetchInterval: 8_000,
    refetchIntervalInBackground: false,
    staleTime: 5_000,
  });

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
        `Delete ${daemon.hostname || "this machine"}? It failed to start and can't be recovered. You can create a new one afterwards.`,
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
    ? "Reliant runs tools on a machine that can see your files. Connect one to open and work on projects."
    : allFailed
      ? "Every machine on your account failed to start. Delete the failed one, then connect a new one."
      : hasAnyCloudDaemon
        ? "Your machines are asleep. Resume one to work on its projects; this page updates when it connects."
        : "You don't have a machine yet. Start one in the cloud or connect your own computer.";

  return (
    <section
      className="rounded-lg border border-border bg-card"
      aria-labelledby="no-active-machine-title"
      data-testid="no-active-machine"
    >
      <div className="flex flex-wrap items-start justify-between gap-3 p-4">
        <div className="flex min-w-0 items-start gap-3">
          <StatusDot
            variant={allFailed ? "error" : "warning"}
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
          onClick={() => setConnectOpen(true)}
          className={pickerButton(hasAnyCloudDaemon && !allFailed ? "secondary" : "primary", "sm")}
        >
          <Plus className="h-3.5 w-3.5" aria-hidden="true" />
          {hasAnyCloudDaemon ? "Connect a new machine" : "Connect a machine"}
        </button>
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
            const name = daemon.hostname || "machine";
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
                <StatusDot
                  variant={STATUS_DOT_VARIANT[statusLabel] ?? "neutral"}
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

      <ConnectMachineModal open={connectOpen} onClose={() => setConnectOpen(false)} />
    </section>
  );
}
