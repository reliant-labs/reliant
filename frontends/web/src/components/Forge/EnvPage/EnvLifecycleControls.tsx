// Copyright (c) 2025 Reliant Labs

/**
 * STOP / START / DELETE for a HOSTED environment, in the env page header.
 *
 * Only rendered for environments the control plane places (persistent,
 * preview): local and self-managed environments have nothing here for it to
 * stop.
 *
 * WHO MAY (orgAuthority.ts, D19): Stop and Start need deploy:write on the
 * environment, which a member may hold bound to it, so they are never
 * disabled on the role alone. Delete needs an org owner or admin, so the menu
 * item is disabled for a known member. Either way the server decides, and a
 * PermissionDenied is shown inline, in words.
 *
 * START is not confirmed — it only costs what the plan already allows — but it
 * can be REFUSED (billing), and that refusal is shown verbatim, inline, not as
 * a generic toast. STOP gets a confirm that says what keeps billing: storage
 * (D16). DELETE (DeleteEnvironmentDialog.tsx) needs the environment name
 * typed, says what is deleted and what is kept, and asks whether to keep the
 * databases.
 */

import { useState } from "react";
import { MoreHorizontal, Play, Square, Trash2 } from "lucide-react";

import DropdownMenu from "@/components/forge-ui/dropdown_menu";
import Modal from "@/components/forge-ui/modal";
import Button from "@/components/forge-ui/button";
import { useSetEnvironmentRunState } from "@/hooks/forge-queries";
import type { CloudEnvStatus } from "@/services/forge/cloudEnvs";

import { useObsReadOnly } from "../Observability/obsReadOnly";
import { DELETE_ENV_NEEDS_ADMIN, refusalText, useOrgAuthority } from "../orgAuthority";
import { environmentStopped } from "../runStateVocabulary";
import { DeleteEnvironmentDialog } from "./DeleteEnvironmentDialog";

export { DeleteEnvironmentDialog };

const stopNeeds = (envName: string) => `Stopping or starting ${envName} needs deploy:write on it.`;

export function EnvLifecycleControls({
  environmentId,
  envName,
  project = "",
  status,
  onDeleted,
}: {
  environmentId: string;
  envName: string;
  /** The forge project (app) the environment belongs to. */
  project?: string;
  status: CloudEnvStatus | undefined;
  onDeleted?: () => void;
}) {
  const setRunState = useSetEnvironmentRunState(environmentId);
  const [stopOpen, setStopOpen] = useState(false);
  const [deleteOpen, setDeleteOpen] = useState(false);

  const workloads = status?.workloads ?? [];
  const stopped = environmentStopped(workloads);
  const startError = setRunState.isError && setRunState.variables === "running" ? setRunState.error : null;
  const lock = useObsReadOnly();
  const authority = useOrgAuthority();
  const canDelete = authority !== "member";

  // The demo console: the controls show, off, saying why (obsReadOnly.tsx); no danger menu.
  if (lock.readOnly) {
    return (
      <div className="flex items-center gap-2" data-testid="env-lifecycle-controls">
        <Button variant="outline" size="sm" leftIcon={stopped ? <Play className="h-3.5 w-3.5" /> : <Square className="h-3.5 w-3.5" />} disabled title={lock.reason}>
          {stopped ? "Start environment" : "Stop environment"}
        </Button>
      </div>
    );
  }

  return (
    <div className="flex flex-col items-end gap-2" data-testid="env-lifecycle-controls">
      <div className="flex items-center gap-2">
        {stopped ? (
          <Button
            variant="outline"
            size="sm"
            leftIcon={<Play className="h-3.5 w-3.5" />}
            loading={setRunState.isPending}
            disabled={setRunState.isPending}
            onClick={() => setRunState.mutate("running")}
            data-testid="env-action-start"
          >
            Start environment
          </Button>
        ) : (
          <Button
            variant="outline"
            size="sm"
            leftIcon={<Square className="h-3.5 w-3.5" />}
            disabled={workloads.length === 0}
            onClick={() => {
              setRunState.reset();
              setStopOpen(true);
            }}
            data-testid="env-action-stop"
          >
            Stop environment
          </Button>
        )}
        <DropdownMenu
          align="right"
          label="More actions"
          trigger={(props) => (
            <button
              type="button"
              {...props}
              className="inline-flex h-8 w-8 items-center justify-center rounded-md border border-border text-ink-muted hover:text-ink focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              data-testid="env-danger-menu"
              aria-label="More actions"
            >
              <MoreHorizontal className="h-4 w-4" aria-hidden="true" />
            </button>
          )}
          groups={[
            {
              items: [
                {
                  label: "Delete environment…",
                  icon: <Trash2 className="h-3.5 w-3.5" />,
                  variant: "danger",
                  testId: "env-action-delete",
                  disabled: !canDelete,
                  hint: canDelete ? undefined : <span title={DELETE_ENV_NEEDS_ADMIN}>Owner or admin</span>,
                  onClick: () => setDeleteOpen(true),
                },
              ],
            },
          ]}
        />
      </div>

      {startError && (
        <p
          role="alert"
          className="max-w-md text-right text-xs text-ink"
          data-testid="env-start-refused"
        >
          Couldn&apos;t start {envName}: {refusalText(startError, stopNeeds(envName))}
        </p>
      )}

      <StopEnvironmentDialog
        isOpen={stopOpen}
        envName={envName}
        pending={setRunState.isPending}
        error={
          setRunState.isError && setRunState.variables === "suspended"
            ? refusalText(setRunState.error, stopNeeds(envName))
            : null
        }
        onClose={() => setStopOpen(false)}
        onConfirm={() => setRunState.mutate("suspended", { onSuccess: () => setStopOpen(false) })}
      />
      <DeleteEnvironmentDialog
        isOpen={deleteOpen}
        environmentId={environmentId}
        envName={envName}
        workloads={workloads.map((workload) => workload.name ?? "").filter((name) => name !== "")}
        databases={
          // Known only when every workload states its tier: an unstated one
          // may be a database, and "no data to keep" must never be a guess.
          status && workloads.every((workload) => !!workload.tier)
            ? workloads.filter((workload) => workload.tier === "database").map((workload) => workload.name ?? "")
            : null
        }
        project={project}
        onClose={() => setDeleteOpen(false)}
        onDeleted={onDeleted}
      />
    </div>
  );
}

export function StopEnvironmentDialog({
  isOpen,
  envName,
  pending,
  error,
  onClose,
  onConfirm,
}: {
  isOpen: boolean;
  envName: string;
  pending: boolean;
  error: string | null;
  onClose: () => void;
  onConfirm: () => void;
}) {
  return (
    <Modal open={isOpen} onClose={onClose} size="sm" title={`Stop ${envName}?`}>
      <div className="space-y-4" data-testid="stop-environment-dialog">
        <p className="text-sm text-ink">
          Compute stops: workloads scale to zero, databases hibernate and static sites are unpublished. Data, URLs,
          secrets and domains are kept. Start brings it back.
        </p>
        {/* D16: Stop is not zero cost. No API reports one environment's
            storage cost on its own (GetProjectCost files database volumes
            under "databases" together with database compute), so this says
            so instead of showing a derived number. */}
        <p className="text-sm text-ink-muted" data-testid="stop-environment-storage-note">
          Storage keeps billing while it is stopped: database volumes and the site&apos;s files are kept. Reliant
          can&apos;t show that amount for one environment yet. Deleting the environment without keeping its data stops
          its charges.
        </p>
        {error && (
          <p role="alert" className="text-xs text-ink" data-testid="stop-environment-error">
            {error}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button variant="outline" size="sm" onClick={onClose} disabled={pending}>
            Cancel
          </Button>
          <Button
            variant="primary"
            size="sm"
            onClick={onConfirm}
            loading={pending}
            disabled={pending}
            data-testid="stop-environment-confirm"
          >
            Stop environment
          </Button>
        </div>
      </div>
    </Modal>
  );
}
