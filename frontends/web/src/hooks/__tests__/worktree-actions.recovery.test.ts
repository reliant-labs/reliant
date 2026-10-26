import { Code, ConnectError } from "@connectrpc/connect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { deleteWorktreeRecovery, restoreWorktreeRecovery } from "../worktree-actions";
import { worktreeServiceKeys } from "../worktree-service-hooks_gen";
import { inboxServiceKeys } from "../inbox-service-hooks_gen";
import { setTransport } from "@/lib/connect";
import { queryClient } from "@/lib/query-client";
import { mockTransport } from "@/lib/test-utils";

// Seed the exact generated keys the real readers use, not a test-only prefix.
const recoveryKey = worktreeServiceKeys.listWorktreeRecoveries({ projectId: "project" });
const workspaceKey = worktreeServiceKeys.getWorktree({ worktreeId: "original-workspace" });
const inboxKey = inboxServiceKeys.listInbox({});
const projectInboxKey = inboxServiceKeys.listInbox({ projectId: "project" });

beforeEach(() => {
  queryClient.setQueryData(recoveryKey, { phase: "removed" });
  queryClient.setQueryData(workspaceKey, { id: "original-workspace" });
  queryClient.setQueryData(inboxKey, { items: [] });
  queryClient.setQueryData(projectInboxKey, { items: [] });
});
afterEach(() => {
  queryClient.clear();
  setTransport(null);
});

describe("durable recovery transport and cache", () => {
  it("forwards explicit destination and stable operation key, invalidating catalog, workspace and inbox readers", async () => {
    const restore = vi.fn(() => ({ recovery: { id: "recovery", phase: "restored" }, worktreeId: "separate" }));
    setTransport(mockTransport({ "WorktreeService.RestoreWorktreeRecovery": restore }));
    const response = await restoreWorktreeRecovery("recovery", "replacement-machine", "stable-key");
    expect(restore).toHaveBeenCalledWith(expect.objectContaining({ recoveryId: "recovery", targetDaemonId: "replacement-machine", idempotencyKey: "stable-key" }));
    expect(response.worktreeId).toBe("separate");
    for (const key of [recoveryKey, workspaceKey, inboxKey, projectInboxKey]) {
      expect(queryClient.getQueryState(key)?.isInvalidated).toBe(true);
    }
  });

  it("invalidates the recovery catalog as well as the inbox after explicit deletion", async () => {
    const remove = vi.fn(() => ({ recovery: { id: "recovery", phase: "refs_deleted" } }));
    setTransport(mockTransport({ "WorktreeService.DeleteWorktreeRecovery": remove }));
    const response = await deleteWorktreeRecovery("recovery", "delete-key");
    expect(remove).toHaveBeenCalledWith(expect.objectContaining({ recoveryId: "recovery", idempotencyKey: "delete-key" }));
    expect(response.recovery?.phase).toBe("refs_deleted");
    expect(queryClient.getQueryState(recoveryKey)?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(inboxKey)?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(projectInboxKey)?.isInvalidated).toBe(true);
    // Invalidating readers must not optimistically erase a source workspace.
    expect(queryClient.getQueryData(workspaceKey)).toEqual({ id: "original-workspace" });
  });

  it("surfaces permission failures without claiming restore or deletion succeeded", async () => {
    const denied = () => { throw new ConnectError("Reconnect GitHub", Code.PermissionDenied); };
    setTransport(mockTransport({
      "WorktreeService.RestoreWorktreeRecovery": denied,
      "WorktreeService.DeleteWorktreeRecovery": denied,
    }));
    await expect(restoreWorktreeRecovery("recovery", "target", "key")).rejects.toThrow("Reconnect GitHub");
    await expect(deleteWorktreeRecovery("recovery", "delete-key")).rejects.toThrow("Reconnect GitHub");
    expect(queryClient.getQueryData(recoveryKey)).toEqual({ phase: "removed" });
  });
});
