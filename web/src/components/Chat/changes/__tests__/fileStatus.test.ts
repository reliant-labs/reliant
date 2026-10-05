import { describe, expect, it } from "vitest";
import { FileChangeStatus } from "../../../../gen/reliant/v1/common_pb";
import { changeGroupOf, lineCountsOf, splitPath, statusLetterOf } from "../fileStatus";
import type { FileChange } from "../types";

const file = (overrides: Partial<FileChange>): FileChange => ({
  path: "src/a.ts",
  status: FileChangeStatus.MODIFIED,
  is_new: false,
  ...overrides,
});

const MODIFIED_DIFF = [
  "diff --git a/src/a.ts b/src/a.ts",
  "index 111..222 100644",
  "--- a/src/a.ts",
  "+++ b/src/a.ts",
  "@@ -1,3 +1,4 @@",
  " keep",
  "-old",
  "+new",
  "+added",
  " keep",
].join("\n");

describe("statusLetterOf", () => {
  it("reads add, delete and rename from the diff header", () => {
    expect(statusLetterOf(file({ diff: MODIFIED_DIFF }))).toBe("M");
    expect(
      statusLetterOf(file({ status: FileChangeStatus.STAGED, diff: "diff --git a/x b/x\nnew file mode 100644\n@@ -0,0 +1 @@\n+x" })),
    ).toBe("A");
    expect(statusLetterOf(file({ diff: "diff --git a/x b/x\ndeleted file mode 100644\n@@ -1 +0,0 @@\n-x" }))).toBe("D");
    expect(
      statusLetterOf(file({ status: FileChangeStatus.STAGED, diff: "diff --git a/x b/y\nsimilarity index 90%\nrename from x\nrename to y\n" })),
    ).toBe("R");
  });

  it("does not mistake diff content for a header", () => {
    const diff = `${MODIFIED_DIFF}\n+new file mode 100644`;
    expect(statusLetterOf(file({ diff }))).toBe("M");
  });

  it("maps untracked, deleted and staged-new statuses", () => {
    expect(statusLetterOf(file({ status: FileChangeStatus.UNTRACKED }))).toBe("U");
    expect(statusLetterOf(file({ status: FileChangeStatus.DELETED }))).toBe("D");
    expect(statusLetterOf(file({ status: FileChangeStatus.STAGED, is_new: true }))).toBe("A");
  });
});

describe("lineCountsOf", () => {
  it("counts added and removed lines inside hunks only", () => {
    expect(lineCountsOf(file({ diff: MODIFIED_DIFF }))).toEqual({ added: 2, removed: 1 });
  });

  it("counts every line of an untracked file as added", () => {
    expect(lineCountsOf(file({ status: FileChangeStatus.UNTRACKED, diff: "a\nb\nc\n" }))).toEqual({ added: 3, removed: 0 });
  });

  it("returns null when the diff is missing or truncated", () => {
    expect(lineCountsOf(file({ diff: "" }))).toBeNull();
    expect(
      lineCountsOf(file({ diff: `${MODIFIED_DIFF}\n... [diff truncated: worktree changes exceeded the transport budget]` })),
    ).toBeNull();
  });
});

describe("changeGroupOf", () => {
  it("lists deleted project files with the other unstaged changes", () => {
    expect(changeGroupOf(file({ status: FileChangeStatus.DELETED }))).toBe("modified");
    expect(changeGroupOf(file({ status: FileChangeStatus.STAGED }))).toBe("staged");
    expect(changeGroupOf(file({ status: FileChangeStatus.UNSPECIFIED }))).toBeNull();
  });
});

describe("splitPath", () => {
  it("splits name from directory", () => {
    expect(splitPath("a/b/c.ts")).toEqual({ name: "c.ts", dir: "a/b" });
    expect(splitPath("c.ts")).toEqual({ name: "c.ts", dir: "" });
  });
});
