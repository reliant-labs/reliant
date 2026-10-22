// Copyright (c) 2025 Reliant Labs

/**
 * The console adapter (WorkloadPages.tsx), which holds the two facts a
 * mutation turns on: WHICH environment id a restart or stop names (resolved
 * from the path's env name through the roster), and WHETHER the environment
 * is one Reliant hosts. The roster has a hosted prod, a self-managed staging,
 * a local dev and a kind this build does not know.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import type { LiveEnv, LiveEnvKind } from "@/services/forge/live";

import { FIXTURE_HOSTED, createWorkloadsFixtureApi } from "../__fixtures__/workloadFixtures";
import { WorkloadsProviders } from "./workloadsTestUtils";

const route = vi.hoisted(() => ({ params: { env: "prod", name: "admin-server" } as Record<string, string> }));
const cloud = vi.hoisted(() => ({
  restartWorkload: vi.fn(),
  setWorkloadRunState: vi.fn(),
  getEnvironmentStatus: vi.fn(),
}));

vi.mock("uplot", async () => (await import("../../Observability/__tests__/obsMocks")).obsMocks.uplot());
vi.mock("@tanstack/react-router", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-router")>()),
  useNavigate: () => vi.fn(),
  useParams: () => route.params,
  useSearch: () => ({}),
}));
vi.mock("@/services/forge/cloudEnvs", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/forge/cloudEnvs")>()),
  restartWorkload: cloud.restartWorkload,
  setWorkloadRunState: cloud.setWorkloadRunState,
  getEnvironmentStatus: cloud.getEnvironmentStatus,
}));

// The roster: env names resolve to the control plane's ids, which differ from
// the fixture telemetry's on purpose for prod, so a test can tell them apart.
const ROSTER: { name: string; id: string; kind: LiveEnvKind }[] = [
  { name: "prod", id: "env-prod", kind: "persistent" },
  { name: "staging", id: "env-staging", kind: "self_managed" },
  { name: "dev", id: "env-dev", kind: "local" },
  { name: "e2e", id: "env-e2e", kind: "unknown" },
];

function liveEnv(id: string, name: string, kind: LiveEnvKind): LiveEnv {
  return {
    id,
    name,
    project: "control-plane",
    kind,
    declaredShape: null,
    declaredBy: null,
    release: "",
    releaseProvenance: null,
    promotedByActor: "",
    promotedByUserId: "",
    phase: "unspecified",
    provenance: "",
    holds: [],
  } as unknown as LiveEnv;
}

vi.mock("../../Console/consoleContext", () => ({
  useForgeConsole: () => ({
    forgeProject: "control-plane",
    projectId: "p1",
    page: { kind: "workload", env: route.params.env, name: route.params.name },
    env: route.params.env,
    setEnv: vi.fn(),
    href: (page: { kind: string }) => `/${page.kind}`,
    roster: {
      isLoading: false,
      resolvingName: false,
      live: { data: { availability: "available" } },
      envs: ROSTER.map((env) => ({ name: env.name, source: "backend", lifecycle: "deployed", forge: null, live: liveEnv(env.id, env.name, env.kind) })),
    },
  }),
}));

const { WorkloadPage, actionsUnavailableReason } = await import("../WorkloadPages");

/** The fixture telemetry, answering for any environment with prod's: every kind gets a workload to act on. */
function everywhere() {
  const base = createWorkloadsFixtureApi();
  return { ...base, getWorkload: (req: Parameters<typeof base.getWorkload>[0]) => base.getWorkload({ ...req, envIds: ["env-prod"] }) };
}

function open(env: string, name = "admin-server") {
  route.params = { env, name };
  return render(
    <WorkloadsProviders obs={everywhere()}>
      <WorkloadPage />
    </WorkloadsProviders>
  );
}

const ready = async () => waitFor(() => expect(screen.getByTestId("workload-detail")).toHaveAttribute("data-state", "ready"));

beforeEach(() => {
  cloud.restartWorkload.mockReset().mockResolvedValue({ environmentId: "env-prod", environmentName: "prod", workload: "admin-server", requestedAt: null });
  cloud.setWorkloadRunState.mockReset().mockResolvedValue(undefined);
  cloud.getEnvironmentStatus.mockReset().mockResolvedValue({
    verdict: "converged",
    currentPromotion: null,
    workloads: [...FIXTURE_HOSTED.get("prod")!.values()],
  });
});

describe("the workload page in the console", () => {
  it("restarts the workload in the environment the path names, by that environment's id", async () => {
    open("prod");
    await ready();
    await userEvent.click(screen.getByTestId("workload-restart"));
    await userEvent.click(within(screen.getByRole("dialog", { name: "Restart admin-server in prod?" })).getByRole("button", { name: "Restart in prod" }));
    await waitFor(() => expect(cloud.restartWorkload).toHaveBeenCalledWith("env-prod", "admin-server"));
    expect(cloud.getEnvironmentStatus).toHaveBeenCalledWith("env-prod");
  });

  it("stops the workload's own deployment, found in that environment's status", async () => {
    open("prod");
    await ready();
    await waitFor(() => expect(screen.getByTestId("workload-stop")).toBeEnabled());
    await userEvent.click(screen.getByTestId("workload-stop"));
    await userEvent.click(within(screen.getByRole("dialog", { name: "Stop admin-server in prod?" })).getByRole("button", { name: "Stop in prod" }));
    await waitFor(() => expect(cloud.setWorkloadRunState).toHaveBeenCalledWith("dep-admin-server", "suspended"));
  });

  it("disables the actions on a self-managed environment, and never asks for a hosted status there", async () => {
    open("staging", "admin-server");
    await ready();
    expect(screen.getByTestId("workload-restart")).toBeDisabled();
    expect(cloud.getEnvironmentStatus).not.toHaveBeenCalled();
    expect(cloud.restartWorkload).not.toHaveBeenCalled();
  });

  it.each([
    ["staging", /runs on your own cluster.*kubectl rollout restart/],
    ["dev", /local environment: it runs on a developer's machine with forge env up/],
    ["e2e", /does not recognise how e2e is run/],
  ])("says what is true of %s when it cannot restart", async (env, reason) => {
    open(env, "admin-server");
    await waitFor(() => expect(screen.getByTestId("workload-detail")).toHaveAttribute("data-state", "ready"));
    expect(screen.getByTestId("workload-restart")).toBeDisabled();
    expect(screen.getByTestId("workload-restart-unavailable")).toHaveTextContent(reason);
    expect(screen.queryByTestId("workload-stop")).toBeNull();
  });
});

describe("actionsUnavailableReason", () => {
  it("allows hosted kinds and names the reason for each other kind", () => {
    expect(actionsUnavailableReason("persistent", "prod")).toBeNull();
    expect(actionsUnavailableReason("preview", "pr-9")).toBeNull();
    expect(actionsUnavailableReason("self_managed", "staging")).toMatch(/your own cluster/);
    expect(actionsUnavailableReason("local", "dev")).toMatch(/developer's machine/);
    expect(actionsUnavailableReason("unknown", "qa")).toMatch(/does not recognise/);
  });
});
