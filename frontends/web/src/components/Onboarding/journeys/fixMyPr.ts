/**
 * The workflows journey's example: "Fix my PR", imported from YAML as the
 * user's own workflow (a draft), complete and wired, schedule included. The
 * tour opens it on the canvas and explains it; the user builds nothing.
 *
 * Seeded from two places: the tour's first step (`ensureFixMyPrExample`,
 * once per project; the slug goes in the journey context as `exampleSlug`)
 * and New workflow's "Fix my PR (example)" start. Both go through
 * `seedFixMyPr`, which emits `workflow:draftCreated`.
 */

import { fetchWorkflow, importWorkflow } from "../../../hooks/workflow-queries";
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

/** This browser's IANA timezone, for the example's weekday schedule; UTC when unknown. */
export function localTimezone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

/** The template with its `name:`, `title:` and the schedule's `timezone:` replaced. */
export function fixMyPrYaml(slug: string, title: string = FIX_MY_PR_TITLE, timezone: string = localTimezone()): string {
  return FIX_MY_PR_TEMPLATE.replace(/^name: .*$/m, `name: ${slug}`)
    .replace(/^title: .*$/m, `title: ${JSON.stringify(title.trim() || FIX_MY_PR_TITLE)}`)
    .replace(/^(\s+)timezone: UTC$/m, `$1timezone: ${/^[A-Za-z0-9_+\-/]+$/.test(timezone) ? timezone : "UTC"}`);
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

/** The journey context keys the example is recorded under. */
export const EXAMPLE_SLUG = "exampleSlug";
export const EXAMPLE_PROJECT = "exampleProjectId";

async function exists(projectId: string, slug: string): Promise<boolean> {
  try {
    const { workflow } = await fetchWorkflow(projectId, { name: slug }, { fresh: true });
    return !!workflow;
  } catch {
    return false;
  }
}

/**
 * The example's slug in `projectId`: the one this tour made before, if it is
 * still there, or a fresh import. Never reuses a "Fix my PR" the user made
 * some other way (the previous tour left an unfinished one).
 */
export async function ensureFixMyPrExample(projectId: string): Promise<string> {
  const context = useTourStore.getState().contexts?.workflows ?? {};
  const known = context[EXAMPLE_SLUG];
  if (typeof known === "string" && known && context[EXAMPLE_PROJECT] === projectId && (await exists(projectId, known))) {
    return known;
  }
  const seeded = await seedFixMyPr(projectId);
  useTourStore.getState().setJourneyContext("workflows", { [EXAMPLE_SLUG]: seeded.slug, [EXAMPLE_PROJECT]: projectId });
  return seeded.slug;
}
