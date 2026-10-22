// Copyright (c) 2025 Reliant Labs

/**
 * STOP / START / DELETE for a HOSTED environment, in the env page header.
 *
 * Only rendered for environments the control plane places (persistent,
 * preview): local and self-managed environments have nothing here for it to
 * stop. The UI does not know the caller's role, so a non-admin's click is
 * answered by the server's PermissionDenied, shown inline, rather than hidden.
 *
 * START is not confirmed — it only costs what the plan already allows — but it
 * can be REFUSED (billing), and that refusal is shown verbatim, inline, not as
 * a generic toast. STOP gets a one-line confirm. DELETE needs the environment
 * name typed, and says plainly that database data is kept but orphaned.
 */

import { useState } from "react";
import { MoreHorizontal, Play, Square, Trash2 } from "lucide-react";

import { Button } from "@/components/ui/Button";
import { Dropdown } from "@/components/ui/Dropdown";
import { Input } from "@/components/ui/Input";
import { Modal } from "@/components/ui/Modal";
import { useDeleteEnvironment, useSetEnvironmentRunState } from "@/hooks/forge-queries";
import { cloudErrorDetail, type CloudEnvStatus } from "@/services/forge/cloudEnvs";

import { environmentStopped } from "../runStateVocabulary";

export function EnvLifecycleControls({
  environmentId,
  envName,
  status,
  onDeleted,
}: {
  environmentId: string;
  envName: string;
  status: CloudEnvStatus | undefined;
  onDeleted?: () => void;
}) {
  const setRunState = useSetEnvironmentRunState(environmentId);
  const [stopOpen, setStopOpen] = useState(false);
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [menuOpen, setMenuOpen] = useState(false);

  const workloads = status?.workloads ?? [];
  const stopped = environmentStopped(workloads);
  const startError = setRunState.isError && setRunState.variables === "running" ? setRunState.error : null;

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
        <Dropdown
          align="right"
          isOpen={menuOpen}
          onOpenChange={setMenuOpen}
          variant="form"
          trigger={
            <button
              type="button"
              onClick={() => setMenuOpen(!menuOpen)}
              className="inline-flex h-8 w-8 items-center justify-center rounded-md border border-border text-muted-foreground hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              data-testid="env-danger-menu"
              aria-label="More actions"
              aria-haspopup="menu"
              aria-expanded={menuOpen}
            >
              <MoreHorizontal className="h-4 w-4" aria-hidden="true" />
            </button>
          }
        >
          <button
            type="button"
            className="flex w-full items-center gap-2 px-3 py-2 text-sm text-destructive-ink hover:bg-accent focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            onClick={() => {
              setMenuOpen(false);
              setDeleteOpen(true);
            }}
            data-testid="env-action-delete"
          >
            <Trash2 className="h-3.5 w-3.5" aria-hidden="true" />
            Delete environment…
          </button>
        </Dropdown>
      </div>

      {startError && (
        <p
          role="alert"
          className="max-w-md text-right text-xs text-foreground"
          data-testid="env-start-refused"
        >
          Couldn&apos;t start {envName}: {cloudErrorDetail(startError)}
        </p>
      )}

      <StopEnvironmentDialog
        isOpen={stopOpen}
        envName={envName}
        pending={setRunState.isPending}
        error={setRunState.isError && setRunState.variables === "suspended" ? cloudErrorDetail(setRunState.error) : null}
        onClose={() => setStopOpen(false)}
        onConfirm={() => setRunState.mutate("suspended", { onSuccess: () => setStopOpen(false) })}
      />
      <DeleteEnvironmentDialog
        isOpen={deleteOpen}
        environmentId={environmentId}
        envName={envName}
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
    <Modal isOpen={isOpen} onClose={onClose} size="sm" title={`Stop ${envName}?`}>
      <div className="space-y-4" data-testid="stop-environment-dialog">
        <p className="text-sm text-foreground">
          Compute stops. Data and URLs are kept. Start brings it back.
        </p>
        {error && (
          <p role="alert" className="text-xs text-foreground" data-testid="stop-environment-error">
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

export function DeleteEnvironmentDialog({
  isOpen,
  environmentId,
  envName,
  onClose,
  onDeleted,
}: {
  isOpen: boolean;
  environmentId: string;
  envName: string;
  onClose: () => void;
  onDeleted?: () => void;
}) {
  const del = useDeleteEnvironment(environmentId);
  const [typed, setTyped] = useState("");
  const matches = typed === envName;

  const close = () => {
    setTyped("");
    del.reset();
    onClose();
  };

  return (
    <Modal isOpen={isOpen} onClose={close} size="md" title={`Delete ${envName}?`}>
      <div className="space-y-4" data-testid="delete-environment-dialog">
        <div className="space-y-2 text-sm text-foreground">
          <p>
            This tears down <span className="font-mono">{envName}</span>: every deployment in it, its
            running workloads, its URLs, and its namespaces.
          </p>
          <p data-testid="delete-environment-data-note">
            Database data is <strong>retained</strong>, but it is orphaned: deploying again creates a
            new environment and does <strong>not</strong> reattach to it.
          </p>
          <p>
            To bring the environment back (empty), run <code className="font-mono">forge env deploy {envName}</code>{" "}
            or use the Deploy button.
          </p>
        </div>
        <Input
          label={`Type ${envName} to confirm`}
          value={typed}
          onChange={(event) => setTyped(event.target.value)}
          autoComplete="off"
          spellCheck={false}
          data-testid="delete-environment-confirm-input"
        />
        {del.isError && (
          <p role="alert" className="text-xs text-foreground" data-testid="delete-environment-error">
            {cloudErrorDetail(del.error)}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button variant="outline" size="sm" onClick={close} disabled={del.isPending}>
            Cancel
          </Button>
          <Button
            variant="destructive"
            size="sm"
            loading={del.isPending}
            disabled={!matches || del.isPending}
            onClick={() =>
              del.mutate(undefined, {
                onSuccess: () => {
                  close();
                  onDeleted?.();
                },
              })
            }
            data-testid="delete-environment-confirm"
          >
            Delete environment
          </Button>
        </div>
      </div>
    </Modal>
  );
}
