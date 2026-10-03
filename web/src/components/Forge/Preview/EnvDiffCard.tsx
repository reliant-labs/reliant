// Copyright (c) 2025 Reliant Labs

/**
 * ONE ENVIRONMENT'S DIFF CARD (§8.2) — what this checkout would change there.
 *
 * ── WHY THE CARD IS CLOSED WHEN IT ARRIVES ──────────────────────────────────
 *
 * Opening it is what asks the daemon, and the daemon's answer costs a REAL KCL
 * render: measured at 2.2–2.5s per environment, two at a time, on the machine
 * the user is also working on. A page that rendered every declared environment
 * on mount would spend ten seconds of someone's CPU to populate cards they
 * might never look at — and it would do it every time they opened the tab.
 *
 * So the fetch is bound to the disclosure rather than to the mount, and the
 * binding is the contract the tests pin: a closed card issues NO query. That is
 * why `enabled` is passed down from `open` instead of being defaulted true in
 * the hook, and why this component owns the toggle rather than taking data as a
 * prop. A parent that fetched and passed the result down would have to decide
 * when to fetch, and the honest answer is "when someone opens this one".
 *
 * Reopening is cheap: React Query holds the answer for the mount, so the
 * second open does not re-render anything.
 *
 * ── THE FOUR THINGS A CARD CAN SAY, AND WHY NONE IS AN ERROR ────────────────
 *
 *   changes      forge's §8.2 categories with counts, expandable to the items
 *   no changes   an ANSWERED diff that found nothing — "deploying this is a
 *                no-op", which is a real and useful answer
 *   first render nothing has ever been deployed here, so there is nothing to
 *                compare against. NOT "everything is new": forge's `added`
 *                holds the whole render, and showing "149 added" where the
 *                card normally shows changes reads as 149 pending changes
 *   unavailable  forge refused, or could not render, or the daemon is offline
 *
 * The last one is ONE LINE AND NEVER AN ERROR BANNER. Preview could not look;
 * that says nothing about the environment, which Live is showing correctly on
 * the other tab. A red banner here would tell the user their environment is
 * broken when what broke was a render on their laptop.
 */

import { useState } from "react";
import { ChevronDown, ChevronRight } from "lucide-react";

import { useForgeEnvDiff } from "@/hooks/forge-queries";
import {
  diffCategories,
  diffEntryFor,
  diffHasChanges,
  diffIsAnswered,
  diffIsFirstRender,
  unavailableReason,
  type EnvDiffCategory,
  type ForgeEnvDiffEntry,
} from "@/services/forge/envDiff";

/** The one line a card shows when the daemon is not answering at all. */
export const DIFF_UNAVAILABLE_COPY =
  "This environment's diff is unavailable right now.";

export interface EnvDiffCardProps {
  projectId: string | null;
  /** The environment this card is about. */
  env: string;
  /**
   * The checkout to render. Empty means the project's main checkout, which is
   * what the daemon uses when nothing is chosen.
   */
  checkoutPath: string;
}

export function EnvDiffCard({ projectId, env, checkoutPath }: EnvDiffCardProps) {
  const [open, setOpen] = useState(false);

  // THE FETCH IS THE DISCLOSURE. `enabled` is the whole lazy contract: until
  // someone opens this card there is no query, and therefore no render on the
  // user's machine.
  const diff = useForgeEnvDiff(projectId, { env, checkoutPath, enabled: open });

  const outcome = diff.data;
  const entry = outcome?.kind === "report" ? diffEntryFor(outcome.report, env) : null;

  return (
    <section
      data-testid={`env-diff-card-${env}`}
      data-open={open}
      className="rounded-lg border border-border"
    >
      <button
        type="button"
        onClick={() => setOpen((previous) => !previous)}
        aria-expanded={open}
        aria-label={`What this checkout would change in ${env}`}
        data-testid={`env-diff-toggle-${env}`}
        className="flex w-full items-center gap-x-3 rounded-lg px-3 py-2 text-left hover:bg-accent/50"
      >
        {open ? (
          <ChevronDown className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
        ) : (
          <ChevronRight className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
        )}
        <span className="text-sm font-medium text-foreground">{env}</span>
        <span className="ml-auto text-xs text-muted-foreground">
          {/* A CLOSED CARD MAKES NO CLAIM. It has not asked, so "no changes"
              here would be a statement nobody checked — the one thing the
              whole module is shaped to avoid. */}
          {!open
            ? "Show what would change"
            : diff.isLoading
              ? "Rendering your checkout…"
              : summaryOf(entry, outcome?.kind)}
        </span>
      </button>

      {open && (
        <div className="space-y-3 border-t border-border px-3 py-3" data-testid={`env-diff-body-${env}`}>
          <EnvDiffBody
            env={env}
            entry={entry}
            isLoading={diff.isLoading}
            unreachable={outcome != null && outcome.kind !== "report"}
            failed={diff.error != null}
          />
        </div>
      )}
    </section>
  );
}

/** The collapsed card's right-hand summary, once there is an answer. */
function summaryOf(entry: ForgeEnvDiffEntry | null, kind: string | undefined): string {
  if (!entry) return kind === "report" ? "Not in forge's report" : "Unavailable";
  if (!diffIsAnswered(entry)) return "Unavailable";
  if (diffIsFirstRender(entry)) return "Nothing deployed yet";
  if (!diffHasChanges(entry)) return "No changes";
  const total = diffCategories(entry).reduce((sum, category) => sum + category.count, 0);
  return `${total} ${total === 1 ? "change" : "changes"}`;
}

function EnvDiffBody({
  env,
  entry,
  isLoading,
  unreachable,
  failed,
}: {
  env: string;
  entry: ForgeEnvDiffEntry | null;
  isLoading: boolean;
  unreachable: boolean;
  failed: boolean;
}) {
  if (isLoading) {
    return (
      <p className="text-sm text-muted-foreground" data-testid={`env-diff-loading-${env}`}>
        Rendering your checkout…
      </p>
    );
  }

  // The daemon did not answer, or forge refused the whole report. ONE LINE.
  // Not an ErrorAlert: see the header comment — this is Preview being unable
  // to look, not the environment being broken.
  if (failed || unreachable || !entry) {
    return (
      <p className="text-sm text-muted-foreground" data-testid={`env-diff-unavailable-${env}`}>
        {DIFF_UNAVAILABLE_COPY}
      </p>
    );
  }

  // forge answered, per environment, that this one's diff is not an answer.
  // Forge's own words, because it knows why.
  if (!diffIsAnswered(entry)) {
    return (
      <div className="space-y-1" data-testid={`env-diff-unanswered-${env}`}>
        <p className="text-sm text-muted-foreground">{unavailableReason(entry)}</p>
        {(entry.wrote ?? []).length > 0 && (
          <ul className="space-y-0.5 font-mono text-2xs text-muted-foreground">
            {(entry.wrote ?? []).map((path) => (
              <li key={path}>{path}</li>
            ))}
          </ul>
        )}
      </div>
    );
  }

  // NOTHING RECORDED TO COMPARE AGAINST. Stated plainly, and the object count
  // is deliberately described as what the render declares rather than as a
  // change set.
  if (diffIsFirstRender(entry)) {
    const declared = entry.diff?.added?.length ?? 0;
    return (
      <div className="space-y-1" data-testid={`env-diff-first-render-${env}`}>
        <p className="text-sm text-foreground">
          Nothing has been deployed to {env} yet, so there is nothing to compare against.
        </p>
        {declared > 0 && (
          <p className="text-xs text-muted-foreground">
            This checkout declares {declared} {declared === 1 ? "object" : "objects"} for {env}.
            Deploy once and later previews will compare against it.
          </p>
        )}
      </div>
    );
  }

  const categories = diffCategories(entry);
  const kindChange = entry.diff?.kind_changed;

  if (categories.length === 0 && !kindChange) {
    return (
      <p className="text-sm text-foreground" data-testid={`env-diff-no-changes-${env}`}>
        No differences. Deploying this checkout to {env} would change nothing.
      </p>
    );
  }

  return (
    <div className="space-y-3">
      {/* A kind change is an ERROR, not a change: a kind is immutable, so this
          cannot be deployed at all. Stated before the categories so it is not
          read as one more item in the list. */}
      {kindChange && (
        <p className="text-sm text-destructive" data-testid={`env-diff-kind-changed-${env}`}>
          This environment is {kindChange.live ?? "unknown"} and cannot become{" "}
          {kindChange.candidate ?? "unknown"}. An environment's kind cannot be changed.
        </p>
      )}

      {/* Weaker evidence, said out loud. A diff against a shape that was merely
          declared is not the same claim as one against what was actually
          applied, and only forge knows which it had. */}
      {entry.live_source === "declared_shape" && (
        <p className="text-xs text-muted-foreground" data-testid={`env-diff-weak-live-${env}`}>
          Compared against this environment's declared shape — nothing has been built for it yet,
          so this is weaker evidence than a comparison against a deployed build.
        </p>
      )}

      {entry.would_be_created && (
        <p className="text-xs text-muted-foreground" data-testid={`env-diff-would-create-${env}`}>
          Deploying would create {env}; it does not exist yet.
        </p>
      )}

      <ul className="space-y-2" data-testid={`env-diff-categories-${env}`}>
        {categories.map((category) => (
          <CategoryRow key={category.key} env={env} category={category} />
        ))}
      </ul>
    </div>
  );
}

/**
 * One §8.2 category: its count, expandable to the items behind it.
 *
 * The count comes from the length of the list it expands to, so the two cannot
 * disagree — a card claiming three changes that opens onto two items is a
 * trust problem, not a cosmetic one.
 */
function CategoryRow({ env, category }: { env: string; category: EnvDiffCategory }) {
  const [open, setOpen] = useState(false);
  return (
    <li data-testid={`env-diff-category-${env}-${category.key}`} data-count={category.count}>
      <button
        type="button"
        onClick={() => setOpen((previous) => !previous)}
        aria-expanded={open}
        className="flex w-full items-center gap-x-2 text-left text-sm text-foreground hover:text-primary"
      >
        {open ? (
          <ChevronDown className="h-3 w-3 shrink-0 text-muted-foreground" aria-hidden="true" />
        ) : (
          <ChevronRight className="h-3 w-3 shrink-0 text-muted-foreground" aria-hidden="true" />
        )}
        <span>{category.label}</span>
        <span className="text-muted-foreground">{category.count}</span>
      </button>
      {open && (
        <ul
          className="mt-1 space-y-0.5 pl-5 text-xs text-muted-foreground"
          data-testid={`env-diff-items-${env}-${category.key}`}
        >
          {category.items.map((item) => (
            <li key={item}>{item}</li>
          ))}
        </ul>
      )}
    </li>
  );
}
