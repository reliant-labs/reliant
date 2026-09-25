// Copyright (c) 2025 Reliant Labs

/**
 * THE VISUAL CONTRACT TESTS.
 *
 * These pin the one property this screen exists to guarantee: there are THREE
 * categories of outcome, and `not_verified` / `unreachable` / `untagged` are
 * visually distinct from BOTH "proven good" and "proven bad".
 *
 * Each test asserts on the RENDERED DISTINCTION, not merely that something
 * rendered. Two hooks are used together and both are checked:
 *
 *   `data-certainty` — the semantic category. A regression that mapped
 *                      not_verified onto known-good would flip this and fail.
 *   the class list   — the actual visible treatment. A regression that kept the
 *                      attribute honest but gave unknown the same solid fill as
 *                      match would pass an attribute-only test, so the
 *                      fill/border axis is asserted directly and the three
 *                      treatments are compared against each other rather than
 *                      against hardcoded strings.
 *
 * Asserting both is the point. Either alone can be satisfied by a rendering that
 * reintroduces the bug.
 */

import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";

import { TopologyView } from "../TopologyView";
import type { TopologyViewProps } from "../TopologyView";
import { CERTAINTY_STYLES } from "../stateVocabulary";
import type { ForgeOutcome, ForgeTopologyReport } from "@/services/forge/topology";
import { ForgeReachability } from "@/gen/reliant/v1/forge_pb";
import type { ForgeReportMeta } from "@/gen/reliant/v1/forge_pb";

// Tooltip portals into document.body and measures layout; jsdom is fine with it,
// but it wraps children in a div — assertions below never rely on the DOM shape
// around a cell, only on the cell element itself.

function meta(overrides: Partial<ForgeReportMeta> = {}): ForgeReportMeta {
  return {
    isForgeProject: true,
    supported: true,
    forgeVersion: "v0.9.1",
    unsupportedReason: "",
    exitCode: 0,
    reachability: ForgeReachability.UNSPECIFIED,
    unreachableReason: "",
    ...overrides,
  } as ForgeReportMeta;
}

function reportOutcome(report: ForgeTopologyReport): ForgeOutcome<ForgeTopologyReport> {
  return { kind: "report", meta: meta(), report };
}

/**
 * A report shaped like control-plane's real one: four images as the union across
 * envs, prod bound and current, one env never promoted.
 */
function baseReport(): ForgeTopologyReport {
  return {
    project: "control-plane",
    latest_release: "v1.5.15",
    releases: ["v1.5.15", "v1.5.14", "v1.3.0"],
    images: ["control-plane", "internal-console", "reliant", "workspace-base"],
    environments: [
      {
        env: "prod",
        declared: true,
        bound: true,
        release: "v1.5.15",
        promoted_at: "2026-09-10T13:53:22Z",
        release_known: true,
        git: { commit: "8585315aaaa", tag: "", dirty: false },
        kube_context: "gke_reliant-labs-475814_us-central1_prod",
        namespace: "control-plane-prod",
        lag: { latest_release: "v1.5.15", current: true, releases_behind: 0 },
        images: [
          { image: "control-plane", digest: "sha256:1ea566aaaaaa", state: "not_verified" },
          { image: "internal-console", digest: "sha256:2bb677bbbbbb", state: "not_verified" },
          { image: "reliant", digest: "sha256:3cc788cccccc", state: "not_verified" },
          { image: "workspace-base", digest: "sha256:4dd899dddddd", state: "not_verified" },
        ],
      },
    ],
    verified: false,
    tally: { not_verified: 4, match: 0, drift: 0, missing: 0, untagged: 0, unreachable: 0 },
    ok: true,
  };
}

/** withStates rewrites prod's image cells to the given states, in column order. */
function withStates(states: string[]): ForgeTopologyReport {
  const report = baseReport();
  const env = report.environments![0];
  env.images = env.images!.map((img, i) => ({ ...img, state: states[i] ?? img.state }));
  return report;
}

function renderView(
  outcome: ForgeOutcome<ForgeTopologyReport>,
  extra: Partial<TopologyViewProps> = {}
) {
  return render(
    <TopologyView
      outcome={outcome}
      isLoading={false}
      onVerify={vi.fn()}
      projectName="control-plane"
      {...extra}
    />
  );
}

/** Returns the state-bearing span inside a cell (the element carrying the treatment). */
function cellBadge(env: string, image: string): HTMLElement {
  const cell = screen.getByTestId(`cell-${env}-${image}`);
  const badge = cell.querySelector<HTMLElement>("[data-certainty]");
  expect(badge, `cell ${env}/${image} should carry a certainty-bearing element`).not.toBeNull();
  return badge!;
}

describe("the three-level certainty vocabulary", () => {
  it("renders not_verified visually distinct from match — different category, fill AND border", () => {
    // control-plane verified (match), internal-console never checked.
    renderView(reportOutcome(withStates(["match", "not_verified", "match", "match"])));

    const verified = cellBadge("prod", "control-plane");
    const unchecked = cellBadge("prod", "internal-console");

    // 1. Different semantic category.
    expect(verified.dataset.certainty).toBe("known-good");
    expect(unchecked.dataset.certainty).toBe("unknown");

    // 2. Different visible treatment, asserted on the axes that carry it.
    //    match is FILLED with a SOLID border; not_verified has NO fill and a
    //    DASHED border. This is what stops "not checked" reading as "fine".
    expect(verified.className).toContain("border-solid");
    expect(verified.className).toMatch(/bg-success/);
    expect(unchecked.className).toContain("border-dashed");
    expect(unchecked.className).toContain("bg-transparent");
    expect(unchecked.className).not.toMatch(/bg-success/);

    // 3. The two class lists are not merely different strings — the fill/border
    //    axis differs, which is the axis that survives greyscale.
    expect(verified.className).not.toBe(unchecked.className);

    // 4. And the words differ, so the distinction survives without CSS at all.
    expect(verified.textContent).toContain("Match");
    expect(unchecked.textContent).toContain("Not checked");
  });

  it("renders unreachable as unknown — not as drift, and not as ok", () => {
    renderView(reportOutcome(withStates(["unreachable", "drift", "match", "not_verified"])));

    const unreachable = cellBadge("prod", "control-plane");
    const drifted = cellBadge("prod", "internal-console");
    const matched = cellBadge("prod", "reliant");

    // Unknown, categorically — neither of the two CERTAIN categories.
    expect(unreachable.dataset.certainty).toBe("unknown");
    expect(unreachable.dataset.certainty).not.toBe("known-bad");
    expect(unreachable.dataset.certainty).not.toBe("known-good");

    // Not painted as a failure: a VPN blip must not look like a release defect,
    // or the check gets switched off.
    expect(unreachable.className).not.toMatch(/bg-destructive/);
    expect(unreachable.className).not.toMatch(/text-destructive/);
    // Nor as a pass.
    expect(unreachable.className).not.toMatch(/bg-success/);
    expect(unreachable.className).not.toMatch(/text-success/);
    // It takes the provisional treatment.
    expect(unreachable.className).toContain("border-dashed");

    // And it is distinguishable from BOTH certain neighbours in the same table.
    expect(unreachable.className).not.toBe(drifted.className);
    expect(unreachable.className).not.toBe(matched.className);
    expect(unreachable.textContent).toContain("Unreachable");
  });

  it("renders untagged as unknown — bytes that cannot be proven are not a pass", () => {
    // The real prod case: 3 match + 1 untagged, because internal-console runs by
    // a mutable tag.
    renderView(reportOutcome(withStates(["match", "untagged", "match", "match"])));

    const untagged = cellBadge("prod", "internal-console");
    const matched = cellBadge("prod", "control-plane");

    expect(untagged.dataset.certainty).toBe("unknown");
    expect(untagged.className).toContain("border-dashed");
    expect(untagged.className).not.toMatch(/bg-success/);
    expect(untagged.className).not.toMatch(/bg-destructive/);
    expect(untagged.className).not.toBe(matched.className);
    expect(untagged.textContent).toContain("Untagged");
  });

  it("gives the three categories three mutually distinct treatments", () => {
    // Pinned at the vocabulary level too, so a restyle that accidentally makes
    // two categories identical fails here even if no cell test happens to
    // compare that particular pair.
    const good = CERTAINTY_STYLES["known-good"];
    const bad = CERTAINTY_STYLES["known-bad"];
    const unknown = CERTAINTY_STYLES.unknown;

    expect(good.container).not.toBe(bad.container);
    expect(good.container).not.toBe(unknown.container);
    expect(bad.container).not.toBe(unknown.container);
    expect(good.foreground).not.toBe(unknown.foreground);
    expect(bad.foreground).not.toBe(unknown.foreground);

    // The certain pair is filled and solid; the unknown one is neither. That is
    // the axis, and it must not be reduced to hue.
    expect(good.container).toContain("border-solid");
    expect(bad.container).toContain("border-solid");
    expect(unknown.container).toContain("border-dashed");
    expect(unknown.container).toContain("bg-transparent");
  });

  it("counts the three categories in the legend rather than collapsing to pass/fail", () => {
    renderView(reportOutcome(withStates(["match", "drift", "not_verified", "untagged"])));

    // 1 match, 1 drift, 2 unknown (not_verified + untagged).
    expect(screen.getByTestId("legend-known-good").textContent).toContain("1");
    expect(screen.getByTestId("legend-known-bad").textContent).toContain("1");
    expect(screen.getByTestId("legend-unknown").textContent).toContain("2");
  });
});

describe("an image absent from an environment's release", () => {
  it("is distinguishable from a zero or a blank cell", () => {
    const report = baseReport();
    // prod's release only contains two of the four union images.
    report.environments![0].images = [
      { image: "control-plane", digest: "sha256:aaa", state: "match" },
      { image: "reliant", digest: "sha256:bbb", state: "match" },
    ];
    renderView(reportOutcome(report));

    const absent = screen.getByTestId("cell-prod-internal-console");
    // Marked structurally, not left empty — a blank invites "missing" or "zero".
    expect(absent.dataset.absent).toBe("true");
    expect(absent.textContent).toContain("·");
    // It says so in text for a screen reader, and it carries no certainty at all
    // because forge made no claim about it.
    expect(absent.textContent).toContain("Not in this release");
    expect(absent.querySelector("[data-certainty]")).toBeNull();

    // A present cell in the same row is not confusable with it.
    const present = screen.getByTestId("cell-prod-control-plane");
    expect(present.dataset.absent).toBeUndefined();
    expect(present.querySelector("[data-certainty]")).not.toBeNull();
  });
});

describe("environment-level facts", () => {
  it("labels promoted_at as PROMOTE time, never deploy time", () => {
    renderView(reportOutcome(baseReport()));
    const promoted = screen.getByTestId("promoted-prod");
    expect(promoted.textContent?.toLowerCase()).toContain("promoted");
    expect(promoted.textContent?.toLowerCase()).not.toContain("deploy");
  });

  it("badges a release cut from a dirty tree", () => {
    const report = baseReport();
    report.environments![0].release = "v1.3.0";
    report.environments![0].git = { commit: "deadbeef", tag: "", dirty: true };
    renderView(reportOutcome(report));
    expect(screen.getByTestId("dirty-prod").textContent).toContain("dirty tree");
  });

  it("does not badge a clean release as dirty", () => {
    renderView(reportOutcome(baseReport()));
    expect(screen.queryByTestId("dirty-prod")).toBeNull();
  });

  it("shows declared:false as its own state rather than dropping the environment", () => {
    const report = baseReport();
    report.environments!.push({
      env: "staging",
      declared: false,
      bound: true,
      release: "v1.3.0",
      release_known: true,
      note: "bound in the ledger, but this checkout has no deploy/kcl/staging/",
      images: [{ image: "control-plane", digest: "sha256:aaa", state: "not_verified" }],
    });
    renderView(reportOutcome(report));

    expect(screen.getByTestId("env-row-staging")).toBeTruthy();
    expect(screen.getByTestId("binding-staging").textContent).toContain("not declared here");
    // Forge's own explanation is carried, not replaced with our wording.
    expect(screen.getByTestId("env-row-staging").textContent).toContain("deploy/kcl/staging/");
  });

  it("shows bound:false as normal, and offers no verify for it", () => {
    const report = baseReport();
    report.environments!.push({
      env: "dev",
      declared: true,
      bound: false,
      release_known: false,
      images: [],
    });
    renderView(reportOutcome(report));

    const row = screen.getByTestId("env-row-dev");
    expect(screen.getByTestId("binding-dev").textContent).toContain("never promoted");
    // Not an error treatment.
    expect(row.className).not.toMatch(/destructive/);
    // Nothing to verify against — there is no binding.
    expect(row.querySelector('button[aria-label^="Verify dev"]')).toBeNull();
  });

  it("keeps the promote-not-deploy caveat in the column header too", () => {
    // The caveat used to live only in the stacked cell's tooltip. Now that the
    // timestamp has a column of its own, the HEADER is what labels it for a
    // reader skimming the table, so it is pinned here.
    renderView(reportOutcome(baseReport()));
    const header = screen.getByRole("columnheader", { name: /promoted/i });
    expect(header.textContent?.toLowerCase()).not.toContain("deploy");
  });

  it("reports promotion lag in both units forge provides", () => {
    const report = baseReport();
    report.environments![0].release = "v1.3.0";
    report.environments![0].lag = {
      latest_release: "v1.5.15",
      current: false,
      releases_behind: 3,
      behind_seconds: 6_000_000,
      behind: "69d10h",
    };
    renderView(reportOutcome(report));

    const row = screen.getByTestId("env-row-prod");
    expect(row.textContent).toContain("3 releases behind");
    expect(row.textContent).toContain("69d10h");
  });
});

/**
 * The table has to BE a table.
 *
 * The regression these guard against is the one the screen shipped with: every
 * env-level fact stacked into the row header's flex column, so rows were ~200px
 * tall, nothing lined up with the image cells, and the "columns" existed only
 * for images. Asserting on `<td>` count and on real `<th scope="col">` headers
 * pins the structure rather than the styling, so a restyle is free but a
 * collapse back into one cell is not.
 */
describe("the matrix is a real table", () => {
  it("gives every env-level fact its own column header", () => {
    renderView(reportOutcome(baseReport()));
    for (const name of [/environment/i, /release/i, /status/i, /promoted/i, /runs on/i, /actions/i]) {
      expect(screen.getByRole("columnheader", { name })).toBeTruthy();
    }
  });

  it("puts each fact in its own cell rather than stacking them in the row header", () => {
    renderView(reportOutcome(baseReport()));
    const row = screen.getByTestId("env-row-prod");

    // Exactly one row header — the env name — and everything else is a <td>.
    expect(row.querySelectorAll("th").length).toBe(1);

    // release + status + promoted + runs-on + 4 images + actions = 9 data cells.
    expect(row.querySelectorAll("td").length).toBe(4 + 4 + 1);

    // The release is NOT inside the row header any more.
    const rowHeader = row.querySelector("th")!;
    expect(rowHeader.textContent).toContain("prod");
    expect(rowHeader.textContent).not.toContain("v1.5.15");
  });

  it("renders a header for every image column, as an identifier", () => {
    renderView(reportOutcome(baseReport()));
    const header = screen.getByRole("columnheader", { name: "internal-console" });
    // An image name is an identifier, so it keeps the mono face and its case.
    expect(header.querySelector(".font-mono")).not.toBeNull();
  });
});

/**
 * Clickable environments.
 *
 * `onOpenEnv` is the topology screen's answer to "I want things to be more
 * clickable and navigatable": the env name becomes the entry point into that
 * env's status screen. Two properties matter and both are asserted — the
 * control is REAL (a button with an accessible name, not a div with a handler),
 * and it does NOT swallow the row, because Promote…/Deploy… are siblings and
 * nesting interactive elements would produce overlapping hit targets.
 */
describe("environment rows as navigation", () => {
  it("makes the environment name a real control with an accessible name", () => {
    const onOpenEnv = vi.fn();
    renderView(reportOutcome(baseReport()), { onOpenEnv, projectId: "proj_1" });

    const control = screen.getByRole("button", { name: "View prod status" });
    fireEvent.click(control);
    expect(onOpenEnv).toHaveBeenCalledWith("prod");
  });

  it("does not nest the promote and deploy controls inside it", () => {
    renderView(reportOutcome(baseReport()), { onOpenEnv: vi.fn(), projectId: "proj_1" });

    const envControl = screen.getByRole("button", { name: "View prod status" });
    const promote = screen.getByTestId("promote-open-prod");
    const deploy = screen.getByTestId("deploy-open-prod");

    expect(envControl.contains(promote)).toBe(false);
    expect(envControl.contains(deploy)).toBe(false);
    expect(promote.contains(envControl)).toBe(false);
  });

  it("renders the env as inert text when there is nowhere to navigate", () => {
    renderView(reportOutcome(baseReport()));
    expect(screen.queryByRole("button", { name: "View prod status" })).toBeNull();
    // The name is still there — it just is not a control.
    expect(within(screen.getByTestId("env-row-prod")).getByText("prod")).toBeTruthy();
  });
});
