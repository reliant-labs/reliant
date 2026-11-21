import { describe, expect, it } from "vitest";
import { FilePen, Terminal, Wrench } from "lucide-react";
import { editDiffStat, sumDiffStats, toolRowIcon } from "../toolRowParts";

describe("editDiffStat", () => {
  it("counts the lines a single edit adds and removes", () => {
    expect(
      editDiffStat("edit", {
        file_path: "src/cart/totals.ts",
        old_string: "a\nb\nc\n",
        new_string: "a\nB\nc\nd\ne\n",
      }),
    ).toEqual({ added: 3, removed: 1 });
  });

  it("sums an edits array, and reads an MCP-prefixed name", () => {
    expect(
      editDiffStat("mcp__reliant__edit", {
        edits: [
          { file_path: "a.ts", old_string: "x\n", new_string: "y\n" },
          { file_path: "b.ts", old_string: "", new_string: "z\n" },
        ],
      }),
    ).toEqual({ added: 2, removed: 1 });
  });

  it("counts a write's content as added", () => {
    expect(editDiffStat("write", { file_path: "n.md", content: "one\ntwo\n" })).toEqual({ added: 2, removed: 0 });
  });

  it("is null for a non-edit, an elided skeleton, or nothing to count", () => {
    expect(editDiffStat("shell", { command: "ls" })).toBeNull();
    expect(editDiffStat("edit", { old_string: "a\n", new_string: "b\n" }, true)).toBeNull();
    expect(editDiffStat("edit", undefined)).toBeNull();
    expect(editDiffStat("edit", { file_path: "a.ts" })).toBeNull();
  });
});

describe("toolRowIcon", () => {
  it("names the kind of work, with a wrench for an unknown tool", () => {
    expect(toolRowIcon("edit")).toBe(FilePen);
    expect(toolRowIcon("mcp__reliant__shell")).toBe(Terminal);
    expect(toolRowIcon("some_mcp_tool")).toBe(Wrench);
  });
});

describe("sumDiffStats", () => {
  it("adds up the edits it counted and skips the rest", () => {
    expect(sumDiffStats([{ added: 2, removed: 1 }, null, { added: 3, removed: 0 }])).toEqual({ added: 5, removed: 1 });
  });

  it("is null when no edit was counted", () => {
    expect(sumDiffStats([])).toBeNull();
    expect(sumDiffStats([null, null])).toBeNull();
  });
});
