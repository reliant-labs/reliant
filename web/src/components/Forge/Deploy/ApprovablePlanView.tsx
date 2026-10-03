// Copyright (c) 2025 Reliant Labs

/**
 * THE PLAN A HUMAN APPROVES — what this deploy would ship, and the one thing
 * on this surface that may appear beside a deploy button.
 *
 * WHY THE OLD IMAGE LIST IS GONE RATHER THAN MOVED. The instant preview shows
 * the images the environment is running NOW. A deploy builds from the chosen
 * checkout and cuts a new release, so those digests are not what a click would
 * ship — they are the previous deploy's. Showing them next to "Deploy" put a
 * confident, precise, WRONG answer in the most load-bearing position on the
 * screen. The plan below is the only document that knows what this deploy
 * ships, so it is the only one rendered here.
 *
 * FINDINGS ARE GROUPED BY CLASS, MOST SEVERE FIRST, because the classes mean
 * genuinely different things to the reader: a stop-class finding is a decision
 * to make, a warning is something to read, and the rest is information. A flat
 * list sorted by section would bury the one that needs a decision among twenty
 * that do not.
 *
 * THE ACKNOWLEDGEMENT IS SEPARATE FROM THE PRIMARY BUTTON, AND PER CODE. Each
 * stop-class finding gets its own tick, because each is its own irreversible
 * consequence; one blanket tick would let a reader accept a database deletion
 * by agreeing to something else. The button is ABSENT until every one is
 * ticked, not disabled — a disabled button invites hunting for the way to
 * enable it, which is the opposite of the pause this exists to create.
 */

import { AlertTriangle, Info, TriangleAlert } from "lucide-react";
import { useId } from "react";

import { cn } from "@/lib/utils";
import { CardInset } from "@/components/forge-ui/card";
import {
  findingClassOf,
  findingsOfClass,
  planOf,
  requiredAcknowledgements,
  stopFindings,
  unacknowledgeableStopFindings,
  approvableRelease,
  type DeployPlanFinding,
  type DeployPlanReport,
  type FindingClass,
} from "@/services/forge/deployPlan";

export interface ApprovablePlanViewProps {
  /** The plan-only document. The ONLY source of what this deploy ships. */
  report: DeployPlanReport;
  /** The stop-class codes the operator has accepted so far. */
  acknowledged: ReadonlySet<string>;
  onAcknowledge: (code: string, accepted: boolean) => void;
}

export function ApprovablePlanView({
  report,
  acknowledged,
  onAcknowledge,
}: ApprovablePlanViewProps) {
  const plan = planOf(report);
  const release = approvableRelease(report);

  // A PLAN THAT COULD NOT BE COMPUTED IS NOT "NO CHANGES". There is nothing to
  // review and nothing to approve, and saying so plainly is the only honest
  // rendering — an empty findings list here would read as a safe deploy.
  if (!plan) {
    return (
      <CardInset data-testid="approvable-plan-none" className="space-y-1">
        <p className="text-sm font-medium text-foreground">
          This deploy&apos;s changes could not be worked out.
        </p>
        <p className="text-xs text-muted-foreground">
          That is not the same as nothing changing — it means the comparison could not be
          made at all, so there is nothing to approve. An environment that has never been
          built has nothing to compare against yet.
        </p>
      </CardInset>
    );
  }

  const stop = stopFindings(plan);
  const warn = findingsOfClass(plan, "warn");
  const info = findingsOfClass(plan, "info");
  const nameless = unacknowledgeableStopFindings(plan);
  const codes = requiredAcknowledgements(plan);

  return (
    <section className="space-y-3" data-testid="approvable-plan">
      {/* WHAT SHIPS. The release this plan was computed for — the artifacts a
          deploy would carry, from the build that produced this plan. */}
      <CardInset className="space-y-1">
        <p className="text-xs font-medium text-foreground">
          {release ? `This deploy ships release ${release}.` : "This deploy ships this plan."}
        </p>
        {plan.config_identical === true && (
          <p className="text-2xs text-muted-foreground">
            The configuration is identical to what is already deployed.
          </p>
        )}
        {plan.live_basis?.drift_observed === true && (
          <p className="text-2xs text-warning">
            What is running has drifted from what was last deployed. This deploy overwrites
            it.
          </p>
        )}
      </CardInset>

      {/* A plan with no findings at all. Stated, because a blank space where a
          change list belongs is indistinguishable from a rendering failure. */}
      {stop.length === 0 && warn.length === 0 && info.length === 0 && (
        <p
          data-testid="approvable-plan-empty"
          className="rounded-lg border border-dashed border-border px-3 py-2 text-xs text-muted-foreground"
        >
          This deploy changes nothing that forge tracks.
        </p>
      )}

      {/* STOP CLASS FIRST: these are decisions, not information. */}
      {stop.length > 0 && (
        <div
          data-testid="approvable-plan-stop"
          className="space-y-2 rounded-lg border border-solid border-destructive/50 bg-destructive/10 px-3 py-2"
        >
          <p className="flex items-center gap-1.5 text-xs font-medium text-foreground">
            <AlertTriangle
              className="h-3.5 w-3.5 shrink-0 text-destructive"
              aria-hidden="true"
            />
            This deploy destroys something that cannot be brought back.
          </p>
          <p className="text-2xs text-muted-foreground">
            Deploying succeeds and the thing is still gone. Accept each one separately.
          </p>

          {codes.map((code) => (
            <AcknowledgeCode
              key={code}
              code={code}
              findings={stop.filter((finding) => finding.code?.trim() === code)}
              accepted={acknowledged.has(code)}
              onChange={(accepted) => onAcknowledge(code, accepted)}
            />
          ))}

          {/* A stop finding with no code cannot be acknowledged — an
              acknowledgement names a code. Said out loud rather than dropped,
              because dropping it would let the plan look approvable. */}
          {nameless.length > 0 && (
            <div
              data-testid="approvable-plan-unacknowledgeable"
              className="space-y-0.5 rounded-md border border-destructive/40 px-2 py-1.5"
            >
              <p className="text-2xs font-medium text-destructive">
                This plan cannot be approved from here.
              </p>
              <p className="text-2xs text-muted-foreground">
                It reports an irreversible change with no name to accept, so there is no way
                to say yes to it specifically. Re-plan, and if it persists this needs a look
                before deploying.
              </p>
              <ul className="space-y-0.5 pt-0.5">
                {nameless.map((finding, index) => (
                  <li key={index} className="text-2xs text-muted-foreground">
                    <FindingLine finding={finding} />
                  </li>
                ))}
              </ul>
            </div>
          )}
        </div>
      )}

      {warn.length > 0 && (
        <FindingGroup
          testId="approvable-plan-warn"
          kind="warn"
          title="Worth reading before you approve."
          findings={warn}
        />
      )}

      {info.length > 0 && (
        <FindingGroup
          testId="approvable-plan-info"
          kind="info"
          title={`What changes (${info.length})`}
          findings={info}
        />
      )}
    </section>
  );
}

/**
 * One stop-class code, with its findings and its own tick.
 *
 * The findings sharing a code are listed together under a single
 * acknowledgement because the code is what forge accepts — two deletions of the
 * same kind are one decision, and asking twice for one decision trains people
 * to tick without reading.
 */
function AcknowledgeCode({
  code,
  findings,
  accepted,
  onChange,
}: {
  code: string;
  findings: DeployPlanFinding[];
  accepted: boolean;
  onChange: (accepted: boolean) => void;
}) {
  const checkboxId = useId();
  return (
    <div className="space-y-1 rounded-md border border-destructive/40 px-2 py-1.5">
      <ul className="space-y-0.5">
        {findings.map((finding, index) => (
          <li key={index} className="text-2xs text-muted-foreground">
            <FindingLine finding={finding} />
          </li>
        ))}
      </ul>
      <label htmlFor={checkboxId} className="flex cursor-pointer items-start gap-2 pt-0.5">
        <input
          id={checkboxId}
          type="checkbox"
          checked={accepted}
          onChange={(event) => onChange(event.target.checked)}
          className="mt-0.5 h-3.5 w-3.5 shrink-0 accent-primary"
          data-testid={`acknowledge-${code}`}
        />
        <span className="text-2xs text-foreground">
          I accept this, and I know it cannot be undone.
        </span>
      </label>
    </div>
  );
}

function FindingGroup({
  testId,
  kind,
  title,
  findings,
}: {
  testId: string;
  kind: FindingClass;
  title: string;
  findings: DeployPlanFinding[];
}) {
  const Icon = kind === "warn" ? TriangleAlert : Info;
  return (
    <CardInset data-testid={testId} className="space-y-1.5">
      <p className="flex items-center gap-1.5 text-xs font-medium text-foreground">
        <Icon
          className={cn("h-3.5 w-3.5 shrink-0", kind === "warn" ? "text-warning" : "text-muted-foreground")}
          aria-hidden="true"
        />
        {title}
      </p>
      <ul className="space-y-0.5">
        {findings.map((finding, index) => (
          <li
            key={index}
            className="text-2xs text-muted-foreground"
            data-finding-class={findingClassOf(finding.class)}
          >
            <FindingLine finding={finding} />
          </li>
        ))}
      </ul>
    </CardInset>
  );
}

function FindingLine({ finding }: { finding: DeployPlanFinding }) {
  return (
    <>
      {finding.subject && <span className="font-mono text-foreground">{finding.subject}</span>}
      {finding.detail && (
        <span>
          {finding.subject ? " — " : ""}
          {finding.detail}
        </span>
      )}
      {!finding.subject && !finding.detail && (
        <span className="font-mono text-foreground">{finding.code ?? "change"}</span>
      )}
    </>
  );
}
