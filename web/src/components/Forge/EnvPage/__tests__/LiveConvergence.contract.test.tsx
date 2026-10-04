// Copyright (c) 2025 Reliant Labs

/**
 * INTENT VERSUS OBSERVED — the contract Live's state line has to hold.
 *
 * The model: an environment's INTENT ("should run v12") is the primary record,
 * written by a promotion. Whether it got there is a SECONDARY observation, made
 * by the platform watching the cluster converge. Nothing is reported by a
 * client, so nothing is labelled as a report.
 *
 * ── THE PROPERTY THESE TESTS EXIST FOR ──────────────────────────────────────
 *
 * ABSENCE MUST NEVER RENDER AS AGREEMENT. Three states mean "we cannot say" —
 * nothing observed, a stale reading, a reading against an older promotion — and
 * each of them has to be visibly distinct from "confirmed running". That is the
 * one failure mode worth a contract test: it is a single wrong `default:` arm
 * away at all times, the resulting screen looks perfectly healthy, and the
 * thing it is wrong about is whether production is running the release someone
 * shipped.
 *
 * The fixture set is the full state space on purpose — converged, converging,
 * failed, unknown, not-reported, never-built — because the bug is always in the
 * arm nobody wrote a case for.
 *
 * ── AND THE ONE THAT IS ABOUT THE SHAPE, NOT THE WORDS ──────────────────────
 *
 * A self-managed environment must render through the SAME component, with no
 * branch on kind. Convergence used to be answerable only where the platform
 * placed the workloads, and the plan for every other kind was a state derived
 * from a forge process's own report — which is why such rows had to say
 * "reported by forge" to be honest. One observation source for every kind is
 * what retired that label, and a `kind === "self_managed"` branch creeping back
 * into the convergence display is how the problem returns.
 */

import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import type { LiveConvergence, LiveEnv } from "@/services/forge/live";

import { LiveState } from "../LiveState";
import { LiveReleases } from "../LiveReleases";
import { LiveSection, type LiveSectionProps } from "../LiveSection";

// ── Fixtures: one per state of the observed half ────────────────────────────

function liveEnv(overrides: Partial<LiveEnv> = {}): LiveEnv {
  return {
    id: "denv_01HZXABCDEF",
    name: "prod",
    project: "hounders",
    kind: "persistent",
    declaredShape: {
      kind: "persistent",
      workloads: [{ name: "api", runtime: "hosted", cluster: "" }],
      secrets: [],
      domains: [],
      clusters: [],
    },
    declaredBy: null,
    release: "v12",
    releaseProvenance: null,
    promotedAt: "2026-10-01T14:02:00.000Z",
    promotedByActor: "ci",
    promotedByUserId: "",
    phase: "unspecified",
    observed: { state: "not-reported" },
    drift: { state: "not-reported" },
    driftDetail: "",
    provenance: "v12 · main@abc1234",
    ...overrides,
  };
}

/** Confirmed on the promoted bundle. */
const CONVERGED = liveEnv({
  phase: "succeeded",
  observed: { state: "converged", observedAt: "2026-10-01T14:04:00.000Z" },
  drift: { state: "in_sync", observedAt: "2026-10-01T14:04:00.000Z" },
  driftDetail: "converged on the promoted bundle sha256:abcd across 1 cluster",
});

/** Observed at another revision, nothing failed: mid-rollout. */
const CONVERGING = liveEnv({
  phase: "progressing",
  observed: { state: "converging", observedAt: "2026-10-01T14:03:00.000Z" },
  drift: { state: "drifted", observedAt: "2026-10-01T14:03:00.000Z" },
  driftDetail: "cluster hosted-us-central1 is running sha256:0000; the promoted bundle is sha256:abcd",
});

/** The platform reported a failure. The arm an operator actually reads. */
const FAILED = liveEnv({
  phase: "degraded",
  observed: { state: "failed", observedAt: "2026-10-01T14:40:00.000Z" },
  drift: { state: "drifted", observedAt: "2026-10-01T14:40:00.000Z" },
  driftDetail: "the reconciler reported a failure on cluster hosted-us-central1: HealthCheckFailed",
});

/** A reading exists and settles nothing: stale, or against an older promotion. */
const UNKNOWN = liveEnv({
  phase: "unknown",
  observed: { state: "unknown", observedAt: "2026-10-01T09:00:00.000Z" },
  drift: { state: "unknown", observedAt: "2026-10-01T09:00:00.000Z" },
  driftDetail: "the newest observation of cluster hosted-us-central1 was made against an earlier promotion",
});

/**
 * NOTHING OBSERVED — and this is the COMMON CASE, not an edge one. The
 * platform's convergence observer is dark by default, so this is what nearly
 * every environment renders as today. It has to look like a normal state.
 */
const NOT_REPORTED = liveEnv();

/** Declared from a render, nothing promoted. There is no intent to compare. */
const NEVER_PROMOTED = liveEnv({ release: "", promotedAt: undefined, promotedByActor: "" });

/** The same observed state, on a cluster the customer owns. */
const SELF_MANAGED_CONVERGED = liveEnv({
  name: "staging",
  kind: "self_managed",
  phase: "succeeded",
  observed: { state: "converged", observedAt: "2026-10-01T14:04:00.000Z" },
  drift: { state: "in_sync", observedAt: "2026-10-01T14:04:00.000Z" },
  declaredShape: {
    kind: "self_managed",
    workloads: [{ name: "api", runtime: "cluster", cluster: "prod-gke" }],
    secrets: [],
    domains: [],
    clusters: ["prod-gke"],
  },
});

// ── The state line ──────────────────────────────────────────────────────────

describe("the observed half of the state line", () => {
  it("states the intent in the present tense, with who promoted it", () => {
    render(<LiveState env={CONVERGED} />);
    expect(screen.getByTestId("live-state-intent")).toHaveTextContent(
      /should be running v12, promoted by ci/i
    );
  });

  it.each([
    ["converged", CONVERGED, /confirmed running/i],
    ["converging", CONVERGING, /still rolling out/i],
    ["failed", FAILED, /couldn't finish rolling out/i],
    ["unknown", UNKNOWN, /can't confirm what's running/i],
    ["not-reported", NOT_REPORTED, /not confirmed yet/i],
  ])("says what was observed for %s", (_name, env, wanted) => {
    render(<LiveState env={env} />);
    expect(screen.getByTestId("live-state-observed")).toHaveTextContent(wanted);
  });

  /**
   * THE LOAD-BEARING ASSERTION. Every state that is not `converged` must be
   * distinguishable from it, and none of them may borrow the confirmed arm's
   * language. A regression here is a green badge over an unapplied release.
   */
  it.each([
    ["converging", CONVERGING],
    ["failed", FAILED],
    ["unknown", UNKNOWN],
    ["not-reported", NOT_REPORTED],
  ])("never renders %s as confirmed", (_name, env) => {
    const { container } = render(<LiveState env={env} />);
    const text = container.textContent ?? "";
    expect(text).not.toMatch(/confirmed running/i);
    expect(text).not.toMatch(/matches what you asked for/i);
    // And the machine-readable half agrees with the words, so a styling
    // change cannot silently diverge from the verdict.
    expect(screen.getByTestId("live-state")).not.toHaveAttribute("data-observed", "converged");
  });

  it("keeps the two absences apart, because they call for different answers", () => {
    // "our readings went stale" is a problem; "nothing has looked yet" is
    // Tuesday. One sentence for both would make the first invisible.
    const { container: unknown, unmount } = render(<LiveState env={UNKNOWN} />);
    const unknownText = unknown.textContent ?? "";
    unmount();
    const { container: absent } = render(<LiveState env={NOT_REPORTED} />);
    expect(unknownText).not.toEqual(absent.textContent ?? "");
  });

  /**
   * Nothing observed is NOT a fault, and must not be dressed as one. This is
   * the state of every environment today, so styling it as a warning would put
   * one on every screen and train people to ignore the real thing.
   */
  it("renders an unobserved environment in the quiet register", () => {
    const { container } = render(<LiveState env={NOT_REPORTED} />);
    expect(container.querySelector(".text-destructive")).toBeNull();
    expect(container.textContent ?? "").not.toMatch(/error|failed|problem|warning/i);
  });

  it("shows the platform's verbatim reason for a failure, as subordinate detail", () => {
    render(<LiveState env={FAILED} />);
    // The plain sentence leads; the searchable string is detail beside it.
    expect(screen.getByTestId("live-state-observed")).toHaveTextContent(
      /couldn't finish rolling out/i
    );
    expect(screen.getByTestId("live-state-detail")).toHaveTextContent(/HealthCheckFailed/);
  });

  it("does not repeat the server's detail line for the states that mean fine or nothing-yet", () => {
    for (const env of [CONVERGED, UNKNOWN, NOT_REPORTED]) {
      const { unmount } = render(<LiveState env={env} />);
      expect(screen.queryByTestId("live-state-detail")).not.toBeInTheDocument();
      unmount();
    }
  });
});

// ── One component for every kind ────────────────────────────────────────────

describe("a self-managed environment renders through the same component", () => {
  it("produces the same observed sentence as a hosted one", () => {
    const { container: hosted, unmount } = render(<LiveState env={CONVERGED} />);
    const hostedObserved = hosted.querySelector('[data-testid="live-state-observed"]')?.textContent;
    unmount();

    const { container: own } = render(<LiveState env={SELF_MANAGED_CONVERGED} />);
    const ownObserved = own.querySelector('[data-testid="live-state-observed"]')?.textContent;

    expect(ownObserved).toEqual(hostedObserved);
  });

  /**
   * ASSERTED AGAINST THE SOURCE, deliberately. A rendering test can only prove
   * the two kinds agree on the fixtures it happens to pass; what has to hold is
   * that the component CANNOT disagree, for any input. Reading the file is the
   * only way to pin the absence of a branch rather than the behaviour of one,
   * and the absence is the thing that retired the "reported by forge" label.
   */
  it("has no branch on environment kind in the convergence display", () => {
    const here = dirname(fileURLToPath(import.meta.url));
    const source = readFileSync(join(here, "..", "LiveState.tsx"), "utf8");
    const code = source.replace(/\/\*[\s\S]*?\*\/|\/\/.*$/gm, "");
    expect(code).not.toMatch(/\.kind\b/);
    expect(code).not.toMatch(/isPlacedKind/);
    expect(code).not.toMatch(/self_managed/);
  });
});

// ── The merged timeline ─────────────────────────────────────────────────────

const PROMOTION = {
  id: "promo-1",
  releaseVersion: "v12",
  kind: "promote" as const,
  fromEnvironmentId: "",
  promotedByUserId: "",
  promotedByActor: "ci",
  note: "",
  createdAt: "2026-10-01T14:02:00.000Z",
  artifacts: [{ name: "api", digest: "sha256:aaaaaaaaaaaa" }],
};

const OBSERVED_OK: LiveConvergence = {
  id: "conv-1",
  state: "converged",
  reason: "ReconciliationSucceeded",
  message: "",
  cluster: "hosted-us-central1",
  observedAt: "2026-10-01T14:04:00.000Z",
};

const OBSERVED_FAIL: LiveConvergence = {
  id: "conv-2",
  state: "failed",
  reason: "HealthCheckFailed",
  message: "deployment api not ready",
  cluster: "hosted-us-central1",
  observedAt: "2026-10-01T14:40:00.000Z",
};

describe("the releases timeline merges intent and observations", () => {
  it("orders both kinds newest first in one list", () => {
    render(
      <LiveReleases
        env={CONVERGED}
        promotions={[PROMOTION]}
        convergences={[OBSERVED_OK, OBSERVED_FAIL]}
        isLoading={false}
        error={null}
      />
    );
    const entries = Array.from(
      screen.getByTestId("live-releases").querySelectorAll("li")
    ).map((li) => li.getAttribute("data-testid"));
    // 14:40 failure, then 14:04 confirmation, then the 14:02 promotion.
    expect(entries).toEqual(["convergence-conv-2", "convergence-conv-1", "promotion-promo-1"]);
  });

  it("marks the two kinds distinctly, with observations as the secondary one", () => {
    render(
      <LiveReleases
        env={CONVERGED}
        promotions={[PROMOTION]}
        convergences={[OBSERVED_OK]}
        isLoading={false}
        error={null}
      />
    );
    expect(screen.getByTestId("promotion-promo-1")).toHaveAttribute("data-entry", "promotion");

    const observation = screen.getByTestId("convergence-conv-1");
    expect(observation).toHaveAttribute("data-entry", "observation");
    // Indented and one size down: a note attached to the decisions, not a peer.
    expect(observation.className).toMatch(/pl-10/);
    expect(observation.className).toMatch(/text-2xs|py-2/);
  });

  it("carries the platform's own words for a failed observation", () => {
    render(
      <LiveReleases
        env={FAILED}
        promotions={[PROMOTION]}
        convergences={[OBSERVED_FAIL]}
        isLoading={false}
        error={null}
      />
    );
    const row = screen.getByTestId("convergence-conv-2");
    expect(row).toHaveAttribute("data-state", "failed");
    expect(row).toHaveTextContent(/HealthCheckFailed/);
    expect(row).toHaveTextContent(/deployment api not ready/);
  });

  /**
   * The common case today: promotions exist, nothing has observed them. The
   * timeline is just the promotions, with NO placeholder row per entry — a
   * "not observed" line beside every promotion is noise that crowds out the
   * entries that mean something.
   */
  it("shows only the promotions when nothing has been observed", () => {
    render(
      <LiveReleases
        env={NOT_REPORTED}
        promotions={[PROMOTION]}
        convergences={[]}
        isLoading={false}
        error={null}
      />
    );
    expect(screen.getByTestId("promotion-promo-1")).toBeInTheDocument();
    expect(screen.getByTestId("live-releases").querySelectorAll("li")).toHaveLength(1);
  });

  it("sorts an undated entry last rather than to the top", () => {
    // An undated row claiming the newest position would displace the one entry
    // a reader looks for first. "We don't know when" is not "just now".
    render(
      <LiveReleases
        env={CONVERGED}
        promotions={[PROMOTION]}
        convergences={[{ ...OBSERVED_OK, observedAt: undefined }]}
        isLoading={false}
        error={null}
      />
    );
    const entries = Array.from(
      screen.getByTestId("live-releases").querySelectorAll("li")
    ).map((li) => li.getAttribute("data-testid"));
    expect(entries).toEqual(["promotion-promo-1", "convergence-conv-1"]);
  });

  it("still says never promoted when there is no history at all", () => {
    render(
      <LiveReleases
        env={NEVER_PROMOTED}
        promotions={[]}
        convergences={[]}
        isLoading={false}
        error={null}
      />
    );
    expect(screen.getByTestId("live-releases-declared")).toBeInTheDocument();
  });
});

// ── The section, end to end ─────────────────────────────────────────────────

function sectionProps(env: LiveEnv | null, overrides: Partial<LiveSectionProps> = {}) {
  return {
    envName: env?.name ?? "fresh",
    env,
    forgeProject: "hounders",
    projectId: "proj-1",
    status: undefined,
    statusLoading: false,
    statusError: null,
    promotions: [],
    promotionsLoading: false,
    promotionsError: null,
    convergences: [],
    selectedSecret: null,
    onSelectSecret: () => {},
    onOpenPreview: () => {},
    ...overrides,
  } satisfies LiveSectionProps;
}

/**
 * LiveSection renders the secrets subtree, which owns a React Query of its
 * own. These tests are about the convergence display, so the client exists
 * only to let the subtree mount — nothing here asserts on secrets.
 */
function renderSection(props: LiveSectionProps) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  return render(
    <QueryClientProvider client={client}>
      <LiveSection {...props} />
    </QueryClientProvider>
  );
}

describe("the Live section", () => {
  it("shows the state line once there is an intent to compare against", () => {
    renderSection(sectionProps(CONVERGED));
    expect(screen.getByTestId("live-state")).toHaveAttribute("data-observed", "converged");
  });

  /**
   * UNCHANGED, and asserted because it is the state most easily broken by
   * adding a state line above it: a never-built environment has no row at all,
   * and the page points at Preview instead of rendering an empty comparison.
   */
  it("says not built yet for an environment with no record, and points at Preview", () => {
    renderSection(sectionProps(null));
    const panel = screen.getByTestId("live-never-built");
    expect(panel).toHaveTextContent(/not built yet/i);
    expect(panel).toHaveTextContent("forge env build fresh");
    expect(screen.queryByTestId("live-state")).not.toBeInTheDocument();
  });

  it("omits the state line when nothing has been promoted, rather than comparing against nothing", () => {
    renderSection(sectionProps(NEVER_PROMOTED));
    expect(screen.queryByTestId("live-state")).not.toBeInTheDocument();
    expect(screen.getByTestId("live-declared-not-built")).toBeInTheDocument();
  });

  it("no longer claims the platform does not watch a customer's own cluster", () => {
    // The retired copy. It was true when the only record of such a deploy was
    // a forge process's own report; the state line above it is now a reading
    // the platform made, so saying this would contradict the screen.
    renderSection(sectionProps(SELF_MANAGED_CONVERGED));
    const note = screen.getByTestId("live-not-placed");
    expect(note.textContent ?? "").not.toMatch(/doesn't watch the cluster/i);
    expect(screen.getByTestId("live-state")).toHaveAttribute("data-observed", "converged");
  });
});
