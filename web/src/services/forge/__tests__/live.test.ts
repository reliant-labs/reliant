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
  DeployConvergenceSchema,
  DeployDriftSchema,
  DeployEnvironmentKind,
  DeployEnvironmentSchema,
  DeployHoldKind,
  DeployHoldSchema,
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
  driftLine,
  intentLine,
  isPlacedKind,
  isQueued,
  liveAvailabilityFromError,
  liveKindLabel,
  neverBuilt,
  observedIsFailure,
  observedLine,
  provenanceLine,
  queuedOnLabel,
  shortCommit,
  toLiveConvergence,
  toLiveEnv,
  toLiveShape,
  type LiveConvergenceState,
  type LiveEnv,
  type LiveProvenance,
} from "../live";

/**
 * A plain LiveEnv, for the copy helpers that take one rather than a wire
 * message. Distinct from the `liveMsg` builders above, which exercise the
 * decode path.
 */
function liveEnvFixture(overrides: Partial<LiveEnv> = {}): LiveEnv {
  return {
    id: "denv_1",
    name: "prod",
    project: "hounders",
    kind: "persistent",
    declaredShape: null,
    declaredBy: null,
    release: "v12",
    releaseProvenance: null,
    promotedByActor: "",
    promotedByUserId: "",
    phase: "unspecified",
    observed: { state: "not-reported" },
    drift: { state: "not-reported" },
    driftDetail: "",
    provenance: "",
    holds: [],
    ...overrides,
  };
}

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
    // The platform places no workloads here — which now governs only the
    // WORKLOAD table, not the convergence state. See the phase test below.
    expect(isPlacedKind("self_managed")).toBe(false);
    expect(isPlacedKind("local")).toBe(false);
    expect(isPlacedKind("unknown")).toBe(false);
  });

  /**
   * THE GUARD THAT WAS REMOVED, pinned in its new direction.
   *
   * This used to assert the opposite: a phase for a non-placed kind was
   * dropped, because the phase then came from PER-DEPLOYMENT observations and
   * a self-managed environment has no deployment rows — so a phase arriving
   * for one could only be a claim nobody had observed.
   *
   * The phase is now derived from the platform's own reading of the cluster
   * converging, which is per ENVIRONMENT and exists for every kind. Dropping
   * it here would discard a real observation and silently downgrade a
   * customer's own cluster to "we cannot say".
   */
  it("keeps the phase for a non-placed kind, because the observation is per environment", () => {
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
    expect(env?.phase).toBe("succeeded");
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

// ── Intent versus observed ──────────────────────────────────────────────────

/**
 * THE DERIVATION, pinned at the layer that makes it.
 *
 * The component tests cover what a reader SEES; these cover the mapping from
 * the wire, which is where a wrong answer originates. The property that
 * matters is one-directional: `converged` must be reachable ONLY from an
 * explicit in_sync verdict. Every absence, every unrecognised value, and every
 * failure has to land somewhere else.
 */
describe("observed convergence", () => {
  function liveMsg(args: {
    kind?: DeployEnvironmentKind;
    promotion?: boolean;
    drift?: { state: string; detail?: string; observedAt?: Date };
    phase?: DeployRolloutPhase;
  }) {
    return create(DeployLiveEnvironmentSchema, {
      environment: create(DeployEnvironmentSchema, {
        id: "cp-prod",
        name: "prod",
        project: "hounders",
        kind: args.kind ?? DeployEnvironmentKind.PERSISTENT,
      }),
      currentPromotion:
        args.promotion === false
          ? undefined
          : create(DeployPromotionSchema, { id: "promo-1", releaseVersion: "v12" }),
      drift: args.drift
        ? create(DeployDriftSchema, {
            state: args.drift.state,
            detail: args.drift.detail ?? "",
            observedAt: args.drift.observedAt
              ? timestampFromDate(args.drift.observedAt)
              : undefined,
          })
        : undefined,
      phase: args.phase ?? DeployRolloutPhase.UNSPECIFIED,
    });
  }

  /**
   * THE COMMON CASE TODAY, and the single most important assertion in this
   * file. The platform's convergence observer is dark by default, so it omits
   * the drift message entirely — and a client that read a MISSING message as
   * agreement would report every environment in the product as confirmed
   * running whatever it was actually running.
   */
  it("reads an absent drift message as not-reported, never as converged", () => {
    const env = toLiveEnv(liveMsg({}));
    expect(env?.drift.state).toBe("not-reported");
    expect(env?.observed.state).toBe("not-reported");
  });

  it("reads in_sync as converged", () => {
    const env = toLiveEnv(
      liveMsg({ drift: { state: "in_sync" }, phase: DeployRolloutPhase.SUCCEEDED })
    );
    expect(env?.observed.state).toBe("converged");
  });

  /**
   * Drift splits on the phase, and the split is the difference between "wait"
   * and "investigate". The platform makes it on whether a reading was an
   * actual reconciler FAILURE rather than merely being at the wrong revision,
   * so collapsing the two would make a normal mid-rollout look like an outage.
   */
  it("splits drifted into converging and failed on the phase", () => {
    const converging = toLiveEnv(
      liveMsg({ drift: { state: "drifted" }, phase: DeployRolloutPhase.PROGRESSING })
    );
    expect(converging?.observed.state).toBe("converging");

    const failed = toLiveEnv(
      liveMsg({ drift: { state: "drifted" }, phase: DeployRolloutPhase.DEGRADED })
    );
    expect(failed?.observed.state).toBe("failed");
  });

  it("reads a present unknown verdict as unknown, distinct from an absent one", () => {
    // "We looked and cannot say" is a different fact from "nothing has
    // looked": the first can be an incident, the second is a configuration.
    const env = toLiveEnv(
      liveMsg({ drift: { state: "unknown" }, phase: DeployRolloutPhase.UNKNOWN })
    );
    expect(env?.drift.state).toBe("unknown");
    expect(env?.observed.state).toBe("unknown");
  });

  it("reads a state this build does not recognise as unknown", () => {
    // A newer platform's verdict must not default into agreement.
    const env = toLiveEnv(liveMsg({ drift: { state: "something_new" } }));
    expect(env?.drift.state).toBe("unknown");
    expect(env?.observed.state).toBe("unknown");
  });

  it("derives the same answer for an environment on the customer's own cluster", () => {
    const hosted = toLiveEnv(
      liveMsg({
        kind: DeployEnvironmentKind.PERSISTENT,
        drift: { state: "in_sync" },
        phase: DeployRolloutPhase.SUCCEEDED,
      })
    );
    const own = toLiveEnv(
      liveMsg({
        kind: DeployEnvironmentKind.SELF_MANAGED,
        drift: { state: "in_sync" },
        phase: DeployRolloutPhase.SUCCEEDED,
      })
    );
    expect(own?.observed).toEqual(hosted?.observed);
    expect(own?.drift).toEqual(hosted?.drift);
  });

  it("carries the observation time and the platform's detail line", () => {
    const at = new Date("2026-10-01T14:04:00.000Z");
    const env = toLiveEnv(
      liveMsg({
        drift: { state: "drifted", detail: "cluster a is running nothing", observedAt: at },
        phase: DeployRolloutPhase.DEGRADED,
      })
    );
    expect(env?.observed.observedAt).toBe(at.toISOString());
    expect(env?.driftDetail).toBe("cluster a is running nothing");
  });
});

// ── Queued deploys ──────────────────────────────────────────────────────────

describe("a queued deploy", () => {
  function queuedMsg(args: { holds?: boolean; drift?: string; phase?: DeployRolloutPhase } = {}) {
    return create(DeployLiveEnvironmentSchema, {
      environment: create(DeployEnvironmentSchema, {
        id: "cp-prod",
        name: "prod",
        project: "hounders",
        kind: DeployEnvironmentKind.PERSISTENT,
        holds:
          args.holds === false
            ? []
            : [
                create(DeployHoldSchema, {
                  kind: DeployHoldKind.BILLING,
                  promotionId: "promo-2",
                  reason: "this runs compute (1 workload) and the organization has no active compute plan",
                  fix: "Subscribe to a Reliant Compute plan in Reliant → Settings → Billing (an org admin can).",
                  actionUrl: "https://app.reliant.dev/forge/env/prod?forgeProject=hounders",
                  callerCanResolve: true,
                  heldSince: timestampFromDate(new Date("2026-10-06T12:00:00.000Z")),
                }),
              ],
      }),
      currentPromotion: create(DeployPromotionSchema, { id: "promo-2", releaseVersion: "v13" }),
      // The readings describe the release STILL RUNNING (v12): in sync with
      // nothing the queued intent names.
      drift: args.drift ? create(DeployDriftSchema, { state: args.drift, observedAt: timestampFromDate(new Date("2026-10-05T20:31:02.000Z")) }) : undefined,
      phase: args.phase ?? DeployRolloutPhase.HELD,
    });
  }

  it("carries every word of the hold, verbatim", () => {
    const env = toLiveEnv(queuedMsg());
    expect(env?.phase).toBe("held");
    expect(env?.holds).toEqual([
      {
        kind: "billing",
        promotionId: "promo-2",
        reason: "this runs compute (1 workload) and the organization has no active compute plan",
        fix: "Subscribe to a Reliant Compute plan in Reliant → Settings → Billing (an org admin can).",
        actionUrl: "https://app.reliant.dev/forge/env/prod?forgeProject=hounders",
        callerCanResolve: true,
        heldSince: "2026-10-06T12:00:00.000Z",
      },
    ]);
    expect(env && isQueued(env)).toBe(true);
    expect(queuedOnLabel(env?.holds ?? [])).toBe("billing");
  });

  /**
   * A held intent has not started, so a reading cannot be about it. Read as
   * "drifted, still progressing" it would say the queued release is ROLLING
   * OUT — the one thing that is certainly not happening.
   */
  it("observes nothing about a held intent, whatever the readings say", () => {
    const env = toLiveEnv(queuedMsg({ drift: "drifted" }));
    expect(env?.observed).toEqual({ state: "queued" });
    expect(observedLine(env!.observed)).toBe("Queued — not rolling out yet");
    expect(observedIsFailure(env!.observed)).toBe(false);
    // The drift verdict is still true of what runs, and is kept.
    expect(env?.drift.state).toBe("drifted");
  });

  it("is nothing queued when the control plane sends no hold", () => {
    const env = toLiveEnv(queuedMsg({ holds: false, phase: DeployRolloutPhase.PENDING }));
    expect(env?.holds).toEqual([]);
    expect(env && isQueued(env)).toBe(false);
    expect(env?.observed.state).not.toBe("queued");
  });

  it("names a hold kind this build does not know as a wait, never as nothing", () => {
    expect(
      queuedOnLabel([
        {
          kind: "unknown",
          promotionId: "p",
          reason: "",
          fix: "",
          actionUrl: "",
          callerCanResolve: false,
        },
      ])
    ).toBe("an action");
  });
});

describe("the state line's words", () => {
  it("states intent in the present tense with the promoter", () => {
    expect(intentLine(liveEnvFixture({ release: "v12", promotedByActor: "ci" }))).toBe(
      "Should be running v12, promoted by ci"
    );
  });

  it("falls back to 'a user' when only an id identifies the promoter", () => {
    expect(intentLine(liveEnvFixture({ release: "v12", promotedByUserId: "u-1" }))).toBe(
      "Should be running v12, promoted by a user"
    );
  });

  it("says nothing when there is no intent to state", () => {
    // The caller has better copy for this (declared-not-built / never-built).
    expect(intentLine(liveEnvFixture({ release: "" }))).toBe("");
  });

  /**
   * No arm of the observed line may name Flux, a reconciler, a Kustomization
   * or a raw wire value. A customer asked whether their release arrived; those
   * are how we answer it, not the answer.
   */
  it("names nothing internal in any state", () => {
    const states: LiveConvergenceState[] = [
      "converged",
      "converging",
      "failed",
      "queued",
      "unknown",
      "not-reported",
    ];
    for (const state of states) {
      const line = observedLine({ state });
      expect(line).not.toMatch(/flux|reconcil|kustomization|bundle|revision|in_sync/i);
      expect(line.length).toBeGreaterThan(0);
    }
  });

  it("only treats a reported failure as a problem", () => {
    expect(observedIsFailure({ state: "failed" })).toBe(true);
    // Neither absence is a fault — "nothing has confirmed this yet" is the
    // normal state of every environment today.
    expect(observedIsFailure({ state: "not-reported" })).toBe(false);
    expect(observedIsFailure({ state: "unknown" })).toBe(false);
    expect(observedIsFailure({ state: "converging" })).toBe(false);
    expect(observedIsFailure({ state: "converged" })).toBe(false);
  });

  it("says nothing about drift when there is no verdict", () => {
    expect(driftLine({ state: "not-reported" })).toBe("");
    expect(driftLine({ state: "in_sync" })).not.toBe("");
    expect(driftLine({ state: "unknown" })).not.toBe("");
  });
});

describe("toLiveConvergence", () => {
  it("keeps the platform's verbatim reason and the cluster it is about", () => {
    const row = toLiveConvergence(
      create(DeployConvergenceSchema, {
        id: "conv-1",
        state: "failed",
        reason: "HealthCheckFailed",
        message: "deployment api not ready",
        cluster: "prod-gke",
        observedAt: timestampFromDate(new Date("2026-10-01T14:40:00.000Z")),
      })
    );
    expect(row).toEqual({
      id: "conv-1",
      state: "failed",
      reason: "HealthCheckFailed",
      message: "deployment api not ready",
      cluster: "prod-gke",
      observedAt: "2026-10-01T14:40:00.000Z",
    });
  });

  it("never folds an unrecognised reading into converged", () => {
    const row = toLiveConvergence(create(DeployConvergenceSchema, { id: "c", state: "whatever" }));
    expect(row.state).toBe("unknown");
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
