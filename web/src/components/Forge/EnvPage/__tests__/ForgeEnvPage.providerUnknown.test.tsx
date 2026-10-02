// Copyright (c) 2025 Reliant Labs

/**
 * FORGE COULD NOT SAY WHERE THIS ENVIRONMENT'S SECRETS LIVE — AND THE PAGE
 * MUST NOT CLAIM IT DID.
 *
 * The bug, as hounders prod showed it: the daemon's forge failed for the env,
 * the control plane held no row for it (never deployed), and the Secrets
 * section said its "forge config names a different secret provider". The KCL
 * declared `secret_provider = forge.HostedSecrets {}`. "forge told us nothing"
 * was being rendered as "forge told us something else".
 *
 * Pinned here, against the real page, the real environment join, and the real
 * managed-secrets hooks — only the transports are fake:
 *
 *   1. With no forge row and no control-plane row the section never renders
 *      the not-hosted sentence, and says instead that forge could not confirm
 *      the provider and that values go to the managed store HostedSecrets
 *      reads.
 *   2. A secret can still be SET: ensure-then-set, with the forge project
 *      name persisted on the project row (no daemon report), the env name
 *      from the route, and the kind the user chose — forge being the only
 *      thing that could have reported it.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

import type { CloudEnv } from "@/services/forge/cloudEnvs";
import type { ForgeTopologyEnv } from "@/services/forge/topology";
import { DeployEnvironmentKind } from "@/gen/controlplane/controlplane/v1/deploy_pb";
import { ForgeReachability, type ForgeReportMeta } from "@/gen/reliant/v1/forge_pb";

const routeState: { env: string } = { env: "prod" };

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => vi.fn(),
  useParams: () => ({ env: routeState.env }),
  useSearch: () => ({ project: "proj-1" }),
}));

// forge's name for the project is persisted on the row — what makes the
// ensure possible with forge unable to report.
vi.mock("@/store/projectStore", () => {
  const currentProject = { id: "proj-1", name: "barksocial", is_forge: true, forge_project_name: "hounders" };
  return {
    useProjectStore: (selector: (s: unknown) => unknown) => selector({ currentProject, projects: [currentProject] }),
  };
});

vi.mock("@/services/controlPlane/config", () => ({
  CONTROL_PLANE_API_URL: "http://127.0.0.1:8090",
  hasControlPlane: true,
}));

function meta(): ForgeReportMeta {
  return {
    isForgeProject: true,
    supported: true,
    forgeVersion: "v0.1.42",
    unsupportedReason: "",
    exitCode: 0,
    reachability: ForgeReachability.OK,
    unreachableReason: "",
  } as ForgeReportMeta;
}

// What forge returns per test: its prod row (or none).
const topologyState: { prod: ForgeTopologyEnv | null } = { prod: null };
const getTopology = vi.fn(() =>
  Promise.resolve({
    kind: "report" as const,
    meta: meta(),
    report: {
      // The live report names no project: forge failed. The join key must
      // come from the project row.
      environments: topologyState.prod ? [topologyState.prod] : [],
    },
  })
);
vi.mock("@/api/forge-grpc", () => ({
  forgeGrpc: {
    getTopology: (...args: unknown[]) => getTopology(...(args as [])),
    listSecrets: () => Promise.reject(new Error("forge.secret_list failed")),
    getEnvStatus: () => Promise.reject(new Error("forge.env_status failed")),
  },
}));

// The control plane holds NO row for prod: it has never been deployed.
const cloudEnvsState: { envs: CloudEnv[] } = { envs: [] };
vi.mock("@/services/forge/cloudEnvs", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/forge/cloudEnvs")>()),
  listProjectEnvironments: () => Promise.resolve(cloudEnvsState.envs),
}));

// The control plane's two services, at the transport.
const ensureEnvironmentRpc = vi.fn();
const setSecretRpc = vi.fn();
const listSecretsRpc = vi.fn();
vi.mock("@/services/controlPlane/client", () => ({
  getControlPlaneClient: () => ({
    ensureEnvironment: ensureEnvironmentRpc,
    setSecret: setSecretRpc,
    listSecrets: listSecretsRpc,
  }),
}));

import { ForgeEnvPage } from "../ForgeEnvPage";

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <ForgeEnvPage />
    </QueryClientProvider>
  );
}

beforeEach(() => {
  routeState.env = "prod";
  cloudEnvsState.envs = [];
  getTopology.mockClear();
  ensureEnvironmentRpc.mockReset().mockResolvedValue({
    environment: { id: "denv_prod", name: "prod", project: "hounders" },
    created: true,
  });
  setSecretRpc.mockReset().mockResolvedValue({ version: 1 });
  listSecretsRpc.mockReset().mockResolvedValue({ secrets: [] });
});

describe("forge could not report prod's secret provider, and prod was never deployed", () => {
  // forge's row when it cannot render the env's KCL: declared, a note, and no
  // destination (forge internal/cli/env_topology.go). This is the page-level
  // shape of "forge null": nothing about where the env runs or what holds its
  // secrets.
  beforeEach(() => {
    topologyState.prod = {
      env: "prod",
      declared: true,
      images: [],
      note: "could not render this environment to resolve where it runs: kcl: exit status 1",
    };
  });

  it("never says the secrets are held by a different provider", async () => {
    renderPage();
    const secrets = await screen.findByTestId("section-secrets");
    expect(within(secrets).queryByTestId("secrets-not-managed")).toBeNull();
    expect(secrets.textContent ?? "").not.toMatch(/different secret provider/i);
  });

  it("says forge could not confirm it, and where values set here go", async () => {
    renderPage();
    const secrets = await screen.findByTestId("section-secrets");
    await waitFor(() => expect(secrets.textContent ?? "").toMatch(/forge could not confirm/i));
    expect(secrets.textContent).toMatch(/managed store/i);
    expect(secrets.textContent).toContain("HostedSecrets");
    // No lookup is keyed on an id nobody has.
    expect(listSecretsRpc).not.toHaveBeenCalled();
  });

  it("sets a secret: ensure with the persisted forge name and the chosen kind, then set", async () => {
    const user = userEvent.setup();
    renderPage();

    const secrets = await screen.findByTestId("section-secrets");
    await user.click(await within(secrets).findByTestId("add-secret"));

    // forge could not report the kind, so the form asks — with no default.
    const kindField = await screen.findByTestId("environment-kind-field");
    expect(kindField.textContent).toMatch(/forge could not report this/i);
    await user.type(screen.getByLabelText("Name"), "STRIPE_SECRET_KEY");
    await user.type(screen.getByLabelText("Value"), "sk_live_x");
    expect(screen.getByTestId("set-secret-submit")).toBeDisabled();

    await user.click(screen.getByTestId("environment-kind-persistent"));
    await user.click(screen.getByTestId("set-secret-submit"));

    await waitFor(() => expect(setSecretRpc).toHaveBeenCalledTimes(1));
    expect(ensureEnvironmentRpc).toHaveBeenCalledTimes(1);
    expect(ensureEnvironmentRpc).toHaveBeenCalledWith({
      spec: { project: "hounders", name: "prod", kind: DeployEnvironmentKind.PERSISTENT },
    });
    expect(setSecretRpc).toHaveBeenCalledWith({
      environmentId: "denv_prod",
      name: "STRIPE_SECRET_KEY",
      secretValue: "sk_live_x",
      cas: 0,
    });
    // The ensure came first: the set is keyed on the id it returned.
    expect(ensureEnvironmentRpc.mock.invocationCallOrder[0]).toBeLessThan(setSecretRpc.mock.invocationCallOrder[0]);
  });
});

describe("forge DID report prod: hosted, persistent, never deployed", () => {
  // forge's real answer for hounders prod (`forge env topology prod --json`).
  beforeEach(() => {
    topologyState.prod = {
      env: "prod",
      declared: true,
      images: [],
      destination: "hosted",
      control_plane_kind: "persistent",
      endpoint: "http://127.0.0.1:8090",
      note: "the control plane has no environment of this name yet — never deployed",
    };
  });

  it("uses forge's kind and does not ask the user for one", async () => {
    const user = userEvent.setup();
    renderPage();

    const secrets = await screen.findByTestId("section-secrets");
    expect(within(secrets).queryByTestId("secrets-not-managed")).toBeNull();
    await user.click(await within(secrets).findByTestId("add-secret"));
    expect(screen.queryByTestId("environment-kind-field")).toBeNull();

    await user.type(screen.getByLabelText("Name"), "STRIPE_SECRET_KEY");
    await user.type(screen.getByLabelText("Value"), "sk_live_x");
    await user.click(screen.getByTestId("set-secret-submit"));

    await waitFor(() => expect(setSecretRpc).toHaveBeenCalledTimes(1));
    expect(ensureEnvironmentRpc).toHaveBeenCalledWith({
      spec: { project: "hounders", name: "prod", kind: DeployEnvironmentKind.PERSISTENT },
    });
  });
});

describe("forge named a non-hosted destination", () => {
  beforeEach(() => {
    topologyState.prod = { env: "prod", declared: true, images: [], destination: "cluster", kube_context: "gke" };
  });

  it("still says the secrets are not in the managed store — that IS what forge reported", async () => {
    renderPage();
    expect(await screen.findByTestId("secrets-not-managed")).toBeTruthy();
  });
});
