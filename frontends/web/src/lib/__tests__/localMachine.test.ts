import { afterEach, expect, it, vi } from "vitest";
import { canUseNativeFolderDialog, fetchLocalDaemonId } from "../localMachine";
afterEach(() => vi.unstubAllGlobals());
it("never assumes a self-hosted machine is this computer when desktop exposes no identity", async () => {
  vi.stubGlobal("electronAPI", { selectDirectory: vi.fn() });
  expect(await fetchLocalDaemonId()).toBeNull();
  expect(canUseNativeFolderDialog("laptop", null)).toBe(false);
});
it("returns unknown in the browser or when the desktop identity request fails", async () => {
  vi.stubGlobal("electronAPI", undefined);
  expect(await fetchLocalDaemonId()).toBeNull();
  expect(canUseNativeFolderDialog("local", "local")).toBe(false);
  vi.stubGlobal("electronAPI", { selectDirectory: vi.fn(), getLocalDaemonId: async () => { throw new Error("IPC unavailable"); } });
  expect(await fetchLocalDaemonId()).toBeNull();
});
it("offers the native dialog only for the exact desktop daemon identity", async () => {
  vi.stubGlobal("electronAPI", { selectDirectory: vi.fn(), getLocalDaemonId: async () => "local" });
  const id = await fetchLocalDaemonId();
  expect(canUseNativeFolderDialog("remote", id)).toBe(false);
  expect(canUseNativeFolderDialog("local", id)).toBe(true);
});
