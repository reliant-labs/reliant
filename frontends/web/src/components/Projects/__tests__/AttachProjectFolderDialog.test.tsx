import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";
import { AttachProjectFolderDialog } from "../AttachProjectFolderDialog";
const mutate = vi.hoisted(() => vi.fn());
vi.mock("@/hooks/project-service-hooks_gen", () => ({ useAttachProjectToDaemon: () => ({ mutateAsync: mutate }) }));
vi.mock("@/hooks/useDaemonStatus", () => ({ useDaemonStatus: () => ({ daemons: [{ daemonId: "laptop", name: "Laptop" }] }) }));
vi.mock("../DirectoryPicker", () => ({ DirectoryPicker: (props: { onSelect: (path: string, daemon: string) => void; onClose: () => void; closeOnSelect: boolean }) => <button onClick={() => { props.onSelect("/code", "laptop"); if (props.closeOnSelect) props.onClose(); }}>Pick folder</button> }));
vi.mock("@/lib/toast-manager", () => ({ toast: { success: vi.fn() } }));
beforeEach(() => { mutate.mockReset(); });
it("attaches the selected machine and preserves warnings until acknowledged", async () => {
  mutate.mockResolvedValue({ warnings: ["This folder is not a git checkout."] });
  const close = vi.fn(); const attached = vi.fn();
  render(<AttachProjectFolderDialog open project={{ id: "p", name: "API" }} onClose={close} onAttached={attached} />);
  fireEvent.click(screen.getByText("Pick folder"));
  await screen.findByText("This folder is not a git checkout.");
  expect(mutate).toHaveBeenCalledWith({ projectId: "p", daemonId: "laptop", path: "/code" });
  expect(attached).toHaveBeenCalledWith({ daemonId: "laptop", path: "/code", warnings: ["This folder is not a git checkout."] });
  expect(close).not.toHaveBeenCalled();
  fireEvent.click(screen.getByText("Done")); expect(close).toHaveBeenCalledOnce();
});
it("keeps a refusal readable and lets the user choose another folder", async () => {
  mutate.mockRejectedValue(new Error("Folder belongs to another project"));
  const close = vi.fn();
  render(<AttachProjectFolderDialog open project={{ id: "p", name: "API" }} onClose={close} />);
  fireEvent.click(screen.getByText("Pick folder"));
  await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("Folder belongs to another project"));
  expect(close).not.toHaveBeenCalled();
  fireEvent.click(screen.getByText("Choose another folder")); expect(screen.getByText("Pick folder")).toBeInTheDocument();
});
