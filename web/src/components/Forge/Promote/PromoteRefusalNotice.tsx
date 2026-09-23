// Copyright (c) 2025 Reliant Labs

/**
 * THE REFUSAL. A first-class outcome, not an error toast.
 *
 * The guard re-planned server-side, found the environment bound to something
 * other than what this page claimed, and wrote NOTHING. Three things follow, and
 * all three are on the screen because a generic failure message conveys none of
 * them:
 *
 *   NOTHING WAS WRITTEN. Stated first and plainly. A user who reads "failed" and
 *   assumes a partial write will go looking for damage that does not exist, or
 *   worse, will try to "fix" a binding that was never touched.
 *
 *   WHAT IT ACTUALLY FOUND, against what we claimed. The diff between the two is
 *   the whole content of a refusal — someone promoted in between, or this page
 *   has been open a while — and `actual_promoted_at` is what tells an operator
 *   whether they were beaten by seconds or are reading a week-old screen.
 *
 *   A RE-PLAN, as the offered action. Not a retry. Retrying would re-send a claim
 *   already known to be stale, so the only useful move is to compute a fresh plan
 *   against real current state and let the user decide again — the binding they
 *   would now be replacing is a different one, and the diff they approved no
 *   longer describes what would happen.
 *
 * The facts here are read off the structured error detail, never parsed from the
 * message string. See promoteRefusalOf in api/forge-grpc.ts.
 */

import { ShieldAlert } from "lucide-react";

import { Button } from "@/components/ui/Button";
import { describeActualBinding, type PromoteRefusal } from "@/services/forge/promote";

export interface PromoteRefusalNoticeProps {
  refusal: PromoteRefusal;
  /** Recompute the plan against real current state. */
  onReplan: () => void;
  isReplanning?: boolean;
}

export function PromoteRefusalNotice({
  refusal,
  onReplan,
  isReplanning,
}: PromoteRefusalNoticeProps) {
  return (
    <section
      data-testid="promote-refusal"
      data-reason={refusal.reason}
      className="space-y-3 rounded-lg border border-solid border-warning/50 bg-warning/10 px-4 py-3"
    >
      <div className="flex items-center gap-2 text-warning">
        <ShieldAlert className="h-4 w-4 shrink-0" aria-hidden="true" />
        {/* The headline fact. Nothing was written. */}
        <h3 className="text-sm font-medium" data-testid="promote-refusal-heading">
          Promote refused — nothing was written
        </h3>
      </div>

      <p className="text-xs text-foreground" data-testid="promote-refusal-detail">
        {refusal.detail ||
          "The environment's binding is not what this page last saw, so the promote was not applied."}
      </p>

      {/* Expected vs actual. The diff IS the refusal. */}
      <dl className="grid gap-x-4 gap-y-1 text-xs sm:grid-cols-[auto_1fr]">
        <dt className="text-muted-foreground">You confirmed</dt>
        <dd className="font-mono text-foreground" data-testid="promote-refusal-expected">
          {refusal.expectedUnbound
            ? "no binding — a first promote"
            : refusal.expectedCurrentRelease || "an unnamed release"}
        </dd>

        <dt className="text-muted-foreground">Actually bound to</dt>
        <dd className="font-mono text-foreground" data-testid="promote-refusal-actual">
          {describeActualBinding(refusal)}
        </dd>

        {refusal.actualPromotedAt && (
          <>
            <dt className="text-muted-foreground">That binding was written</dt>
            {/* Seconds ago or last week — this is what says whether someone is
                working alongside you right now. */}
            <dd className="text-foreground" data-testid="promote-refusal-when">
              {formatStamp(refusal.actualPromotedAt)}
            </dd>
          </>
        )}
      </dl>

      <div className="flex items-center gap-2">
        {/* Re-plan, NOT retry. The claim is known stale; only a fresh plan can
            tell the user what promoting would do now. */}
        <Button
          variant="secondary"
          size="xs"
          onClick={onReplan}
          loading={isReplanning}
          disabled={isReplanning}
          data-testid="promote-refusal-replan"
        >
          {isReplanning ? "Re-planning…" : "Show me the current diff"}
        </Button>
        <span className="text-2xs text-muted-foreground">
          The diff you approved no longer describes what would happen.
        </span>
      </div>
    </section>
  );
}

function formatStamp(value: string): string {
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) return value;
  return parsed.toLocaleString(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}
