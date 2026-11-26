/**
 * Every anchor a tour points at is still rendered somewhere.
 *
 * A journey step, phase or multi-spotlight label (and a contextual tip) finds
 * its element by selector. When a refactor drops or renames the attribute,
 * nothing fails: the spotlight just never finds its target, and the step shows
 * a centred card about a control that is not there. The one-shell nav
 * (reliant#3191) moved the chrome these selectors name, so this pins them.
 *
 * Static, over the source: for each anchor attribute a selector names
 * (`[data-tour='x']`, `[data-testid='x']`, …), some non-test file under src/
 * must render that attribute with that value: as a JSX attribute, a props
 * object key (`{ "data-testid": … }`) or, for test ids, a component's `testId`
 * prop, literally or as a template whose fixed prefix the value starts with
 * (`data-testid={`obs-issue-${id}`}`). A selector string naming the anchor
 * (`"[data-tour='x']"`) does not count. The shell nav is data
 * (Layout/sidebar/navConfig): there an item's `tour: "x"` is its `data-tour`,
 * which NavRenderer renders.
 * Data attributes (`data-trace-id`, `data-span-id`, …) are fixture values,
 * not anchors, and are not checked here (forgeJourney.test.tsx pins those).
 */
import { readdirSync, readFileSync, statSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

import { JOURNEYS } from "../journeys/registry";
// The workflows journey's content is registered by the Reliant shells.
import "../journeys/catalog";
import { CONTEXTUAL_TIP_DEFINITIONS } from "../contextualTipsRegistry";
import { DEMO_TIPS } from "../../Forge/Demo/tips/tipCopy";
import type { AdvanceOn, JourneyPhase, JourneyStep, StepPresentation } from "../types";

const SRC = resolve(dirname(fileURLToPath(import.meta.url)), "../../..");

/** The attributes that are anchors: a selector on any other attribute is not checked. */
const ANCHOR_ATTRS = ["data-tour", "data-onboarding", "data-contextual-tip", "data-testid", "aria-label"] as const;

function sourceFiles(dir: string, out: string[] = []): string[] {
  for (const entry of readdirSync(dir)) {
    if (entry === "__tests__" || entry === "__preview__" || entry === "__fixtures__" || entry === "gen" || entry === "node_modules") continue;
    const path = join(dir, entry);
    if (statSync(path).isDirectory()) sourceFiles(path, out);
    else if (/\.tsx?$/.test(entry) && !/\.(test|spec)\.tsx?$/.test(entry)) out.push(path);
  }
  return out;
}

/** The value expression after a setter: a string, a template, or a JSX `{…}` (to its first `}`). */
const VALUE = "\"[^\"]*\"|'[^']*'|`[^`]*`|\\{[^}]*\\}";

/** Where a line sets `attr`: `attr=`, `"attr":`, and a component's `testId` for test ids; never inside a selector (`[attr=`). */
const SETTERS: Record<string, RegExp> = Object.fromEntries(
  ANCHOR_ATTRS.map((attr) => {
    const names = attr === "data-testid" ? `${attr}|"${attr}"|testId` : `${attr}|"${attr}"`;
    return [attr, new RegExp(`(?<![\\[\\w-])(?:${names})\\s*[=:]\\s*(${VALUE})`, "g")];
  }),
);

/** The nav config's items name their `data-tour` as `tour: "x"` (NavItemBase.tour), and NavRenderer renders it. */
const NAV_CONFIG = join("components", "Layout", "sidebar", "navConfig");
const NAV_TOUR = new RegExp(`(?<![\\w-])tour\\s*:\\s*(${VALUE})`, "g");

/**
 * The shell nav's anchors: one set, which the public demo's journey and the
 * signed-in tour both point at (demo DI phase 3). Pinned here before any
 * journey names them, so a rename fails now, not when a tour lands on it.
 */
const SHELL_NAV_ANCHORS = [
  "forge-console-nav",
  "nav-drill-in",
  "nav-overview",
  "nav-observability",
  "nav-analytics",
  "nav-deployments",
  "nav-tests",
  "nav-data",
  "nav-security",
  "nav-configure",
];

/** For each anchor attribute, every value a non-test source line sets it to. */
const SET_VALUES: Record<string, string[]> = (() => {
  const values: Record<string, string[]> = Object.fromEntries(ANCHOR_ATTRS.map((a) => [a, []]));
  for (const file of sourceFiles(SRC)) {
    const navConfig = file.includes(NAV_CONFIG);
    for (const line of readFileSync(file, "utf8").split("\n")) {
      // A comment naming an anchor does not render it.
      if (/^\s*(\*|\/\/|\/\*|\{\/\*)/.test(line)) continue;
      for (const attr of ANCHOR_ATTRS) for (const m of line.matchAll(SETTERS[attr])) values[attr].push(m[1]);
      if (navConfig) for (const m of line.matchAll(NAV_TOUR)) values["data-tour"].push(m[1]);
    }
  }
  return values;
})();

/** Whether some line sets `attr` to `value`: a literal, or a template whose fixed prefix `value` starts with. */
function rendered(attr: string, value: string): boolean {
  return SET_VALUES[attr].some((set) => {
    if (set.includes(`"${value}"`) || set.includes(`'${value}'`)) return true;
    for (const m of set.matchAll(/`([^`$]+)\$\{/g)) if (value.startsWith(m[1])) return true;
    return false;
  });
}

interface Anchor {
  where: string;
  attr: string;
  value: string;
}

function anchorsOf(selector: string | undefined, where: string): Anchor[] {
  if (!selector) return [];
  const out: Anchor[] = [];
  for (const m of selector.matchAll(/\[([a-z-]+)=['"]([^'"]+)['"]\]/g)) {
    if ((ANCHOR_ATTRS as readonly string[]).includes(m[1])) out.push({ where, attr: m[1], value: m[2] });
  }
  return out;
}

/** The selectors a required action (one, or several together) or a phase's `when` waits on. */
const advanceSelectors = (on: AdvanceOn | AdvanceOn[] | undefined): string[] =>
  (Array.isArray(on) ? on : on ? [on] : []).flatMap((a) => (a.kind === "element" || a.kind === "click") && a.selector ? [a.selector] : []);

function presentationAnchors(p: StepPresentation | undefined, where: string): Anchor[] {
  if (!p) return [];
  return [...anchorsOf(p.target, where), ...(p.targets ?? []).flatMap((t) => anchorsOf(t.selector, `${where} "${t.label}"`))];
}

function stepAnchors(step: JourneyStep): Anchor[] {
  const where = step.id;
  return [
    ...presentationAnchors(step, where),
    ...Object.entries(step.copyByShell ?? {}).flatMap(([shell, copy]) => presentationAnchors(copy, `${where} (${shell})`)),
    ...advanceSelectors(step.advanceOn).flatMap((sel) => anchorsOf(sel, `${where} advanceOn`)),
    ...(step.phases ?? []).flatMap((phase: JourneyPhase, i) => [
      ...presentationAnchors(phase, `${where} phase ${i}`),
      ...advanceSelectors(phase.when).flatMap((sel) => anchorsOf(sel, `${where} phase ${i} when`)),
    ]),
  ];
}

const ANCHORS: Anchor[] = [
  ...Object.values(JOURNEYS).flatMap((journey) => [...journey.steps, ...(journey.outro ?? [])].flatMap(stepAnchors)),
  ...CONTEXTUAL_TIP_DEFINITIONS.flatMap((tip) => anchorsOf(tip.targetSelector, `tip ${tip.id}`)),
  // The public demo's page tips (Forge/Demo/tips): a renamed anchor leaves the tip in a corner, off its subject.
  ...Object.entries(DEMO_TIPS).flatMap(([key, tip]) => anchorsOf(tip.target, `demo-tip ${key}`)),
];

describe("tour anchors", () => {
  it("finds the anchors to check (both journeys and the contextual tips)", () => {
    const where = new Set(ANCHORS.map((a) => a.where.split(" ")[0]));
    expect(where).toContain("wf-outro-layout");
    expect(where).toContain("fg-console");
    // The forge journey's shell steps, over the shell nav's anchors.
    for (const id of ["fg-nav", "fg-deployments", "fg-analytics", "fg-tests", "fg-data", "fg-security", "fg-configure"]) expect(where).toContain(id);
    expect(where).toContain("tip");
    expect(where).toContain("demo-tip");
    expect(ANCHORS.length).toBeGreaterThan(20);
  });

  it("every anchor a step, phase, label or tip names is rendered by some component", () => {
    const missing = ANCHORS.filter((a) => !rendered(a.attr, a.value)).map((a) => `${a.where}: [${a.attr}='${a.value}']`);
    expect(missing).toEqual([]);
  });

  it("the shell nav renders its anchors (navConfig `tour`, NavRenderer's `forge-console-nav` and `nav-drill-in`)", () => {
    const missing = SHELL_NAV_ANCHORS.filter((id) => !rendered("data-tour", id));
    expect(missing).toEqual([]);
  });

  it("the workflows outro labels the chat chrome by data-tour anchors, not by aria-label", () => {
    const outro = JOURNEYS.workflows.outro?.find((s) => s.id === "wf-outro-layout");
    expect(outro?.targets?.map((t) => t.selector)).toEqual([
      "[data-onboarding='chat-input']",
      "[data-tour='header-sidebar-toggle']",
      "[data-tour='header-panel-toggles']",
    ]);
  });
});
