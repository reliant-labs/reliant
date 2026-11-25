// yours: scaffolded once, never touched again — forge will not overwrite this file
// Exercise the same navigation contract when the environment's window exposes
// a non-configurable location, as jsdom's own Window does.
import { vi } from "vitest";

const fixedWindow = Object.create(window) as Window & typeof globalThis;
Object.defineProperty(fixedWindow, "location", {
  configurable: false,
  value: window.location,
});
vi.stubGlobal("window", fixedWindow);
vi.resetModules();
await import("./test-setup");
await import("./test-setup.test");
