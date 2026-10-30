// Copyright (c) 2025 Reliant Labs

/**
 * releases.ts is the one file that knows DeployService's release shapes.
 * These pin the readings the pages rely on: the cut commit's subject and
 * author, a capped range's `truncated`, a scoped promotion's targets
 *, a historical free-text gate status read as error (the proto's rule),
 * the comparison states, and a rollout phase this build does not know never
 * reading as success.
 */

import { describe, expect, it, vi } from "vitest";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";

import {
  DeployCommitRangeSchema,
  DeployCommitSchema,
  DeployGateSchema,
  DeployPromotionSchema,
  DeployReleaseSchema,
  DeployRolloutPhase,
  DeployRolloutSchema,
  DeploySourceProvenanceSchema,
  DeployWorkloadRolloutSchema,
} from "@/gen/controlplane/controlplane/v1/deploy_pb";
import {
  CompareDeployReleasesResponseSchema,
  DeployChangeKind,
  DeployCommitComparisonSchema,
  DeployCommitComparisonState,
  DeployObjectComparisonSchema,
  DeployObjectComparisonState,
} from "@/gen/controlplane/services/deploy/v1/deploy_pb";

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "http://127.0.0.1:8090", hasControlPlane: true }));

import { toReleaseComparison, toReleasePromotion, toReleaseRecord, toReleaseRollout } from "../releases";

describe("toReleaseRecord", () => {
  it("reads the cut commit's subject and author, and a capped range", () => {
    const record = toReleaseRecord(
      create(DeployReleaseSchema, {
        version: "20261018.155157-8aecd00c380b",
        gitCommit: "0a076aff1234",
        createdAt: timestampFromDate(new Date("2026-10-18T15:51:57Z")),
        provenance: create(DeploySourceProvenanceSchema, { branch: "main", commitSubject: "fix(identity): owner key", commitAuthor: "sean", forgeVersion: "7fb6d653" }),
        commits: create(DeployCommitRangeSchema, {
          baseRelease: "20261018.084518-53649dd6416d",
          commits: [create(DeployCommitSchema, { sha: "0a076aff", subject: "fix(identity): owner key", author: "sean" })],
          total: 0,
          truncated: true,
        }),
      })
    );
    expect(record).toMatchObject({
      subject: "fix(identity): owner key",
      author: "sean",
      branch: "main",
      forgeVersion: "7fb6d653",
      createdAt: "2026-10-18T15:51:57.000Z",
      commits: { baseRelease: "20261018.084518-53649dd6416d", total: 0, truncated: true },
    });
  });

  it("leaves subject and author empty for a release cut before they were recorded", () => {
    expect(toReleaseRecord(create(DeployReleaseSchema, { version: "v1" }))).toMatchObject({ subject: "", author: "", commits: null, run: null });
  });
});

describe("toReleasePromotion", () => {
  it("keeps a scoped deploy's targets, and both kinds of gate, reading an unknown status as error", () => {
    const promotion = toReleasePromotion(
      create(DeployPromotionSchema, {
        id: "p1",
        releaseVersion: "v1",
        targets: ["admin-web"],
        gates: [create(DeployGateSchema, { name: "ci:full", status: "passed" })],
        recordedGates: [create(DeployGateSchema, { name: "smoke", status: "green-ish" })],
      })
    );
    expect(promotion.targets).toEqual(["admin-web"]);
    expect(promotion.gates).toMatchObject([
      { name: "ci:full", status: "passed", source: "claimed", rawStatus: "" },
      { name: "smoke", status: "error", source: "recorded", rawStatus: "green-ish" },
    ]);
  });
});

describe("toReleaseComparison", () => {
  it("maps the commit and object states, so an empty list is never read as no change", () => {
    const comparison = toReleaseComparison(
      create(CompareDeployReleasesResponseSchema, {
        commits: create(DeployCommitComparisonSchema, { state: DeployCommitComparisonState.UNRECORDED }),
        artifacts: [{ name: "control-plane", change: DeployChangeKind.MODIFIED, from: "sha256:a", to: "sha256:b" }],
        unchangedArtifacts: 12,
        objects: create(DeployObjectComparisonSchema, { state: DeployObjectComparisonState.BUNDLE_MISSING, detail: "no bundle" }),
      })
    );
    expect(comparison.commits.state).toBe("unrecorded");
    expect(comparison.artifacts).toEqual([{ name: "control-plane", kind: "", change: "modified", from: "sha256:a", to: "sha256:b" }]);
    expect(comparison.unchangedArtifacts).toBe(12);
    expect(comparison.objects).toMatchObject({ state: "bundle-missing", detail: "no bundle" });
  });
});

describe("toReleaseRollout", () => {
  it("never reads a phase it does not know as success", () => {
    expect(toReleaseRollout(create(DeployRolloutSchema, { phase: 99 as never })).phase).toBe("unknown");
  });

  it("keeps forge-applied slots apart, with their reason and no phase", () => {
    const rollout = toReleaseRollout(
      create(DeployRolloutSchema, {
        phase: DeployRolloutPhase.SUCCEEDED,
        workloads: [create(DeployWorkloadRolloutSchema, { name: "api", phase: DeployRolloutPhase.SUCCEEDED, reason: "at the bundle" })],
        forgeApplied: [
          create(DeployWorkloadRolloutSchema, {
            name: "edge/web/api",
            artifact: "web",
            pinnedDigest: "sha256:abc",
            phase: DeployRolloutPhase.UNSPECIFIED,
            reason: "applied by forge (not observed by the control plane)",
          }),
        ],
      })
    );
    expect(rollout.workloads.map((w) => w.reason)).toEqual(["at the bundle"]);
    // Not routed through the phase mapping: UNSPECIFIED is "not judged here", never "unknown".
    expect(rollout.forgeApplied).toEqual([
      { name: "edge/web/api", artifact: "web", pinnedDigest: "sha256:abc", reason: "applied by forge (not observed by the control plane)" },
    ]);
  });
});
