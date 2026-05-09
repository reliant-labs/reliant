/**
 * Tests for GitHubConnectStep confirm logic — verifies cloneRepo is always
 * called (even when daemon is provisioning) and that clone failures are
 * non-blocking.
 *
 * Mirrors the handleCloud.test.ts pattern: extract orchestration logic into
 * a pure function that uses mocked API calls.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Daemon, GitRepo } from "../api";

// ── Mock the API module ──────────────────────────────────────

const mockListDaemons = vi.fn<() => Promise<{ daemons: Daemon[] }>>();
const mockGetActiveDaemonName = vi.fn<(daemons: Daemon[]) => string>();
const mockGetFirstDaemonId = vi.fn<(daemons: Daemon[]) => string>();
const mockCloneRepo = vi.fn<
  (req: {
    daemonName: string;
    gitRepo: string;
    gitBranch: string;
    path: string;
    accountLogin?: string;
  }) => Promise<{ clonedPath: string }>
>();

vi.mock("../api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../api")>();
  return {
    ...actual,
    listDaemons: (...args: Parameters<typeof mockListDaemons>) => mockListDaemons(...args),
    getActiveDaemonName: (...args: Parameters<typeof mockGetActiveDaemonName>) =>
      mockGetActiveDaemonName(...args),
    getFirstDaemonId: (...args: Parameters<typeof mockGetFirstDaemonId>) =>
      mockGetFirstDaemonId(...args),
    cloneRepo: (...args: Parameters<typeof mockCloneRepo>) => mockCloneRepo(...args),
  };
});

// ── Re-implement handleConfirm clone orchestration as a testable function ──

interface ConfirmResult {
  clonedPath: string;
  daemonName: string;
  cloneSucceeded: boolean;
}

const CLOUD_PROJECT_ROOT = "/home/workspace/projects";

/**
 * Mirrors the clone orchestration in GitHubConnectStep.handleConfirm:
 * 1. List daemons
 * 2. Pick active daemon name or fall back to first daemon ID
 * 3. Always attempt clone (non-blocking on failure)
 */
async function confirmCloneLogic(
  repo: GitRepo,
  branch: string,
  projectPath: string,
): Promise<ConfirmResult> {
  const { listDaemons, getActiveDaemonName, getFirstDaemonId, cloneRepo } =
    await import("../api");

  const { daemons } = await listDaemons();
  const daemonName = getActiveDaemonName(daemons) || getFirstDaemonId(daemons);
  if (!daemonName) {
    throw new Error("Hosted workspace is still starting. Try again in a moment.");
  }

  // Always attempt clone — CloneRepo queues via JetStream when daemon is offline
  let clonedPath = projectPath;
  let cloneSucceeded = true;
  try {
    const result = await cloneRepo({
      daemonName,
      gitRepo: repo.cloneUrl,
      gitBranch: branch,
      path: projectPath,
      accountLogin: repo.accountLogin,
    });
    clonedPath = result.clonedPath;
  } catch {
    // Clone may fail for transient reasons; warn but don't block
    cloneSucceeded = false;
  }

  return { clonedPath, daemonName, cloneSucceeded };
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
  accountLogin: "user",
};

const projectPath = `${CLOUD_PROJECT_ROOT}/my-app`;

// ── Tests ────────────────────────────────────────────────────

beforeEach(() => {
  vi.clearAllMocks();
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("GitHubConnectStep confirm — clone orchestration", () => {
  it("calls cloneRepo with active daemon name", async () => {
    mockListDaemons.mockResolvedValue({
      daemons: [{ hostname: "ws-active", status: 3 }],
    });
    mockGetActiveDaemonName.mockReturnValue("ws-active");
    mockGetFirstDaemonId.mockReturnValue("");
    mockCloneRepo.mockResolvedValue({ clonedPath: projectPath });

    const result = await confirmCloneLogic(testRepo, "main", projectPath);

    expect(mockCloneRepo).toHaveBeenCalledWith({
      daemonName: "ws-active",
      gitRepo: "https://github.com/user/my-app.git",
      gitBranch: "main",
      path: projectPath,
      accountLogin: "user",
    });
    expect(result.daemonName).toBe("ws-active");
    expect(result.cloneSucceeded).toBe(true);
  });

  it("falls back to first daemon ID when no active daemon (provisioning)", async () => {
    mockListDaemons.mockResolvedValue({
      daemons: [{ name: "daemons/onboarding-workspace", status: 0 }],
    });
    mockGetActiveDaemonName.mockReturnValue(""); // No active daemon
    mockGetFirstDaemonId.mockReturnValue("daemons/onboarding-workspace");
    mockCloneRepo.mockResolvedValue({ clonedPath: projectPath });

    const result = await confirmCloneLogic(testRepo, "develop", projectPath);

    expect(mockCloneRepo).toHaveBeenCalledWith(
      expect.objectContaining({
        daemonName: "daemons/onboarding-workspace",
        gitRepo: "https://github.com/user/my-app.git",
        gitBranch: "develop",
      }),
    );
    expect(result.daemonName).toBe("daemons/onboarding-workspace");
    expect(result.cloneSucceeded).toBe(true);
  });

  it("does not throw when cloneRepo fails — proceeds with plan update", async () => {
    mockListDaemons.mockResolvedValue({
      daemons: [{ hostname: "ws-active", status: 3 }],
    });
    mockGetActiveDaemonName.mockReturnValue("ws-active");
    mockGetFirstDaemonId.mockReturnValue("");
    mockCloneRepo.mockRejectedValue(new Error("daemon unreachable"));

    const result = await confirmCloneLogic(testRepo, "main", projectPath);

    expect(mockCloneRepo).toHaveBeenCalled();
    expect(result.cloneSucceeded).toBe(false);
    // Falls back to the original projectPath
    expect(result.clonedPath).toBe(projectPath);
  });

  it("uses clonedPath from successful clone result", async () => {
    const overriddenPath = "/home/workspace/projects/my-app-123";
    mockListDaemons.mockResolvedValue({
      daemons: [{ hostname: "ws-active", status: 1 }],
    });
    mockGetActiveDaemonName.mockReturnValue("ws-active");
    mockGetFirstDaemonId.mockReturnValue("");
    mockCloneRepo.mockResolvedValue({ clonedPath: overriddenPath });

    const result = await confirmCloneLogic(testRepo, "main", projectPath);

    expect(result.clonedPath).toBe(overriddenPath);
  });

  it("passes correct repo URL, branch, and accountLogin to cloneRepo", async () => {
    const customRepo: GitRepo = {
      ...testRepo,
      cloneUrl: "https://github.com/org/special-repo.git",
      accountLogin: "org-bot",
    };
    mockListDaemons.mockResolvedValue({
      daemons: [{ daemonId: "d-99", status: 3 }],
    });
    mockGetActiveDaemonName.mockReturnValue("d-99");
    mockGetFirstDaemonId.mockReturnValue("");
    mockCloneRepo.mockResolvedValue({ clonedPath: projectPath });

    await confirmCloneLogic(customRepo, "feature/xyz", projectPath);

    expect(mockCloneRepo).toHaveBeenCalledWith({
      daemonName: "d-99",
      gitRepo: "https://github.com/org/special-repo.git",
      gitBranch: "feature/xyz",
      path: projectPath,
      accountLogin: "org-bot",
    });
  });

  it("throws when no daemon is found at all", async () => {
    mockListDaemons.mockResolvedValue({ daemons: [] });
    mockGetActiveDaemonName.mockReturnValue("");
    mockGetFirstDaemonId.mockReturnValue("");

    await expect(confirmCloneLogic(testRepo, "main", projectPath)).rejects.toThrow(
      "Hosted workspace is still starting",
    );
    expect(mockCloneRepo).not.toHaveBeenCalled();
  });
});
