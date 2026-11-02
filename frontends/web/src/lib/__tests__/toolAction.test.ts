import { describe, expect, it } from "vitest";
import { formatToolDuration, toolActionVerb, toolDurationMs } from "../toolAction";

describe("toolActionVerb", () => {
  it("names what a call does, live and done", () => {
    expect(toolActionVerb("edit", "done")).toBe("Edited");
    expect(toolActionVerb("edit", "live")).toBe("Editing");
    expect(toolActionVerb("shell", "done")).toBe("Ran");
    expect(toolActionVerb("view", "live")).toBe("Reading");
    expect(toolActionVerb("mcp__reliant__spawn", "done")).toBe("Spawned");
  });

  it("has no verb for a tool it does not know, so the row keeps its name", () => {
    expect(toolActionVerb("some_custom_tool", "done")).toBeNull();
  });
});

describe("tool durations", () => {
  it("measures between the recorded start and end", () => {
    expect(toolDurationMs("2026-11-01T10:00:00.000Z", "2026-11-01T10:00:04.100Z")).toBe(4100);
    expect(toolDurationMs("1700000000", "1700000003")).toBe(3000);
  });

  it("is unknown without both ends, or when the clock ran backwards", () => {
    expect(toolDurationMs(undefined, "2026-11-01T10:00:04Z")).toBeNull();
    expect(toolDurationMs("2026-11-01T10:00:04Z", "2026-11-01T10:00:00Z")).toBeNull();
    expect(toolDurationMs("not a time", "2026-11-01T10:00:00Z")).toBeNull();
  });

  it("formats short and long durations for a row", () => {
    expect(formatToolDuration(320)).toBe("320ms");
    expect(formatToolDuration(4100)).toBe("4.1s");
    expect(formatToolDuration(42_000)).toBe("42s");
    expect(formatToolDuration(65_000)).toBe("1m 05s");
    expect(formatToolDuration(3_720_000)).toBe("1h 02m");
  });
});
