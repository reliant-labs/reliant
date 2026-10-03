// Copyright (c) 2025 Reliant Labs

/**
 * SETTING A SECRET BEFORE THE ENVIRONMENT'S FIRST BUILD — and the ONE path
 * that creates the environment's row (#353, design §10).
 *
 * ── WHAT THIS FILE USED TO PIN, AND WHY IT CHANGED ──────────────────────────
 *
 * It used to pin a SECOND EnsureEnvironment living in secretStore.ts
 * (`ensureEnvironmentForSecrets` / `setSecretEnsuringEnvironment`), reachable
 * from the set-secret form, which accepted `controlPlaneKind: ""` and expected
 * a caller to have filled it in — from a radio group the form showed the user.
 *
 * An environment's kind is IMMUTABLE once its row exists. So that design had a
 * human answering, under a secret form, a question whose wrong answer produces
 * an environment that can only be abandoned — and the control plane then had
 * to catch it server-side with a FailedPrecondition. #353 deletes the
 * question, and R4 deletes the ensure that existed to consume its answer.
 *
 * ── THE CONTRACT NOW ────────────────────────────────────────────────────────
 *
 * There is exactly ONE writer of that immutable field in the browser:
 * Preview's Register (services/forge/register.ts), which reads forge's own
 * render and sends the kind AND shape from it. So "an empty kind can never be
 * sent" is not enforced by a validation branch that a caller might skip — it
 * holds because no other path to EnsureEnvironment exists, and because the one
 * path takes a candidate that `registerCandidate` refuses to produce without a
 * kind forge stated.
 *
 * That is a stronger property than the guard it replaces, and these tests pin
 * both halves: the absence of a second ensure, and the refusal of the one that
 * remains.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";

const ensureEnvironmentRpc = vi.fn();
const setSecretRpc = vi.fn();

// One fake transport for both services. `getControlPlaneClient` is called with
// the service descriptor, so returning a single object with every method on it
// is enough — and it means an ensure fired by ANY module under test is
// observable here, which is what the "no second ensure" assertions rest on.
vi.mock("@/services/controlPlane/client", () => ({
  getControlPlaneClient: () => ({
    ensureEnvironment: ensureEnvironmentRpc,
    setSecret: setSecretRpc,
  }),
}));

vi.mock("@/services/controlPlane/config", () => ({
  CONTROL_PLANE_API_URL: "http://localhost:8090",
}));

import * as secretStore from "../secretStore";
import { availabilityExplanation, managedStoreTarget, setSecret } from "../secretStore";
import { registerCandidate, registerEnvironment } from "../register";

beforeEach(() => {
  ensureEnvironmentRpc.mockReset().mockResolvedValue({
    environment: { id: "denv_created", name: "staging", project: "acme" },
    created: true,
  });
  setSecretRpc.mockReset().mockResolvedValue({ version: 1 });
});

describe("the secret store has no ensure path of its own", () => {
  // Pinned as an ABSENCE, which is the only way to state it. A second ensure
  // would not fail any other test in this file — it would simply give the
  // immutable kind a second author, and whichever call landed first would win.
  it.each([
    "ensureEnvironmentForSecrets",
    "setSecretEnsuringEnvironment",
    // The predicate that advertised `not-ensured` / `provider-unknown` as
    // writable, on the strength of the ensure above. A function promising a
    // write nothing can perform is worse than no function.
    "availabilitySupportsWrite",
  ])("does not export %s", (name) => {
    expect(name in secretStore).toBe(false);
  });

  it("writes only against an environment id the caller already has", async () => {
    await setSecret({ environmentId: "denv_existing", name: "API_KEY", value: "v", cas: 0 });

    expect(setSecretRpc).toHaveBeenCalledTimes(1);
    expect(setSecretRpc.mock.calls[0][0].environmentId).toBe("denv_existing");
    // The write did not create anything on the way. §10 state 1 is a single
    // round trip to a row that already exists.
    expect(ensureEnvironmentRpc).not.toHaveBeenCalled();
  });
});

describe("a no-row environment is §10 state 3, not a writable one", () => {
  it("still refuses to invent an id for a hosted env forge never ensured", () => {
    // Unchanged, and still the point: there is genuinely nothing to LIST
    // until the row exists, and a fabricated id is what this state refuses.
    expect(
      managedStoreTarget({ destination: "hosted", endpoint: "http://localhost:8090", environment_id: "" })
    ).toEqual({ kind: "none", availability: "not-ensured" });
  });

  it.each(["not-ensured", "provider-unknown"] as const)(
    "tells a %s env what to RUN rather than offering a write",
    (availability) => {
      const text = availabilityExplanation(availability) ?? "";

      // The remedy, which is what makes the environment writable for good.
      expect(text).toMatch(/forge env build/);
      expect(text).toMatch(/Preview/);

      // And NOT the retired promise. "Values you set here are kept and used
      // by the first deploy" could only be honoured by guessing the
      // environment's immutable kind.
      expect(text.toLowerCase()).not.toContain("you can still set values");
      expect(text.toLowerCase()).not.toContain("kept and used by the first deploy");
    }
  );

  it("never tells a provider-unknown env that forge named a different provider", () => {
    // Unchanged from before R4: "unknown" must not be reported as a positive
    // claim about some other secret provider.
    const text = (availabilityExplanation("provider-unknown") ?? "").toLowerCase();
    expect(text).not.toContain("different");
    expect(text).not.toContain("not hosted");
  });

  it("does not send users to the CLI to read a store, only to build an env", () => {
    // The distinction the old copy got wrong: `forge secret set` is a WRITE
    // instruction, and offering it here implied the value belongs somewhere
    // this console cannot see. The honest instruction is to build.
    for (const availability of ["not-ensured", "provider-unknown"] as const) {
      expect(availabilityExplanation(availability) ?? "").not.toContain("forge secret set");
    }
  });
});

describe("Register is the single writer of the environment's kind", () => {
  const SHAPE = { kind: "persistent", workloads: [], secrets: [], domains: [], clusters: [] };

  it("ensures the row with forge's own kind and the shape from the same render", async () => {
    const candidate = registerCandidate(
      { project: "acme", env: "staging", kind: "persistent", shape: SHAPE },
      { project: "acme", env: "staging" }
    );
    expect(candidate.ok).toBe(true);
    if (!candidate.ok) return;

    const id = await registerEnvironment(candidate.candidate);

    expect(id).toBe("denv_created");
    expect(ensureEnvironmentRpc).toHaveBeenCalledTimes(1);
    const spec = ensureEnvironmentRpc.mock.calls[0][0].spec;
    expect(spec.project).toBe("acme");
    expect(spec.name).toBe("staging");
    // PERSISTENT = 1, and it came from the render rather than from a form.
    expect(spec.kind).toBe(1);
    expect(spec.shape).toEqual(SHAPE);
  });

  it("refuses to produce a candidate at all when forge stated no kind", () => {
    // THE REPLACEMENT FOR "reject an empty kind". The old contract validated
    // a string field at the call; this one makes the call unconstructable, so
    // there is no code path on which an empty kind reaches the wire.
    const candidate = registerCandidate(
      { project: "acme", env: "staging", kind: "", shape: { ...SHAPE, kind: "" } },
      { project: "acme", env: "staging" }
    );

    expect(candidate.ok).toBe(false);
    if (candidate.ok) return;
    expect(candidate.reason).toMatch(/did not say how this environment runs/i);
    expect(ensureEnvironmentRpc).not.toHaveBeenCalled();
  });

  it("refuses an unrecognised kind from a newer forge rather than coercing it", async () => {
    const candidate = registerCandidate(
      { project: "acme", env: "staging", kind: "something_new", shape: { ...SHAPE, kind: "something_new" } },
      { project: "acme", env: "staging" }
    );

    // Not coerced to the nearest familiar kind, which would write a claim
    // forge never made into a field nothing can change.
    expect(candidate.ok).toBe(false);
    expect(ensureEnvironmentRpc).not.toHaveBeenCalled();
  });

  it("refuses to ensure without a project, rather than filing the env under the wrong key", () => {
    // (org, project, name) is the environment's identity. A blank project
    // would create a DIFFERENT environment from the one on screen.
    const candidate = registerCandidate(
      { env: "staging", kind: "persistent", shape: SHAPE },
      { project: null, env: "staging" }
    );

    expect(candidate.ok).toBe(false);
    if (candidate.ok) return;
    expect(candidate.reason).toMatch(/which forge project/i);
    expect(ensureEnvironmentRpc).not.toHaveBeenCalled();
  });
});
