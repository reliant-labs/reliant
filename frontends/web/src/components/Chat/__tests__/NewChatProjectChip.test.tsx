/**
 * The new chat's project chip: it names the project the chat will start in
 * and changes it inline (to that project's main workspace), or adds one.
 */
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { seedProjects } from "@/test/projectList";
import { useAuthStore } from "@/store/authStore";
import { useAddProjectDialogStore } from "@/store/addProjectDialogStore";
import { useProjectStore, type Project } from "@/store/projectStore";

const openNewChat = vi.hoisted(() => vi.fn(async () => {}));
vi.mock("@/store/openChat", () => ({ openNewChat }));
vi.mock("../../forge-ui/tooltip", () => ({
  default: ({ children }: { children: React.ReactNode }) => <>{children}</>,
}));

import { NewChatProjectChip } from "../NewChatProjectChip";

const alpha = { id: "p-alpha", name: "alpha", path: "/a", last_active: "2025-01-01T00:00:00Z" };
const beta = { id: "p-beta", name: "beta", path: "/b", last_active: "2025-02-01T00:00:00Z" };

function renderChip() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  seedProjects(client, [alpha, beta]);
  return render(
    <QueryClientProvider client={client}>
      <NewChatProjectChip />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  openNewChat.mockClear();
  useAuthStore.setState({ session: { access_token: "t" } as never });
  useProjectStore.setState({ currentProject: alpha as unknown as Project });
  useAddProjectDialogStore.setState({ isOpen: false, method: null });
});

describe("NewChatProjectChip", () => {
  it("names the project the new chat starts in", () => {
    renderChip();
    expect(screen.getByTestId("new-chat-project-chip")).toHaveTextContent("alpha");
  });

  it("moves the new chat to another project", async () => {
    renderChip();
    await userEvent.click(screen.getByTestId("new-chat-project-chip"));
    await userEvent.click(await screen.findByTestId("new-chat-project-p-beta"));
    expect(openNewChat).toHaveBeenCalledWith({ projectId: "p-beta" });
  });

  it("does nothing when the same project is picked", async () => {
    renderChip();
    await userEvent.click(screen.getByTestId("new-chat-project-chip"));
    await userEvent.click(await screen.findByTestId("new-chat-project-p-alpha"));
    expect(openNewChat).not.toHaveBeenCalled();
  });

  it("offers Add project", async () => {
    renderChip();
    await userEvent.click(screen.getByTestId("new-chat-project-chip"));
    await userEvent.click(await screen.findByTestId("new-chat-project-add"));
    expect(useAddProjectDialogStore.getState().isOpen).toBe(true);
  });
});
