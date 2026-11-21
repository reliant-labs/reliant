import { afterEach, describe, expect, it, vi } from "vitest";
import { BinaryWriter } from "@bufbuild/protobuf/wire";
import { Code, ConnectError } from "@connectrpc/connect";
import { clientHeaderValue, clientIdentity, isBelowMinimum, upgradeRequiredInfo } from "../clientVersion";

type W = { electronAPI?: unknown; forgeDesktop?: unknown };

afterEach(() => {
  delete (window as unknown as W).electronAPI;
  delete (window as unknown as W).forgeDesktop;
  vi.unstubAllGlobals();
});

describe("isBelowMinimum (semver precedence, the server's rule)", () => {
  it.each([
    ["1.8.9", "1.9.0", true],
    ["1.9.0", "1.9.0", false],
    ["1.10.0", "1.9.0", false],
    ["2.0.0", "1.9.0", false],
    // A dev build of the next patch is a prerelease of it: below that patch,
    // above the release it follows.
    ["1.9.0-dev+8bcfc6ee", "1.9.0", true],
    ["1.9.1-dev+8bcfc6ee", "1.9.0", false],
    // Build metadata never orders.
    ["1.9.0+abc", "1.9.0", false],
    // Prerelease identifiers: numeric before alphanumeric, numerically.
    ["1.9.0-rc.2", "1.9.0-rc.10", true],
    ["1.9.0-alpha", "1.9.0-1", false],
    ["1.9.0-rc", "1.9.0-rc.1", true],
    // A tolerated leading v.
    ["v1.8.0", "1.9.0", true],
  ])("%s below %s: %s", (version, min, below) => {
    expect(isBelowMinimum(version, min)).toBe(below);
  });

  it("never forces what it cannot compare, and an empty floor is no floor", () => {
    expect(isBelowMinimum("unknown", "1.9.0")).toBe(false);
    expect(isBelowMinimum("8bcfc6ee", "1.9.0")).toBe(false);
    expect(isBelowMinimum("1.8.0", "")).toBe(false);
    expect(isBelowMinimum("1.8.0", undefined)).toBe(false);
    expect(isBelowMinimum("1.8.0", "garbage")).toBe(false);
  });
});

describe("clientIdentity", () => {
  it("is web/<baked version> in a browser", () => {
    vi.stubGlobal("__RELIANT_BUILD__", { version: "1.9.2", commit: "abc", ref: "main", built: "" });
    expect(clientIdentity()).toEqual({ kind: "web", version: "1.9.2" });
    expect(clientHeaderValue()).toBe("web/1.9.2");
  });

  it("still names its kind when the build has no version, so the server can tell it apart", () => {
    vi.stubGlobal("__RELIANT_BUILD__", { version: "unknown", commit: "unknown", ref: "unknown", built: "" });
    expect(clientHeaderValue()).toBe("web/unknown");
  });

  it("is desktop/<app version> inside Electron, the version that has to update", () => {
    vi.stubGlobal("__RELIANT_BUILD__", { version: "1.9.1", commit: "abc", ref: "main", built: "" });
    (window as unknown as W).electronAPI = {};
    (window as unknown as W).forgeDesktop = { appVersion: "1.9.2" };
    expect(clientHeaderValue()).toBe("desktop/1.9.2");
  });

  it("never puts a value that is not a version token in the header", () => {
    vi.stubGlobal("__RELIANT_BUILD__", { version: "1.9.2\r\nX-Evil: 1", commit: "", ref: "", built: "" });
    expect(clientHeaderValue()).toBe("web/unknown");
  });
});

describe("upgradeRequiredInfo", () => {
  function errorInfo(reason: string, metadata: Record<string, string>): Uint8Array {
    const w = new BinaryWriter().uint32(10).string(reason).uint32(18).string("reliantlabs.io");
    for (const [k, v] of Object.entries(metadata)) {
      w.uint32(26).bytes(new BinaryWriter().uint32(10).string(k).uint32(18).string(v).finish());
    }
    return w.finish();
  }

  function refused(...details: { type: string; value: Uint8Array }[]): ConnectError {
    const err = new ConnectError(
      "refused",
      Code.FailedPrecondition,
      new Headers({ "x-forge-error-reason": "upgrade_required" }),
    );
    err.details.push(...details);
    return err;
  }

  it("reads the floor from the ErrorInfo detail", () => {
    const err = refused({
      type: "google.rpc.ErrorInfo",
      value: errorInfo("UPGRADE_REQUIRED", {
        client_kind: "web",
        client_version: "1.8.0",
        min_version: "1.9.0",
        upgrade_url: "https://reliantlabs.io/download",
        upgrade_command: "",
      }),
    });
    expect(upgradeRequiredInfo(err)).toEqual({
      minVersion: "1.9.0",
      upgradeUrl: "https://reliantlabs.io/download",
      upgradeCommand: undefined,
    });
  });

  it("is empty for another ErrorInfo, a malformed detail, or none", () => {
    expect(
      upgradeRequiredInfo(refused({ type: "google.rpc.ErrorInfo", value: errorInfo("OTHER", { min_version: "9" }) })),
    ).toEqual({});
    expect(upgradeRequiredInfo(refused({ type: "google.rpc.ErrorInfo", value: new Uint8Array([10, 99]) }))).toEqual({});
    expect(upgradeRequiredInfo(refused())).toEqual({});
    expect(upgradeRequiredInfo(new Error("x"))).toEqual({});
  });
});
