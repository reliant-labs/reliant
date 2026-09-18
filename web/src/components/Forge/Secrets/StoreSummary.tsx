// Copyright (c) 2025 Reliant Labs

/**
 * The headline: what the provider is, what the store is, and the one number a
 * developer came here for.
 *
 * The missing count is the headline ONLY when it means something. `forge secret
 * ensure` exits non-zero when a declared secret has no value, and that exit is
 * what stops `forge env up` — so under a `file` provider the count is a
 * pre-flight verdict and is rendered as one. Under `external` or `none` the same
 * arithmetic is not a verdict at all, and the panel says what IS true instead of
 * putting a red number where the reader's eye expects one.
 *
 * The store line distinguishes four situations that all produce "no values":
 *
 *   absent        no store file has been created. The action is: create it.
 *   empty         a store file exists and holds nothing. Someone started this
 *                 and the values never landed — a half-finished setup, not an
 *                 untouched one, and worth saying so.
 *   populated     a store with keys in it.
 *   external      the values live where forge does not read.
 *
 * Flattening absent and empty into "no secrets" is the specific thing this panel
 * refuses to do, because the two call for different next steps.
 */

import { AlertTriangle, CheckCircle2, CircleDashed, FileWarning, FileX, Lock } from "lucide-react";
import type { LucideIcon } from "lucide-react";

import { cn } from "@/lib/utils";
import type { ForgeSecretsReport } from "@/services/forge/secrets";
import {
  blocksEnvUp,
  missingNames,
  presenceTally,
  providerExplanation,
  providerKind,
  providerLabel,
  storeKeyCount,
  storeState,
} from "@/services/forge/secrets";

import { CERTAINTY_LABELS, CERTAINTY_STYLES } from "../stateVocabulary";

interface StoreLine {
  icon: LucideIcon;
  title: string;
  body: string;
  /** Dashed/unfilled when the state is UNKNOWN rather than certainly bad. */
  tone: "bad" | "good" | "unknown";
}

function describeStore(report: ForgeSecretsReport): StoreLine {
  const state = storeState(report);
  const path = report.store_path || "the configured path";
  switch (state) {
    case "absent":
      return {
        icon: FileX,
        title: "No store file yet",
        tone: "bad",
        body: `Nothing exists at ${path}. The store has not been created, so no declared secret can have a value. Create it and set the values before bringing this environment up.`,
      };
    case "empty":
      return {
        icon: FileWarning,
        title: "Store exists, but it is empty",
        tone: "bad",
        body: `${path} exists and holds no keys at all. This is a half-finished setup rather than an untouched one — the file was created and the values never landed.`,
      };
    case "populated":
      return {
        icon: CheckCircle2,
        title: `Store holds ${storeKeyCount(report)} key${storeKeyCount(report) === 1 ? "" : "s"}`,
        tone: "good",
        body: `Read from ${path}. Reliant is told which keys exist, never what they contain.`,
      };
    case "external":
      return {
        icon: Lock,
        title: "Values are held externally",
        tone: "unknown",
        body: "An external secret manager owns these values. Forge has no store file for this environment and did not consult the manager, so presence below is unknown rather than missing.",
      };
    default:
      return {
        icon: CircleDashed,
        title: "No secret store configured",
        tone: "unknown",
        body: "This environment declares no secret provider, so there is no store for forge to have read. The secrets below are what workloads ask for, not a statement about any value.",
      };
  }
}

const TONE_CLASSES: Record<StoreLine["tone"], string> = {
  bad: CERTAINTY_STYLES["known-bad"].container,
  good: CERTAINTY_STYLES["known-good"].container,
  unknown: CERTAINTY_STYLES.unknown.container,
};

const TONE_FOREGROUND: Record<StoreLine["tone"], string> = {
  bad: CERTAINTY_STYLES["known-bad"].foreground,
  good: CERTAINTY_STYLES["known-good"].foreground,
  unknown: CERTAINTY_STYLES.unknown.foreground,
};

export function StoreSummary({ report }: { report: ForgeSecretsReport }) {
  const kind = providerKind(report);
  const holdsValues = kind === "file";
  const missing = missingNames(report);
  const blocked = blocksEnvUp(report);
  const tally = presenceTally(report);
  const store = describeStore(report);
  const StoreIcon = store.icon;

  return (
    <div className="space-y-3" data-testid="secrets-summary">
      {/* The verdict, when there is one to give. */}
      {holdsValues ? (
        <div
          data-testid="secrets-verdict"
          data-blocked={blocked ? "true" : "false"}
          className={cn(
            "flex items-start gap-3 rounded-lg p-4",
            blocked ? CERTAINTY_STYLES["known-bad"].container : CERTAINTY_STYLES["known-good"].container
          )}
        >
          {blocked ? (
            <AlertTriangle
              className={cn("mt-0.5 h-5 w-5 shrink-0", CERTAINTY_STYLES["known-bad"].foreground)}
              aria-hidden="true"
            />
          ) : (
            <CheckCircle2
              className={cn("mt-0.5 h-5 w-5 shrink-0", CERTAINTY_STYLES["known-good"].foreground)}
              aria-hidden="true"
            />
          )}
          <div className="space-y-1">
            <p
              className={cn(
                "text-sm font-medium",
                blocked
                  ? CERTAINTY_STYLES["known-bad"].foreground
                  : CERTAINTY_STYLES["known-good"].foreground
              )}
            >
              {blocked ? (
                <>
                  <span data-testid="missing-count" className="font-mono">
                    {missing.length}
                  </span>{" "}
                  declared secret{missing.length === 1 ? "" : "s"} ha
                  {missing.length === 1 ? "s" : "ve"} no value
                </>
              ) : (
                "Every declared secret has a value"
              )}
            </p>
            <p className="text-xs text-muted-foreground">
              {blocked
                ? "forge secret ensure exits non-zero on this, so forge env up will refuse to start this environment until the values are set."
                : "forge secret ensure passes for this environment, so nothing here blocks forge env up."}
            </p>
          </div>
        </div>
      ) : (
        // Provider does not hold values: there is no verdict to render, and a
        // count of "missing" here would be an invented outage.
        <div
          data-testid="secrets-no-verdict"
          className={cn("flex items-start gap-3 rounded-lg p-4", CERTAINTY_STYLES.unknown.container)}
        >
          <CircleDashed
            className={cn("mt-0.5 h-5 w-5 shrink-0", CERTAINTY_STYLES.unknown.foreground)}
            aria-hidden="true"
          />
          <div className="space-y-1">
            <p className="text-sm font-medium text-muted-foreground">
              Presence is unknown for this environment
            </p>
            <p className="text-xs text-muted-foreground">{providerExplanation(kind)}</p>
          </div>
        </div>
      )}

      <div className="grid gap-3 md:grid-cols-2">
        {/* Provider. */}
        <div className="rounded-lg border border-border p-3" data-testid="secrets-provider">
          <p className="text-xs font-medium text-foreground">
            {providerLabel(kind)}
            {report.provider && (
              <span className="pl-2 font-mono text-2xs text-muted-foreground">
                provider: {report.provider}
              </span>
            )}
          </p>
          <p className="pt-1.5 text-2xs leading-snug text-muted-foreground">
            {providerExplanation(kind)}
          </p>
        </div>

        {/* Store. The four-way distinction lives here. */}
        <div
          className={cn("rounded-lg p-3", TONE_CLASSES[store.tone])}
          data-testid="secrets-store"
          data-store-state={storeState(report)}
        >
          <div className={cn("flex items-center gap-2", TONE_FOREGROUND[store.tone])}>
            <StoreIcon className="h-4 w-4 shrink-0" aria-hidden="true" />
            <p className="text-xs font-medium">{store.title}</p>
          </div>
          <p className="pt-1.5 text-2xs leading-snug text-muted-foreground">{store.body}</p>
        </div>
      </div>

      {/* The three-level roll-up, same categories as the topology screen. */}
      <div className="flex flex-wrap gap-x-4 gap-y-1" data-testid="presence-tally">
        {(["known-good", "known-bad", "unknown"] as const).map((certainty) => (
          <span
            key={certainty}
            data-testid={`tally-${certainty}`}
            className={cn("inline-flex items-baseline gap-1.5 text-2xs", CERTAINTY_STYLES[certainty].foreground)}
          >
            <span className="font-mono text-xs">{tally[certainty]}</span>
            {CERTAINTY_LABELS[certainty]}
          </span>
        ))}
      </div>
    </div>
  );
}
