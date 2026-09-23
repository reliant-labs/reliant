// Copyright (c) 2025 Reliant Labs

/**
 * Data-layer tests.
 *
 * The certainty mapping is asserted here EXHAUSTIVELY, at the level where it is
 * decided, in addition to being pinned through the rendered table. Both matter: a
 * component test proves the screen is right today, this proves the rule is right
 * for every state including ones no test renders.
 */

import { describe, expect, it } from "vitest";

import {
  certaintyOf,
  certaintyTally,
  cellFor,
  classifyForgeResponse,
  describeLag,
  envBindingState,
  imageNames,
  isDirtyRelease,
  isObserved,
  mergeVerifyIntoTopology,
  shortDigest,
  stateLabel,
  tallyOf,
  type ForgeTopologyReport,
  type ForgeVerifyReport,
} from "../topology";
import { ForgeReachability } from "@/gen/reliant/v1/forge_pb";
import type { ForgeReportMeta } from "@/gen/reliant/v1/forge_pb";

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

describe("certaintyOf", () => {
  it("maps all six forge states into exactly three categories", () => {
    expect(certaintyOf("match")).toBe("known-good");

    expect(certaintyOf("drift")).toBe("known-bad");
    expect(certaintyOf("missing")).toBe("known-bad");

    // The three that are neither. not_verified is the DEFAULT state, so this is
    // the assertion that stops an unchecked environment being reported clean.
    expect(certaintyOf("not_verified")).toBe("unknown");
    expect(certaintyOf("unreachable")).toBe("unknown");
    expect(certaintyOf("untagged")).toBe("unknown");
  });

  it("treats an unrecognised or absent state as unknown, never as good", () => {
    // A newer forge could add a state. Guessing "good" for an unread state is the
    // one wrong direction, because it is indistinguishable from a verified pass.
    expect(certaintyOf("some_future_state")).toBe("unknown");
    expect(certaintyOf(undefined)).toBe("unknown");
    expect(certaintyOf("")).toBe("unknown");
  });

  it("distinguishes states that were observed from states that were not", () => {
    // untagged WAS observed (we saw a tag) but is still not provable, which is
    // why observation and certainty are separate questions.
    expect(isObserved("untagged")).toBe(true);
    expect(certaintyOf("untagged")).toBe("unknown");

    expect(isObserved("not_verified")).toBe(false);
    expect(isObserved("unreachable")).toBe(false);
  });

  it("gives every state its own label, so none are conflated in text", () => {
    const labels = [
      "match",
      "drift",
      "missing",
      "untagged",
      "unreachable",
      "not_verified",
    ].map(stateLabel);
    expect(new Set(labels).size).toBe(labels.length);
    expect(stateLabel("not_verified")).toBe("Not checked");
  });
});

describe("classifyForgeResponse", () => {
  it("prefers is_forge_project and supported over parsing the body", () => {
    // Both guarantee an empty body; reporting them as malformed JSON would
    // replace an actionable message with a generic parse complaint.
    expect(classifyForgeResponse(meta({ isForgeProject: false }), "").kind).toBe(
      "not-forge-project"
    );
    expect(classifyForgeResponse(meta({ supported: false }), "").kind).toBe("unsupported");
  });

  it("reports an empty body under UNREACHABLE as unreachable, not malformed", () => {
    const outcome = classifyForgeResponse(
      meta({ reachability: ForgeReachability.UNREACHABLE, exitCode: 2 }),
      ""
    );
    expect(outcome.kind).toBe("unreachable");
  });

  it("returns a report when forge exited 2 but DID produce one", () => {
    // A partial report still says which envs were read; the per-image
    // `unreachable` states carry the uncertainty from there.
    const outcome = classifyForgeResponse<ForgeTopologyReport>(
      meta({ reachability: ForgeReachability.UNREACHABLE, exitCode: 2 }),
      JSON.stringify({ environments: [{ env: "prod" }] })
    );
    expect(outcome.kind).toBe("report");
  });

  it("survives a missing meta without claiming the project is fine", () => {
    const outcome = classifyForgeResponse(undefined, "");
    expect(outcome.kind).toBe("not-forge-project");
  });
});

describe("imageNames", () => {
  it("uses the union array forge provides", () => {
    expect(imageNames({ images: ["b", "a"] })).toEqual(["b", "a"]);
  });

  it("derives the union from the environments when the array is absent", () => {
    // Without this the matrix would render zero columns for a report shape that
    // still contains every image.
    const report: ForgeTopologyReport = {
      environments: [
        { env: "prod", images: [{ image: "reliant" }, { image: "control-plane" }] },
        { env: "dev", images: [{ image: "reliant" }] },
      ],
    };
    expect(imageNames(report)).toEqual(["control-plane", "reliant"]);
  });

  it("returns nothing for a null or empty report rather than throwing", () => {
    expect(imageNames(null)).toEqual([]);
    expect(imageNames({})).toEqual([]);
  });
});

describe("cellFor", () => {
  it("marks an image the environment's release does not contain as absent", () => {
    const cell = cellFor({ env: "prod", images: [{ image: "reliant" }] }, "workspace-base");
    expect(cell.kind).toBe("absent");
  });

  it("defaults a state-less image row to not_verified, which is unknown", () => {
    const cell = cellFor({ env: "prod", images: [{ image: "reliant" }] }, "reliant");
    expect(cell.kind).toBe("state");
    if (cell.kind !== "state") return;
    expect(cell.state).toBe("not_verified");
    expect(cell.certainty).toBe("unknown");
  });
});

describe("mergeVerifyIntoTopology", () => {
  const base: ForgeTopologyReport = {
    images: ["control-plane", "internal-console"],
    environments: [
      {
        env: "prod",
        bound: true,
        declared: true,
        images: [
          { image: "control-plane", digest: "sha256:aaa", state: "not_verified" },
          { image: "internal-console", digest: "sha256:bbb", state: "not_verified" },
        ],
      },
      {
        env: "preprod",
        bound: true,
        declared: true,
        images: [{ image: "control-plane", digest: "sha256:ccc", state: "not_verified" }],
      },
    ],
  };

  const verify: ForgeVerifyReport = {
    env: "prod",
    bound: true,
    images: [
      { image: "control-plane", declared: "sha256:aaa", running: "sha256:aaa", state: "match" },
      {
        image: "internal-console",
        declared: "sha256:bbb",
        running: "ghcr.io/acme/internal-console:latest",
        state: "untagged",
        detail: "runs by a mutable tag",
      },
    ],
  };

  it("upgrades only the named environment's cells", () => {
    const merged = mergeVerifyIntoTopology(base, "prod", verify);
    const prod = merged.environments!.find((e) => e.env === "prod")!;
    const preprod = merged.environments!.find((e) => e.env === "preprod")!;

    expect(prod.images!.map((i) => i.state)).toEqual(["match", "untagged"]);
    // The environment nobody verified stays honestly unverified.
    expect(preprod.images!.map((i) => i.state)).toEqual(["not_verified"]);
  });

  it("carries the observed digest and detail through", () => {
    const merged = mergeVerifyIntoTopology(base, "prod", verify);
    const untagged = merged.environments![0].images![1];
    expect(untagged.running).toBe("ghcr.io/acme/internal-console:latest");
    expect(untagged.detail).toBe("runs by a mutable tag");
  });

  it("leaves an image the verify did not mention at its previous state", () => {
    // A verify that did not see an image has not established that the release
    // stopped declaring it.
    const partial: ForgeVerifyReport = {
      env: "prod",
      images: [{ image: "control-plane", state: "match" }],
    };
    const merged = mergeVerifyIntoTopology(base, "prod", partial);
    expect(merged.environments![0].images!.map((i) => i.state)).toEqual(["match", "not_verified"]);
  });

  it("does not mutate the input report", () => {
    mergeVerifyIntoTopology(base, "prod", verify);
    expect(base.environments![0].images![0].state).toBe("not_verified");
  });

  it("recomputes the tally so the header cannot disagree with the cells", () => {
    const merged = mergeVerifyIntoTopology(base, "prod", verify);
    expect(merged.tally).toEqual({
      not_verified: 1,
      match: 1,
      drift: 0,
      missing: 0,
      untagged: 1,
      unreachable: 0,
    });
  });
});

describe("certaintyTally", () => {
  it("rolls the six counts into three", () => {
    const report: ForgeTopologyReport = {
      environments: [
        {
          env: "prod",
          images: [
            { image: "a", state: "match" },
            { image: "b", state: "drift" },
            { image: "c", state: "missing" },
            { image: "d", state: "not_verified" },
            { image: "e", state: "untagged" },
            { image: "f", state: "unreachable" },
          ],
        },
      ],
    };
    expect(certaintyTally(report)).toEqual({
      "known-good": 1,
      "known-bad": 2,
      unknown: 3,
    });
  });

  it("counts an unrecognised state as unknown in tallyOf too", () => {
    const report: ForgeTopologyReport = {
      environments: [{ env: "prod", images: [{ image: "a", state: "future_state" }] }],
    };
    expect(tallyOf(report).not_verified).toBe(1);
    expect(certaintyTally(report)["known-good"]).toBe(0);
  });
});

describe("environment-level facts", () => {
  it("separates never-promoted from not-declared-here from active", () => {
    expect(envBindingState({ env: "dev", bound: false, declared: true })).toBe("unbound");
    expect(envBindingState({ env: "staging", bound: true, declared: false })).toBe("undeclared");
    expect(envBindingState({ env: "prod", bound: true, declared: true })).toBe("active");
  });

  it("detects a dirty-tree release only when forge says dirty", () => {
    expect(isDirtyRelease({ env: "preprod", git: { dirty: true } })).toBe(true);
    expect(isDirtyRelease({ env: "prod", git: { dirty: false } })).toBe(false);
    expect(isDirtyRelease({ env: "prod" })).toBe(false);
  });

  it("reports both lag units, and omits an uncomputable time gap instead of saying zero", () => {
    expect(describeLag({ current: true })).toBe("On the latest release");
    expect(describeLag({ current: false, releases_behind: 3, behind: "69d10h" })).toBe(
      "3 releases behind · 69d10h"
    );
    // behind absent means the gap could not be computed — an unknown gap and a
    // zero gap are different claims, so nothing is substituted.
    expect(describeLag({ current: false, releases_behind: 1 })).toBe("1 release behind");
    expect(describeLag(undefined)).toBeNull();
  });

  it("shortens a digest without inventing one", () => {
    expect(shortDigest("sha256:1ea566aaaabbbbcccc")).toBe("1ea566aaaabb");
    expect(shortDigest(undefined)).toBe("");
  });
});
