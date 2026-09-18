/**
 * The Forge sidebar entry is gated on a LIVE forge.yaml check, never on
 * `Project.is_forge`.
 *
 * WHY THIS TEST EXISTS. `Project.is_forge` is populated at clone /
 * project-create time and never recomputed on read (the proto field comment says
 * so). Verified against the real dev database: the `control-plane` project row
 * carries is_forge = false while control-plane/forge.yaml exists on disk. So
 * gating the nav entry on that column would hide the forge screens from the very
 * project they were built against — and the failure is SILENT, because a missing
 * nav entry is indistinguishable from a feature nobody built.
 *
 * The authority is `ForgeReportMeta.isForgeProject`, which the daemon derives by
 * statting forge.yaml at request time, surfaced through useForgeProject. These
 * cases pin that the entry follows the live answer and, specifically, that a
 * stale-false `is_forge` on the project row does NOT suppress it.
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
const forgeGate = vi.hoisted(() => ({
  value: { isForgeProject: false, isDetermined: false },
}));

vi.mock("../../hooks/useForgeProject", () => ({
  useForgeProject: () => forgeGate.value,
}));

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
    forgeGate.value = { isForgeProject: false, isDetermined: false };
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

  it("shows the entry when the daemon confirms a forge project", () => {
    setProject(true);
    forgeGate.value = { isForgeProject: true, isDetermined: true };

    renderSidebar();

    expect(screen.getByTestId("sidebar-forge-button")).toBeInTheDocument();
  });

  it("hides the entry for an ordinary non-forge project", () => {
    setProject(false);
    forgeGate.value = { isForgeProject: false, isDetermined: true };

    renderSidebar();

    expect(screen.queryByTestId("sidebar-forge-button")).not.toBeInTheDocument();
  });

  /**
   * THE REGRESSION THIS FILE IS FOR. control-plane's row says is_forge = false
   * while forge.yaml exists, so the live check says yes. The entry must follow
   * the live check. A future change that reads Project.is_forge to decide fails
   * here.
   */
  it("shows the entry when the row's is_forge is stale-false but forge.yaml exists", () => {
    setProject(false);
    forgeGate.value = { isForgeProject: true, isDetermined: true };

    renderSidebar();

    expect(screen.getByTestId("sidebar-forge-button")).toBeInTheDocument();
  });

  /**
   * The inverse skew: a row claiming is_forge = true for a project whose
   * forge.yaml is gone (deleted, or a path that moved). Trusting the column here
   * would offer screens with nothing behind them.
   */
  it("hides the entry when the row claims is_forge but forge.yaml is absent", () => {
    setProject(true);
    forgeGate.value = { isForgeProject: false, isDetermined: true };

    renderSidebar();

    expect(screen.queryByTestId("sidebar-forge-button")).not.toBeInTheDocument();
  });

  it("does not show the entry before the live answer arrives", () => {
    setProject(true);
    forgeGate.value = { isForgeProject: false, isDetermined: false };

    renderSidebar();

    expect(screen.queryByTestId("sidebar-forge-button")).not.toBeInTheDocument();
  });

  /**
   * THE EXPERIMENTAL GATE. While the forge UI is unreleased it is off in a
   * packaged build, and then the entry must not exist EVEN FOR a confirmed forge
   * project — the strongest possible "yes" on the other condition.
   *
   * This is the case that would regress if someone removed the gate from the
   * sidebar and relied on the route guard alone: the nav entry would reappear
   * for every forge project in a shipped build.
   */
  it("hides the entry when the experimental gate is off, even for a forge project", () => {
    setProject(true);
    forgeGate.value = { isForgeProject: true, isDetermined: true };
    experimentalGate.enabled = false;

    renderSidebar();

    expect(screen.queryByTestId("sidebar-forge-button")).not.toBeInTheDocument();
  });

  it("requires BOTH gates — the experimental one alone is not enough", () => {
    setProject(false);
    forgeGate.value = { isForgeProject: false, isDetermined: true };
    experimentalGate.enabled = true;

    renderSidebar();

    expect(screen.queryByTestId("sidebar-forge-button")).not.toBeInTheDocument();
  });
});
