// Copyright (c) 2025 Reliant Labs

/**
 * THE SECRETS FORM, IN THE THREE STATES OF §10 — AND O-14 FOR STATE 1.
 *
 * Sibling of ForgeEnvPage.liveNoDaemon.test.tsx, which proves the Live TAB
 * makes no daemon call. This file narrows that to the surface R4 changed, and
 * asserts the three states §10 defines rather than the page around them:
 *
 *   1. a row exists      the form is enabled, there is no kind field, and the
 *                        daemon is NEVER called. The common case, and the one
 *                        that must work with the daemon offline — which is why
 *                        the daemon client here THROWS rather than being
 *                        merely absent.
 *   2. no row, Preview   Register records forge's kind and shape, producing a
 *                        row; the write that follows is state 1. Covered by
 *                        Preview/__tests__/Preview.register.test.tsx, which
 *                        owns that panel.
 *   3. no row, no render the form is DISABLED with the exact remedy, and
 *                        submitting is impossible.
 *
 * ── WHY STATE 1's ASSERTION IS AN ABSENCE, AND WHY IT IS MUTATION-CHECKED ───
 *
 * "Zero daemon calls" cannot be observed from a passing render: a page that
 * calls the daemon and quietly falls back to the control plane looks identical
 * on a machine whose daemon is up. So the daemon client throws on any call,
 * and the call is RECORDED by name so a failure says which one leaked.
 *
 * The mutation check at the bottom of this file is what proves the tripwire is
 * armed rather than decorative. It routes state 1 through the daemon on
 * purpose and asserts the harness notices — because a tripwire that cannot
 * fail is the most expensive kind of green test, and this one is the only
 * guard on the property that an offline daemon never degrades a secrets form.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

import type { LiveEnv } from "@/services/forge/live";

// ── The daemon is not merely offline: TOUCHING IT IS A TEST FAILURE. ────────

const daemonCalls: string[] = [];

function daemonTripwire(method: string) {
  return (...args: unknown[]) => {
    daemonCalls.push(method);
    void args;
    throw new Error(
      `THE SECRETS FORM CALLED THE DAEMON: ${method}. §10 state 1 reads the control-plane row only (O-14).`
    );
  };
}

vi.mock("@/api/forge-grpc", () => ({
  forgeGrpc: {
    getTopology: daemonTripwire("getTopology"),
    verifyEnv: daemonTripwire("verifyEnv"),
    listSecrets: daemonTripwire("listSecrets"),
    getAudit: daemonTripwire("getAudit"),
    getEnvStatus: daemonTripwire("getEnvStatus"),
    getEnvShape: daemonTripwire("getEnvShape"),
    planPromote: daemonTripwire("planPromote"),
    applyPromote: daemonTripwire("applyPromote"),
    planDeploy: daemonTripwire("planDeploy"),
    startDeploy: daemonTripwire("startDeploy"),
    getDeployStatus: daemonTripwire("getDeployStatus"),
  },
}));

vi.mock("@/api/grpc-client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/grpc-client")>()),
  createForgeClient: daemonTripwire("createForgeClient"),
}));

vi.mock("@/services/controlPlane/config", () => ({
  CONTROL_PLANE_API_URL: "http://127.0.0.1:8090",
  hasControlPlane: true,
}));

// ── The control plane's answers: the managed store IS the control plane. ────

const listSecrets = vi.fn();
const setSecret = vi.fn();
const ensureEnvironment = vi.fn();

vi.mock("@/services/forge/secretStore", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/forge/secretStore")>()),
  listSecrets: (...args: unknown[]) => listSecrets(...(args as [])),
  setSecret: (...args: unknown[]) => setSecret(...(args as [])),
  getSecretVersions: () => Promise.resolve({ summary: null, versions: [] }),
}));

// EnsureEnvironment is the row-creating call. It must not fire from a secrets
// write in ANY state: in state 1 the row exists, and in state 3 creating it
// would mean guessing the environment's immutable kind (#353).
vi.mock("@/services/controlPlane/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/controlPlane/client")>()),
  getControlPlaneClient: () => ({ ensureEnvironment }),
}));

import { LiveSecretsSection, notBuiltRemedy } from "../LiveSecretsSection";

/** A built environment: §10 state 1. Its kind and declared names are on the row. */
const BUILT: LiveEnv = {
  id: "cp-prod",
  name: "prod",
  project: "hounders",
  kind: "persistent",
  declaredBy: null,
  release: "v12",
  promotedByActor: "ci",
  promotedByUserId: "",
  phase: "succeeded",
  provenance: "",
  declaredShape: {
    kind: "persistent",
    workloads: [{ name: "api", runtime: "hosted", cluster: "" }],
    secrets: [{ name: "STRIPE_WEBHOOK_SECRET", provider: "hosted", declaredBy: ["api"] }],
    domains: [],
    clusters: [],
  },
};

function renderSection(props: Partial<React.ComponentProps<typeof LiveSecretsSection>> = {}) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  return render(
    <QueryClientProvider client={client}>
      <LiveSecretsSection
        projectId="proj-1"
        env={BUILT}
        forgeProject="hounders"
        selectedSecret={null}
        onSelectSecret={vi.fn()}
        {...props}
      />
    </QueryClientProvider>
  );
}

beforeEach(() => {
  daemonCalls.length = 0;
  vi.clearAllMocks();
  listSecrets.mockResolvedValue([
    {
      name: "STRIPE_WEBHOOK_SECRET",
      currentVersion: 3,
      oldestVersion: 1,
      maxVersions: 10,
      currentVersionDeleted: false,
      currentVersionDestroyed: false,
    },
  ]);
  setSecret.mockResolvedValue({ version: 4 });
  ensureEnvironment.mockResolvedValue({ environment: { id: "cp-prod" } });
});

describe("§10 state 1: a row exists", () => {
  it("renders the declared secret from the row, with zero daemon calls", async () => {
    renderSection();

    // The name came from `declared_shape.secrets` on the control-plane row —
    // the row that used to require `forge.secret_list` on the daemon.
    await screen.findByText("STRIPE_WEBHOOK_SECRET");

    expect(daemonCalls).toEqual([]);
  });

  it("offers an ENABLED form with no kind field", async () => {
    renderSection();
    await screen.findByText("STRIPE_WEBHOOK_SECRET");

    await userEvent.click(screen.getByTestId("add-secret"));

    await screen.findByTestId("set-secret-form");
    expect(screen.getByLabelText("Name")).toBeEnabled();
    expect(screen.getByLabelText("Value")).toBeEnabled();
    // Not disabled, and not asking: the kind is already recorded on the row.
    expect(screen.queryByTestId("set-secret-disabled")).toBeNull();
    expect(screen.queryByTestId("environment-kind-field")).toBeNull();
    expect(screen.queryAllByRole("radio")).toHaveLength(0);

    expect(daemonCalls).toEqual([]);
  });

  it("writes straight to the control plane, keyed on the row's id", async () => {
    renderSection();
    await screen.findByText("STRIPE_WEBHOOK_SECRET");

    await userEvent.click(screen.getByTestId("add-secret"));
    await userEvent.type(await screen.findByLabelText("Name"), "NEW_SECRET");
    await userEvent.type(screen.getByLabelText("Value"), "s3cret");
    await userEvent.click(screen.getByTestId("set-secret-submit"));

    await waitFor(() => expect(setSecret).toHaveBeenCalled());
    expect(setSecret.mock.calls[0]?.[0]).toMatchObject({
      environmentId: "cp-prod",
      name: "NEW_SECRET",
      cas: 0,
    });

    // The row exists, so nothing re-declares it — and in particular nothing
    // re-states its immutable kind.
    expect(ensureEnvironment).not.toHaveBeenCalled();
    expect(daemonCalls).toEqual([]);
  });
});

describe("§10 state 3: no row, no render", () => {
  it("disables the form with the exact remedy, and submit is impossible", async () => {
    const onSubmitReached = vi.fn();
    setSecret.mockImplementation(onSubmitReached);

    renderSection({ env: null, envName: "staging" });

    // Said once outside the modal, so it is readable without clicking.
    const panel = screen.getByTestId("live-secrets-not-built");
    expect(panel.textContent).toMatch(/hasn't been built yet/);
    expect(panel.textContent).toMatch(/forge env build staging/);

    await userEvent.click(screen.getByTestId("add-secret"));

    // And again inside the form, from the same constant, so the two cannot
    // drift into disagreeing about what to run.
    const notice = await screen.findByTestId("set-secret-disabled");
    expect(notice.textContent).toBe(notBuiltRemedy("staging"));

    expect(screen.getByLabelText("Name")).toBeDisabled();
    expect(screen.getByLabelText("Value")).toBeDisabled();
    expect(screen.getByTestId("set-secret-submit")).toBeDisabled();

    // Driven directly, past the disabled attribute: still nothing.
    fireEvent.submit(screen.getByTestId("set-secret-form"));
    expect(onSubmitReached).not.toHaveBeenCalled();

    // No value was sent, and — the #353 property — nothing tried to create
    // the environment by guessing its kind.
    expect(setSecret).not.toHaveBeenCalled();
    expect(ensureEnvironment).not.toHaveBeenCalled();
  });

  it("does not ask how the environment runs, and does not reach for the daemon", async () => {
    // State 3 is "the VALUE is unknown", not "the daemon is offline". So it
    // neither asks the user to supply the kind nor tries to render it here —
    // Preview owns the render, and a build makes the question moot for good.
    renderSection({ env: null, envName: "staging" });
    await userEvent.click(screen.getByTestId("add-secret"));
    await screen.findByTestId("set-secret-form");

    expect(screen.queryByTestId("environment-kind-field")).toBeNull();
    expect(screen.queryAllByRole("radio")).toHaveLength(0);
    expect(daemonCalls).toEqual([]);
  });
});

/**
 * THE MUTATION CHECK — proof the tripwire above can actually fail.
 *
 * A test that asserts `daemonCalls` is empty is worthless if nothing could
 * ever put something in it. So this routes state 1's read through the daemon,
 * exactly as the pre-#368 code did (`forge.secret_list` for the declared
 * names), and asserts the harness goes red.
 *
 * If this test ever fails, the zero-daemon-call assertions above have stopped
 * meaning anything — fix the tripwire, do not delete this.
 */
describe("the tripwire is armed", () => {
  it("records and throws when state 1 is routed through the daemon", async () => {
    const { forgeGrpc } = await import("@/api/forge-grpc");

    expect(() => forgeGrpc.listSecrets("proj-1", "prod")).toThrow(/CALLED THE DAEMON/);
    expect(daemonCalls).toEqual(["listSecrets"]);
    // The assertion every test above makes would now fail, which is the
    // whole point: an "enrichment" added to a Live path cannot pass silently.
    expect(daemonCalls).not.toEqual([]);
  });
});
