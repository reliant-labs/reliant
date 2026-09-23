// Copyright (c) 2025 Reliant Labs

/**
 * THE REFUSAL. A first-class outcome, not an error toast — and four of them, each
 * calling for a different action.
 *
 * NOTHING WAS APPLIED. Stated in every heading, first and plainly, because a user
 * who reads "failed" and assumes a partial apply will go looking for damage in a
 * cluster that was never touched — or worse, will try to "fix" it.
 *
 * WHY FOUR SETS OF COPY RATHER THAN ONE. A single "refused, try again" would be
 * actively wrong for two of the reasons:
 *
 *   stale_declared_context  The env's KCL now names a DIFFERENT cluster than the
 *                           one that was reviewed. The server checks this FIRST,
 *                           because a wrong cluster is worse than a wrong release,
 *                           and the copy leads with the two context names rather
 *                           than with the word "refused".
 *   stale_current_release   The bound release moved, so a deploy would ship
 *                           digests nobody reviewed. Re-plan and re-review.
 *   guard_refused           Forge itself will not deploy — kubectl's contexts
 *                           could not be listed, or the declared context is not in
 *                           the kubeconfig. Retrying changes nothing until the
 *                           kubeconfig does, and forge supplies the fix, so the
 *                           fix is what is shown.
 *   already_running         A deploy is in flight. The offered action is to WATCH
 *                           IT, not to re-plan and certainly not to retry: two
 *                           concurrent applies race each other's rollout and leave
 *                           the cluster converging toward two manifest streams.
 *
 * The facts are read off the structured error detail, never parsed from the
 * message string. See deployRefusalOf in api/forge-grpc.ts.
 */

import { Button } from "@/components/ui/Button";
import { describeActualBinding, type DeployRefusal } from "@/services/forge/deploy";

import { REFUSAL_COPY, REFUSAL_ICON } from "./deployVocabulary";

export interface DeployRefusalNoticeProps {
  refusal: DeployRefusal;
  /** Recompute the plan against real current state. */
  onReplan: () => void;
  isReplanning?: boolean;
  /**
   * Watch the deploy the refusal named. Offered ONLY for already-running, which
   * is the one reason whose correct action is not a re-plan.
   */
  onWatchRunning?: (handle: string) => void;
}

export function DeployRefusalNotice({
  refusal,
  onReplan,
  isReplanning,
  onWatchRunning,
}: DeployRefusalNoticeProps) {
  const copy = REFUSAL_COPY[refusal.reason];
  const Icon = REFUSAL_ICON;
  const alreadyRunning = refusal.reason === "already-running";
  const canWatch = alreadyRunning && !!onWatchRunning && refusal.runningHandle !== "";

  return (
    <section
      data-testid="deploy-refusal"
      data-reason={refusal.reason}
      className="space-y-3 rounded-lg border border-solid border-warning/50 bg-warning/10 px-4 py-3"
    >
      <div className="flex items-center gap-2 text-warning">
        <Icon className="h-4 w-4 shrink-0" aria-hidden="true" />
        <h3 className="text-sm font-medium" data-testid="deploy-refusal-heading">
          {copy.heading}
        </h3>
      </div>

      {/* This build's own explanation of what to do about THIS reason. */}
      <p className="text-xs text-foreground" data-testid="deploy-refusal-explanation">
        {copy.explanation}
      </p>

      {/* And the server's sentence, when it sent one. Kept separate from the copy
          above so an improved server message never has to be parsed to be
          believed, and never replaces the actionable text. */}
      {refusal.detail && (
        <p className="text-2xs text-muted-foreground" data-testid="deploy-refusal-detail">
          {refusal.detail}
        </p>
      )}

      {/* THE CLUSTER DIFF, on a stale-context refusal. This is the whole content
          of that refusal: you approved X, it now says Y. */}
      {refusal.reason === "stale-declared-context" && (
        <dl
          className="grid gap-x-4 gap-y-1 text-xs sm:grid-cols-[auto_1fr]"
          data-testid="deploy-refusal-context-diff"
        >
          <dt className="text-muted-foreground">You approved the cluster</dt>
          <dd className="font-mono text-foreground" data-testid="deploy-refusal-expected-context">
            {refusal.expectedDeclaredContext || "an unnamed cluster"}
          </dd>
          <dt className="text-muted-foreground">It now declares</dt>
          <dd className="font-mono text-destructive" data-testid="deploy-refusal-actual-context">
            {refusal.actualDeclaredContext || "no cluster at all"}
          </dd>
        </dl>
      )}

      {/* The release diff, on a stale-release refusal. */}
      {refusal.reason === "stale-current-release" && (
        <dl
          className="grid gap-x-4 gap-y-1 text-xs sm:grid-cols-[auto_1fr]"
          data-testid="deploy-refusal-release-diff"
        >
          <dt className="text-muted-foreground">You reviewed</dt>
          <dd className="font-mono text-foreground" data-testid="deploy-refusal-expected-release">
            {refusal.expectedUnbound
              ? "no release binding"
              : refusal.expectedCurrentRelease || "an unnamed release"}
          </dd>
          <dt className="text-muted-foreground">Actually bound to</dt>
          <dd className="font-mono text-foreground" data-testid="deploy-refusal-actual-release">
            {describeActualBinding(refusal)}
          </dd>
        </dl>
      )}

      {/* Forge's own fix, on a guard refusal. It knows what would make the deploy
          possible; repeating it here saves the operator guessing. */}
      {refusal.reason === "guard-refused" && (refusal.guardFix || refusal.guardReason) && (
        <div className="space-y-1" data-testid="deploy-refusal-guard">
          {refusal.guardReason && (
            <p className="text-2xs text-muted-foreground">Reason: {refusal.guardReason}</p>
          )}
          {refusal.guardFix && (
            <p className="text-xs text-foreground" data-testid="deploy-refusal-guard-fix">
              {refusal.guardFix}
            </p>
          )}
        </div>
      )}

      <div className="flex flex-wrap items-center gap-2">
        {canWatch ? (
          <>
            {/* The in-flight deploy, offered as the action. Starting a second is
                not on this panel at all. */}
            <Button
              variant="secondary"
              size="xs"
              onClick={() => onWatchRunning?.(refusal.runningHandle)}
              data-testid="deploy-refusal-watch"
            >
              {copy.action}
            </Button>
            <span className="font-mono text-2xs text-muted-foreground" data-testid="deploy-refusal-handle">
              {refusal.runningHandle}
            </span>
          </>
        ) : (
          <Button
            variant="secondary"
            size="xs"
            onClick={onReplan}
            loading={isReplanning}
            disabled={isReplanning}
            data-testid="deploy-refusal-replan"
          >
            {isReplanning ? "Re-planning…" : copy.action}
          </Button>
        )}
        {alreadyRunning && !canWatch && (
          // Refused as already-running but with no handle to follow: say so
          // rather than silently offering a re-plan as though it were the point.
          <span className="text-2xs text-muted-foreground">
            The running deploy&apos;s handle was not reported, so it cannot be watched from here.
          </span>
        )}
      </div>
    </section>
  );
}
