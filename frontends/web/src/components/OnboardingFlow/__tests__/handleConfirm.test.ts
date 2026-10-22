/**
 * Onboarding's add-a-repo orchestration.
 *
 * These drive `addRepoProject` — the real module GitHubConnectStep calls.
 * The previous version of this file RE-IMPLEMENTED the step's clone sequence
 * inside the test so it could assert on it, which meant the assertions pinned
 * the copy rather than the product: the component could diverge completely and
 * these would still pass.
 *
 * What they pin now: one server call replaces the four-call chain
 * (CloneRepo → CreateProject → MarkProjectInstalled + already-exists
 * recovery), the daemon refresh stays best-effort, and a queued clone is
 * reported as queued rather than as a finished checkout.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { DaemonInfo as Daemon } from "@/gen/reliant/v1/daemon_registry_pb";
import { DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";

const mockListDaemons = vi.fn<() => Promise<{ daemons: Daemon[] }>>();
const mockCreateDaemon = vi.fn();
const mockCreateProjectFromRepo = vi.fn();
const mockCloneRepo = vi.fn();
const mockMarkProjectInstalled = vi.fn();

// The LIST comes from the daemon registry — the one daemon list
// (docs/design/one-daemon-list.md) — while CreateDaemon stays a control-plane
// command, so the two are stubbed separately.
vi.mock("@/api/grpc-client", () => ({
  grpcClient: {
    daemonRegistry: () => ({
      listDaemons: (...args: unknown[]) => mockListDaemons(...(args as [])),
    }),
  },
}));

vi.mock("@/services/controlPlane/daemon", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/services/controlPlane/daemon")>();
  return {
    ...actual,
    createDaemon: (...args: unknown[]) => mockCreateDaemon(...args),
  };
});

vi.mock("@/api/project-grpc", () => ({
  projectGrpc: {
    createProjectFromRepo: (...args: unknown[]) => mockCreateProjectFromRepo(...args),
    markProjectInstalled: (...args: unknown[]) => mockMarkProjectInstalled(...args),
  },
}));

vi.mock("@/services/controlPlane/git", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/services/controlPlane/git")>();
  return {
    ...actual,
    gitService: { ...actual.gitService, cloneRepo: (...a: unknown[]) => mockCloneRepo(...a) },
  };
});

import { addRepoProject, pickOnboardingDaemon } from "../addRepoProject";
import { markMachineGone, resetGoneMachinesForTest } from "@/lib/goneMachines";

function makeDaemon(partial: Partial<Daemon>): Daemon {
  return partial as unknown as Daemon;
}

const CLONE_URL = "https://github.com/user/my-app.git";
const projectPath = "/home/workspace/projects/my-app";

function cloneArgs(overrides: Record<string, string> = {}) {
  return {
    cloneUrl: CLONE_URL,
    branch: "main",
    path: projectPath,
    name: "my-app",
    ...overrides,
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  mockCreateDaemon.mockResolvedValue(undefined);
  mockCreateProjectFromRepo.mockResolvedValue({
    project: { id: "proj-1" },
    projectDaemon: { path: projectPath },
    queued: false,
    daemonName: "ws-active",
  });
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("addRepoProject", () => {
  beforeEach(() => {
    mockListDaemons.mockResolvedValue({
      daemons: [makeDaemon({ daemonId: "daemon-active-uuid", hostname: "ws-active", status: DaemonStatus.ACTIVE })],
    });
  });

  it("adds the project in ONE server call, keyed by the daemon's UUID", async () => {
    const result = await addRepoProject(cloneArgs());

    expect(mockCreateProjectFromRepo).toHaveBeenCalledWith({
      cloneUrl: CLONE_URL,
      daemonId: "daemon-active-uuid",
      name: "my-app",
      branch: "main",
      path: projectPath,
    });
    expect(result.projectId).toBe("proj-1");
    expect(result.daemonId).toBe("daemon-active-uuid");
  });

  it("no longer drives the four-call chain", async () => {
    // Each of those calls could fail on its own and leave a project with no
    // checkout, or a checkout with no project. That is the whole reason the
    // sequence moved server-side.
    await addRepoProject(cloneArgs());

    expect(mockCloneRepo).not.toHaveBeenCalled();
    expect(mockMarkProjectInstalled).not.toHaveBeenCalled();
  });

  it("reports a queued clone as queued, not as a finished checkout", async () => {
    mockCreateProjectFromRepo.mockResolvedValue({
      project: { id: "proj-2" },
      projectDaemon: { path: projectPath },
      queued: true,
      daemonName: "ws-booting",
    });

    const result = await addRepoProject(cloneArgs());

    expect(result.queued).toBe(true);
    expect(result.machineName).toBe("ws-booting");
  });

  it("propagates the failure so the step shows it inline", async () => {
    mockCreateProjectFromRepo.mockRejectedValue(new Error("daemon unreachable"));

    await expect(addRepoProject(cloneArgs())).rejects.toThrow("daemon unreachable");
  });

  it("takes the server's path for the checkout over the requested one", async () => {
    mockCreateProjectFromRepo.mockResolvedValue({
      project: { id: "proj-3" },
      projectDaemon: { path: "/home/workspace/projects/my-app-123" },
      queued: false,
      daemonName: "ws-active",
    });

    const result = await addRepoProject(cloneArgs());

    expect(result.clonedPath).toBe("/home/workspace/projects/my-app-123");
  });

  it("re-runs CreateDaemon with the picked repo so daemons.git_repo is populated", async () => {
    await addRepoProject(cloneArgs());

    expect(mockCreateDaemon).toHaveBeenCalledWith({
      name: "onboarding-daemon",
      daemonType: 1,
      size: 1,
      gitRepo: CLONE_URL,
      gitBranch: "main",
    });
  });

  it("still adds the project when the CreateDaemon refresh fails", async () => {
    // The refresh is about the MACHINE, not the project. The clone populates
    // the tree regardless, so a refresh failure must not block onboarding.
    mockCreateDaemon.mockRejectedValue(new Error("refresh failed"));

    const result = await addRepoProject(cloneArgs());

    expect(mockCreateProjectFromRepo).toHaveBeenCalled();
    expect(result.projectId).toBe("proj-1");
  });
});

describe("addRepoProject — daemon selection", () => {
  it("clones onto a still-provisioning machine rather than refusing", async () => {
    // Onboarding's machine is frequently still booting, and the clone is
    // durably queued. Refusing here would strand the user at the last step.
    mockListDaemons.mockResolvedValue({
      daemons: [makeDaemon({ daemonId: "pending-uuid", status: DaemonStatus.PENDING })],
    });

    const result = await addRepoProject(cloneArgs({ branch: "develop" }));

    expect(result.daemonId).toBe("pending-uuid");
    expect(mockCreateProjectFromRepo).toHaveBeenCalledWith(
      expect.objectContaining({ daemonId: "pending-uuid", branch: "develop" }),
    );
  });

  it("throws when the account has no machine at all", async () => {
    mockListDaemons.mockResolvedValue({ daemons: [] });

    await expect(addRepoProject(cloneArgs())).rejects.toThrow("still starting");
    expect(mockCreateProjectFromRepo).not.toHaveBeenCalled();
    expect(mockCreateDaemon).not.toHaveBeenCalled();
  });
});

describe("pickOnboardingDaemon", () => {
  it("prefers a running machine over one that is still booting", () => {
    const picked = pickOnboardingDaemon([
      makeDaemon({ daemonId: "pending", status: DaemonStatus.PENDING }),
      makeDaemon({ daemonId: "active", status: DaemonStatus.ACTIVE }),
    ]);

    expect(picked?.daemonId).toBe("active");
  });

  // A clone queued onto a FAILED machine does not "report its own failure":
  // the control plane accepts it and nothing ever drains the queue, so the
  // project sits installing forever. A failed machine is never auto-picked
  // (MACHINE_LIST_BUGS_2026-10-07); addRepoProject says why instead.
  it("never falls back to a failed machine", () => {
    const picked = pickOnboardingDaemon([makeDaemon({ daemonId: "failed", status: DaemonStatus.FAILED })]);

    expect(picked).toBeUndefined();
  });

  it("never picks a machine the control plane said is gone", () => {
    markMachineGone("ghost");
    try {
      const picked = pickOnboardingDaemon([
        makeDaemon({ daemonId: "ghost", status: DaemonStatus.PENDING }),
        makeDaemon({ daemonId: "live", status: DaemonStatus.SUSPENDED }),
      ]);
      expect(picked?.daemonId).toBe("live");
    } finally {
      resetGoneMachinesForTest();
    }
  });
});
