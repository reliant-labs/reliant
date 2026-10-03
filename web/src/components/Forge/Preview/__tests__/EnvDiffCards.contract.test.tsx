// Copyright (c) 2025 Reliant Labs

/**
 * THE §8.2 DIFF CARDS' CONTRACT, against documents FORGE ACTUALLY PRODUCED.
 *
 * ── WHY THE FIXTURES ARE CAPTURED AND NOT WRITTEN ───────────────────────────
 *
 * Every fixture under __fixtures__ came out of the pinned forge, not out of a
 * keyboard:
 *
 *   env-diff-never-built.json  `forge env diff --all --json` in a freshly
 *                              scaffolded throwaway project (three envs, each
 *                              with nothing recorded to compare against)
 *   env-diff-error.json        the same, with one env's KCL made invalid, so
 *                              forge reports a REAL per-env `error` beside two
 *                              healthy ones
 *   env-diff-changed.json      release.DiffShapes — the same pure function
 *                              `forge env diff` calls — over two real rendered
 *                              shapes, one mutated per §8.2 category
 *   env-diff-identical.json    the same function over one shape twice: an
 *                              ANSWERED diff that found nothing
 *
 * This matters because the alternative already failed. The service layer's
 * first version was typed against invented field names — `objects_added`,
 * `objects_changed`, `images_changed`, `secrets_needed` — and forge emits NONE
 * of them; it emits `added`, `removed` and `changed` as arrays of objects. A
 * hand-written fixture would have used the invented names, every assertion
 * would have passed, and the UI would have rendered a nine-object change set as
 * "no changes" to the person about to deploy it. Tests that agree with the code
 * about a shape neither one checked are worse than no tests: they are evidence
 * pointing the wrong way.
 *
 * So these tests parse forge's bytes. If forge's document moves, they fail.
 */

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";

import neverBuilt from "./__fixtures__/env-diff-never-built.json";
import withError from "./__fixtures__/env-diff-error.json";
import changed from "./__fixtures__/env-diff-changed.json";
import identical from "./__fixtures__/env-diff-identical.json";

import type { ForgeEnvDiffReport } from "@/services/forge/envDiff";
import {
  diffCategories,
  diffEntryFor,
  diffHasChanges,
  diffIsAnswered,
  diffIsFirstRender,
} from "@/services/forge/envDiff";

// ── The daemon. Every test decides what diffEnv answers. ──

const diffEnv = vi.fn();

vi.mock("@/api/forge-grpc", () => ({
  forgeGrpc: {
    diffEnv: (...args: unknown[]) => diffEnv(...args),
  },
}));

import { EnvDiffCard } from "../EnvDiffCard";
import { EnvDiffCards, declaredEnvNames } from "../EnvDiffCards";

/** forge's report, wrapped as the transport's classified outcome. */
function report(document: unknown) {
  return Promise.resolve({ kind: "report", report: document as ForgeEnvDiffReport, meta: {} });
}

function renderCard(env: string) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  });
  return render(
    <QueryClientProvider client={client}>
      <EnvDiffCard projectId="proj-1" env={env} checkoutPath="/src/demo" />
    </QueryClientProvider>
  );
}

beforeEach(() => {
  diffEnv.mockReset();
});

// ─── THE LAZY CONTRACT ──────────────────────────────────────────────────────

describe("when the card has not been opened", () => {
  it("does NOT ask the daemon", async () => {
    // THE POINT OF THE WHOLE DESIGN. forge's answer is a real KCL render —
    // seconds of the user's own CPU, per environment — so a page that fetched
    // on mount would spend it on cards nobody looked at.
    diffEnv.mockImplementation(() => report(changed));

    renderCard("prod");

    // Nothing to wait FOR, which is the difficulty with asserting an absence.
    // A microtask flush is enough: an enabled query fires its queryFn during
    // the mount's effects, so if it were going to call, it already would have.
    await Promise.resolve();
    expect(diffEnv).not.toHaveBeenCalled();
    expect(screen.getByTestId("env-diff-card-prod")).toHaveAttribute("data-open", "false");
  });

  it("makes no claim about what would change", async () => {
    diffEnv.mockImplementation(() => report(identical));
    renderCard("prod");
    await Promise.resolve();

    // A closed card has not asked, so "No changes" here would be a statement
    // nobody checked — the exact failure this surface is shaped to avoid.
    expect(screen.queryByText(/No changes/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/No differences/i)).not.toBeInTheDocument();
    expect(screen.getByText(/Show what would change/i)).toBeInTheDocument();
  });

  it("asks EXACTLY ONCE, for its own environment, when opened", async () => {
    diffEnv.mockImplementation(() => report(changed));
    renderCard("prod");

    await userEvent.click(screen.getByTestId("env-diff-toggle-prod"));
    await waitFor(() => expect(diffEnv).toHaveBeenCalledTimes(1));

    // Its own env, not --all: the cost is per render, so a card buys one
    // environment rather than every environment's render at once.
    expect(diffEnv).toHaveBeenCalledWith(
      expect.objectContaining({ projectId: "proj-1", env: "prod", checkoutPath: "/src/demo" })
    );
    expect(diffEnv.mock.calls[0][0].all).not.toBe(true);
  });
});

// ─── THE §8.2 CATEGORIES, FROM FORGE'S OWN DOCUMENT ─────────────────────────

describe("an environment with changes", () => {
  it("groups forge's categories with counts", async () => {
    diffEnv.mockImplementation(() => report(changed));
    renderCard("prod");
    await userEvent.click(screen.getByTestId("env-diff-toggle-prod"));

    await waitFor(() =>
      expect(screen.getByTestId("env-diff-categories-prod")).toBeInTheDocument()
    );

    // Each of these is a real key in the captured document, and the counts are
    // the lengths of forge's own arrays.
    const expected: Array<[string, number]> = [
      ["objects_added", 1],
      ["objects_removed", 1],
      ["objects_changed", 1],
      ["workloads_added", 1],
      ["runtime_changes", 1],
      ["secrets_added", 3],
      ["domains_added", 1],
    ];
    for (const [key, count] of expected) {
      const row = screen.getByTestId(`env-diff-category-prod-${key}`);
      expect(row).toHaveAttribute("data-count", String(count));
    }
  });

  it("omits the categories that are empty rather than listing them as zero", async () => {
    diffEnv.mockImplementation(() => report(changed));
    renderCard("prod");
    await userEvent.click(screen.getByTestId("env-diff-toggle-prod"));
    await waitFor(() =>
      expect(screen.getByTestId("env-diff-categories-prod")).toBeInTheDocument()
    );

    // Twelve rows of mostly zeroes makes the reader scan for the ones that
    // happened, and reads as a checklist to verify. What changed is the
    // information.
    expect(
      screen.queryByTestId("env-diff-category-prod-clusters_removed")
    ).not.toBeInTheDocument();
    expect(
      screen.queryByTestId("env-diff-category-prod-domains_removed")
    ).not.toBeInTheDocument();
  });

  it("expands a category to the items behind it, and the count matches", async () => {
    diffEnv.mockImplementation(() => report(changed));
    renderCard("prod");
    await userEvent.click(screen.getByTestId("env-diff-toggle-prod"));
    await waitFor(() =>
      expect(screen.getByTestId("env-diff-category-prod-secrets_added")).toBeInTheDocument()
    );

    const row = screen.getByTestId("env-diff-category-prod-secrets_added");
    await userEvent.click(row.querySelector("button")!);

    const items = screen.getByTestId("env-diff-items-prod-secrets_added");
    // A card claiming three that opens onto two is a trust problem, not a
    // cosmetic one — which is why the count is the list's length.
    expect(items.querySelectorAll("li")).toHaveLength(3);
  });

  it("says which half of a changed object moved", async () => {
    // "would run a new build" and "config changed" ask different things of the
    // reader, and forge splits them precisely so the card need not flatten
    // them. The captured object has an image move and no config change.
    diffEnv.mockImplementation(() => report(changed));
    renderCard("prod");
    await userEvent.click(screen.getByTestId("env-diff-toggle-prod"));
    await waitFor(() =>
      expect(screen.getByTestId("env-diff-category-prod-objects_changed")).toBeInTheDocument()
    );

    const row = screen.getByTestId("env-diff-category-prod-objects_changed");
    await userEvent.click(row.querySelector("button")!);
    expect(screen.getByTestId("env-diff-items-prod-objects_changed").textContent).toContain(
      "would run a new build"
    );
  });

  it("distinguishes a MISSING secret from an unverifiable one", async () => {
    // forge#398's rule, carried through to the sentence a user reads. The
    // captured presence map has DB_URL false, API_KEY true, and deliberately
    // OMITS the external-provider secret — absent means unverifiable, and
    // telling that user to set a secret that is fine is the failure.
    diffEnv.mockImplementation(() => report(changed));
    renderCard("prod");
    await userEvent.click(screen.getByTestId("env-diff-toggle-prod"));
    await waitFor(() =>
      expect(screen.getByTestId("env-diff-category-prod-secrets_added")).toBeInTheDocument()
    );

    const row = screen.getByTestId("env-diff-category-prod-secrets_added");
    await userEvent.click(row.querySelector("button")!);
    const text = screen.getByTestId("env-diff-items-prod-secrets_added").textContent ?? "";

    expect(text).toContain("DB_URL [hosted] — not set yet");
    expect(text).toContain("API_KEY [hosted] — already set");
    expect(text).toContain("EXTERNAL_TOKEN [external_secrets] — declared; presence not verifiable");
  });
});

// ─── NO DIFFERENCES ─────────────────────────────────────────────────────────

describe("an environment with no differences", () => {
  it("says so plainly", async () => {
    diffEnv.mockImplementation(() => report(identical));
    renderCard("prod");
    await userEvent.click(screen.getByTestId("env-diff-toggle-prod"));

    await waitFor(() =>
      expect(screen.getByTestId("env-diff-no-changes-prod")).toBeInTheDocument()
    );
    expect(screen.getByTestId("env-diff-no-changes-prod").textContent).toContain(
      "No differences"
    );
  });

  it("is an ANSWER, not the absence of one", () => {
    // The two look identical in the data and mean opposite things. This is the
    // assertion that keeps them apart at the source.
    const entry = diffEntryFor(identical as ForgeEnvDiffReport, "prod")!;
    expect(diffIsAnswered(entry)).toBe(true);
    expect(diffHasChanges(entry)).toBe(false);
    expect(diffIsFirstRender(entry)).toBe(false);
  });
});

// ─── DIFF UNAVAILABLE ───────────────────────────────────────────────────────

describe("when the diff is unavailable", () => {
  it("shows forge's own per-environment reason, and no error banner", async () => {
    // From the captured document where one env's KCL was made invalid. The
    // other two envs in the same document are fine, which is the per-env
    // status's whole purpose.
    diffEnv.mockImplementation(() => report(withError));
    renderCard("staging");
    await userEvent.click(screen.getByTestId("env-diff-toggle-staging"));

    await waitFor(() =>
      expect(screen.getByTestId("env-diff-unanswered-staging")).toBeInTheDocument()
    );
    expect(screen.getByTestId("env-diff-unanswered-staging").textContent).toContain(
      "InvalidSyntax"
    );
    // NEVER an error banner: Preview could not look, which says nothing about
    // the environment — Live is showing it correctly on the other tab.
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("never reads a failed render as no changes", () => {
    const entry = diffEntryFor(withError as ForgeEnvDiffReport, "staging")!;
    expect(entry.status).toBe("error");
    expect(diffIsAnswered(entry)).toBe(false);
    // The pairing: this is false for a failure AND for a genuine no-op, so a
    // caller reading it alone renders a broken render as a safe deploy.
    expect(diffHasChanges(entry)).toBe(false);
    expect(diffCategories(entry)).toEqual([]);
  });

  it("shows one line when the daemon is offline", async () => {
    diffEnv.mockImplementation(() =>
      Promise.reject(new ConnectError("no daemon connected for user", Code.Unavailable))
    );
    renderCard("prod");
    await userEvent.click(screen.getByTestId("env-diff-toggle-prod"));

    // The generous timeout is not flake-padding: the forge queries retry an
    // Unavailable ONCE (forgeRetry), with React Query's backoff in between, and
    // that retry is deliberate — a daemon that is starting up answers the
    // second call. So the card is legitimately still "rendering" for about a
    // second, and asserting inside that window would be asserting against the
    // retry rather than against the outcome.
    await waitFor(
      () => expect(screen.getByTestId("env-diff-unavailable-prod")).toBeInTheDocument(),
      { timeout: 5_000 }
    );
    // One line, and NOT an error banner. The daemon being asleep says nothing
    // about the environment, which Live renders correctly from the control
    // plane on the other tab.
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(diffEnv).toHaveBeenCalledTimes(2);
  });
});

// ─── NEVER BUILT ────────────────────────────────────────────────────────────

describe("an environment nothing has been deployed to", () => {
  it("says there is nothing to compare, NOT that everything is new", async () => {
    // The §8.2 rule most likely to be got wrong. forge's `added` holds the
    // WHOLE render for a never-built env, so presenting it as a change set
    // invites someone to read a routine first render as pending changes.
    diffEnv.mockImplementation(() => report(neverBuilt));
    renderCard("dev");
    await userEvent.click(screen.getByTestId("env-diff-toggle-dev"));

    await waitFor(() =>
      expect(screen.getByTestId("env-diff-first-render-dev")).toBeInTheDocument()
    );
    const text = screen.getByTestId("env-diff-first-render-dev").textContent ?? "";
    expect(text).toContain("Nothing has been deployed to dev yet");
    // The objects are described as what the render DECLARES, never as changes.
    expect(text).toContain("declares 2 objects");
    expect(screen.queryByTestId("env-diff-categories-dev")).not.toBeInTheDocument();
  });

  it("is recognised from forge's own two signals", () => {
    const entry = diffEntryFor(neverBuilt as ForgeEnvDiffReport, "dev")!;
    // Both are set by forge independently and answer different questions:
    // live_source says nothing was recorded, live_unknown is the diff's own
    // statement that it had no live side.
    expect(entry.live_source).toBe("none");
    expect(entry.diff?.live_unknown).toBe(true);
    expect(diffIsFirstRender(entry)).toBe(true);
    // And the objects really are all there, which is why the flag is needed.
    expect((entry.diff?.added ?? []).length).toBeGreaterThan(0);
  });
});

// ─── WHICH CARDS APPEAR ─────────────────────────────────────────────────────

describe("the card list", () => {
  it("leads with the environment the user is looking at", () => {
    const names = declaredEnvNames(
      {
        environments: [
          { env: "dev", declared: true },
          { env: "prod", declared: true },
          { env: "staging", declared: true },
        ],
      },
      "staging"
    );
    expect(names).toEqual(["staging", "dev", "prod"]);
  });

  it("omits environments this checkout does not declare", () => {
    // Code that does not declare an environment would do nothing to it, so
    // Preview has no answer for it. That environment is Live's subject.
    const names = declaredEnvNames(
      { environments: [{ env: "dev", declared: true }, { env: "legacy", declared: false }] },
      "dev"
    );
    expect(names).toEqual(["dev"]);
  });

  it("is absent entirely when there is nothing to show", () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <EnvDiffCards projectId="proj-1" topology={null} checkoutPath="" currentEnv="" />
      </QueryClientProvider>
    );
    // A heading over a blank area asks the reader to work out whether
    // something failed.
    expect(screen.queryByTestId("env-diff-cards")).not.toBeInTheDocument();
  });

  it("renders a card per declared environment, none of which fetches", async () => {
    diffEnv.mockImplementation(() => report(neverBuilt));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <EnvDiffCards
          projectId="proj-1"
          topology={{
            environments: [
              { env: "dev", declared: true },
              { env: "prod", declared: true },
              { env: "staging", declared: true },
            ],
          }}
          checkoutPath=""
          currentEnv="prod"
        />
      </QueryClientProvider>
    );

    await Promise.resolve();
    expect(screen.getAllByTestId(/^env-diff-card-/)).toHaveLength(3);
    // THREE closed cards is three renders NOT paid for. `--all` would have
    // bought every one of them before anyone asked.
    expect(diffEnv).not.toHaveBeenCalled();
  });
});
