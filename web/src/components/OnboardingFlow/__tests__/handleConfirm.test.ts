/**
 * Tests for GitHubConnectStep confirm logic — verifies cloneRepo is always
 * called and that clone failures now propagate to the outer catch so the user
 * sees the error in the inline error block.
 *
 * Mirrors the handleCloud.test.ts pattern: extract orchestration logic into
 * a pure function that uses mocked API calls.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Daemon } from "@/services/controlPlane/daemon";
import type { GitRepo } from "@/services/controlPlane/git";
import { DaemonStatus } from "@/gen/controlplane/v1/public/shared_pb";

// ── Mock the daemon + git modules ────────────────────────────

const mockListDaemons = vi.fn<() => Promise<{ daemons: Daemon[] }>>();
const mockCloneRepo = vi.fn<
  (req: {
    daemonId: string;
    gitRepo: string;
    gitBranch: string;
    path: string;
  }) => Promise<{ clonedPath: string }>
>();
const mockCreateDaemon = vi.fn<
  (req: {
    name: string;
    daemonType: number;
    size: number;
    gitRepo: string;
    gitBranch: string;
  }) => Promise<void>
>();

vi.mock("@/services/controlPlane/daemon", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("@/services/controlPlane/daemon")>();
  return {
    ...actual,
    listDaemons: (...args: Parameters<typeof mockListDaemons>) =>
      mockListDaemons(...args),
    createDaemon: (...args: Parameters<typeof mockCreateDaemon>) =>
      mockCreateDaemon(...args),
  };
});

vi.mock("@/services/controlPlane/git", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("@/services/controlPlane/git")>();
  return {
    ...actual,
    gitService: {
      ...actual.gitService,
      cloneRepo: (...args: Parameters<typeof mockCloneRepo>) =>
        mockCloneRepo(...args),
    },
  };
});

function makeDaemon(partial: Partial<Daemon>): Daemon {
  return partial as unknown as Daemon;
}

// ── Re-implement handleConfirm clone orchestration as a testable function ──

interface ConfirmResult {
  clonedPath: string;
  daemonId: string;
}

const CLOUD_PROJECT_ROOT = "/home/workspace/projects";
const ONBOARDING_DAEMON_NAME = "onboarding-daemon";
const DAEMON_TYPE_MANAGED = 1;
const DAEMON_SIZE_SMALL = 1;

/**
 * Mirrors the clone orchestration in GitHubConnectStep.handleConfirm:
 * 1. List daemons
 * 2. Pick the active daemon, falling back to the first daemon by index
 *    (the daemon's UUID is what the server keys lookups by)
 * 3. Re-run CreateDaemon with the picked repo so the daemon row stores
 *    git_repo and the workspace command carries it to the controller
 *    (non-blocking on failure)
 * 4. Always attempt clone — failures propagate (the user sees them in the
 *    confirm-step error block).
 */
async function confirmCloneLogic(
  repo: GitRepo,
  branch: string,
  projectPath: string,
): Promise<ConfirmResult> {
  const { listDaemons, createDaemon, DAEMON_STATUS_ACTIVE } = await import(
    "@/services/controlPlane/daemon"
  );
  const { gitService } = await import("@/services/controlPlane/git");

  const { daemons } = await listDaemons();
  const daemon =
    daemons.find((d) => d.status === DAEMON_STATUS_ACTIVE) ?? daemons[0];
  if (!daemon) {
    throw new Error("Hosted workspace is still starting. Try again in a moment.");
  }

  try {
    await createDaemon({
      name: ONBOARDING_DAEMON_NAME,
      daemonType: DAEMON_TYPE_MANAGED,
      size: DAEMON_SIZE_SMALL,
      gitRepo: repo.cloneUrl,
      gitBranch: branch,
    });
  } catch {
    // Refresh may fail (e.g. plan limit, transient error); proceed with clone.
  }

  const cloneResult = await gitService.cloneRepo({
    daemonId: daemon.id,
    gitRepo: repo.cloneUrl,
    gitBranch: branch,
    path: projectPath,
  });
  return { clonedPath: cloneResult.clonedPath, daemonId: daemon.id };
}

// ── Test data ────────────────────────────────────────────────

const testRepo: GitRepo = {
  fullName: "user/my-app",
  cloneUrl: "https://github.com/user/my-app.git",
  defaultBranch: "main",
  description: "A test repo",
  private: false,
  language: "TypeScript",
  updatedAt: "2024-01-01T00:00:00Z",
};

const projectPath = `${CLOUD_PROJECT_ROOT}/my-app`;

// ── Tests ────────────────────────────────────────────────────

beforeEach(() => {
  vi.clearAllMocks();
  // Default: createDaemon refresh succeeds. Individual tests can override.
  mockCreateDaemon.mockResolvedValue(undefined);
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("GitHubConnectStep confirm — clone orchestration", () => {
  it("calls cloneRepo with the active daemon's UUID", async () => {
    mockListDaemons.mockResolvedValue({
      daemons: [
        makeDaemon({
          id: "daemon-active-uuid",
          hostname: "ws-active",
          status: DaemonStatus.ACTIVE,
        }),
      ],
    });
    mockCloneRepo.mockResolvedValue({ clonedPath: projectPath });

    const result = await confirmCloneLogic(testRepo, "main", projectPath);

    expect(mockCloneRepo).toHaveBeenCalledWith({
      daemonId: "daemon-active-uuid",
      gitRepo: "https://github.com/user/my-app.git",
      gitBranch: "main",
      path: projectPath,
    });
    expect(result.daemonId).toBe("daemon-active-uuid");
  });

  it("falls back to the first daemon's UUID when none are active (provisioning)", async () => {
    mockListDaemons.mockResolvedValue({
      daemons: [
        makeDaemon({
          id: "pending-uuid",
          name: "onboarding-daemon",
          status: DaemonStatus.PENDING,
        }),
      ],
    });
    mockCloneRepo.mockResolvedValue({ clonedPath: projectPath });

    const result = await confirmCloneLogic(testRepo, "develop", projectPath);

    expect(mockCloneRepo).toHaveBeenCalledWith(
      expect.objectContaining({
        daemonId: "pending-uuid",
        gitRepo: "https://github.com/user/my-app.git",
        gitBranch: "develop",
      }),
    );
    expect(result.daemonId).toBe("pending-uuid");
  });

  it("propagates the cloneRepo error to the caller — the UI surfaces it", async () => {
    mockListDaemons.mockResolvedValue({
      daemons: [makeDaemon({ id: "d-1", status: DaemonStatus.ACTIVE })],
    });
    mockCloneRepo.mockRejectedValue(new Error("daemon unreachable"));

    await expect(
      confirmCloneLogic(testRepo, "main", projectPath),
    ).rejects.toThrow("daemon unreachable");
  });

  it("uses clonedPath from successful clone result", async () => {
    const overriddenPath = "/home/workspace/projects/my-app-123";
    mockListDaemons.mockResolvedValue({
      daemons: [makeDaemon({ id: "d-1", status: DaemonStatus.ACTIVE })],
    });
    mockCloneRepo.mockResolvedValue({ clonedPath: overriddenPath });

    const result = await confirmCloneLogic(testRepo, "main", projectPath);

    expect(result.clonedPath).toBe(overriddenPath);
  });

  it("passes correct repo URL and branch to cloneRepo", async () => {
    const customRepo: GitRepo = {
      ...testRepo,
      cloneUrl: "https://github.com/org/special-repo.git",
    };
    mockListDaemons.mockResolvedValue({
      daemons: [makeDaemon({ id: "d-99", status: DaemonStatus.ACTIVE })],
    });
    mockCloneRepo.mockResolvedValue({ clonedPath: projectPath });

    await confirmCloneLogic(customRepo, "feature/xyz", projectPath);

    expect(mockCloneRepo).toHaveBeenCalledWith({
      daemonId: "d-99",
      gitRepo: "https://github.com/org/special-repo.git",
      gitBranch: "feature/xyz",
      path: projectPath,
    });
  });

  it("throws when no daemon is found at all", async () => {
    mockListDaemons.mockResolvedValue({ daemons: [] });

    await expect(confirmCloneLogic(testRepo, "main", projectPath)).rejects.toThrow(
      "Hosted workspace is still starting",
    );
    expect(mockCloneRepo).not.toHaveBeenCalled();
    expect(mockCreateDaemon).not.toHaveBeenCalled();
  });

  it("re-runs CreateDaemon with the picked repo so daemons.git_repo is populated", async () => {
    mockListDaemons.mockResolvedValue({
      daemons: [makeDaemon({ id: "d-1", status: DaemonStatus.ACTIVE })],
    });
    mockCloneRepo.mockResolvedValue({ clonedPath: projectPath });

    await confirmCloneLogic(testRepo, "main", projectPath);

    expect(mockCreateDaemon).toHaveBeenCalledWith({
      name: "onboarding-daemon",
      daemonType: 1,
      size: 1,
      gitRepo: "https://github.com/user/my-app.git",
      gitBranch: "main",
    });
  });

  it("still clones when CreateDaemon refresh fails (non-blocking)", async () => {
    mockListDaemons.mockResolvedValue({
      daemons: [makeDaemon({ id: "d-1", status: DaemonStatus.ACTIVE })],
    });
    mockCreateDaemon.mockRejectedValue(new Error("refresh failed"));
    mockCloneRepo.mockResolvedValue({ clonedPath: projectPath });

    const result = await confirmCloneLogic(testRepo, "main", projectPath);

    expect(mockCreateDaemon).toHaveBeenCalled();
    expect(mockCloneRepo).toHaveBeenCalled();
    expect(result.clonedPath).toBe(projectPath);
  });
});
