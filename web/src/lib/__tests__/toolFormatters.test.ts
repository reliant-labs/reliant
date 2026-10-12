import { describe, it, expect } from "vitest";
import {
  formatShellParams,
  formatWaitForMachineParams,
  getToolCategory,
  TOOL_FORMATTERS,
  ToolCategory,
} from "../toolFormatters";

describe("formatShellParams", () => {
  // The collapsed tool row shows `summary`, so description-over-command is the
  // whole point: prose reads better than a command line at a glance. The exact
  // command must still survive in structured/fullText for the expanded view.
  it("prefers the model-authored description for the collapsed summary", () => {
    const result = formatShellParams({
      command: "rg -n foo .",
      description: "Search for foo",
    });

    expect(result.summary).toBe("Search for foo");
    expect(result.structured?.command).toBe("rg -n foo .");
    expect(result.structured?.description).toBe("Search for foo");
    expect(result.fullText).toBe("shell(rg -n foo .)");
  });

  it("falls back to the command when no description is given", () => {
    const result = formatShellParams({ command: "ls -la" });

    expect(result.summary).toBe("ls -la");
    expect(result.structured?.description).toBeUndefined();
  });

  it("treats a whitespace-only description as absent", () => {
    expect(formatShellParams({ command: "ls", description: "   " }).summary).toBe("ls");
  });

  it("handles a bare string input", () => {
    expect(formatShellParams("echo hi").summary).toBe("echo hi");
  });

  it("returns an empty summary when there is no command", () => {
    expect(formatShellParams({ description: "orphan" }).summary).toBe("");
  });
});

describe("wait_for_machine", () => {
  it("is an execution tool with a registered formatter", () => {
    expect(getToolCategory("wait_for_machine")).toBe(ToolCategory.EXECUTION);
    expect(TOOL_FORMATTERS.wait_for_machine).toBe(formatWaitForMachineParams);
  });

  it("reads as a wait on the machine and surfaces the timeout", () => {
    expect(formatWaitForMachineParams({ timeout_seconds: 120 })).toEqual({
      summary: "machine",
      fullText: "wait_for_machine(machine)",
      structured: { timeoutSeconds: 120 },
    });
  });

  it("handles missing or non-object input", () => {
    expect(formatWaitForMachineParams({}).structured).toEqual({ timeoutSeconds: undefined });
    expect(formatWaitForMachineParams(null as never).summary).toBe("machine");
  });
});
