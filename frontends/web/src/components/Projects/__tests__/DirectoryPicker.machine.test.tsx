import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { expect, it, vi } from "vitest";
import { DirectoryPicker } from "../DirectoryPicker";
const calls = vi.hoisted(() => ({ list: vi.fn(), create: vi.fn() }));
vi.mock("@/hooks/useDaemonStatus", () => ({ useDaemonStatus: () => ({ daemons: [{ daemonId: "a", hostname: "Alpha", status: 1 }, { daemonId: "b", hostname: "Beta", status: 1 }] }) }));
vi.mock("@/hooks/file-system-service-hooks_gen", () => ({
  useListDirectory: (req: { daemonId: string }) => { calls.list(req); return { data: { path: req.daemonId === "b" ? "/beta" : "/alpha", entries: [] }, isFetching: false, isPlaceholderData: false }; },
  useCreateDirectory: () => ({ mutateAsync: calls.create, isPending: false }),
}));
it("switches disks, creates on the selected machine, and returns that machine", async () => {
  const select = vi.fn();
  render(<QueryClientProvider client={new QueryClient()}><DirectoryPicker isOpen daemonId="a" onClose={() => {}} onSelect={select} /></QueryClientProvider>);
  fireEvent.change(screen.getByLabelText("Machine"), { target: { value: "b" } });
  await waitFor(() => expect(calls.list).toHaveBeenLastCalledWith({ path: "", daemonId: "b" }));
  fireEvent.click(screen.getByText("New folder"));
  fireEvent.change(screen.getByPlaceholderText("New folder name"), { target: { value: "code" } });
  fireEvent.click(screen.getByText("Create"));
  await waitFor(() => expect(calls.create).toHaveBeenCalledWith({ path: "/beta/code", daemonId: "b" }));
  fireEvent.click(screen.getByText("Use this folder")); expect(select).toHaveBeenCalledWith("/beta", "b");
});
