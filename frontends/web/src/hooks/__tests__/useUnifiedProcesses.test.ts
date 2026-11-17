import { describe, expect, it } from "vitest";
import { BackgroundProcessStatus, type BackgroundProcess } from "../background-queries";
import type { PackageProcess } from "../package-queries";
import { mergeProcesses } from "../useUnifiedProcesses";

const t = (minute: number) => `2026-11-17T10:${String(minute).padStart(2, "0")}:00Z`;

describe("mergeProcesses", () => {
  // The daemon has one process table, read through two services: a Commands
  // panel process is in both lists, an agent's only in BackgroundService's.
  it("lists each process once, a panel process with what both services know", () => {
    const pkg: PackageProcess[] = [
      { id: "p1", command: "npm run dev", status: BackgroundProcessStatus.RUNNING, worktree_id: "wt", working_dir: "/w", start_time: t(1) },
    ];
    const bg: BackgroundProcess[] = [
      { id: "p1", command: "npm run dev", status: BackgroundProcessStatus.RUNNING, working_dir: "/w", start_time: t(1), session_id: "", daemon_id: "m1", ports: [{ port: 5173, protocol: "tcp", state: "LISTEN", address: "" }] },
      { id: "a1", command: "go test ./...", status: BackgroundProcessStatus.COMPLETED, exit_code: 0, working_dir: "/w", start_time: t(2), session_id: "", chat_id: "chat-1", thread_id: "th", daemon_id: "m2" },
    ];

    const merged = mergeProcesses(pkg, bg);

    expect(merged.map((p) => p.id)).toEqual(["a1", "p1"]);
    expect(merged[1]).toMatchObject({ source: "package", worktree_id: "wt", daemon_id: "m1", ports: [{ port: 5173, protocol: "tcp" }] });
    expect(merged[0]).toMatchObject({ source: "background", chat_id: "chat-1", thread_id: "th", daemon_id: "m2" });
  });
});
