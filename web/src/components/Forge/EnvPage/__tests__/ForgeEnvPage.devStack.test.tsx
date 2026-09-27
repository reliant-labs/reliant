// Copyright (c) 2025 Reliant Labs

/**
 * The Dev stack section, with the daemon UP, against control-plane's real
 * shape: `dev` is `mixed` (host processes + k3d clusters + compose) and runs
 * on this machine; `prod` is `mixed` too and runs on GKE.
 *
 * The section must appear for dev — the stack is the thing a developer opens
 * it for — and must NOT appear for prod, whose env status lists the host
 * frontends `forge env up` would launch but nothing forge owns is running.
 * Rendering those on prod's page is how prod once appeared to run two
 * services while its cluster ran sixteen.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";

import { joinEnvironments } from "@/services/forge/environments";
import type { ForgeEnvStatusReport } from "@/services/forge/status";
import type { ForgeTopologyEnv } from "@/services/forge/topology";
import { ForgeReachability, type ForgeReportMeta } from "@/gen/reliant/v1/forge_pb";

const routeState: { env: string } = { env: "dev" };

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => vi.fn(),
  useParams: () => ({ env: routeState.env }),
  useSearch: () => ({ project: "proj-1" }),
}));

vi.mock("@/store/projectStore", () => ({
  useProjectStore: (selector: (s: unknown) => unknown) =>
    selector({ currentProject: { id: "proj-1", name: "control-plane" } }),
}));

vi.mock("@/services/controlPlane/config", () => ({
  CONTROL_PLANE_API_URL: "http://127.0.0.1:8090",
  hasControlPlane: true,
}));

function meta(): ForgeReportMeta {
  return {
    isForgeProject: true,
    supported: true,
    forgeVersion: "v0.1.18",
    unsupportedReason: "",
    exitCode: 0,
    reachability: ForgeReachability.OK,
    unreachableReason: "",
  } as ForgeReportMeta;
}

const FORGE_ENVS: ForgeTopologyEnv[] = [
  { env: "dev", declared: true, bound: false, destination: "mixed", images: [] },
  {
    env: "prod",
    declared: true,
    bound: true,
    release: "v1.5.18",
    destination: "mixed",
    kube_context: "gke_reliant-labs-475814_us-central1_prod",
    namespace: "control-plane-prod",
    images: [{ image: "control-plane", digest: "sha256:f4cf", state: "not_verified" }],
  },
];

const STATUS: Record<string, ForgeEnvStatusReport> = {
  dev: {
    env: "dev",
    destination: "mixed",
    services: [{ name: "admin-server", kind: "host", port: 8090, listening: true, owned: true }],
    checks: [{ name: "Compose Infra", status: "pass" }],
    workloads: { status: "pass", env: "dev", clusters: [], workloads: [] },
  },
  prod: {
    env: "prod",
    destination: "mixed",
    services: [{ name: "reliant-web", kind: "frontend", port: 3100, listening: false }],
    checks: [{ name: "Cluster Workloads", status: "pass" }],
    workloads: { status: "pass", env: "prod", clusters: [], workloads: [] },
  },
};

vi.mock("@/hooks/forge-queries", () => {
  const mutation = () => ({ mutate: vi.fn(), mutateAsync: vi.fn(), reset: vi.fn(), isPending: false, error: null });
  return {
    useForgeEnvironments: () => ({
      envs: joinEnvironments(FORGE_ENVS, []),
      topology: {
        data: { kind: "report", meta: meta(), report: { project: "control-plane", latest_release: "v1.5.18", environments: FORGE_ENVS } },
        error: null,
        isLoading: false,
      },
      daemon: "ok",
      cloud: { data: { availability: "no-access", envs: [], detail: "" }, isLoading: false },
      projectName: { name: "control-plane", source: "daemon" },
      isLoading: false,
    }),
    useCloudEnvStatus: () => ({ data: undefined, isLoading: false, error: null }),
    useCloudPromotions: () => ({ data: undefined, isLoading: false, error: null }),
    useForgeEnvStatus: (_projectId: string | null, env: string) => ({
      data: { kind: "report", meta: meta(), report: STATUS[env] },
      isLoading: false,
      error: null,
    }),
    useVerifyForgeEnv: () => ({ verify: vi.fn(), pendingEnv: null, lastOutcome: null }),
    useForgeSecrets: () => ({ data: undefined, isLoading: false }),
    useManagedSecrets: () => ({ data: { availability: "not-hosted", secrets: [] }, isLoading: false }),
    useManagedSecretVersions: () => ({ data: undefined, isLoading: false }),
    useSetManagedSecret: mutation,
    useDeleteManagedSecret: mutation,
    useUndeleteManagedSecret: mutation,
    useDestroyManagedSecret: mutation,
  };
});

import { ForgeEnvPage } from "../ForgeEnvPage";

describe("the Dev stack section", () => {
  beforeEach(() => {
    routeState.env = "dev";
  });

  it("appears for a mixed env whose stack forge env up is running here", () => {
    render(<ForgeEnvPage />);
    expect(screen.getByTestId("section-dev-stack")).toBeTruthy();
    expect(screen.getByTestId("forge-status-service-admin-server")).toBeTruthy();
  });

  it("does not appear for prod, whose host services are not running here", () => {
    routeState.env = "prod";
    render(<ForgeEnvPage />);
    expect(screen.queryByTestId("section-dev-stack")).toBeNull();
    // Its secrets are not in the managed store — one sentence, no file-store UI.
    expect(screen.getByTestId("secrets-not-managed")).toBeTruthy();
    // A cluster binding can be verified; it cannot be claimed healthy until it is.
    expect(screen.getByTestId("verify-prod")).toBeTruthy();
    expect(screen.getByTestId("health-prod").textContent).toBe("Not verified");
  });
});
