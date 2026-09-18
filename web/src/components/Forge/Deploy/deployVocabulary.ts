// Copyright (c) 2025 Reliant Labs

/**
 * THE DEPLOY SCREEN'S VOCABULARY, layered on stateVocabulary.ts rather than
 * forking it.
 *
 * The three certainty levels and their treatments are IMPORTED — solid fill and
 * a continuous border for a measured answer, no fill and a DASHED border for
 * anything unestablished. This screen adds no fourth level and no alternative
 * palette, because the whole value of that vocabulary is that a reader who
 * learned it on the topology matrix reads this screen the same way.
 *
 * What is added here is per-state detail WITHIN those levels:
 *
 *   ROLLOUT STATES. Four of them, three of which share the `unknown` treatment
 *   and none of which share an icon or a sentence. "forge stopped waiting"
 *   (timed_out), "forge was told not to wait" (not_waited) and "this build does
 *   not recognise what forge said" (unknown) are three different reasons there
 *   is no answer, and the operator's next move differs for each — so collapsing
 *   them into one grey dash would hide exactly the thing this screen exists to
 *   surface.
 *
 *   JOB DISPOSITIONS. Whether the invocation finished is a separate axis from
 *   whether the deploy worked, so it gets its own labels. `running` is not a
 *   success and not a failure; `unknown` means manifests may or may not have
 *   landed; `failed` is the single case where nothing can have shipped.
 *
 *   REFUSAL COPY. Four reasons, four different actions. A shared "deploy
 *   refused, try again" would be worse than useless: for already_running the
 *   correct move is to watch the deploy that exists rather than start a second
 *   one, and for guard_refused no amount of retrying helps until the kubeconfig
 *   changes.
 *
 * Only semantic tokens appear here: success / warning / destructive /
 * muted-foreground / border, all defined in index.css and driven by
 * data-color-scheme and .dark.
 */

import {
  AlertTriangle,
  CheckCircle2,
  CircleDashed,
  Clock,
  HelpCircle,
  Loader2,
  MinusCircle,
  ShieldAlert,
  Tag,
} from "lucide-react";
import type { LucideIcon } from "lucide-react";

import { CERTAINTY_STYLES } from "../stateVocabulary";
import type {
  DeployJobDisposition,
  DeployPreflightStatus,
  DeployRefusalReason,
  DeployRolloutMode,
  DeployRolloutState,
} from "@/services/forge/deploy";
import { rolloutCertainty } from "@/services/forge/deploy";

export interface RolloutStateStyle {
  /** Container classes, taken from the shared certainty treatment. */
  container: string;
  /** Icon + text colour, likewise shared. */
  foreground: string;
  icon: LucideIcon;
  label: string;
  /** One sentence saying what this state does and does not establish. */
  blurb: string;
}

/**
 * ROLLOUT_STATE_STYLES gives each state its container and foreground from the
 * SHARED certainty map, so a state cannot acquire a bespoke appearance here, and
 * its own icon and words, so three kinds of "we do not know" stay tellable
 * apart.
 */
export const ROLLOUT_STATE_STYLES: Record<DeployRolloutState, RolloutStateStyle> = {
  ready: {
    ...CERTAINTY_STYLES[rolloutCertainty("ready")],
    icon: CheckCircle2,
    label: "Ready",
    blurb: "The resource reached its ready condition. A positive answer.",
  },
  failed: {
    ...CERTAINTY_STYLES[rolloutCertainty("failed")],
    icon: AlertTriangle,
    label: "Failed",
    blurb: "The resource reported a genuine failure — not a budget that expired.",
  },
  timed_out: {
    ...CERTAINTY_STYLES[rolloutCertainty("timed_out")],
    icon: Clock,
    label: "Timed out",
    blurb:
      "The readiness budget expired with no verdict. NOT a failure and NOT a success — it may be mid-pull on a cold node, or it may be stuck. Forge does not know which.",
  },
  not_waited: {
    ...CERTAINTY_STYLES[rolloutCertainty("not_waited")],
    icon: MinusCircle,
    label: "Not waited on",
    blurb:
      "Forge never asked. The manifest was applied and nothing was observed converging, so this resource's state is unknown rather than fine.",
  },
  unknown: {
    ...CERTAINTY_STYLES[rolloutCertainty("unknown")],
    icon: CircleDashed,
    label: "Not known",
    blurb:
      "No outcome was recorded for this resource, or forge reported a state this build does not recognise. Unknown, not healthy.",
  },
};

/** Human label for the rollout mode, which changes what an absent answer MEANS. */
export const ROLLOUT_MODE_LABELS: Record<DeployRolloutMode, string> = {
  wait: "Waiting for every resource to become ready",
  warn: "Waiting and reporting, but not failing the deploy",
  skip: "Applying without waiting — nothing will be observed converging",
  unknown: "Forge did not say how it would wait",
};

/**
 * PREFLIGHT_STATUS_LABELS. Every `skipped_*` value says NOTHING WAS CHECKED
 * rather than nothing was wrong, in those words, because an unchecked deploy
 * presented as a clean one is the specific misreading this section prevents.
 */
export const PREFLIGHT_STATUS_LABELS: Record<DeployPreflightStatus, string> = {
  ran: "Ran against the live target",
  skipped_flag: "SKIPPED — nothing was checked",
  skipped_rollback: "Skipped: a rollback reuses what is already in the cluster, so there is nothing to pre-verify",
  skipped_no_cluster: "Skipped: this environment has no cluster to check anything against",
  unknown: "Not recorded — nothing was checked",
};

export interface JobDispositionStyle {
  container: string;
  foreground: string;
  icon: LucideIcon;
  label: string;
  /** The sentence an operator acts on. */
  blurb: string;
}

/**
 * JOB_DISPOSITION_STYLES describes the INVOCATION, not the deploy's verdict.
 *
 * `running` and `unknown` both take the unfilled, dashed treatment, because
 * neither is an outcome. They differ by icon (a spinner versus a question mark)
 * and by copy: one says an answer is coming, the other says none ever will.
 */
export const JOB_DISPOSITION_STYLES: Record<DeployJobDisposition, JobDispositionStyle> = {
  running: {
    ...CERTAINTY_STYLES.unknown,
    icon: Loader2,
    label: "Deploy in flight",
    blurb:
      "Forge is still applying and watching. This is neither a success nor a failure — the outcome does not exist yet.",
  },
  completed: {
    // Deliberately the unknown treatment: the JOB finished, which says nothing
    // about whether the deploy worked. The verdict is rendered separately, from
    // forge's report, and it is what carries a hue.
    ...CERTAINTY_STYLES.unknown,
    icon: CheckCircle2,
    label: "Forge finished and reported",
    blurb: "Forge exited and produced a report. Whether the deploy succeeded is what the report says.",
  },
  failed: {
    ...CERTAINTY_STYLES["known-bad"],
    icon: AlertTriangle,
    label: "Never started — nothing was applied",
    blurb:
      "The invocation did not get off the ground, so no manifest can have reached the cluster. This is the one failure that is safe to retry.",
  },
  unknown: {
    ...CERTAINTY_STYLES.unknown,
    icon: HelpCircle,
    label: "Outcome unknown — manifests may or may not have reached the cluster",
    blurb:
      "The deploy started and ended without a determinable outcome: killed by a timeout or a signal, the daemon restarted and lost the handle, or forge exited without a parseable report. Do not retry blind — verify the environment and see what is actually running.",
  },
};

export interface RefusalCopy {
  /** The headline. Every one of them states that nothing was applied. */
  heading: string;
  /** What happened, in terms of the claim the operator made. */
  explanation: string;
  /** The label on the offered action. */
  action: string;
}

/**
 * REFUSAL_COPY: four reasons, four actions.
 *
 * stale_declared_context comes first in the server's checking order and is the
 * gravest, because a wrong cluster is worse than a wrong release — so its copy
 * leads with the cluster names rather than with the word "refused".
 *
 * already_running is the one whose action is not a re-plan: a second concurrent
 * apply races the first one's rollout and leaves the cluster converging toward
 * two manifest streams, so the offer is to WATCH the deploy that exists.
 */
export const REFUSAL_COPY: Record<DeployRefusalReason, RefusalCopy> = {
  "stale-declared-context": {
    heading: "Refused — this environment now declares a DIFFERENT cluster",
    explanation:
      "Nothing was applied. The cluster you approved is not the one this environment's KCL names now, and forge deploys to the declared context — so continuing would have written to a cluster you never saw. Re-plan to see where it would land now.",
    action: "Show me the current plan",
  },
  "stale-current-release": {
    heading: "Refused — the bound release moved",
    explanation:
      "Nothing was applied. This environment is bound to a different release than the one you reviewed, and a deploy ships the bound release's pinned digests — so continuing would have shipped code nobody looked at. Re-plan to review what would ship now.",
    action: "Show me the current plan",
  },
  "guard-refused": {
    heading: "Refused by forge — it will not deploy this environment",
    explanation:
      "Nothing was applied. Forge's own declared-cluster guard stopped this: either kubectl's contexts could not be listed, or the context this environment declares is not in your kubeconfig. Re-planning will not change that until the kubeconfig does.",
    action: "Re-check",
  },
  "already-running": {
    heading: "Refused — a deploy of this environment is already in flight",
    explanation:
      "Nothing new was applied. Two concurrent applies race each other's rollout and leave the cluster converging toward two different manifest streams, so forge refused rather than queueing. Watch the deploy that is already running instead of starting another.",
    action: "Watch the running deploy",
  },
  unknown: {
    heading: "Refused — nothing was applied",
    explanation:
      "Nothing was applied. The server refused this deploy for a reason this build does not recognise; its own explanation is below. Re-plan to see the environment's current state.",
    action: "Show me the current plan",
  },
};

/** Icon for a refusal. Shared: every refusal is the guard doing its job. */
export const REFUSAL_ICON: LucideIcon = ShieldAlert;

/** Icon for a tag-pinned image — a caveat, not a failure. See stateVocabulary. */
export const TAG_PINNING_ICON: LucideIcon = Tag;
