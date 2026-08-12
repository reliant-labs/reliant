import { describe, it, expect } from "vitest";
import { formatShellParams } from "../toolFormatters";

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
