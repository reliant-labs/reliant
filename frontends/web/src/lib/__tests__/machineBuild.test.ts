// Copyright (c) 2025 Reliant Labs
import { describe, expect, it } from "vitest";
import { DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";
import { machineBuildLabel, UNKNOWN_BUILD_LABEL } from "../machineBuild";

describe("machineBuildLabel", () => {
  it("shows the reported version", () => {
    expect(machineBuildLabel({ status: DaemonStatus.ACTIVE, version: "1.8.3-dev+b675d224", commit: "b675d22417c3" })).toBe(
      "1.8.3-dev+b675d224",
    );
  });

  it("falls back to a short commit when no version was reported", () => {
    expect(machineBuildLabel({ status: DaemonStatus.ACTIVE, version: "", commit: "b675d22417c3deadbeef" })).toBe("b675d224");
  });

  it("calls an attached daemon that reported nothing an unknown build", () => {
    expect(machineBuildLabel({ status: DaemonStatus.ACTIVE, version: "", commit: "" })).toBe(UNKNOWN_BUILD_LABEL);
    expect(UNKNOWN_BUILD_LABEL).toBe("unknown build");
  });

  it("says nothing for a machine that is not attached", () => {
    for (const status of [DaemonStatus.DISCONNECTED, DaemonStatus.SUSPENDED, DaemonStatus.PENDING, DaemonStatus.FAILED]) {
      expect(machineBuildLabel({ status, version: "1.8.3", commit: "abc" })).toBeNull();
    }
  });
});
