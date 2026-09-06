/**
 * What the user watches while the commit point runs. "Fire when done."
 *
 * This is a THIN wrapper over {@link DaemonConnectingGate}, not a replacement
 * for it. That gate polls, has a tested `derivePhase`, and its timeout and
 * failure phases are the product of a real bug fix — rebuilding them here would
 * be a second waiting surface that drifts from the one that works. So when
 * there is a machine to wait for, this renders it verbatim and only adds the
 * task checklist above it.
 *
 * What it adds that the daemon gate cannot know: the commit's OTHER task. The
 * daemon gate infers one thing from `ListDaemons`; a commit reports what it was
 * asked to do and how each part went, which is what makes a partial failure
 * legible ("AI access ready / your machine couldn't start") instead of a
 * blank wait.
 */
import { Check, Loader2, X } from "lucide-react";
import { cn } from "@/lib/utils";
import { DaemonConnectingGate } from "./DaemonConnectingGate";
import type { CommitResult, CommitTask } from "./commitLaunchPlan";

const TASK_LABELS: Record<CommitTask["name"], string> = {
  grant_ai_access: "AI access",
  provision_daemon: "Your machine",
};

interface ProvisioningGateProps {
  commit: CommitResult;
  /** Dismiss the gate. Enabled on `complete` AND on `partial`. */
  onContinue: () => void;
  /**
   * Re-run the commit. Wired to an explicit button and nothing else — a commit
   * that retried itself would be a billable call firing without a user asking.
   */
  onRetry?: () => void;
}

function TaskRow({ task }: { task: CommitTask }) {
  if (task.status === "skipped") return null;

  const icon =
    task.status === "complete" ? (
      <Check className="h-4 w-4 text-emerald-500" />
    ) : task.status === "failed" ? (
      <X className="h-4 w-4 text-destructive" />
    ) : (
      <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
    );

  return (
    <li className="flex items-start gap-2 text-left">
      <span className="mt-0.5 flex-shrink-0">{icon}</span>
      <span className="min-w-0">
        <span
          className={cn(
            "block text-sm font-medium",
            task.status === "failed" ? "text-destructive" : "text-foreground",
          )}
        >
          {TASK_LABELS[task.name]}
        </span>
        <span className="block text-xs text-muted-foreground">
          {task.detail}
        </span>
      </span>
    </li>
  );
}

export function ProvisioningGate({
  commit,
  onContinue,
  onRetry,
}: ProvisioningGateProps) {
  const visibleTasks = commit.tasks.filter((t) => t.status !== "skipped");
  const daemonTask = commit.tasks.find((t) => t.name === "provision_daemon");
  const waitingOnMachine =
    daemonTask?.status === "complete" && Boolean(commit.daemonId);

  // A commit that asked for nothing has nothing to show. Skipping straight
  // through is correct — the local + own-key path should not be made to watch
  // a checklist of work that did not happen.
  if (visibleTasks.length === 0) {
    return (
      <div data-testid="provisioning-gate-nothing-to-do">
        <button
          type="button"
          onClick={onContinue}
          className="w-full rounded-lg bg-primary py-3 text-sm font-semibold text-primary-foreground transition-colors hover:bg-primary/90"
        >
          Continue
        </button>
      </div>
    );
  }

  return (
    <div className="space-y-5" data-testid="provisioning-gate">
      <div className="space-y-1 text-center">
        <h2 className="text-xl font-semibold tracking-tight text-foreground">
          Setting up your workspace
        </h2>
      </div>

      <ul className="mx-auto max-w-[42ch] space-y-3">
        {visibleTasks.map((task) => (
          <TaskRow key={task.name} task={task} />
        ))}
      </ul>

      {/* The machine is provisioned but not yet reachable — hand off to the
          gate that already knows how to narrate that wait, including the
          three-minute clone and the wedged image pull. */}
      {waitingOnMachine ? (
        <DaemonConnectingGate
          daemonRef={commit.daemonId}
          onContinue={onContinue}
        />
      ) : (
        <div className="space-y-2">
          {/* Continue is enabled on PARTIAL as well as COMPLETE. They paid and
              got something; trapping them in the wizard to punish a
              provisioning failure is the worst available response. */}
          <button
            type="button"
            onClick={onContinue}
            className="w-full rounded-lg bg-primary py-3 text-sm font-semibold text-primary-foreground transition-colors hover:bg-primary/90"
          >
            Continue
          </button>
          {commit.status !== "complete" && onRetry && (
            <button
              type="button"
              onClick={onRetry}
              className="w-full rounded-lg border border-border/40 bg-background py-2.5 text-sm font-medium text-foreground transition-colors hover:bg-muted"
            >
              Try again
            </button>
          )}
          {commit.status === "failed" && (
            <p className="text-center text-xs text-muted-foreground">
              Quote <span className="font-mono">{commit.commitKey}</span> if you
              contact support.
            </p>
          )}
        </div>
      )}
    </div>
  );
}
