/**
 * The forced upgrade prompt reloads the tab. A composer draft must survive it,
 * including the last keystrokes, which the composer saves only after a 300ms
 * debounce (useChatInputState): the prompt flushes them before it reloads.
 *
 * "After the reload" is a fresh workspace store module rehydrating from
 * localStorage, which is all a reload keeps.
 *
 * The modules load once, at collection, not inside a test: importing this
 * graph for the first time (projectStore reaches most of the app's stores)
 * takes seconds on a loaded runner, and a test that imported it after
 * vi.resetModules() spent its 5s timeout loading code. The stores are reset
 * between tests instead. The debounce timer is faked, so it cannot
 * fire mid-test however slowly the test runs: only the flush can have saved
 * the draft.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, renderHook, screen } from "@testing-library/react";
import { useProjectStore } from "../../../store/projectStore";
import { useWorkspaceStateStore } from "../../../store/workspaceStateStore";
import { useChatInputState } from "../useChatInputState";
import { AppOutOfDateModal } from "../../AppOutOfDateModal";

vi.mock("@tanstack/react-router", () => ({ useNavigate: () => vi.fn() }));
vi.mock("../../../hooks/chat-queries", () => ({
  useChat: () => ({ data: undefined }),
  seedChatDetail: vi.fn(),
  patchChatCaches: vi.fn(),
}));
vi.mock("../../../hooks/settings-queries", () => ({ useDefaultWorkflow: () => "default" }));

const PROJECT = "project-1";
const DRAFT = "half a thought about the migration";

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  useWorkspaceStateStore.setState(useWorkspaceStateStore.getInitialState(), true);
  // After the reset, which persist writes through to storage.
  localStorage.clear();
  useProjectStore.setState({ currentProject: { id: PROJECT } as never });
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("a composer draft across the forced reload", () => {
  it("is written before the reload and restored after it", async () => {
    const { result } = renderHook(() => useChatInputState({}));
    act(() => result.current.setInput(DRAFT));
    // Still inside the debounce: nothing on disk yet. A plain reload now
    // would have lost it.
    expect(localStorage.getItem("workspace-state") ?? "").not.toContain(DRAFT);

    const reload = vi.fn();
    vi.stubGlobal("location", { ...window.location, reload });
    render(<AppOutOfDateModal isOpen onClose={vi.fn()} data={{ forced: true }} />);
    fireEvent.click(screen.getByRole("button", { name: "Reload" }));

    expect(reload).toHaveBeenCalledTimes(1);
    expect(localStorage.getItem("workspace-state")).toContain(DRAFT);

    // The reload: a new page rehydrates from storage alone. Only the store
    // module is fresh; it was transformed at collection, so this re-runs a
    // small module rather than loading a graph.
    cleanup();
    vi.resetModules();
    const fresh = await import("../../../store/workspaceStateStore");
    expect(fresh.useWorkspaceStateStore).not.toBe(useWorkspaceStateStore);
    expect(fresh.useWorkspaceStateStore.getState().getNewChatDraft(PROJECT)).toBe(DRAFT);
  });

  it("is also flushed when the page is hidden for any other reload or close", () => {
    const { result } = renderHook(() => useChatInputState({}));
    act(() => result.current.setInput(DRAFT));
    expect(localStorage.getItem("workspace-state") ?? "").not.toContain(DRAFT);

    window.dispatchEvent(new Event("pagehide"));

    expect(localStorage.getItem("workspace-state")).toContain(DRAFT);
  });
});
