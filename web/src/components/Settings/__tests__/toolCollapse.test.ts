import { describe, it, expect } from "vitest";
import {
  getToolCategory,
  shouldToolBeCollapsed,
  DEFAULT_TOOL_COLLAPSE_SETTINGS,
} from "../ToolCallSettings";

describe("tool collapse defaults", () => {
  it("collapses bash", () => {
    expect(getToolCategory("bash")).toBe("execution");
    expect(shouldToolBeCollapsed("bash")).toBe(true);
  });

  it("collapses spawn, including the MCP-prefixed name the agent actually emits", () => {
    expect(getToolCategory("spawn")).toBe("agent");
    expect(shouldToolBeCollapsed("spawn")).toBe(true);
    expect(shouldToolBeCollapsed("mcp__reliant__spawn")).toBe(true);
  });

  it("keeps file edits expanded — the diff is the point of the call", () => {
    expect(getToolCategory("edit")).toBe("fileWrite");
    expect(shouldToolBeCollapsed("edit")).toBe(false);
    expect(shouldToolBeCollapsed("write")).toBe(false);
  });

  it("collapses reads and searches", () => {
    expect(shouldToolBeCollapsed("view")).toBe(true);
    expect(shouldToolBeCollapsed("grep")).toBe(true);
  });

  it("collapses unknown tools by default", () => {
    expect(getToolCategory("some_new_tool")).toBeNull();
    expect(shouldToolBeCollapsed("some_new_tool")).toBe(true);
  });

  it("only file edits are expanded by default", () => {
    const expanded = Object.entries(DEFAULT_TOOL_COLLAPSE_SETTINGS)
      .filter(([, collapsed]) => !collapsed)
      .map(([k]) => k);
    expect(expanded).toEqual(["fileWrite"]);
  });

  describe("narrow surfaces", () => {
    it("collapses file edits on mobile", () => {
      // The desktop rationale for expanding edits ("the diff is the point")
      // assumes a pane wide enough to read a diff in. On a phone one expanded
      // edit pushes the rest of the conversation off-screen.
      expect(shouldToolBeCollapsed("edit", "desktop")).toBe(false);
      expect(shouldToolBeCollapsed("edit", "mobile")).toBe(true);
      expect(shouldToolBeCollapsed("write", "mobile")).toBe(true);
      expect(shouldToolBeCollapsed("patch", "mobile")).toBe(true);
    });

    it("collapses file edits in an embed", () => {
      // Same reasoning: an embedded widget is a narrow column in someone
      // else's layout, and its height is not ours to spend.
      expect(shouldToolBeCollapsed("edit", "embed")).toBe(true);
      expect(shouldToolBeCollapsed("write", "embed")).toBe(true);
    });

    it("collapses every category on mobile regardless of settings", () => {
      const toolsByCategory = [
        "view", // fileView
        "edit", // fileWrite — the only desktop-expanded category
        "grep", // searchRead
        "bash", // execution
        "create_plan", // planning
        "some_server/some_tool", // mcp
        "spawn", // agent
        "unknown_future_tool", // no category
      ];
      for (const tool of toolsByCategory) {
        expect(
          shouldToolBeCollapsed(tool, "mobile"),
          `${tool} should be collapsed on mobile`,
        ).toBe(true);
      }
    });

    it("defaults to desktop behavior when no surface is passed", () => {
      // Every existing call site omits the argument; none of them should
      // change behavior.
      expect(shouldToolBeCollapsed("edit")).toBe(shouldToolBeCollapsed("edit", "desktop"));
      expect(shouldToolBeCollapsed("bash")).toBe(shouldToolBeCollapsed("bash", "desktop"));
    });
  });
});
