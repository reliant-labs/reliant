// Copyright (c) 2025 Reliant Labs

/**
 * "Use a folder on <machine>": tell Reliant a project is already checked out
 * in a folder on one of the user's machines — no clone.
 *
 * Pick the machine (or arrive pinned to one), browse THAT machine's disk, and
 * attach the folder (AttachProjectToDaemon). The server checks the folder on
 * the machine first: it refuses a missing path, a file, a folder that is
 * another project's, or a machine still cloning this project, and it attaches
 * but WARNS for a folder that is not a git checkout or whose remote is not
 * the project's. Refusals and warnings are both shown here, in the server's
 * own sentences, so the user sees exactly what the machine reported.
 *
 * Shared by the new-chat machine picker and Settings → Projects.
 */

import { useEffect, useState } from "react";
import { AlertTriangle, CheckCircle2, Loader2, XCircle } from "lucide-react";

import Modal from "../forge-ui/modal";
import { useAttachProjectToDaemon } from "../../hooks/project-service-hooks_gen";
import { useDaemonStatus } from "../../hooks/useDaemonStatus";
import { daemonLabel } from "../Automations/daemonChoices";
import { toast } from "../../lib/toast-manager";
import { DirectoryPicker } from "./DirectoryPicker";
import { describeMachineFolderError } from "./machineFolderErrors";

export interface AttachedFolder {
  daemonId: string;
  path: string;
  warnings: string[];
}

interface AttachProjectFolderDialogProps {
  open: boolean;
  onClose: () => void;
  project: { id: string; name: string };
  /** The machine to browse first. Unset: the user's default machine. */
  daemonId?: string;
  /** Keep the dialog on `daemonId`: no machine selector. */
  lockMachine?: boolean;
  /** After the folder is attached (warnings, if any, are still on screen). */
  onAttached?: (attached: AttachedFolder) => void;
}

type Step =
  | { kind: "browse" }
  | { kind: "attaching"; daemonId: string; path: string }
  | { kind: "attached"; daemonId: string; path: string; warnings: string[] }
  | { kind: "failed"; daemonId: string; path: string; message: string };

const BUTTON =
  "inline-flex h-9 items-center justify-center gap-2 rounded-md px-4 text-sm font-medium transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50";
const PRIMARY = `${BUTTON} bg-primary text-primary-foreground shadow-sm hover:bg-primary/90`;
const SECONDARY = `${BUTTON} border border-input bg-card text-foreground hover:bg-muted`;

export function AttachProjectFolderDialog({
  open,
  onClose,
  project,
  daemonId,
  lockMachine = false,
  onAttached,
}: AttachProjectFolderDialogProps) {
  const { daemons } = useDaemonStatus();
  const attach = useAttachProjectToDaemon({ meta: { silenceErrorToast: true } });
  const [step, setStep] = useState<Step>({ kind: "browse" });
  // The machine to reopen the browser on after a refusal: the one just tried.
  const [browseDaemonId, setBrowseDaemonId] = useState<string | undefined>(daemonId);

  useEffect(() => {
    if (open) {
      setStep({ kind: "browse" });
      setBrowseDaemonId(daemonId);
    }
  }, [open, daemonId]);

  const machineName = (id: string) =>
    daemonLabel(
      daemons.find((d) => d.daemonId === id),
      id,
    );

  const attachFolder = async (path: string, chosenDaemonId: string | undefined) => {
    const target = chosenDaemonId ?? daemonId;
    if (!target) {
      setStep({ kind: "failed", daemonId: "", path, message: "Choose a machine first." });
      return;
    }
    setBrowseDaemonId(target);
    setStep({ kind: "attaching", daemonId: target, path });
    try {
      const response = await attach.mutateAsync({ projectId: project.id, daemonId: target, path });
      const warnings = response.warnings ?? [];
      const machine = response.projectDaemon?.daemonName || machineName(target);
      onAttached?.({ daemonId: target, path, warnings });
      if (warnings.length === 0) {
        toast.success(`${project.name} is now on ${machine}, in ${path}`);
        onClose();
        return;
      }
      setStep({ kind: "attached", daemonId: target, path, warnings });
    } catch (err) {
      setStep({ kind: "failed", daemonId: target, path, message: describeMachineFolderError(err, machineName(target)) });
    }
  };

  if (!open) return null;

  if (step.kind === "browse") {
    return (
      <DirectoryPicker
        isOpen
        closeOnSelect={false}
        onClose={onClose}
        daemonId={browseDaemonId}
        machineSelectable={!lockMachine}
        onDaemonChange={setBrowseDaemonId}
        selectLabel={`Use this folder for ${project.name}`}
        onSelect={(path, chosen) => void attachFolder(path, chosen)}
      />
    );
  }

  const machine = step.daemonId ? machineName(step.daemonId) : "the machine";
  return (
    <Modal
      open
      onClose={step.kind === "attaching" ? () => {} : onClose}
      title={
        step.kind === "attaching"
          ? `Adding ${project.name} on ${machine}…`
          : step.kind === "attached"
            ? `${project.name} is on ${machine}`
            : `Couldn't use that folder`
      }
      size="md"
      hideCloseButton={step.kind === "attaching"}
    >
      <div className="flex flex-col gap-4" data-testid="attach-folder-dialog">
        <p className="truncate rounded-md border border-border/60 bg-background px-3 py-2 font-mono text-sm text-muted-foreground" title={step.path}>
          {step.path}
        </p>

        {step.kind === "attaching" && (
          <div className="flex items-center gap-2 text-sm text-muted-foreground" role="status">
            <Loader2 className="h-4 w-4 animate-spin motion-reduce:animate-none" aria-hidden="true" />
            Checking the folder on {machine}…
          </div>
        )}

        {step.kind === "attached" && (
          <>
            <div className="flex items-start gap-2 text-sm text-foreground">
              <CheckCircle2 className="mt-0.5 h-4 w-4 flex-shrink-0 text-success-ink" aria-hidden="true" />
              <span>
                Added. Chats on {machine} now work in this folder. {machine} reported:
              </span>
            </div>
            <ul className="flex flex-col gap-2" data-testid="attach-folder-warnings">
              {step.warnings.map((warning) => (
                <li
                  key={warning}
                  className="flex items-start gap-2 rounded-md border border-warning/30 bg-warning/10 px-3 py-2 text-sm text-warning-ink"
                >
                  <AlertTriangle className="mt-0.5 h-4 w-4 flex-shrink-0" aria-hidden="true" />
                  <span>{warning}</span>
                </li>
              ))}
            </ul>
            <div className="flex justify-end">
              <button type="button" onClick={onClose} className={PRIMARY}>
                Done
              </button>
            </div>
          </>
        )}

        {step.kind === "failed" && (
          <>
            <div
              role="alert"
              className="flex items-start gap-2 rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive-ink"
            >
              <XCircle className="mt-0.5 h-4 w-4 flex-shrink-0" aria-hidden="true" />
              <span>{step.message}</span>
            </div>
            <div className="flex justify-end gap-2">
              <button type="button" onClick={onClose} className={SECONDARY}>
                Cancel
              </button>
              <button type="button" onClick={() => setStep({ kind: "browse" })} className={PRIMARY}>
                Choose another folder
              </button>
            </div>
          </>
        )}
      </div>
    </Modal>
  );
}
