// Copyright (c) 2025 Reliant Labs

/**
 * What a daemon-backed forge surface shows when the project is not checked
 * out on any machine that could answer — the server's ProjectCheckoutMissing
 * detail (internal/projectcheckout).
 *
 * It used to land under DaemonNeeded's "Your daemon isn't answering", which
 * was false twice over: the daemon was answering, and waiting or retrying
 * could never fix it. The project simply has no checkout on the machine that
 * was asked — or on any of the user's machines — so the remedy is to put it
 * on one: clone it there, or connect the machine whose disk holds the folder.
 *
 * Four cases, from the detail's state and whether it names a machine:
 *
 *   cloning        a clone onto a machine is still landing; nothing to do
 *   clone-failed   the last clone failed; try it again
 *   gone           the machine that had it no longer has the folder
 *   nowhere        no machine of the user's holds it (daemon_id empty)
 */

import { useState } from "react";
import { FolderX, GitFork, Loader2, RefreshCw } from "lucide-react";

import Button from "@/components/forge-ui/button";
import { ProjectCheckoutState, type ProjectCheckoutMissing } from "@/gen/reliant/v1/filesystem_pb";
import { createProjectFromRepo } from "@/hooks/project-queries";
import { useDaemonStatus } from "@/hooks/useDaemonStatus";
import { machineDisplayName } from "@/lib/machineName";
import { describeCloneError, pickCloneTarget } from "../../Projects/cloneTargets";

export type CheckoutMissingKind = "cloning" | "clone-failed" | "gone" | "nowhere";

export function checkoutMissingKind(missing: ProjectCheckoutMissing): CheckoutMissingKind {
  if (missing.state === ProjectCheckoutState.CLONING) return "cloning";
  if (missing.state === ProjectCheckoutState.CLONE_FAILED) return "clone-failed";
  return missing.daemonId ? "gone" : "nowhere";
}

export interface CheckoutMissingCopy {
  headline: string;
  body: string;
  /** The button's label, or null when there is nothing to click but Retry. */
  action: string | null;
}

/**
 * The sentences, as a pure function so the four cases are tested without
 * rendering. `machine` names the detail's machine; `cloneTarget` the machine
 * a clone would go to when the detail names none.
 */
export function checkoutMissingCopy(
  missing: ProjectCheckoutMissing,
  { project, machine, cloneTarget, what }: { project: string; machine: string; cloneTarget: string | null; what: string },
): CheckoutMissingCopy {
  const cloneable = missing.remoteUrl !== "";
  switch (checkoutMissingKind(missing)) {
    case "cloning":
      return {
        headline: `${project} is still being cloned onto ${machine}`,
        body: `This shows ${what} as soon as the clone lands.`,
        action: null,
      };
    case "clone-failed":
      return {
        headline: `Cloning ${project} onto ${machine} failed`,
        body: missing.installError || `The clone did not finish, so ${machine} has no checkout to read.`,
        action: cloneable ? "Try the clone again" : null,
      };
    case "gone":
      return {
        headline: `${project}'s folder is missing on ${machine}`,
        body: `It was at ${missing.path}, and is not there any more — moved or deleted.`,
        action: cloneable ? `Clone it onto ${machine}` : null,
      };
    case "nowhere":
      if (!cloneable) {
        return {
          headline: `${project} isn't on any of your connected machines`,
          body: `This shows ${what} from the project's folder. Connect the machine that holds it, then retry.`,
          action: null,
        };
      }
      return {
        headline: `${project} isn't checked out on any of your machines`,
        body: cloneTarget
          ? `This shows ${what} from a checkout of the project. Clone it onto a machine to see it here.`
          : `This shows ${what} from a checkout of the project. Add a machine, then clone it there.`,
        action: cloneTarget ? `Clone it onto ${cloneTarget}` : null,
      };
  }
}

export function CheckoutMissing({
  missing,
  projectName,
  what,
  onRetry,
  retrying = false,
}: {
  missing: ProjectCheckoutMissing;
  projectName?: string;
  /** What this surface shows, e.g. "what forge env up is running for dev". */
  what: string;
  /** Asks again: after a clone is queued, and for the cases only time fixes. */
  onRetry?: () => void;
  retrying?: boolean;
}) {
  const { daemons } = useDaemonStatus();
  const [cloning, setCloning] = useState(false);
  const [cloneError, setCloneError] = useState<string | null>(null);

  const project = projectName || "This project";
  const named = daemons.find((d) => d.daemonId === missing.daemonId);
  const machine = named ? machineDisplayName(named) : "that machine";
  // With no machine in the detail, the clone goes where a new project's
  // would: the running, most recently used cloud machine.
  const target = missing.daemonId ? null : pickCloneTarget(daemons);
  const copy = checkoutMissingCopy(missing, {
    project,
    machine,
    cloneTarget: target ? machineDisplayName(target) : null,
    what,
  });

  const clone = async () => {
    const daemonId = missing.daemonId || target?.daemonId;
    if (!daemonId) return;
    setCloning(true);
    setCloneError(null);
    try {
      await createProjectFromRepo({
        cloneUrl: missing.remoteUrl,
        daemonId,
        name: projectName,
        // The machine's own path when the detail has one; otherwise the
        // server places it where that machine keeps projects.
        path: missing.daemonId ? missing.path : undefined,
      });
      onRetry?.();
    } catch (err) {
      setCloneError(describeCloneError(err, target ? machineDisplayName(target) : machine));
    } finally {
      setCloning(false);
    }
  };

  const kind = checkoutMissingKind(missing);
  const Icon = kind === "cloning" ? Loader2 : FolderX;
  return (
    <div
      data-testid="forge-checkout-missing"
      data-kind={kind}
      className="flex items-start gap-3 rounded-lg border border-dashed border-border bg-surface-sunken px-4 py-4"
    >
      <Icon
        className={
          kind === "cloning"
            ? "mt-0.5 h-4 w-4 shrink-0 animate-spin text-ink-muted motion-reduce:animate-none"
            : "mt-0.5 h-4 w-4 shrink-0 text-ink-muted"
        }
        aria-hidden="true"
      />
      <div className="min-w-0 flex-1 space-y-1">
        <p className="text-sm text-ink">{copy.headline}</p>
        <p className="text-xs text-ink-muted">{copy.body}</p>
        {cloneError && (
          <p className="text-xs text-danger-ink" role="alert">
            {cloneError}
          </p>
        )}
      </div>
      {copy.action ? (
        <Button
          variant="outline"
          size="sm"
          onClick={() => void clone()}
          loading={cloning}
          disabled={cloning}
          leftIcon={<GitFork className="h-3 w-3" />}
          data-testid="forge-checkout-clone"
        >
          {copy.action}
        </Button>
      ) : (
        onRetry && (
          <Button
            variant="outline"
            size="sm"
            onClick={onRetry}
            loading={retrying}
            disabled={retrying}
            leftIcon={<RefreshCw className="h-3 w-3" />}
            data-testid="forge-checkout-retry"
          >
            Retry
          </Button>
        )
      )}
    </div>
  );
}
