// Copyright (c) 2025 Reliant Labs

/**
 * The session's watch over queued deploys: what it starts watching, when it
 * settles, and that it settles EXACTLY ONCE — the notification hangs off that.
 */

import { describe, expect, it } from "vitest";

import type { LiveConvergenceState, LiveEnv, LiveHold } from "../live";
import {
  QUEUED_POLL_MS,
  RELEASING_POLL_MS,
  observeQueuedDeploys,
  watchPollInterval,
  watchedProjects,
  type WatchedDeploys,
} from "../queuedDeploys";

const BILLING: LiveHold = {
  kind: "billing",
  promotionId: "promo-2",
  reason: "this runs compute (1 workload) and the organization has no active compute plan",
  fix: "",
  actionUrl: "",
  callerCanResolve: true,
};

function env(name: string, args: { queued?: boolean; observed?: LiveConvergenceState; release?: string } = {}): LiveEnv {
  return {
    id: `cp-${name}`,
    name,
    project: "hounders",
    kind: "persistent",
    declaredShape: null,
    declaredBy: null,
    release: args.release ?? "v13",
    releaseProvenance: null,
    promotedByActor: "",
    promotedByUserId: "",
    phase: args.queued ? "held" : "unspecified",
    observed: { state: args.queued ? "queued" : (args.observed ?? "not-reported") },
    drift: { state: "not-reported" },
    driftDetail: "",
    provenance: "",
    holds: args.queued ? [BILLING] : [],
  };
}

describe("observeQueuedDeploys", () => {
  it("watches a queued environment, and nothing else", () => {
    const { watched, settled } = observeQueuedDeploys({}, "hounders", [env("prod", { queued: true }), env("staging")]);
    expect(watched).toEqual({
      "cp-prod": { envId: "cp-prod", envName: "prod", forgeProject: "hounders", queued: true },
    });
    expect(settled).toEqual([]);
  });

  it("follows a released deploy through its rollout and settles it LIVE exactly once", () => {
    let watched: WatchedDeploys = observeQueuedDeploys({}, "hounders", [env("prod", { queued: true })]).watched;

    // Billing set up: the hold is gone, the release is rolling out.
    const rolling = observeQueuedDeploys(watched, "hounders", [env("prod", { observed: "converging" })]);
    expect(rolling.settled).toEqual([]);
    expect(rolling.watched["cp-prod"]?.queued).toBe(false);
    watched = rolling.watched;

    const live = observeQueuedDeploys(watched, "hounders", [env("prod", { observed: "converged" })]);
    expect(live.settled).toEqual([
      {
        deploy: { envId: "cp-prod", envName: "prod", forgeProject: "hounders", queued: false },
        outcome: "live",
        release: "v13",
      },
    ]);
    expect(live.watched).toEqual({});

    // The same reading again — a poll, a second screen — says nothing more.
    expect(observeQueuedDeploys(live.watched, "hounders", [env("prod", { observed: "converged" })]).settled).toEqual([]);
  });

  it("settles a rollout that failed, once, as failed", () => {
    const watched = observeQueuedDeploys({}, "hounders", [env("prod", { queued: true })]).watched;
    const { settled, watched: after } = observeQueuedDeploys(watched, "hounders", [env("prod", { observed: "failed" })]);
    expect(settled.map((s) => s.outcome)).toEqual(["failed"]);
    expect(after).toEqual({});
  });

  /**
   * The environment converged on the OLD release while the new one waited.
   * Nothing settles until the hold is gone: a queued env's reading is about
   * what runs, not about the queued release.
   */
  it("never settles an environment that is still queued", () => {
    const watched = observeQueuedDeploys({}, "hounders", [env("prod", { queued: true })]).watched;
    const again = observeQueuedDeploys(watched, "hounders", [env("prod", { queued: true })]);
    expect(again.settled).toEqual([]);
    expect(again.watched).toBe(watched);
  });

  it("drops a watched environment that was deleted, without a word", () => {
    const watched = observeQueuedDeploys({}, "hounders", [env("prod", { queued: true })]).watched;
    const { settled, watched: after } = observeQueuedDeploys(watched, "hounders", []);
    expect(settled).toEqual([]);
    expect(after).toEqual({});
  });

  it("leaves another project's watch alone", () => {
    const watched = observeQueuedDeploys({}, "other", [{ ...env("prod", { queued: true }), id: "cp-other-prod" }]).watched;
    const { watched: after } = observeQueuedDeploys(watched, "hounders", [env("staging")]);
    expect(after).toBe(watched);
  });

  it("starts watching again when a newer deploy is queued after a release", () => {
    let watched = observeQueuedDeploys({}, "hounders", [env("prod", { queued: true })]).watched;
    watched = observeQueuedDeploys(watched, "hounders", [env("prod", { observed: "converging" })]).watched;
    watched = observeQueuedDeploys(watched, "hounders", [env("prod", { queued: true, release: "v14" })]).watched;
    expect(watched["cp-prod"]?.queued).toBe(true);
  });
});

describe("watchPollInterval", () => {
  it("polls a release on its way out quickly, a queue slowly, and nothing otherwise", () => {
    const queued = observeQueuedDeploys({}, "hounders", [env("prod", { queued: true })]).watched;
    expect(watchPollInterval(queued, "hounders")).toBe(QUEUED_POLL_MS);

    const releasing = observeQueuedDeploys(queued, "hounders", [env("prod", { observed: "converging" })]).watched;
    expect(watchPollInterval(releasing, "hounders")).toBe(RELEASING_POLL_MS);

    expect(watchPollInterval(releasing, "other")).toBe(false);
    expect(watchedProjects(releasing)).toEqual(["hounders"]);
  });
});
