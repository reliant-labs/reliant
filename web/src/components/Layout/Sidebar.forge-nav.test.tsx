/**
 * The Forge sidebar entry is gated on the EXPERIMENTAL FLAG ONLY — never on
 * whether the current project is a forge project.
 *
 * WHY IT IS NOT ALSO GATED ON THE PROJECT. Forge is a top-level feature, so
 * the entry sits beside New chat / Projects / Workflows and stays PUT. A nav
 * item that appears and disappears as you switch projects is harder to learn
 * than one that is always there, and "is this a forge project?" is a question
 * the destination answers better than the nav can: clicking it in an ordinary
 * project lands on the NotForgeProject screen, which says so in words.
 *
 * It also removes a failure mode that actually shipped. The old gate required
 * a LIVE GetTopology call to succeed, so when that RPC broke on a report-type
 * mismatch the entire feature silently vanished — a missing nav entry is
 * indistinguishable from a feature nobody built, and nothing in the UI said
 * otherwise. A nav entry whose visibility depends on an RPC is a nav entry
 * that a bug can delete.
 *
 * `Project.is_forge` is irrelevant here now, and these cases pin that: it is
 * populated at create time and never recomputed (control-plane's row carries
 * is_forge=false while its forge.yaml exists), so neither value may affect
 * the entry either way.
 */
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ButtonHTMLAttributes, ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { Sidebar } from "./Sidebar";
import { ChatActivity } from "../../gen/reliant/v1/chat_pb";
import { useChatStore } from "../../store/chatStore";
import { useActivityStore } from "../../store/activityStore";
import { useWorktreeStore } from "../../store/worktreeStore";
import { useProcessStore } from "../../store/processStore";
import { useProjectStore } from "../../store/projectStore";
import { useChatListPreferencesStore } from "../../store/chatListPreferencesStore";

vi.mock("../ui/Tooltip", () => ({
  Tooltip: ({ children }: { children: ReactNode }) => <>{children}</>,
}));

vi.mock("../ui/Button", () => ({
  Button: ({ children, ...props }: ButtonHTMLAttributes<HTMLButtonElement>) => (
    <button {...props}>{children}</button>
  ),
}));

vi.mock("../ui/ContextMenu", () => ({ ContextMenu: () => null }));
vi.mock("../ui/ActivityDot", () => ({ ActivityDot: () => <div /> }));
vi.mock("../../hooks/useDebounce", () => ({ useDebounce: <T,>(value: T) => value }));

vi.mock("../../hooks/chat-queries", () => ({
  useChatList: () => ({ data: [], isLoading: false }),
  useArchivedChats: () => ({ data: [], isFetched: true }),
  useDeleteChat: () => ({ mutateAsync: vi.fn() }),
  useRenameChat: () => ({ mutateAsync: vi.fn() }),
  useUnarchiveChat: () => ({ mutateAsync: vi.fn() }),
}));

vi.mock("../../hooks/message-queries", () => ({
  useMarkUnread: () => ({ mutateAsync: vi.fn() }),
}));

// The gate under test. Mocked at the hook boundary so each case can state the
// LIVE verdict directly, which is the thing the entry must follow.
// The EXPERIMENTAL gate, independent of the is-this-a-forge-project question.
// Mocked separately because the two conditions are independent and the entry
// requires BOTH — a test that could only vary one of them would pass while the
// other was ignored. Default true here so the pre-existing cases below exercise
// the forge-project logic they were written for.
const experimentalGate = vi.hoisted(() => ({ enabled: true }));

vi.mock("../../lib/forgeFeature", () => ({
  isForgeUIEnabled: () => experimentalGate.enabled,
}));

function renderSidebar() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={queryClient}>
      <Sidebar onOpenForge={vi.fn()} />
    </QueryClientProvider>
  );
}

describe("Sidebar forge nav entry", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    experimentalGate.enabled = true;

    Object.defineProperty(HTMLElement.prototype, "scrollIntoView", {
      configurable: true,
      value: vi.fn(),
    });

    useChatStore.setState({
      chats: new Map(),
      activeChatId: null,
      selectChat: vi.fn(),
    } as Partial<ReturnType<typeof useChatStore.getState>>);

    useActivityStore.setState({
      activities: new Map<string, ChatActivity>(),
    } as Partial<ReturnType<typeof useActivityStore.getState>>);

    useWorktreeStore.setState({
      worktrees: [],
      currentWorktree: null,
      switchWorktreeContext: vi.fn(async () => undefined),
    } as Partial<ReturnType<typeof useWorktreeStore.getState>>);

    useProcessStore.setState({
      fetchProcesses: vi.fn(),
    } as Partial<ReturnType<typeof useProcessStore.getState>>);

    useChatListPreferencesStore.setState({
      sortOrder: "recent_activity",
      viewMode: "grouped",
      filters: {},
      setSortOrder: vi.fn(),
      setViewMode: vi.fn(),
      setFilters: vi.fn(),
      resetFilters: vi.fn(),
      resetAll: vi.fn(),
    });
  });

  /**
   * setProject writes a project row carrying an explicit `is_forge`. The value is
   * deliberately independent of the live gate, so a test can put the two in
   * conflict — which is the real-world state for control-plane.
   */
  function setProject(isForge: boolean) {
    useProjectStore.setState({
      currentProject: {
        id: "project-1",
        name: "control-plane",
        path: "/tmp/project",
        is_git_repo: true,
        is_forge: isForge,
        worktree_count: 1,
        created_at: "2024-01-01T00:00:00.000Z",
        updated_at: "2024-01-01T00:00:00.000Z",
        last_active: "2024-01-01T00:00:00.000Z",
      },
    } as unknown as Partial<ReturnType<typeof useProjectStore.getState>>);
  }

  it("shows the entry whenever the experimental flag is on", () => {
    setProject(true);
    renderSidebar();
    expect(screen.getByTestId("sidebar-forge-button")).toBeInTheDocument();
  });

  // THE REVERSAL. An ordinary project still gets the entry; the destination
  // explains itself rather than the nav hiding the feature.
  it("shows the entry for a project that is NOT a forge project", () => {
    setProject(false);
    renderSidebar();
    expect(screen.getByTestId("sidebar-forge-button")).toBeInTheDocument();
  });

  // is_forge is stale by construction — populated at create time, never
  // recomputed — so neither value may move the entry.
  it.each([true, false])("ignores Project.is_forge = %s", (isForge) => {
    setProject(isForge);
    renderSidebar();
    expect(screen.getByTestId("sidebar-forge-button")).toBeInTheDocument();
  });

  // The flag is the ONLY gate, so turning it off must remove the entry
  // completely — a packaged build with no opt-in behaves as it did before the
  // feature existed.
  it("hides the entry when the experimental gate is off", () => {
    experimentalGate.enabled = false;
    setProject(true);
    renderSidebar();
    expect(screen.queryByTestId("sidebar-forge-button")).not.toBeInTheDocument();
  });

  // Forge is promoted, so it sits directly under New chat and ABOVE Projects.
  // Pinned because the position is the product decision, not an accident of
  // where the JSX was appended.
  it("sits between New chat and Projects", () => {
    setProject(true);
    renderSidebar();
    const labels = screen
      .getByRole("navigation", { name: /chat sidebar navigation/i })
      .querySelectorAll("button");
    const text = Array.from(labels).map((b) => b.textContent?.trim());
    expect(text.slice(0, 3)).toEqual(["New chat", "Forge", "Projects"]);
  });
});
