import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, expect, it, vi } from "vitest";
import { DirectoryPicker } from "../DirectoryPicker";

const listing = { data: { path: "/code", entries: [] }, isFetching: false, isPlaceholderData: false };
vi.mock("@/hooks/useDaemonStatus", () => ({ useDaemonStatus: () => ({ daemons: [
  { daemonId: "remote", hostname: "Remote default", status: 1, isDefault: true },
  { daemonId: "local", hostname: "This computer", status: 1 },
] }) }));
vi.mock("@/hooks/file-system-service-hooks_gen", () => ({
  useListDirectory: () => listing,
  useCreateDirectory: () => ({ mutateAsync: vi.fn(), isPending: false }),
}));
afterEach(() => vi.unstubAllGlobals());

function picker(daemonId = "local", isOpen = true, onSelect = vi.fn()) {
  return <QueryClientProvider client={new QueryClient()}><DirectoryPicker isOpen={isOpen} daemonId={daemonId} onClose={() => {}} onSelect={onSelect} /></QueryClientProvider>;
}

it("offers the native chooser only on the desktop identity, not the remote default", async () => {
  const selectDirectory = vi.fn().mockResolvedValue("/native/code");
  const getLocalDaemonId = vi.fn().mockResolvedValue("local");
  vi.stubGlobal("electronAPI", { selectDirectory, getLocalDaemonId });
  const onSelect = vi.fn();
  render(picker("local", true, onSelect));
  fireEvent.click(await screen.findByText("Use system dialog…"));
  await waitFor(() => expect(onSelect).toHaveBeenCalledWith("/native/code", "local"));
  fireEvent.change(screen.getByLabelText("Machine"), { target: { value: "remote" } });
  expect(screen.queryByText("Use system dialog…")).toBeNull();
  expect(selectDirectory).toHaveBeenCalledTimes(1);
});

it.each(["remote", "local"])("does not guess locality for %s on older Electron", async (daemon) => {
  const selectDirectory = vi.fn();
  vi.stubGlobal("electronAPI", { selectDirectory });
  render(picker(daemon));
  await screen.findByText("Use this folder");
  expect(screen.queryByText("Use system dialog…")).toBeNull();
  expect(selectDirectory).not.toHaveBeenCalled();
});

it.each([null, "remote"])("hides the native chooser for unavailable or mismatched identity %s", async (id) => {
  const getLocalDaemonId = vi.fn().mockResolvedValue(id);
  vi.stubGlobal("electronAPI", { selectDirectory: vi.fn(), getLocalDaemonId });
  render(picker());
  await waitFor(() => expect(getLocalDaemonId).toHaveBeenCalled());
  expect(screen.queryByText("Use system dialog…")).toBeNull();
});

it("re-reads an identity that was unavailable when the mounted picker first opened", async () => {
  const getLocalDaemonId = vi.fn().mockResolvedValue(null);
  vi.stubGlobal("electronAPI", { selectDirectory: vi.fn(), getLocalDaemonId });
  const view = render(picker());
  await waitFor(() => expect(getLocalDaemonId).toHaveBeenCalledTimes(1));
  view.rerender(picker("local", false));
  getLocalDaemonId.mockResolvedValue("local");
  view.rerender(picker());
  expect(await screen.findByText("Use system dialog…")).toBeInTheDocument();
});

it("does not open the native chooser if the account/server identity changed since render", async () => {
  const getLocalDaemonId = vi.fn().mockResolvedValue("local");
  const selectDirectory = vi.fn();
  vi.stubGlobal("electronAPI", { selectDirectory, getLocalDaemonId });
  render(picker());
  const button = await screen.findByText("Use system dialog…");
  getLocalDaemonId.mockResolvedValue(null);
  fireEvent.click(button);
  await waitFor(() => expect(getLocalDaemonId).toHaveBeenCalledTimes(2));
  expect(selectDirectory).not.toHaveBeenCalled();
});

it("discards a native folder if the identity changes while the dialog is open", async () => {
  const getLocalDaemonId = vi.fn().mockResolvedValue("local");
  const selectDirectory = vi.fn().mockImplementation(async () => {
    getLocalDaemonId.mockResolvedValue("other-account");
    return "/native/code";
  });
  vi.stubGlobal("electronAPI", { selectDirectory, getLocalDaemonId });
  const onSelect = vi.fn();
  render(picker("local", true, onSelect));
  fireEvent.click(await screen.findByText("Use system dialog…"));
  await waitFor(() => expect(getLocalDaemonId).toHaveBeenCalledTimes(3));
  expect(onSelect).not.toHaveBeenCalled();
});
