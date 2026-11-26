import { fileNodeExpandable, fileNodeUnavailableReason } from "./symlinks";
import type { FileNode } from "./index";

const dir = (over: Partial<FileNode> = {}): FileNode => ({ name: "d", path: "d", type: "directory", ...over });

describe("file tree folder expandability", () => {
  it("expands a directory with children, linked or not", () => {
    expect(fileNodeExpandable(dir({ hasChildren: true }))).toBe(true);
    expect(fileNodeExpandable(dir({ hasChildren: true, symlinkStatus: "resolved", symlinkTarget: "../shared" }))).toBe(true);
    // Eagerly-loaded data without the hint.
    expect(fileNodeExpandable(dir({ children: [{ name: "f", path: "d/f", type: "file" }] }))).toBe(true);
  });

  it("does not expand an empty directory or a file", () => {
    expect(fileNodeExpandable(dir({ hasChildren: false, children: [] }))).toBe(false);
    expect(fileNodeExpandable({ name: "f", path: "f", type: "file" })).toBe(false);
  });

  it("never expands a link the daemon refused to follow, and says why", () => {
    for (const status of ["loop", "denied"] as const) {
      const node = dir({ hasChildren: true, symlinkStatus: status, symlinkTarget: ".." });
      expect(fileNodeExpandable(node)).toBe(false);
      expect(fileNodeUnavailableReason(node)).toContain("→ ..");
    }
    const dangling: FileNode = { name: "x", path: "x", type: "file", symlinkStatus: "broken", symlinkTarget: "missing" };
    expect(fileNodeUnavailableReason(dangling)).toBe("Broken symbolic link → missing: its target does not exist");
  });

  it("gives no reason for anything that opens", () => {
    expect(fileNodeUnavailableReason(dir({ symlinkStatus: "resolved" }))).toBeNull();
    expect(fileNodeUnavailableReason(dir())).toBeNull();
  });
});
