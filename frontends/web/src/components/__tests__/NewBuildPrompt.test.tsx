import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render } from "@testing-library/react";

const toast = vi.hoisted(() => ({ info: vi.fn(), dismiss: vi.fn() }));
vi.mock("sonner", () => ({ toast }));

const RUNNING = vi.hoisted(() => ({
  version: "1.8.2-dev+8bcfc6ee",
  commit: "8bcfc6ee0000000000000000000000000000aaaa",
  ref: "main",
  built: "2026-10-22T10:00:00.000Z",
}));
vi.mock("../../lib/appBuild", () => ({ appBuild: () => RUNNING }));

import { NewBuildPrompt, NEW_BUILD_TOAST_ID } from "../NewBuildPrompt";
import { useModalStore } from "../../store/modalStore";

const NEWER = { ...RUNNING, version: "1.8.2-dev+9c0ffee1", commit: "9c0ffee10000000000000000000000000000bbbb" };

function deployed(body: unknown) {
  vi.stubGlobal("fetch", vi.fn(async () => ({ ok: true, json: async () => body })));
}

/** Mounts the prompt and lets its first check settle. */
async function mount(enabled = true) {
  render(<NewBuildPrompt enabled={enabled} />);
  await act(async () => {
    await new Promise((r) => setTimeout(r, 0));
  });
}

beforeEach(() => {
  useModalStore.getState().closeModal();
});

afterEach(() => {
  cleanup();
  toast.info.mockReset();
  toast.dismiss.mockReset();
  vi.unstubAllGlobals();
});

describe("NewBuildPrompt", () => {
  it("shows nothing while the deployed build is the one running", async () => {
    deployed(RUNNING);
    await mount();
    expect(fetch).toHaveBeenCalled();
    expect(toast.info).not.toHaveBeenCalled();
  });

  it("offers a reload, without forcing one, when a newer build is deployed", async () => {
    const reload = vi.fn();
    vi.stubGlobal("location", { ...window.location, reload });
    deployed(NEWER);
    await mount();

    expect(toast.info).toHaveBeenCalledTimes(1);
    const [title, opts] = toast.info.mock.calls[0];
    expect(title).toBe("A new version of Reliant is available");
    expect(opts).toMatchObject({ id: NEW_BUILD_TOAST_ID, duration: Infinity, action: { label: "Reload" } });
    expect(reload).not.toHaveBeenCalled();

    opts.action.onClick();
    expect(reload).toHaveBeenCalledTimes(1);
  });

  it("shows nothing when the deployed build cannot be fetched", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => { throw new TypeError("Failed to fetch"); }));
    await mount();
    expect(fetch).toHaveBeenCalled();
    expect(toast.info).not.toHaveBeenCalled();
  });

  it("does not watch where it does not apply (desktop, dev server)", async () => {
    deployed(NEWER);
    await mount(false);
    expect(fetch).not.toHaveBeenCalled();
    expect(toast.info).not.toHaveBeenCalled();
  });

  it("steps aside for the blocking out-of-date modal", async () => {
    useModalStore.getState().openModal("app-out-of-date", { procedure: "reliant.v1.X/Y" });
    deployed(NEWER);
    await mount();
    expect(toast.info).not.toHaveBeenCalled();
    expect(toast.dismiss).toHaveBeenCalledWith(NEW_BUILD_TOAST_ID);
  });
});
