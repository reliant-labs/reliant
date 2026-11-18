import { describe, expect, it } from "vitest";
import { BackgroundProcessStatus } from "../../gen/reliant/v1/common_pb";
import { formatElapsed, processStatusView, shortCommand } from "../processStatus";
import { reaperNote } from "../../components/Processes/reaperNote";

describe("processStatusView", () => {
  // Running is in progress, not success: the styling contract keeps green
  // (success) for a clean exit, so a running process is a pulsing neutral dot.
  it("reads running as an in-progress neutral state", () => {
    expect(processStatusView(BackgroundProcessStatus.RUNNING)).toMatchObject({ label: "Running", tone: "neutral", running: true });
  });

  it("tells a clean exit from a non-zero one", () => {
    expect(processStatusView(BackgroundProcessStatus.COMPLETED, 0)).toMatchObject({ label: "Completed", tone: "success" });
    expect(processStatusView(BackgroundProcessStatus.COMPLETED, 2)).toMatchObject({ label: "Exit 2", tone: "danger" });
    expect(processStatusView(BackgroundProcessStatus.FAILED, 1)).toMatchObject({ label: "Failed (exit 1)", tone: "danger" });
  });

  it("calls a stop a stop, and flags one Reliant did not make", () => {
    expect(processStatusView(BackgroundProcessStatus.KILLED)).toMatchObject({ label: "Stopped", tone: "neutral", running: false });
    expect(processStatusView(BackgroundProcessStatus.KILLED_EXTERNALLY).tone).toBe("warning");
    expect(processStatusView(BackgroundProcessStatus.STALE).tone).toBe("warning");
  });
});

describe("formatElapsed", () => {
  it.each([
    [8_000, "8s"],
    [4 * 60_000, "4m"],
    [72 * 60_000, "1h12m"],
    [72 * 3_600_000, "3d"],
  ])("%d ms reads %s", (ms, want) => {
    expect(formatElapsed(0, ms)).toBe(want);
  });

  it("never goes negative", () => {
    expect(formatElapsed(10_000, 0)).toBe("0s");
  });
});

describe("shortCommand", () => {
  it("names a script or target by itself", () => {
    expect(shortCommand("npm run dev")).toBe("dev");
    expect(shortCommand("make build")).toBe("build");
  });

  it("keeps a whole command's first line, cut to fit", () => {
    expect(shortCommand("go test ./...\necho done")).toBe("go test ./...");
    expect(shortCommand("x".repeat(60), 10)).toBe("xxxxxxxxx…");
  });
});

describe("reaperNote", () => {
  const now = Date.parse("2026-11-17T10:00:00Z");

  it("says a kept-alive process is left running", () => {
    expect(reaperNote({ keep_alive: true }, now)).toMatch(/Kept alive/);
  });

  it("says how long until an idle process is stopped", () => {
    expect(reaperNote({ stops_after: "2026-11-17T14:00:00Z" }, now)).toBe(
      "Stopped if idle for 4h0m more, once the chat is done.",
    );
    expect(reaperNote({ stops_after: "2026-11-17T09:00:00Z" }, now)).toMatch(/may stop it soon/);
  });

  it("says nothing when the machine does not report it", () => {
    expect(reaperNote({}, now)).toBeNull();
  });
});
