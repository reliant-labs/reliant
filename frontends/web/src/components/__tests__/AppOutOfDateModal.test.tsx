import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

const navigate = vi.hoisted(() => vi.fn());
vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => navigate,
}));

import { AppOutOfDateModal } from "../AppOutOfDateModal";

type ElectronLike = { checkForUpdates: () => Promise<unknown> };

afterEach(() => {
  cleanup();
  navigate.mockReset();
  delete (window as unknown as { electronAPI?: ElectronLike }).electronAPI;
  vi.unstubAllGlobals();
});

const data = { procedure: "reliant.v1.ChatService/CreateChat" };

describe("AppOutOfDateModal", () => {
  it("sends the desktop app to its updater", () => {
    const checkForUpdates = vi.fn(async () => ({}));
    (window as unknown as { electronAPI: ElectronLike }).electronAPI = { checkForUpdates };
    const onClose = vi.fn();
    render(<AppOutOfDateModal isOpen onClose={onClose} data={data} />);

    expect(screen.getByText("Update Reliant to continue")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Check for updates" }));

    expect(onClose).toHaveBeenCalled();
    expect(checkForUpdates).toHaveBeenCalledTimes(1);
    expect(navigate).toHaveBeenCalledWith({ to: "/settings/$section", params: { section: "about" } });
  });

  it("reloads a browser tab onto the current bundle", () => {
    const reload = vi.fn();
    vi.stubGlobal("location", { ...window.location, reload });
    render(<AppOutOfDateModal isOpen onClose={vi.fn()} data={data} />);

    expect(screen.getByText("Reliant has been updated")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Reload" }));

    expect(reload).toHaveBeenCalledTimes(1);
    expect(navigate).not.toHaveBeenCalled();
  });
});
