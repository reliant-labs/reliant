import { QueryClient, QueryClientProvider, focusManager, onlineManager } from "@tanstack/react-query";
import { act, cleanup, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ConnectError, Code } from "@connectrpc/connect";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { useWorktreePR, useWorktreeMachineActive } from "../worktree-git-queries";
import { invalidateWorktreeQueries } from "../worktree-queries";
import { DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";

vi.mock("../worktree-queries", async (importOriginal) => ({
  ...await importOriginal<typeof import("../worktree-queries")>(),
  useWorktreeById: () => ({ daemon_id: "machine-a" }),
}));

let qc: QueryClient;
function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
}
const initial = { worktreeId: "wt-a", repoId: "repo-a", branch: "feature", machineActive: true, visible: true };
const found = { exists: true, url: "https://github.com/o/r/pull/1", number: 1, state: "OPEN", title: "One" };
async function settle() { await act(async () => { await vi.advanceTimersByTimeAsync(1); }); }
async function advance(ms: number) { await act(async () => { await vi.advanceTimersByTimeAsync(ms); }); await settle(); }
beforeEach(() => {
  vi.useFakeTimers();
  qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: Infinity } } });
  focusManager.setFocused(true);
  onlineManager.setOnline(true);
});
afterEach(() => {
  cleanup(); qc.clear(); setTransport(null); vi.useRealTimers();
  focusManager.setFocused(undefined); onlineManager.setOnline(true);
});

describe("worktree PR lifecycle", () => {
  it("discovers a PR after its machine wakes without a push, remount or manual refresh", async () => {
    let awake = false;
    const registry = vi.fn(() => ({ daemons: awake ? [{ daemonId: "machine-a", status: DaemonStatus.ACTIVE }] : [] }));
    const lookup = vi.fn(() => found);
    setTransport(mockTransport({
      "DaemonRegistryService.ListDaemons": registry,
      "WorktreeService.GetWorktreePR": lookup,
    }));
    const hook = renderHook(() => {
      const machineActive = useWorktreeMachineActive(initial.worktreeId);
      return useWorktreePR({ ...initial, machineActive });
    }, { wrapper });
    await settle();
    expect(registry).toHaveBeenCalledTimes(1);
    expect(lookup).not.toHaveBeenCalled();
    expect(hook.result.current.status).toBe("error");
    awake = true;
    await advance(30_000);
    expect(registry).toHaveBeenCalledTimes(2);
    expect(lookup).toHaveBeenCalledTimes(1);
    expect(hook.result.current.existing?.url).toBe(found.url);
  });

  it("shares a canonical query and discovers an external PR without a filesystem event", async () => {
    let answer = { exists: false };
    const lookup = vi.fn(() => answer);
    setTransport(mockTransport({ "WorktreeService.GetWorktreePR": lookup }));
    const a = renderHook(() => useWorktreePR(initial), { wrapper });
    const b = renderHook(() => useWorktreePR(initial), { wrapper });
    expect(a.result.current.status).toBe("loading");
    await settle();
    expect(a.result.current.status).toBe("absent");
    expect(lookup).toHaveBeenCalledTimes(1);
    answer = found;
    await advance(60_000);
    expect(a.result.current.existing?.url).toBe(found.url);
    expect(b.result.current.status).toBe("found");
    expect(lookup).toHaveBeenCalledTimes(2);
  });

  it("refreshes on manual retry, focus, reconnect, invalidation and machine recovery", async () => {
    const lookup = vi.fn(() => found);
    setTransport(mockTransport({ "WorktreeService.GetWorktreePR": lookup }));
    const hook = renderHook((props) => useWorktreePR(props), { initialProps: initial, wrapper });
    await settle();
    expect(hook.result.current.status).toBe("found");
    act(() => hook.result.current.retry()); await settle();
    expect(lookup).toHaveBeenCalledTimes(2);
    act(() => focusManager.setFocused(false));
    act(() => focusManager.setFocused(true)); await settle();
    expect(lookup).toHaveBeenCalledTimes(3);
    act(() => onlineManager.setOnline(false));
    act(() => onlineManager.setOnline(true)); await settle();
    expect(lookup).toHaveBeenCalledTimes(4);
    await act(async () => { await invalidateWorktreeQueries(qc, "wt-a"); }); await settle();
    expect(lookup).toHaveBeenCalledTimes(5);
    hook.rerender({ ...initial, machineActive: false });
    await advance(300_000);
    expect(lookup).toHaveBeenCalledTimes(5);
    hook.rerender(initial); await settle();
    expect(lookup).toHaveBeenCalledTimes(6);
  });

  it("never polls hidden panels or sleeping machines, and catches up when visible", async () => {
    const lookup = vi.fn(() => found);
    setTransport(mockTransport({ "WorktreeService.GetWorktreePR": lookup }));
    const hook = renderHook((props) => useWorktreePR(props), { initialProps: { ...initial, visible: false }, wrapper });
    await advance(300_000);
    expect(lookup).not.toHaveBeenCalled();
    hook.rerender(initial); await settle();
    expect(lookup).toHaveBeenCalledTimes(1);
    hook.rerender({ ...initial, visible: false }); await advance(300_000);
    expect(lookup).toHaveBeenCalledTimes(1);
  });

  it("pauses in a hidden browser tab and refreshes when it becomes visible", async () => {
    const lookup = vi.fn(() => found);
    setTransport(mockTransport({ "WorktreeService.GetWorktreePR": lookup }));
    const visibility = vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
    renderHook(() => useWorktreePR(initial), { wrapper }); await settle();
    visibility.mockReturnValue("hidden");
    act(() => document.dispatchEvent(new Event("visibilitychange")));
    await advance(300_000);
    expect(lookup).toHaveBeenCalledTimes(1);
    visibility.mockReturnValue("visible");
    act(() => document.dispatchEvent(new Event("visibilitychange"))); await settle();
    expect(lookup).toHaveBeenCalledTimes(2);
    visibility.mockRestore();
  });

  it("keeps only a same-scope known link on failure and backs off errors", async () => {
    let fail = false;
    const lookup = vi.fn(() => { if (fail) throw new ConnectError("GitHub unavailable", Code.Unavailable); return found; });
    setTransport(mockTransport({ "WorktreeService.GetWorktreePR": lookup }));
    const hook = renderHook((props) => useWorktreePR(props), { initialProps: initial, wrapper });
    await settle(); fail = true;
    act(() => hook.result.current.retry()); await settle();
    expect(hook.result.current.status).toBe("found");
    expect(hook.result.current.error).toContain("GitHub unavailable");
    await advance(60_000);
    expect(lookup).toHaveBeenCalledTimes(2);
    await advance(60_000);
    expect(lookup).toHaveBeenCalledTimes(3);
    hook.rerender({ ...initial, branch: "other" });
    expect(hook.result.current.existing).toBeNull();
    await settle();
    expect(hook.result.current.status).toBe("error");
  });

  it.each(["branch", "repoId", "worktreeId"] as const)("isolates stale in-flight results across %s changes", async (field) => {
    let finish!: (value: typeof found) => void;
    const lookup = vi.fn().mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; })).mockReturnValue({ exists: false });
    setTransport(mockTransport({ "WorktreeService.GetWorktreePR": lookup }));
    const hook = renderHook((props) => useWorktreePR(props), { initialProps: initial, wrapper });
    await settle();
    hook.rerender({ ...initial, [field]: "different" }); await settle();
    expect(hook.result.current.status).toBe("absent");
    await act(async () => { finish(found); }); await settle();
    expect(hook.result.current.existing).toBeNull();
    expect(hook.result.current.status).toBe("absent");
  });

  it("isolates repositories with identical branch names", async () => {
    setTransport(mockTransport({ "WorktreeService.GetWorktreePR": (req: unknown) =>
      (req as { repoId: string }).repoId === "repo-a" ? found : { exists: false } }));
    const a = renderHook(() => useWorktreePR(initial), { wrapper });
    const b = renderHook(() => useWorktreePR({ ...initial, repoId: "repo-b" }), { wrapper });
    await settle();
    expect(a.result.current.status).toBe("found");
    expect(b.result.current.status).toBe("absent");
  });
});
