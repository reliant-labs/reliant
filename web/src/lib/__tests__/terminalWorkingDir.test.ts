import { describe, expect, it } from "vitest";
import { WorktreeStatus } from "../../gen/reliant/v1/worktree_pb";
import { resolveTerminalWorkingDir } from "../terminalWorkingDir";

// ---------------------------------------------------------------------------
// A terminal scoped to a workspace must start IN that workspace. The old chain
// was `worktree.path || project.path`, so a workspace whose path was not known
// yet (CreateWorktree returns CREATING with path "") silently produced a shell
// in the main checkout — under the workspace's tab, where a `git commit` lands
// on the wrong branch. Falling back is only correct when there is no
// workspace, or the workspace IS the main checkout.
// ---------------------------------------------------------------------------

const project = { path: "/home/u/src/proj" };

describe("resolveTerminalWorkingDir", () => {
  it("uses the workspace path when the workspace has one", () => {
    expect(
      resolveTerminalWorkingDir(
        { path: "/home/u/.reliant/worktrees/proj/triggers", is_main: false, status: WorktreeStatus.ACTIVE },
        project,
      ),
    ).toEqual({ kind: "ready", path: "/home/u/.reliant/worktrees/proj/triggers" });
  });

  it("waits, rather than falling back to the project, while a workspace is being created", () => {
    expect(
      resolveTerminalWorkingDir({ path: "", is_main: false, status: WorktreeStatus.CREATING }, project),
    ).toEqual({ kind: "pending" });
  });

  it("reports a workspace that failed to create instead of opening the project root", () => {
    expect(
      resolveTerminalWorkingDir({ path: "", is_main: false, status: WorktreeStatus.FAILED }, project),
    ).toEqual({ kind: "failed" });
  });

  it("never hands a non-main workspace the project path, whatever its status", () => {
    // A pathless non-main row with an unexpected status is still not the
    // project root. Waiting is recoverable; a shell in the wrong checkout is not.
    expect(
      resolveTerminalWorkingDir({ path: "", is_main: false, status: WorktreeStatus.ACTIVE }, project),
    ).toEqual({ kind: "pending" });
  });

  it("uses the project path for the main workspace", () => {
    expect(
      resolveTerminalWorkingDir({ path: "", is_main: true, status: WorktreeStatus.UNSPECIFIED }, project),
    ).toEqual({ kind: "ready", path: project.path });
  });

  it("uses the project path when no workspace is selected", () => {
    expect(resolveTerminalWorkingDir(null, project)).toEqual({ kind: "ready", path: project.path });
  });

  it("is pending when there is no project to fall back to", () => {
    expect(resolveTerminalWorkingDir(null, null)).toEqual({ kind: "pending" });
  });
});
