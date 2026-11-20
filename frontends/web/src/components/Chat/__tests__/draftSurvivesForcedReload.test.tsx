/**
 * The forced upgrade prompt reloads the tab. A composer draft must survive it,
 * including the last keystrokes, which the composer saves only after a 300ms
 * debounce (useChatInputState): the prompt flushes them before it reloads.
 *
 * "After the reload" is a fresh module graph rehydrating from localStorage,
 * which is all a reload keeps.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, renderHook, screen } from "@testing-library/react";

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
  localStorage.clear();
  vi.resetModules();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("a composer draft across the forced reload", () => {
  it("is written before the reload and restored after it", async () => {
    const { useProjectStore } = await import("../../../store/projectStore");
    const { useChatInputState } = await import("../useChatInputState");
    const { AppOutOfDateModal } = await import("../../AppOutOfDateModal");
    useProjectStore.setState({ currentProject: { id: PROJECT } as never });

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

    // The reload: a new page rehydrates from storage alone.
    cleanup();
    vi.resetModules();
    const { useWorkspaceStateStore } = await import("../../../store/workspaceStateStore");
    expect(useWorkspaceStateStore.getState().getNewChatDraft(PROJECT)).toBe(DRAFT);
  });

  it("is also flushed when the page is hidden for any other reload or close", async () => {
    const { useProjectStore } = await import("../../../store/projectStore");
    const { useChatInputState } = await import("../useChatInputState");
    useProjectStore.setState({ currentProject: { id: PROJECT } as never });

    const { result } = renderHook(() => useChatInputState({}));
    act(() => result.current.setInput(DRAFT));
    window.dispatchEvent(new Event("pagehide"));

    expect(localStorage.getItem("workspace-state")).toContain(DRAFT);
  });
});
