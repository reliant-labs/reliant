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
    expect(navigate).toHaveBeenCalledWith({ to: "/settings/about" });
  });

  describe("forced (the server requires a newer build)", () => {
    const forced = { forced: true, minVersion: "1.9.0" };

    it("cannot be dismissed: no Not now, no close button, and Escape and the backdrop do nothing", () => {
      const onClose = vi.fn();
      render(<AppOutOfDateModal isOpen onClose={onClose} data={forced} />);

      expect(screen.getByText("Reload Reliant to continue")).toBeInTheDocument();
      expect(screen.getByText(/upgrade to the latest version/)).toBeInTheDocument();
      expect(screen.getByText("The oldest supported version is 1.9.0.")).toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Not now" })).toBeNull();
      expect(screen.queryByRole("button", { name: "Close dialog" })).toBeNull();

      fireEvent.keyDown(document, { key: "Escape" });
      const overlay = screen.getByRole("dialog").parentElement!;
      fireEvent.click(overlay);
      expect(onClose).not.toHaveBeenCalled();
    });

    it("flushes pending drafts, then reloads", async () => {
      const { registerDraftFlush } = await import("../../lib/draftFlush");
      const order: string[] = [];
      const unregister = registerDraftFlush(() => order.push("flush"));
      const reload = vi.fn(() => order.push("reload"));
      vi.stubGlobal("location", { ...window.location, reload });
      render(<AppOutOfDateModal isOpen onClose={vi.fn()} data={forced} />);

      fireEvent.click(screen.getByRole("button", { name: "Reload" }));
      unregister();

      expect(order).toEqual(["flush", "reload"]);
    });

    it("keeps the desktop prompt up while it starts the update", () => {
      const checkForUpdates = vi.fn(async () => ({}));
      (window as unknown as { electronAPI: ElectronLike }).electronAPI = { checkForUpdates };
      const onClose = vi.fn();
      render(<AppOutOfDateModal isOpen onClose={onClose} data={forced} />);

      fireEvent.click(screen.getByRole("button", { name: "Update now" }));

      expect(checkForUpdates).toHaveBeenCalledTimes(1);
      expect(onClose).not.toHaveBeenCalled();
    });
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
