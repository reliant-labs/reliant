/**
 * Machine first, then project: when the user picked the machine themselves,
 * the project chip lists the projects on that machine as choices and every
 * other one as "Set up <project> on <machine>" — the same split the machine
 * picker makes the other way round. A plain folder cannot be cloned, so it
 * is listed but disabled. Nothing here starts a chat that cannot run.
 */
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { ProjectInstallState } from "@/gen/reliant/v1/project_pb";
import { seedProjects } from "@/test/projectList";
import { useAuthStore } from "@/store/authStore";
import { useProjectStore, type Project } from "@/store/projectStore";

const openNewChat = vi.hoisted(() => vi.fn(async () => {}));
const createProjectFromRepo = vi.hoisted(() => vi.fn(async () => ({ queued: true })));
vi.mock("@/store/openChat", () => ({ openNewChat }));
vi.mock("@/hooks/project-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/project-queries")>()),
  createProjectFromRepo,
}));
vi.mock("../../forge-ui/tooltip", () => ({
  default: ({ children }: { children: React.ReactNode }) => <>{children}</>,
}));
vi.mock("sonner", () => ({ toast: { error: vi.fn() } }));

import { NewChatProjectChip } from "../NewChatProjectChip";

const api = { id: "p-api", name: "api", path: "/home/workspace/projects/api", last_active: "2025-03-01T00:00:00Z" };
const temp = {
  id: "p-temp",
  name: "temp",
  path: "/home/workspace/projects/temp",
  last_active: "2025-02-01T00:00:00Z",
  remote_url: "https://github.com/acme/temp",
};
const notes = { id: "p-notes", name: "notes", path: "/Users/me/notes", last_active: "2025-01-01T00:00:00Z" };

const installs = [
  { project_id: "p-api", daemon_id: "d-cicd", install_state: ProjectInstallState.INSTALLED },
  { project_id: "p-temp", daemon_id: "d-laptop", install_state: ProjectInstallState.INSTALLED },
  { project_id: "p-notes", daemon_id: "d-laptop", install_state: ProjectInstallState.INSTALLED },
];

function renderChip(machine?: { daemonId: string; name: string }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  seedProjects(client, [api, temp, notes]);
  return render(
    <QueryClientProvider client={client}>
      <NewChatProjectChip machine={machine} installs={installs} />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  openNewChat.mockClear();
  createProjectFromRepo.mockClear();
  useAuthStore.setState({ session: { access_token: "t" } as never });
  useProjectStore.setState({ currentProject: api as unknown as Project });
});

describe("NewChatProjectChip with a picked machine", () => {
  it("offers the machine's projects as choices, and the rest as set-up actions", async () => {
    renderChip({ daemonId: "d-cicd", name: "cicd" });
    await userEvent.click(screen.getByTestId("new-chat-project-chip"));
    expect(await screen.findByRole("group", { name: "On cicd" })).toHaveTextContent("api");
    expect(screen.getByTestId("new-chat-project-set-up-p-temp")).toHaveTextContent("Set up temp on cicd");
    // A folder project has nothing to clone from: listed, not offered.
    const folder = screen.getByTestId("new-chat-project-set-up-p-notes");
    expect(folder).toHaveTextContent("notes");
    expect(folder).toHaveTextContent("Folder elsewhere");
    expect(folder).toHaveAttribute("aria-disabled", "true");
  });

  it("sets a project up on the machine, then starts the chat in it there", async () => {
    renderChip({ daemonId: "d-cicd", name: "cicd" });
    await userEvent.click(screen.getByTestId("new-chat-project-chip"));
    await userEvent.click(await screen.findByTestId("new-chat-project-set-up-p-temp"));
    expect(createProjectFromRepo).toHaveBeenCalledWith({
      cloneUrl: "https://github.com/acme/temp",
      daemonId: "d-cicd",
      name: "temp",
      path: "/home/workspace/projects/temp",
    });
    expect(openNewChat).toHaveBeenCalledWith({ projectId: "p-temp" });
  });

  it("is the plain project list when the machine is the default one", async () => {
    renderChip(undefined);
    await userEvent.click(screen.getByTestId("new-chat-project-chip"));
    expect(await screen.findByRole("group", { name: "Start in" })).toBeInTheDocument();
    await userEvent.click(screen.getByTestId("new-chat-project-p-temp"));
    expect(openNewChat).toHaveBeenCalledWith({ projectId: "p-temp" });
  });
});
