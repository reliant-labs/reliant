/**
 * A chat's projects as chips (Chat/ChatProjectChips): the primary first and
 * never removable, the attached ones with a ×, and Add project, whose
 * selector leaves out the chat's projects, puts the machine's first and
 * offers the rest as a set-up there. A refusal's set-up is a notice.
 */
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { ProjectInstallState } from "@/gen/reliant/v1/project_pb";
import { seedProjects } from "@/test/projectList";
import { useAuthStore } from "@/store/authStore";
import type { ChatProjectsControl } from "@/hooks/useChatProjects";

vi.mock("@/hooks/project-catalog-queries", () => ({
  useProjectCatalog: () => ({ catalog: undefined, isLoading: false, error: new Error("no catalog here"), refetch: () => {} }),
}));
vi.mock("@/hooks/sidebar-project-queries", () => ({
  useSidebarProjectPrefs: () => ({ prefs: { pinned: new Set(), hidden: new Set(), showAll: false } }),
}));
vi.mock("../../forge-ui/tooltip", () => ({
  default: ({ children }: { children: React.ReactNode }) => <>{children}</>,
}));

import { ChatProjectChips } from "../ChatProjectChips";

const api = { id: "p-api", name: "api", path: "/w/api", last_active: "2025-04-01T00:00:00Z" };
const web = { id: "p-web", name: "web", path: "/w/web", last_active: "2025-03-01T00:00:00Z" };
const temp = { id: "p-temp", name: "temp", path: "/w/temp", last_active: "2025-02-15T00:00:00Z", remote_url: "https://github.com/acme/temp" };
const local = { id: "p-local", name: "local", path: "/w/local", last_active: "2025-02-01T00:00:00Z" };
const notes = { id: "p-notes", name: "notes", path: "/Users/me/notes", last_active: "2025-01-01T00:00:00Z" };

function control(over: Partial<ChatProjectsControl> = {}): ChatProjectsControl {
  return {
    projects: [
      { projectId: "p-api", name: "api", primary: true },
      { projectId: "p-web", name: "web", primary: false },
    ],
    supported: true,
    machine: { daemonId: "d-cicd", name: "cicd" },
    installs: [
      { project_id: "p-local", daemon_id: "d-cicd", install_state: ProjectInstallState.INSTALLED },
      { project_id: "p-temp", daemon_id: "d-laptop", install_state: ProjectInstallState.INSTALLED },
      { project_id: "p-notes", daemon_id: "d-laptop", install_state: ProjectInstallState.INSTALLED },
    ],
    pending: new Set(),
    add: vi.fn(),
    remove: vi.fn(),
    setUp: null,
    confirmSetUp: vi.fn(),
    dismissSetUp: vi.fn(),
    ...over,
  };
}

function renderChips(c: ChatProjectsControl, props: Partial<React.ComponentProps<typeof ChatProjectChips>> = {}) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  seedProjects(client, [api, web, temp, local, notes]);
  return render(
    <QueryClientProvider client={client}>
      <ChatProjectChips control={c} {...props} />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  useAuthStore.setState({ session: { access_token: "t" } as never });
});

describe("ChatProjectChips", () => {
  it("lists the primary first, without a ×, and the attached projects with one", async () => {
    const c = control();
    renderChips(c);
    const chips = screen.getAllByTestId(/^chat-project-chip-/);
    expect(chips.map((chip) => chip.textContent)).toEqual(["api", "web"]);
    expect(within(chips[0]).queryByRole("button", { name: /Remove/ })).toBeNull();
    await userEvent.click(within(chips[1]).getByRole("button", { name: "Remove web from this chat" }));
    expect(c.remove).toHaveBeenCalledWith("p-web");
  });

  it("adds from the selector: the chat's projects left out, the machine's first, the rest set up there", async () => {
    const c = control();
    renderChips(c);
    await userEvent.click(screen.getByTestId("chat-project-add"));
    await screen.findByTestId("project-selector-row-p-local");
    expect(screen.queryByTestId("project-selector-row-p-api")).toBeNull();
    expect(screen.queryByTestId("project-selector-row-p-web")).toBeNull();
    const rows = screen.getAllByTestId(/^project-selector-row-/).map((row) => row.getAttribute("data-testid"));
    expect(rows[0]).toBe("project-selector-row-p-local");
    expect(screen.getByTestId("project-selector-row-p-temp")).toHaveTextContent("Set up on cicd");
    expect(screen.getByTestId("project-selector-row-p-notes")).toHaveAttribute("aria-disabled", "true");
    await userEvent.click(screen.getByTestId("project-selector-row-p-temp"));
    expect(c.add).toHaveBeenCalledWith(expect.objectContaining({ id: "p-temp" }));
  });

  it("offers no add where the chat cannot have more projects", () => {
    renderChips(control({ supported: false, projects: [{ projectId: "p-api", name: "api", primary: true }] }));
    expect(screen.queryByTestId("chat-project-add")).toBeNull();
  });

  it("is only the add control beside another project chip (showPrimary off, one project)", () => {
    renderChips(control({ projects: [{ projectId: "p-api", name: "api", primary: true }] }), {
      showPrimary: false,
      addLabel: "Project",
    });
    expect(screen.queryAllByTestId(/^chat-project-chip-/)).toHaveLength(0);
    expect(screen.getByTestId("chat-project-add")).toHaveTextContent("Project");
  });

  it("says when a project must be set up on the machine first, and sets it up on request", async () => {
    const c = control({
      setUp: { projectId: "p-temp", projectName: "temp", machine: { daemonId: "d-cicd", name: "cicd" }, canClone: true, state: "offer" },
    });
    renderChips(c);
    expect(screen.getByTestId("chat-project-setup")).toHaveTextContent("temp isn't set up on cicd");
    await userEvent.click(screen.getByTestId("chat-project-setup-confirm"));
    expect(c.confirmSetUp).toHaveBeenCalled();
  });
});
