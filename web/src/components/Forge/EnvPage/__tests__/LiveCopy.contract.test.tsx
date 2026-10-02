// Copyright (c) 2025 Reliant Labs

/**
 * WHAT THE LIVE SURFACES MAY SAY — no internal nouns (#366).
 *
 * A customer did not choose our control plane, cannot visit its hostname, has
 * no use for the id it files their environment under, and does not run the
 * clusters we host. Naming any of those spends the most valuable line on the
 * screen telling them something they cannot act on — and in an ERROR state it
 * is worse than useless, because it answers a question they did not ask
 * instead of the one they did ("is my environment gone?").
 *
 * These tests pin the ABSENCE, which is exactly the kind of thing a later
 * "let's show a bit more detail" quietly undoes. They run over the states
 * where internal nouns previously leaked: the empty and error paths, which are
 * the ones nobody looks at in a screenshot review.
 *
 * The kube context is NOT banned everywhere — for a cluster the customer owns
 * it is their own name and carries information. It is banned for the
 * environments WE run, which is what the kinds distinguish.
 */

import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { Code, ConnectError } from "@connectrpc/connect";

import type { LiveEnv } from "@/services/forge/live";

import { LiveReleases } from "../LiveReleases";
import { LiveWorkloads } from "../LiveWorkloads";
import { CloudNotice } from "../../SourceNotices";

/** Nouns that are ours, not the customer's. */
const INTERNAL_NOUNS = [
  /control[ -]plane/i,
  /admin\.reliantapi\.com/i,
  /endpoint/i,
  /environment id/i,
  /\bdenv_/i,
  /ledger/i,
  /kube[ -]?context/i,
];

function expectNoInternalNouns(text: string) {
  for (const noun of INTERNAL_NOUNS) {
    expect(text, `copy must not name "${noun}"`).not.toMatch(noun);
  }
}

function liveEnv(overrides: Partial<LiveEnv> = {}): LiveEnv {
  return {
    id: "denv_01HZXABCDEF",
    name: "prod",
    project: "hounders",
    kind: "persistent",
    declaredShape: null,
    declaredBy: null,
    release: "",
    releaseProvenance: null,
    promotedByActor: "",
    promotedByUserId: "",
    phase: "unspecified",
    provenance: "",
    ...overrides,
  };
}

describe("the releases timeline", () => {
  it("names nothing internal while loading, empty, or failing", () => {
    const states = [
      { promotions: undefined, isLoading: true, error: null },
      { promotions: [], isLoading: false, error: null },
      {
        promotions: undefined,
        isLoading: false,
        error: new ConnectError("upstream connect error", Code.Unavailable) as unknown as Error,
      },
    ];

    for (const state of states) {
      const { container, unmount } = render(<LiveReleases env={liveEnv()} {...state} />);
      expectNoInternalNouns(container.textContent ?? "");
      unmount();
    }
  });

  it("says what to do when it could not load, rather than why our side failed", () => {
    render(
      <LiveReleases
        env={liveEnv()}
        promotions={undefined}
        isLoading={false}
        error={new Error("rpc failed") as Error}
      />
    );
    const text = screen.getByTestId("live-releases-error").textContent ?? "";
    expect(text).toMatch(/couldn't load/i);
    expect(text).toMatch(/try again/i);
  });

  it("no longer tells the reader to go and run git log themselves", () => {
    // The retired copy. A history the screen refuses to show, with homework
    // attached, is not a history.
    const { container } = render(
      <LiveReleases env={liveEnv()} promotions={[]} isLoading={false} error={null} />
    );
    expect(container.textContent ?? "").not.toMatch(/git log/i);
    expect(container.textContent ?? "").not.toMatch(/\.forge\/promotions/);
  });
});

describe("the workloads section", () => {
  it("names nothing internal in any state", () => {
    const states: Array<Parameters<typeof LiveWorkloads>[0]> = [
      { env: liveEnv(), status: undefined, isLoading: true, error: null },
      { env: liveEnv(), status: undefined, isLoading: false, error: null },
      { env: liveEnv(), status: undefined, isLoading: false, error: new Error("rpc failed") },
      { env: liveEnv({ kind: "local" }), status: undefined, isLoading: false, error: null },
      {
        env: liveEnv({ kind: "self_managed", declaredShape: null }),
        status: undefined,
        isLoading: false,
        error: null,
      },
    ];

    for (const props of states) {
      const { container, unmount } = render(<LiveWorkloads {...props} />);
      expectNoInternalNouns(container.textContent ?? "");
      unmount();
    }
  });

  it("names the environment, not our infrastructure, when it could not load", () => {
    render(
      <LiveWorkloads
        env={liveEnv({ name: "prod" })}
        status={undefined}
        isLoading={false}
        error={new Error("rpc failed")}
      />
    );
    const text = screen.getByTestId("live-workloads-error").textContent ?? "";
    expect(text).toMatch(/couldn't load what's running in prod/i);
    expect(text).toMatch(/try again/i);
  });
});

describe("the unavailable notices", () => {
  it("say what the reader cannot see and what to do, naming nothing internal", () => {
    for (const availability of ["no-access", "not-configured", "unreachable"] as const) {
      const { container, unmount } = render(
        <CloudNotice availability={availability} detail={undefined} />
      );
      expectNoInternalNouns(container.textContent ?? "");
      unmount();
    }
  });

  it("still passes the server's own message through as detail", () => {
    // The one place a technical string beats a friendly one: it is what a
    // support conversation needs, and it is clearly subordinate.
    render(<CloudNotice availability="unreachable" detail="connect: connection refused" />);
    expect(screen.getByTestId("forge-cloud-unreachable").textContent).toContain(
      "connect: connection refused"
    );
  });
});

describe("a hosted environment's identity", () => {
  it("never renders the id we file it under", () => {
    // The row's id is a primary key. It appeared on screen once as an empty
    // state ("none yet — this deploy creates it") that read as a fault the
    // user had to fix.
    const env = liveEnv({ id: "denv_01HZXABCDEF", release: "v12", provenance: "v12 · main@abc1234" });
    const { container } = render(
      <LiveReleases env={env} promotions={[]} isLoading={false} error={null} />
    );
    expect(container.textContent ?? "").not.toContain("denv_01HZXABCDEF");
  });
});
