import { describe, expect, it } from "vitest";

import { machineDisplayName, machineDisplayNameOr } from "../machineName";

describe("machineDisplayName", () => {
  // The owner's new machine on 2026-10-07: named "default", listed as its pod
  // hostname with the doubled ws- prefix.
  it("shows the name the owner gave a managed machine, not its pod hostname", () => {
    expect(
      machineDisplayName({
        daemonId: "2aab1465-f76b-4d95-b61b-480d9ba070e6",
        name: "default",
        hostname: "ws-ws-2aab1465",
        daemonType: "managed",
      }),
    ).toBe("default");
  });

  it("never shows a managed machine's pod hostname, even before its name arrives", () => {
    const label = machineDisplayName({
      daemonId: "2aab1465-f76b-4d95-b61b-480d9ba070e6",
      hostname: "ws-ws-2aab1465",
      daemonType: "managed",
    });
    expect(label).toBe("Cloud machine (2aab1465)");
    expect(label).not.toContain("ws-ws-");
  });

  it("uses a self-hosted machine's real hostname when it has no name", () => {
    expect(
      machineDisplayName({
        daemonId: "38181976-2e7c-47a0-96b8-83f0a9c73d84",
        hostname: "Seans-MacBook-Pro-2.local",
        daemonType: "self_hosted",
      }),
    ).toBe("Seans-MacBook-Pro-2.local");
  });

  it("prefers a self-hosted machine's name over its hostname", () => {
    expect(
      machineDisplayName({ daemonId: "x", name: "laptop", hostname: "host.local", daemonType: "self_hosted" }),
    ).toBe("laptop");
  });

  it("tags a self-hosted machine whose hostname is a placeholder", () => {
    expect(
      machineDisplayName({
        daemonId: "38181976-2e7c-47a0-96b8-83f0a9c73d84",
        hostname: "38181976-2e7c-47a0-96b8-83f0a9c73d84",
        daemonType: "self_hosted",
      }),
    ).toBe("Self-hosted machine (38181976)");
  });

  it("ignores a whitespace-only name", () => {
    expect(machineDisplayName({ daemonId: "abc12345", name: "  ", daemonType: "managed" })).toBe(
      "Cloud machine (abc12345)",
    );
  });

  it("falls back to the caller's phrase for a machine that isn't loaded", () => {
    expect(machineDisplayNameOr(undefined, "your machine")).toBe("your machine");
    expect(machineDisplayNameOr({ daemonId: "a", name: "default" }, "your machine")).toBe("default");
  });
});
