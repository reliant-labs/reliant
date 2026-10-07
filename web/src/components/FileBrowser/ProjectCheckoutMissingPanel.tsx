import { useState } from "react";
import { AlertCircle, FolderX, GitFork, Loader2 } from "lucide-react";

import { projectGrpc } from "../../api/project-grpc";
import {
  ProjectCheckoutState,
  type ProjectCheckoutMissing,
} from "../../gen/reliant/v1/filesystem_pb";
import { useDaemonStatus } from "../../hooks/useDaemonStatus";
import { machineDisplayName } from "../../lib/machineName";
import { describeCloneError } from "../Projects/cloneTargets";

/**
 * What the file tree shows when the project's directory is not on the machine
 * it reached — instead of the raw "read dir …: no such file or directory" it
 * used to print, once per surface, for every read.
 *
 * Two situations, which the server tells apart (ProjectCheckoutMissing):
 *
 *   - CLONING: the clone onto this machine is queued or running. This is the
 *     normal few seconds after "Clone from GitHub" (the owner's forge clone on
 *     2026-10-07 landed eight seconds after it was queued). Nothing to do but
 *     wait; the tree retries on its own.
 *   - ABSENT / CLONE_FAILED: nothing is putting the project here. Typically it
 *     was cloned onto a different machine — houndersclub on the owner's new
 *     machine. A repository project can be cloned here in one click; a plain
 *     folder cannot be recreated from anywhere, so it is only explained.
 */
export function ProjectCheckoutMissingPanel({
  missing,
  projectName,
  onCloneQueued,
}: {
  missing: ProjectCheckoutMissing;
  projectName: string;
  /** Called once a clone onto this machine has been queued. */
  onCloneQueued: () => void;
}) {
  const { daemons } = useDaemonStatus();
  const [cloning, setCloning] = useState(false);
  const [cloneError, setCloneError] = useState<string | null>(null);

  const daemon = daemons.find((d) => d.daemonId === missing.daemonId);
  const machine = daemon ? machineDisplayName(daemon) : "this machine";
  const canClone = Boolean(missing.remoteUrl && missing.daemonId);

  if (missing.state === ProjectCheckoutState.CLONING) {
    return (
      <div className="flex h-full items-center justify-center p-4" data-testid="project-checkout-cloning">
        <div className="max-w-xs space-y-2 text-center">
          <Loader2 className="mx-auto h-6 w-6 animate-spin text-muted-foreground" aria-hidden="true" />
          <p className="text-sm font-medium text-foreground">
            Cloning {projectName} onto {machine}…
          </p>
          <p className="text-xs text-muted-foreground">The files appear here as soon as the clone lands.</p>
        </div>
      </div>
    );
  }

  const cloneHere = async () => {
    if (!canClone) return;
    setCloning(true);
    setCloneError(null);
    try {
      await projectGrpc.createProjectFromRepo({
        cloneUrl: missing.remoteUrl,
        daemonId: missing.daemonId,
        name: projectName,
        path: missing.path,
      });
      onCloneQueued();
    } catch (err) {
      setCloneError(describeCloneError(err, machine));
    } finally {
      setCloning(false);
    }
  };

  const failed = missing.state === ProjectCheckoutState.CLONE_FAILED;
  return (
    <div className="flex h-full items-center justify-center p-4" data-testid="project-checkout-missing">
      <div className="max-w-xs space-y-3 text-center">
        {failed ? (
          <AlertCircle className="mx-auto h-7 w-7 text-destructive-ink" aria-hidden="true" />
        ) : (
          <FolderX className="mx-auto h-7 w-7 text-muted-foreground" aria-hidden="true" />
        )}
        <div className="space-y-1">
          <p className="text-sm font-medium text-foreground">
            {failed ? `Cloning ${projectName} onto ${machine} failed` : `${projectName} isn't on ${machine}`}
          </p>
          <p className="text-xs text-muted-foreground">
            {failed && missing.installError
              ? missing.installError
              : canClone
                ? `It was cloned onto another machine, or its folder was removed. Clone it here to work on it on ${machine}.`
                : `Its folder, ${missing.path}, doesn't exist on ${machine}.`}
          </p>
        </div>
        {canClone && (
          <button
            type="button"
            onClick={() => void cloneHere()}
            disabled={cloning}
            className="inline-flex items-center gap-1.5 rounded-md bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground hover:bg-primary/90 disabled:opacity-60"
            data-testid="project-checkout-clone-here"
          >
            {cloning ? (
              <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
            ) : (
              <GitFork className="h-3.5 w-3.5" aria-hidden="true" />
            )}
            {failed ? "Try the clone again" : `Clone it onto ${machine}`}
          </button>
        )}
        {cloneError && (
          <p className="text-xs text-destructive-ink" role="alert">
            {cloneError}
          </p>
        )}
      </div>
    </div>
  );
}
