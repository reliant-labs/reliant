// Copyright (c) 2025 Reliant Labs

/**
 * Setting a secret BEFORE the environment's first deploy.
 *
 * The managed store is keyed by a control-plane `deploy_environments` row id,
 * and that row — not anything in OpenBao — is the only prerequisite for a
 * write. control-plane pins the backend half of this claim in
 * internal/isolation/predeploy_secret_integration_test.go: with the row
 * present and NOTHING deployed, set/list/delete all work, because KV-v2
 * creates its path on first write.
 *
 * So the chicken-and-egg the user hit was never a storage problem. forge's CLI
 * already resolves it — every mutating hosted command ensures the environment
 * first (forge/internal/cli/hosted_env_resolver.go) — and the browser was the
 * one caller that did not, leaving "Set value" hidden behind CLI instructions
 * for a user whose whole reason for being on the page is that they have not
 * deployed yet.
 *
 * These tests pin the browser doing what the CLI does: ensure, then write.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";

const ensureEnvironmentRpc = vi.fn();
const setSecretRpc = vi.fn();

// One fake transport for both services. `getControlPlaneClient` is called with
// the service descriptor, so returning a single object with every method on it
// is enough and keeps the call ORDER observable across the two services —
// which is the property under test.
vi.mock("@/services/controlPlane/client", () => ({
  getControlPlaneClient: () => ({
    ensureEnvironment: ensureEnvironmentRpc,
    setSecret: setSecretRpc,
  }),
}));

vi.mock("@/services/controlPlane/config", () => ({
  CONTROL_PLANE_API_URL: "http://localhost:8090",
}));

import {
  ensureEnvironmentForSecrets,
  managedStoreTarget,
  availabilityExplanation,
  availabilitySupportsWrite,
  setSecretEnsuringEnvironment,
} from "../secretStore";

beforeEach(() => {
  ensureEnvironmentRpc.mockReset().mockResolvedValue({
    environment: { id: "denv_created", name: "staging", project: "acme" },
    created: true,
  });
  setSecretRpc.mockReset().mockResolvedValue({ version: 1 });
});

describe("a hosted env that was never ensured is still writable", () => {
  it("treats not-ensured as a state that can be written to", () => {
    // The distinction that matters: not-ensured is a row we can CREATE, so it
    // must not be lumped in with the states where no write is possible.
    expect(availabilitySupportsWrite("not-ensured")).toBe(true);
  });

  it("treats provider-unknown as writable too — the ensure path creates the row", () => {
    expect(availabilitySupportsWrite("provider-unknown")).toBe(true);
  });

  it("never tells a provider-unknown env that forge named a different provider", () => {
    const text = availabilityExplanation("provider-unknown") ?? "";
    expect(text).toMatch(/could not confirm/i);
    expect(text).toContain("HostedSecrets");
    expect(text.toLowerCase()).toContain("managed store");
    expect(text.toLowerCase()).not.toContain("different");
    expect(text.toLowerCase()).not.toContain("not hosted");
  });

  it.each(["not-hosted", "other-control-plane", "unreachable", "no-control-plane", "not-configured"] as const)(
    "still refuses to write a %s env",
    (availability) => {
      expect(availabilitySupportsWrite(availability)).toBe(false);
    }
  );

  it("no longer tells the user to go and run the CLI", () => {
    const text = availabilityExplanation("not-ensured") ?? "";
    expect(text).not.toContain("forge secret set");
    // And it should say the useful thing instead: the value is kept and used
    // by the first deploy.
    expect(text.toLowerCase()).toContain("first deploy");
  });

  it("still keeps the target a non-lookup — there is no id to read with yet", () => {
    // Writability must not be confused with readability. Until the row
    // exists there is genuinely nothing to LIST, and inventing an id is the
    // thing this state exists to refuse.
    expect(
      managedStoreTarget({ destination: "hosted", endpoint: "http://localhost:8090", environment_id: "" })
    ).toEqual({ kind: "none", availability: "not-ensured" });
  });
});

describe("ensureEnvironmentForSecrets", () => {
  it("creates the row from the env's declared identity and returns its id", async () => {
    const id = await ensureEnvironmentForSecrets({
      project: "acme",
      name: "staging",
      controlPlaneKind: "persistent",
    });

    expect(id).toBe("denv_created");
    expect(ensureEnvironmentRpc).toHaveBeenCalledTimes(1);
    const spec = ensureEnvironmentRpc.mock.calls[0][0].spec;
    expect(spec.project).toBe("acme");
    expect(spec.name).toBe("staging");
    // PERSISTENT = 1. The kind is IMMUTABLE server-side, so sending the wrong
    // one is not a cosmetic error — it is an environment that can never be
    // corrected, only abandoned.
    expect(spec.kind).toBe(1);
  });

  it("sends LOCAL for an env whose control plane is only its secret store", async () => {
    await ensureEnvironmentForSecrets({ project: "acme", name: "dev", controlPlaneKind: "local" });
    expect(ensureEnvironmentRpc.mock.calls[0][0].spec.kind).toBe(3);
  });

  it("refuses to guess a kind forge did not report", async () => {
    // UNSPECIFIED is refused by the server precisely so a caller cannot get a
    // silently-persistent environment. Failing here names the real problem
    // rather than sending a guess and reading the server's refusal.
    await expect(
      ensureEnvironmentForSecrets({ project: "acme", name: "staging", controlPlaneKind: "" })
    ).rejects.toThrow();
    expect(ensureEnvironmentRpc).not.toHaveBeenCalled();
  });

  it("refuses to ensure without a project, rather than creating it under the wrong key", async () => {
    // (org, project, name) is the environment's identity. An empty project
    // would create a DIFFERENT environment from the one on screen and then
    // write the user's secret into it.
    await expect(
      ensureEnvironmentForSecrets({ project: "", name: "staging", controlPlaneKind: "persistent" })
    ).rejects.toThrow();
    expect(ensureEnvironmentRpc).not.toHaveBeenCalled();
  });
});

describe("setSecretEnsuringEnvironment", () => {
  it("ensures the environment, then writes the secret against the new id", async () => {
    const result = await setSecretEnsuringEnvironment({
      environmentId: "",
      env: { project: "acme", name: "staging", controlPlaneKind: "persistent" },
      name: "STRIPE_SECRET_KEY",
      value: "sk-test-not-a-real-key",
    });

    expect(ensureEnvironmentRpc).toHaveBeenCalledTimes(1);
    expect(setSecretRpc).toHaveBeenCalledTimes(1);
    expect(setSecretRpc.mock.calls[0][0].environmentId).toBe("denv_created");
    expect(setSecretRpc.mock.calls[0][0].secretValue).toBe("sk-test-not-a-real-key");
    expect(result.version).toBe(1);
    // The caller needs the id back so the surface can re-read with it instead
    // of staying stuck in not-ensured until something else refreshes.
    expect(result.environmentId).toBe("denv_created");
  });

  it("does not ensure when the environment already has an id", async () => {
    await setSecretEnsuringEnvironment({
      environmentId: "denv_existing",
      env: { project: "acme", name: "staging", controlPlaneKind: "persistent" },
      name: "STRIPE_SECRET_KEY",
      value: "v",
    });

    expect(ensureEnvironmentRpc).not.toHaveBeenCalled();
    expect(setSecretRpc.mock.calls[0][0].environmentId).toBe("denv_existing");
  });

  it("does not write the secret when the ensure fails", async () => {
    ensureEnvironmentRpc.mockRejectedValue(new Error("forbidden"));

    await expect(
      setSecretEnsuringEnvironment({
        environmentId: "",
        env: { project: "acme", name: "staging", controlPlaneKind: "persistent" },
        name: "STRIPE_SECRET_KEY",
        value: "sk-test-not-a-real-key",
      })
    ).rejects.toThrow();

    // The value must not be sent anywhere if we could not establish WHERE it
    // belongs — a write keyed on a failed ensure could land in the wrong row.
    expect(setSecretRpc).not.toHaveBeenCalled();
  });

  it("keeps the secret value out of the error when the ensure fails", async () => {
    ensureEnvironmentRpc.mockRejectedValue(new Error("forbidden"));
    const value = "sk-test-super-secret-value";

    await expect(
      setSecretEnsuringEnvironment({
        environmentId: "",
        env: { project: "acme", name: "staging", controlPlaneKind: "persistent" },
        name: "STRIPE_SECRET_KEY",
        value,
      })
    ).rejects.toSatisfy((err: unknown) => !JSON.stringify((err as Error).message).includes(value));
  });
});
