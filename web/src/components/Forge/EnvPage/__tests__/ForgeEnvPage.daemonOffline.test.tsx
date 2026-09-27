// Copyright (c) 2025 Reliant Labs

/**
 * THE DAEMON IS OFFLINE, AND A HOSTED ENVIRONMENT IS STILL FULLY ON SCREEN.
 *
 * This is the property the redesign exists for. The daemon's topology query
 * fails with a transport error; the control plane answers. The Environment
 * page for a Reliant cloud env must render its workloads, health, release
 * history and secrets from the control plane, and say "daemon offline" only in
 * the one place it matters (the Promote/Deploy actions it withdraws).
 *
 * And the mirror image for a LOCAL env: its secrets still come from the
 * managed store, but its Dev stack section says the daemon is offline — the
 * only section that needs it.
 *
 * The environment LIST is the real useForgeEnvironments join, with only its
 * transports mocked: the daemon's topology RPC rejects, and the control plane
 * answers only for the forge project name persisted on the Reliant project
 * row ("hounders", deliberately NOT the display name "barksocial"). So the
 * page finding its env at all proves the join key came from the project row —
 * no daemon report, no browser cache, no guess from the display name. The
 * per-env hooks are mocked at their boundary, so what is under test beyond the
 * join is the page's routing of sources to sections, not react-query.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";

import type { CloudEnv, CloudEnvStatus, CloudPromotion } from "@/services/forge/cloudEnvs";

const routeState: { env: string } = { env: "prod" };

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => vi.fn(),
  useParams: () => ({ env: routeState.env }),
  useSearch: () => ({ project: "proj-1" }),
}));

// The Reliant project's display name and forge's name for it differ, as they
// do for the real Bark Social checkout (forge.yaml `name: hounders`).
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

const CLOUD_ENVS: CloudEnv[] = [
  { id: "cp-prod", name: "prod", project: "hounders", kind: "persistent" },
  { id: "cp-dev", name: "dev", project: "hounders", kind: "local" },
];

// The daemon is offline: every topology read fails in transport.
const getTopology = vi.fn(() =>
  Promise.reject(new ConnectError("no daemon connected for user", Code.Unavailable))
);
vi.mock("@/api/forge-grpc", () => ({ forgeGrpc: { getTopology: (...args: unknown[]) => getTopology(...(args as [])) } }));

// The control plane files environments under forge's name for the project.
const listProjectEnvironments = vi.fn((project: string) =>
  Promise.resolve(CLOUD_ENVS.filter((env) => env.project === project))
);
vi.mock("@/services/forge/cloudEnvs", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/forge/cloudEnvs")>()),
  listProjectEnvironments: (project: string) => listProjectEnvironments(project),
}));

const STATUS: CloudEnvStatus = {
  verdict: "converged",
  workloads: [{ name: "api", tier: "backend", verdict: "converged", url: "https://wild-mongoose.reliantapps.dev" }],
  currentPromotion: null,
  observedAt: "2026-09-26T12:00:00.000Z",
};

const PROMOTIONS: CloudPromotion[] = [
  {
    id: "promo-1",
    releaseVersion: "v7",
    kind: "promote",
    fromEnvironmentId: "",
    promotedByUserId: "u1",
    promotedByActor: "",
    note: "",
    createdAt: "2026-09-26T11:00:00.000Z",
    artifacts: [{ name: "api", digest: "sha256:abc" }],
  },
];

const useForgeEnvStatus = vi.fn(() => ({ data: undefined, isLoading: false, error: null }));
const useForgeSecrets = vi.fn(() => ({ data: undefined, isLoading: false }));
const useManagedSecrets = vi.fn(() => ({
  data: { availability: "available", secrets: [{ name: "STRIPE_KEY", currentVersion: 2, oldestVersion: 1, maxVersions: 0, currentVersionDeleted: false, currentVersionDestroyed: false }] },
  isLoading: false,
}));

vi.mock("@/hooks/forge-queries", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/hooks/forge-queries")>();
  const mutation = () => ({ mutate: vi.fn(), mutateAsync: vi.fn(), reset: vi.fn(), isPending: false, error: null });
  return {
    // The join under test: real, over the mocked transports above.
    useForgeEnvironments: actual.useForgeEnvironments,
    useCloudEnvStatus: (id: string | null) => ({ data: id ? STATUS : undefined, isLoading: false, error: null }),
    useCloudPromotions: (id: string | null) => ({ data: id ? PROMOTIONS : undefined, isLoading: false, error: null }),
    useForgeEnvStatus: (...args: unknown[]) => useForgeEnvStatus(...(args as [])),
    useVerifyForgeEnv: () => ({ verify: vi.fn(), pendingEnv: null, lastOutcome: null }),
    useForgeSecrets: (...args: unknown[]) => useForgeSecrets(...(args as [])),
    useManagedSecrets: (...args: unknown[]) => useManagedSecrets(...(args as [])),
    useManagedSecretVersions: () => ({ data: undefined, isLoading: false }),
    useSetManagedSecret: mutation,
    useDeleteManagedSecret: mutation,
    useUndeleteManagedSecret: mutation,
    useDestroyManagedSecret: mutation,
  };
});

import { ForgeEnvPage } from "../ForgeEnvPage";

function renderPage() {
  // retryDelay 0: the offline daemon's one permitted retry must not outlast findBy's timeout.
  const client = new QueryClient({ defaultOptions: { queries: { retryDelay: 0 } } });
  return render(
    <QueryClientProvider client={client}>
      <ForgeEnvPage />
    </QueryClientProvider>
  );
}

beforeEach(() => {
  getTopology.mockClear();
  listProjectEnvironments.mockClear();
  useForgeEnvStatus.mockClear();
  useForgeSecrets.mockClear();
  useManagedSecrets.mockClear();
});

describe("a Reliant cloud environment with the daemon offline", () => {
  beforeEach(() => {
    routeState.env = "prod";
  });

  it("joins on the project row's forge name, with no daemon report", async () => {
    renderPage();
    expect((await screen.findByTestId("forge-env-page")).getAttribute("data-where")).toBe("cloud");
    expect(getTopology).toHaveBeenCalled();
    // Asked by forge's name, never by the Reliant display name.
    expect(listProjectEnvironments.mock.calls.map(([project]) => project)).toEqual(["hounders"]);
  });

  it("renders workloads, health, releases and secrets from the control plane", async () => {
    renderPage();

    const page = await screen.findByTestId("forge-env-page");
    expect(page.getAttribute("data-where")).toBe("cloud");

    // Workloads: the control plane's deployment, with its URL.
    const workloads = screen.getByTestId("section-workloads");
    expect(within(workloads).getByTestId("hosted-url-prod-api").getAttribute("href")).toBe(
      "https://wild-mongoose.reliantapps.dev"
    );
    // Health: the control plane's verdict, not "daemon offline".
    expect(screen.getByTestId("health-prod").getAttribute("data-verdict")).toBe("converged");
    // Releases: the control plane's ledger.
    expect(within(screen.getByTestId("section-releases")).getByTestId("promotion-promo-1").textContent).toContain("v7");
    // Secrets: the managed store, keyed by the control plane's own id.
    expect(within(screen.getByTestId("section-secrets")).getByTestId("secret-row-STRIPE_KEY")).toBeTruthy();
    expect(useManagedSecrets).toHaveBeenCalledWith(
      "proj-1",
      "prod",
      expect.objectContaining({ kind: "lookup", environmentId: "cp-prod" })
    );
  });

  it("says the daemon is offline only as a notice, and withdraws only promote/deploy", async () => {
    renderPage();
    expect((await screen.findByTestId("forge-daemon-offline")).textContent).toMatch(/except Promote and Deploy/);
    expect(screen.queryByTestId("promote-open-prod")).toBeNull();
    expect(screen.queryByTestId("deploy-open-prod")).toBeNull();
    expect(screen.queryByTestId("verify-prod")).toBeNull();
    // No local-only section, and no daemon query was made for it.
    expect(screen.queryByTestId("section-dev-stack")).toBeNull();
    expect(useForgeEnvStatus).toHaveBeenCalledWith(null, "prod");
    // Declarations are forge's — not asked for with the daemon down.
    expect(useForgeSecrets).toHaveBeenCalledWith(null, "prod");
  });
});

describe("a LOCAL environment with the daemon offline", () => {
  beforeEach(() => {
    routeState.env = "dev";
  });

  it("keeps its managed secrets and says only the dev stack needs the daemon", async () => {
    renderPage();

    expect((await screen.findByTestId("forge-env-page")).getAttribute("data-where")).toBe("local");
    expect(screen.getByTestId("where-dev").textContent).toBe("Local");
    expect(screen.getByTestId("workloads-local").textContent).toMatch(/forge env up/);
    expect(screen.getByTestId("releases-local")).toBeTruthy();

    // Secrets are the managed store's, keyed by the LOCAL env's own id, and
    // writable — the control plane routes a LOCAL write to its local store.
    const secrets = screen.getByTestId("section-secrets");
    expect(within(secrets).getByTestId("secrets-local-pull").textContent).toMatch(/forge env up/);
    expect(within(secrets).getByTestId("add-secret")).toBeTruthy();
    expect(useManagedSecrets).toHaveBeenCalledWith(
      "proj-1",
      "dev",
      expect.objectContaining({ kind: "lookup", environmentId: "cp-dev" })
    );

    expect(within(screen.getByTestId("section-dev-stack")).getByTestId("dev-stack-daemon-offline")).toBeTruthy();
    // Never offers to promote or deploy a local env.
    expect(screen.queryByTestId("deploy-open-dev")).toBeNull();
  });
});
