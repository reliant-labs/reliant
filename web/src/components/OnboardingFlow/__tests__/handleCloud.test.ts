/**
 * Tests for the handleCloud flow logic in ComputeStep.
 *
 * We test the orchestration logic (list → reuse / resume / create) by
 * extracting it into a pure function that mirrors handleCloud's behavior
 * and mocking the API calls.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Daemon } from "../api";

// ── Mock the API module ──────────────────────────────────────

const mockListDaemons = vi.fn<() => Promise<{ daemons: Daemon[] }>>();
const mockResumeDaemon = vi.fn<(id: string) => Promise<void>>();
const mockCreateDaemon = vi.fn<(req: Record<string, unknown>) => Promise<void>>();
const mockHasActiveDaemon = vi.fn<(daemons: Daemon[]) => boolean>();
const mockGetFirstDaemonId = vi.fn<(daemons: Daemon[]) => string>();

vi.mock("../api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../api")>();
  return {
    ...actual,
    listDaemons: (...args: Parameters<typeof mockListDaemons>) => mockListDaemons(...args),
    resumeDaemon: (...args: Parameters<typeof mockResumeDaemon>) => mockResumeDaemon(...args),
    createDaemon: (...args: Parameters<typeof mockCreateDaemon>) => mockCreateDaemon(...args),
    hasActiveDaemon: (...args: Parameters<typeof mockHasActiveDaemon>) => mockHasActiveDaemon(...args),
    getFirstDaemonId: (...args: Parameters<typeof mockGetFirstDaemonId>) => mockGetFirstDaemonId(...args),
  };
});

// ── Re-implement handleCloud orchestration as a testable function ─────

type HandleCloudResult = {
  provisioning: boolean;
  error?: string;
};

/**
 * Mirrors the handleCloud logic from ComputeStep.tsx for unit testing
 * without needing to render React components.
 */
async function handleCloudLogic(): Promise<HandleCloudResult> {
  const { listDaemons, hasActiveDaemon, getFirstDaemonId, resumeDaemon, createDaemon } = await import("../api");

  const existing = await listDaemons();
  const daemons = existing.daemons;

  // Path 1: Active daemon → reuse
  if (hasActiveDaemon(daemons)) {
    return { provisioning: false };
  }

  // Path 2: Daemon exists but not active → resume
  if (daemons.length > 0) {
    const daemonId = getFirstDaemonId(daemons);
    if (!daemonId) {
      throw new Error("Found existing daemon but could not determine its ID. Please try again.");
    }
    try {
      await resumeDaemon(daemonId);
    } catch {
      // Resume failed — proceed with provisioning state
    }
    return { provisioning: true };
  }

  // Path 3: No daemons → create
  try {
    await createDaemon({
      name: "onboarding-workspace",
      daemonType: 1,
      size: 1,
      gitRepo: "",
      gitBranch: "main",
    });
  } catch (err) {
    const message = err instanceof Error ? err.message.toLowerCase() : "";
    if (message.includes("plan limit") || message.includes("already") || message.includes("exists")) {
      const fallback = await listDaemons();
      const fallbackId = getFirstDaemonId(fallback.daemons);
      if (fallbackId) {
        try {
          await resumeDaemon(fallbackId);
        } catch {
          // Fallback resume failed — proceed anyway
        }
      }
    } else {
      throw err;
    }
  }

  return { provisioning: true };
}

// ── Tests ────────────────────────────────────────────────────

beforeEach(() => {
  vi.clearAllMocks();
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("handleCloud orchestration", () => {
  it("reuses active daemon without creating or resuming", async () => {
    mockListDaemons.mockResolvedValue({
      daemons: [{ daemonId: "d-1", hostname: "ws-active", status: 3 }],
    });
    mockHasActiveDaemon.mockReturnValue(true);

    const result = await handleCloudLogic();

    expect(result.provisioning).toBe(false);
    expect(mockResumeDaemon).not.toHaveBeenCalled();
    expect(mockCreateDaemon).not.toHaveBeenCalled();
  });

  it("resumes inactive daemon when one exists", async () => {
    mockListDaemons.mockResolvedValue({
      daemons: [{ name: "daemons/ws-idle", status: 2 }],
    });
    mockHasActiveDaemon.mockReturnValue(false);
    mockGetFirstDaemonId.mockReturnValue("daemons/ws-idle");
    mockResumeDaemon.mockResolvedValue(undefined);

    const result = await handleCloudLogic();

    expect(result.provisioning).toBe(true);
    expect(mockResumeDaemon).toHaveBeenCalledWith("daemons/ws-idle");
    expect(mockCreateDaemon).not.toHaveBeenCalled();
  });

  it("throws clear error when daemon exists but ID cannot be determined", async () => {
    mockListDaemons.mockResolvedValue({
      daemons: [{ status: 0 }], // no ID fields at all
    });
    mockHasActiveDaemon.mockReturnValue(false);
    mockGetFirstDaemonId.mockReturnValue("");

    await expect(handleCloudLogic()).rejects.toThrow(
      "Found existing daemon but could not determine its ID",
    );
  });

  it("proceeds to provisioning state even if resumeDaemon fails", async () => {
    mockListDaemons.mockResolvedValue({
      daemons: [{ daemon_id: "d-2", status: 0 }],
    });
    mockHasActiveDaemon.mockReturnValue(false);
    mockGetFirstDaemonId.mockReturnValue("d-2");
    mockResumeDaemon.mockRejectedValue(new Error("daemon not ready"));

    const result = await handleCloudLogic();

    expect(result.provisioning).toBe(true);
    expect(mockResumeDaemon).toHaveBeenCalledWith("d-2");
  });

  it("creates a new daemon when none exist", async () => {
    mockListDaemons.mockResolvedValue({ daemons: [] });
    mockHasActiveDaemon.mockReturnValue(false);
    mockCreateDaemon.mockResolvedValue(undefined);

    const result = await handleCloudLogic();

    expect(result.provisioning).toBe(true);
    expect(mockCreateDaemon).toHaveBeenCalledWith(
      expect.objectContaining({ name: "onboarding-workspace" }),
    );
    expect(mockResumeDaemon).not.toHaveBeenCalled();
  });

  it("falls back to resume when create fails with 'plan limit'", async () => {
    // First listDaemons returns empty
    mockListDaemons
      .mockResolvedValueOnce({ daemons: [] })
      // Fallback listDaemons finds the existing one
      .mockResolvedValueOnce({
        daemons: [{ name: "daemons/existing", status: 0 }],
      });
    mockHasActiveDaemon.mockReturnValue(false);
    mockCreateDaemon.mockRejectedValue(new Error("plan limit reached"));
    mockGetFirstDaemonId.mockReturnValue("daemons/existing");
    mockResumeDaemon.mockResolvedValue(undefined);

    const result = await handleCloudLogic();

    expect(result.provisioning).toBe(true);
    expect(mockResumeDaemon).toHaveBeenCalledWith("daemons/existing");
  });

  it("falls back to resume when create fails with 'already exists'", async () => {
    mockListDaemons
      .mockResolvedValueOnce({ daemons: [] })
      .mockResolvedValueOnce({
        daemons: [{ daemonId: "ws-1", status: 2 }],
      });
    mockHasActiveDaemon.mockReturnValue(false);
    mockCreateDaemon.mockRejectedValue(new Error("daemon already exists"));
    mockGetFirstDaemonId.mockReturnValue("ws-1");
    mockResumeDaemon.mockResolvedValue(undefined);

    const result = await handleCloudLogic();

    expect(result.provisioning).toBe(true);
    expect(mockResumeDaemon).toHaveBeenCalledWith("ws-1");
  });

  it("proceeds even if fallback resume fails after plan-limit create error", async () => {
    mockListDaemons
      .mockResolvedValueOnce({ daemons: [] })
      .mockResolvedValueOnce({
        daemons: [{ name: "daemons/ws", status: 0 }],
      });
    mockHasActiveDaemon.mockReturnValue(false);
    mockCreateDaemon.mockRejectedValue(new Error("plan limit reached"));
    mockGetFirstDaemonId.mockReturnValue("daemons/ws");
    mockResumeDaemon.mockRejectedValue(new Error("not ready"));

    const result = await handleCloudLogic();

    expect(result.provisioning).toBe(true);
  });

  it("skips fallback resume when fallback listDaemons returns empty", async () => {
    mockListDaemons
      .mockResolvedValueOnce({ daemons: [] })
      .mockResolvedValueOnce({ daemons: [] });
    mockHasActiveDaemon.mockReturnValue(false);
    mockCreateDaemon.mockRejectedValue(new Error("already exists"));
    mockGetFirstDaemonId.mockReturnValue("");

    const result = await handleCloudLogic();

    expect(result.provisioning).toBe(true);
    expect(mockResumeDaemon).not.toHaveBeenCalled();
  });

  it("propagates unexpected create errors", async () => {
    mockListDaemons.mockResolvedValue({ daemons: [] });
    mockHasActiveDaemon.mockReturnValue(false);
    mockCreateDaemon.mockRejectedValue(new Error("internal server error"));

    await expect(handleCloudLogic()).rejects.toThrow("internal server error");
  });
});
