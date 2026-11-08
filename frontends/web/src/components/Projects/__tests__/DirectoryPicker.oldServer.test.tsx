import { fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { expect, it, vi } from "vitest";
import { DirectoryPicker } from "../DirectoryPicker";

// A server older than ListDirectoryRequest.daemon_id ignores the machine the
// picker names and lists (and would mkdir on) the default machine, echoing no
// daemonId. Its listing must not be shown as the chosen machine's, and no
// folder may be made or chosen through it.
const calls = vi.hoisted(() => ({ create: vi.fn() }));
vi.mock("@/hooks/useDaemonStatus", () => ({ useDaemonStatus: () => ({ daemons: [
  { daemonId: "a", hostname: "Alpha", status: 1, isDefault: true },
  { daemonId: "b", hostname: "Beta", status: 1 },
] }) }));
vi.mock("@/hooks/file-system-service-hooks_gen", () => ({
  useListDirectory: () => ({
    data: { path: "/alpha-home", entries: [{ name: "alpha-only", path: "/alpha-home/alpha-only", isDirectory: true }] },
    isFetching: false,
    isPlaceholderData: false,
  }),
  useCreateDirectory: () => ({ mutateAsync: calls.create, isPending: false }),
}));

it("refuses to browse, mkdir or choose on a named machine an older server ignored", () => {
  const select = vi.fn();
  render(<QueryClientProvider client={new QueryClient()}><DirectoryPicker isOpen daemonId="b" onClose={() => {}} onSelect={select} /></QueryClientProvider>);
  expect(screen.getByRole("alert").textContent).toContain("cannot browse Beta");
  expect(screen.queryByText("alpha-only")).toBeNull();
  expect(screen.queryByText("/alpha-home")).toBeNull();
  const newFolder = screen.getByText("New folder").closest("button")!;
  expect(newFolder.disabled).toBe(true);
  fireEvent.click(newFolder);
  expect(screen.queryByPlaceholderText("New folder name")).toBeNull();
  expect(calls.create).not.toHaveBeenCalled();
  const use = screen.getByText("Use this folder").closest("button")!;
  expect(use.disabled).toBe(true);
  fireEvent.click(use);
  expect(select).not.toHaveBeenCalled();
});
