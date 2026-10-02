// Copyright (c) 2025 Reliant Labs

/**
 * live.ts is the data half of the owner's rule: Live reads the control plane
 * directly, so everything the env page SAYS about an environment is derived
 * here, from one GetLiveView message. These tests pin the derivations the
 * screens cannot restate for themselves:
 *
 *   - the provenance line (design §2.1), including the two-source form that
 *     is the whole reason the line exists;
 *   - the kinds, especially self_managed and the unknown fallback;
 *   - the three not-an-error states (deployed / declared-not-built / never
 *     built), which a component must not be able to confuse;
 *   - availability, which decides whether an empty screen is a state or a
 *     failure.
 */

import { describe, expect, it, vi } from "vitest";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";

import {
  DeployEnvironmentKind,
  DeployEnvironmentSchema,
  DeployLiveEnvironmentSchema,
  DeployPromotionSchema,
  DeployReleaseSchema,
  DeployRolloutPhase,
  DeploySourceProvenanceSchema,
} from "@/gen/controlplane/controlplane/v1/deploy_pb";

vi.mock("@/services/controlPlane/config", () => ({
  CONTROL_PLANE_API_URL: "http://127.0.0.1:8090",
  hasControlPlane: true,
}));

import {
  declaredNotBuilt,
  describeSource,
  isPlacedKind,
  liveAvailabilityFromError,
  liveKindLabel,
  neverBuilt,
  provenanceLine,
  shortCommit,
  toLiveEnv,
  toLiveShape,
  type LiveProvenance,
} from "../live";

// ── Fixtures ────────────────────────────────────────────────────────────────

function provenance(overrides: Partial<LiveProvenance> = {}): LiveProvenance {
  return {
    repo: "github.com/reliant-labs/hounders",
    commit: "abc1234def567",
    branch: "main",
    tag: "",
    dirty: false,
    tree: "",
    forgeVersion: "v0.1.44",
    worktree: null,
    ...overrides,
  };
}

function provenanceMsg(overrides: Partial<LiveProvenance> = {}) {
  const value = provenance(overrides);
  return create(DeploySourceProvenanceSchema, {
    repo: value.repo,
    commit: value.commit,
    branch: value.branch,
    tag: value.tag,
    dirty: value.dirty,
    tree: value.tree,
    forgeVersion: value.forgeVersion,
  });
}

/** The canonical JSON of forge's release.Shape, as a Struct carries it. */
const SELF_MANAGED_SHAPE = {
  kind: "self_managed",
  workloads: [{ name: "api", runtime: "cluster", cluster: "prod-gke" }],
  secrets: [
    { name: "STRIPE_WEBHOOK_SECRET", provider: "hosted", declared_by: ["api"] },
    { name: "DATABASE_URL", provider: "hosted", declared_by: ["api", "worker"] },
  ],
  domains: ["app.example.com"],
  clusters: ["prod-gke"],
  objects: [],
};

// ── The provenance line ─────────────────────────────────────────────────────

describe("describeSource", () => {
  it("renders ref@commit with the commit shortened", () => {
    expect(describeSource(provenance())).toBe("main@abc1234");
  });

  it("appends the claims a render made about itself", () => {
    expect(describeSource(provenance({ branch: "feat-x", commit: "def5678abc", dirty: true }))).toBe(
      "feat-x@def5678, unmerged, dirty"
    );
  });

  it("does not call a tagged render unmerged", () => {
    // A tag is the reviewable path. Claiming "unmerged" for one would put a
    // warning on the most trustworthy source there is.
    expect(describeSource(provenance({ branch: "feat-x", tag: "v1.2.3", commit: "aaaaaaa" }))).toBe(
      "v1.2.3@aaaaaaa"
    );
  });

  it("falls back to the worktree label when nothing names a ref", () => {
    expect(
      describeSource(
        provenance({
          branch: "",
          commit: "cccccccdd",
          worktree: { key: "wt-1", label: "scratch", hostId: "host-1" },
        })
      )
    ).toBe("scratch@ccccccc");
  });

  it("is empty for no provenance at all, rather than inventing a source", () => {
    expect(describeSource(null)).toBe("");
    expect(describeSource(provenance({ branch: "", commit: "", tag: "" }))).toBe("");
  });
});

describe("provenanceLine", () => {
  it("shows BOTH sources when images and config differ — §2.1's example", () => {
    expect(
      provenanceLine({
        release: "v12",
        images: provenance({ branch: "main", commit: "abc1234ff" }),
        config: provenance({ branch: "feat-x", commit: "def5678aa", dirty: true }),
      })
    ).toBe("v12 · images main@abc1234 · config feat-x@def5678, unmerged, dirty");
  });

  it("collapses to one source when they agree", () => {
    // Repeating identical halves would imply a distinction that is not there.
    const same = provenance({ branch: "main", commit: "abc1234ff" });
    expect(provenanceLine({ release: "v12", images: same, config: same })).toBe("v12 · main@abc1234");
  });

  it("renders the release alone when nothing recorded a source", () => {
    expect(provenanceLine({ release: "v12", images: null, config: null })).toBe("v12");
  });

  it("renders the config source alone before anything is promoted", () => {
    // Declared, not built yet: there is no release and no images, but the
    // declaration knows where it came from.
    expect(
      provenanceLine({ release: "", images: null, config: provenance({ branch: "feat-x", commit: "def5678" }) })
    ).toBe("feat-x@def5678, unmerged");
  });

  it("is empty when there is nothing to say", () => {
    expect(provenanceLine({ release: "", images: null, config: null })).toBe("");
  });
});

describe("shortCommit", () => {
  it("takes seven characters, and stays empty for an absent commit", () => {
    expect(shortCommit("abc1234def567")).toBe("abc1234");
    expect(shortCommit("   ")).toBe("");
  });
});

// ── Kinds ───────────────────────────────────────────────────────────────────

describe("kinds", () => {
  it("maps SELF_MANAGED and calls it 'Your cluster'", () => {
    const env = toLiveEnv(
      create(DeployLiveEnvironmentSchema, {
        environment: create(DeployEnvironmentSchema, {
          id: "cp-prod",
          name: "prod",
          project: "hounders",
          kind: DeployEnvironmentKind.SELF_MANAGED,
        }),
      })
    );
    expect(env?.kind).toBe("self_managed");
    expect(liveKindLabel("self_managed")).toBe("Your cluster");
  });

  it("maps an unrecognised kind to unknown, never to persistent", () => {
    // Folding it into persistent would offer cloud language — rollouts,
    // platform deployments — for an environment that may have neither.
    const env = toLiveEnv(
      create(DeployLiveEnvironmentSchema, {
        environment: create(DeployEnvironmentSchema, {
          id: "cp-x",
          name: "x",
          project: "hounders",
          kind: 99 as DeployEnvironmentKind,
        }),
      })
    );
    expect(env?.kind).toBe("unknown");
  });

  it("treats only persistent and preview as placed by the platform", () => {
    expect(isPlacedKind("persistent")).toBe(true);
    expect(isPlacedKind("preview")).toBe(true);
    // Promoted but not converged: the platform places nothing.
    expect(isPlacedKind("self_managed")).toBe(false);
    expect(isPlacedKind("local")).toBe(false);
    expect(isPlacedKind("unknown")).toBe(false);
  });

  it("drops a phase for a non-placed kind", () => {
    // A server that sent a phase for a self-managed env would be claiming a
    // convergence nobody observed.
    const env = toLiveEnv(
      create(DeployLiveEnvironmentSchema, {
        environment: create(DeployEnvironmentSchema, {
          id: "cp-prod",
          name: "prod",
          project: "hounders",
          kind: DeployEnvironmentKind.SELF_MANAGED,
        }),
        phase: DeployRolloutPhase.SUCCEEDED,
      })
    );
    expect(env?.phase).toBe("unspecified");
  });

  it("keeps the phase for a placed kind", () => {
    const env = toLiveEnv(
      create(DeployLiveEnvironmentSchema, {
        environment: create(DeployEnvironmentSchema, {
          id: "cp-prod",
          name: "prod",
          project: "hounders",
          kind: DeployEnvironmentKind.PERSISTENT,
        }),
        phase: DeployRolloutPhase.DEGRADED,
      })
    );
    expect(env?.phase).toBe("degraded");
  });
});

// ── The declared shape ──────────────────────────────────────────────────────

describe("toLiveShape", () => {
  it("decodes snake_case keys, which is what a Struct carries", () => {
    const shape = toLiveShape(SELF_MANAGED_SHAPE);
    expect(shape?.kind).toBe("self_managed");
    expect(shape?.workloads).toEqual([{ name: "api", runtime: "cluster", cluster: "prod-gke" }]);
    expect(shape?.secrets.map((secret) => secret.name)).toEqual([
      "STRIPE_WEBHOOK_SECRET",
      "DATABASE_URL",
    ]);
    expect(shape?.secrets[1]?.declaredBy).toEqual(["api", "worker"]);
    expect(shape?.domains).toEqual(["app.example.com"]);
    expect(shape?.clusters).toEqual(["prod-gke"]);
  });

  it("is null for an absent or empty shape — the never-built state", () => {
    expect(toLiveShape(undefined)).toBeNull();
    expect(toLiveShape({})).toBeNull();
  });

  it("distinguishes 'declares nothing' from 'no shape recorded'", () => {
    // An env that genuinely declares no workloads still HAS a shape. Folding
    // it into null would make it indistinguishable from never-built, and the
    // page says something different about each.
    const shape = toLiveShape({ kind: "local", workloads: [], secrets: [], domains: [], clusters: [] });
    expect(shape).not.toBeNull();
    expect(shape?.workloads).toEqual([]);
    expect(shape?.kind).toBe("local");
  });

  it("degrades a malformed shape to the fields that parsed", () => {
    // A shape from a newer or broken forge must render rather than throw the
    // page away.
    const shape = toLiveShape({
      kind: "persistent",
      workloads: [{ name: "api" }, { runtime: "cluster" }, "nonsense"],
      secrets: [{ name: "OK" }, { provider: "hosted" }],
      domains: ["a.example.com", 7],
      clusters: "not-an-array",
      future_field: { anything: true },
    } as Record<string, unknown>);
    // The nameless workload and the bare string are dropped; the named one survives.
    expect(shape?.workloads).toEqual([{ name: "api", runtime: "", cluster: "" }]);
    expect(shape?.secrets).toEqual([{ name: "OK", provider: "", declaredBy: [] }]);
    expect(shape?.domains).toEqual(["a.example.com"]);
    expect(shape?.clusters).toEqual([]);
  });
});

// ── The three not-an-error states ───────────────────────────────────────────

describe("the states that are not errors", () => {
  it("reads a deployed env: release, provenance line and promotion facts", () => {
    const env = toLiveEnv(
      create(DeployLiveEnvironmentSchema, {
        environment: create(DeployEnvironmentSchema, {
          id: "cp-prod",
          name: "prod",
          project: "hounders",
          kind: DeployEnvironmentKind.PERSISTENT,
          declaredShape: SELF_MANAGED_SHAPE,
          declaredBy: provenanceMsg({ branch: "feat-x", commit: "def5678aa", dirty: true }),
        }),
        currentPromotion: create(DeployPromotionSchema, {
          id: "promo-1",
          releaseVersion: "v12",
          promotedByActor: "ci",
          createdAt: timestampFromDate(new Date("2026-10-02T12:00:00Z")),
        }),
        currentRelease: create(DeployReleaseSchema, {
          version: "v12",
          provenance: provenanceMsg({ branch: "main", commit: "abc1234ff" }),
        }),
      })
    );

    expect(env?.release).toBe("v12");
    expect(env?.promotedByActor).toBe("ci");
    expect(env?.promotedAt).toBe("2026-10-02T12:00:00.000Z");
    expect(env?.provenance).toBe("v12 · images main@abc1234 · config feat-x@def5678, unmerged, dirty");
    expect(declaredNotBuilt(env!)).toBe(false);
    expect(neverBuilt(env!)).toBe(false);
  });

  it("reads a declared-but-not-built env as its own state", () => {
    // A row with a shape and NO promotion. Someone registered it from
    // Preview, or a build ran and nothing was promoted. Secrets can be set.
    const env = toLiveEnv(
      create(DeployLiveEnvironmentSchema, {
        environment: create(DeployEnvironmentSchema, {
          id: "cp-staging",
          name: "staging",
          project: "hounders",
          kind: DeployEnvironmentKind.SELF_MANAGED,
          declaredShape: SELF_MANAGED_SHAPE,
          declaredBy: provenanceMsg({ branch: "feat-x", commit: "def5678" }),
          declaredAt: timestampFromDate(new Date("2026-10-02T09:00:00Z")),
        }),
      })
    );

    expect(env?.release).toBe("");
    expect(declaredNotBuilt(env!)).toBe(true);
    expect(neverBuilt(env!)).toBe(false);
    expect(env?.declaredShape?.secrets).toHaveLength(2);
    expect(env?.declaredAt).toBe("2026-10-02T09:00:00.000Z");
  });

  it("reads a row with no declaration and no promotion as never built", () => {
    const env = toLiveEnv(
      create(DeployLiveEnvironmentSchema, {
        environment: create(DeployEnvironmentSchema, {
          id: "cp-new",
          name: "new",
          project: "hounders",
          kind: DeployEnvironmentKind.PERSISTENT,
        }),
      })
    );
    expect(neverBuilt(env!)).toBe(true);
    expect(declaredNotBuilt(env!)).toBe(false);
    expect(env?.provenance).toBe("");
  });

  it("drops a row with no environment or no identity rather than rendering a blank", () => {
    expect(toLiveEnv(create(DeployLiveEnvironmentSchema, {}))).toBeNull();
    expect(
      toLiveEnv(
        create(DeployLiveEnvironmentSchema, {
          environment: create(DeployEnvironmentSchema, { id: "", name: "prod" }),
        })
      )
    ).toBeNull();
  });

  it("ignores a zero promotion timestamp instead of showing 1 Jan 1970", () => {
    const env = toLiveEnv(
      create(DeployLiveEnvironmentSchema, {
        environment: create(DeployEnvironmentSchema, {
          id: "cp-prod",
          name: "prod",
          project: "hounders",
          kind: DeployEnvironmentKind.PERSISTENT,
        }),
        currentPromotion: create(DeployPromotionSchema, { id: "p", releaseVersion: "v1" }),
      })
    );
    expect(env?.promotedAt).toBeUndefined();
  });

  it("falls back to the promotion's provenance copy for an imported row", () => {
    const env = toLiveEnv(
      create(DeployLiveEnvironmentSchema, {
        environment: create(DeployEnvironmentSchema, {
          id: "cp-prod",
          name: "prod",
          project: "hounders",
          kind: DeployEnvironmentKind.PERSISTENT,
        }),
        currentPromotion: create(DeployPromotionSchema, {
          id: "promo-1",
          releaseVersion: "v3",
          releaseProvenance: provenanceMsg({ branch: "main", commit: "fed4321aa" }),
        }),
      })
    );
    expect(env?.provenance).toBe("v3 · main@fed4321");
  });
});

// ── Availability ────────────────────────────────────────────────────────────

describe("liveAvailabilityFromError", () => {
  it("classifies the states that are NOT errors", () => {
    // A role that may not read deploy state, and a control plane that does
    // not serve the domain, are states to describe — not red banners.
    expect(liveAvailabilityFromError(new ConnectError("nope", Code.PermissionDenied))).toBe("no-access");
    expect(liveAvailabilityFromError(new ConnectError("nope", Code.Unimplemented))).toBe("not-configured");
  });

  it("classifies everything else as unreachable", () => {
    expect(liveAvailabilityFromError(new ConnectError("down", Code.Unavailable))).toBe("unreachable");
    expect(liveAvailabilityFromError(new Error("boom"))).toBe("unreachable");
  });
});
