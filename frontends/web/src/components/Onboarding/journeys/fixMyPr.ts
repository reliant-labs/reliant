/**
 * The workflows journey's seed: "Fix my PR", imported from YAML as the
 * user's own draft (journeys-spec.md §2.1). Import stores a definition that
 * fails validation as a draft with its findings (ImportWorkflow, the same
 * rule as SaveWorkflow), which is what this template needs: it leaves out
 * the edge the user draws in W2 and the Approval they add in W4.
 *
 * Seeded from two places: New workflow's "Fix my PR (guided)" start, and
 * W1's "Create it for me". Both go through `seedFixMyPr`, which records the
 * slug in the journey context (later steps' routes read it) and emits
 * `workflow:draftCreated`.
 */

import { importWorkflow } from "../../../hooks/workflow-queries";
import { useTourStore } from "../../../store/tourStore";
import { tourEvents } from "../tourEvents";
import templateYaml from "./fixMyPrTemplate.yaml?raw";

/** `start` on `workflow:draftCreated` for the guided seed. */
export const FIX_MY_PR_START = "tour-fix-my-pr";
export const FIX_MY_PR_TITLE = "Fix my PR";
const BASE_SLUG = "fix-my-pr";
/** How many taken slugs (fix-my-pr, fix-my-pr-2, …) to step over before giving up. */
const MAX_SLUG_TRIES = 20;

export const FIX_MY_PR_TEMPLATE: string = templateYaml;

/**
 * A workflow name in the form the server compares it in (workflowref.Slug):
 * lower case, spaces and underscores as hyphens, nothing but [a-z0-9-], no
 * leading, trailing or repeated hyphens. New workflow derives a draft's slug
 * from its title the same way, so a renamed guided draft gets its own slug.
 */
export function workflowSlug(title: string): string {
  return title
    .trim()
    .toLowerCase()
    .replace(/[ _]/g, "-")
    .replace(/[^a-z0-9-]/g, "")
    .replace(/-+/g, "-")
    .replace(/^-|-$/g, "");
}

/**
 * The slug and title to try on attempt `n` (0-based), numbered like the
 * server's CreateWorkflowDraft: "Fix my PR" / fix-my-pr, then
 * "Fix my PR 2" / fix-my-pr-2, … A title with no slug-able characters
 * falls back to fix-my-pr.
 */
export function fixMyPrName(n: number, title: string = FIX_MY_PR_TITLE): { slug: string; title: string } {
  const base = title.trim() || FIX_MY_PR_TITLE;
  const slug = workflowSlug(base) || BASE_SLUG;
  return n === 0 ? { slug, title: base } : { slug: `${slug}-${n + 1}`, title: `${base} ${n + 1}` };
}

/** The template with its `name:` and `title:` replaced. */
export function fixMyPrYaml(slug: string, title: string = FIX_MY_PR_TITLE): string {
  return FIX_MY_PR_TEMPLATE.replace(/^name: .*$/m, `name: ${slug}`).replace(
    /^title: .*$/m,
    `title: ${JSON.stringify(title.trim() || FIX_MY_PR_TITLE)}`,
  );
}

export interface SeededWorkflow {
  slug: string;
  draftId: string;
  title: string;
}

/**
 * Import the template under the first free slug. Throws with the server's
 * message when the import fails for any reason other than a taken slug.
 */
export async function seedFixMyPr(projectId: string, title: string = FIX_MY_PR_TITLE): Promise<SeededWorkflow> {
  for (let n = 0; n < MAX_SLUG_TRIES; n++) {
    const name = fixMyPrName(n, title);
    const result = await importWorkflow(projectId, fixMyPrYaml(name.slug, name.title));
    if (result.conflict) continue;
    if (!result.success || !result.slug) throw new Error(result.message || "Could not create the workflow");
    const seeded = { slug: result.slug, draftId: result.id, title: name.title };
    useTourStore.getState().setJourneyContext("workflows", { workflowSlug: seeded.slug, draftId: seeded.draftId });
    tourEvents.emit("workflow:draftCreated", { slug: seeded.slug, draftId: seeded.draftId, start: FIX_MY_PR_START });
    return seeded;
  }
  throw new Error(
    `Every name from ${fixMyPrName(0, title).slug} to ${fixMyPrName(MAX_SLUG_TRIES - 1, title).slug} is taken`,
  );
}
