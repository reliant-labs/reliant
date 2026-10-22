import { fireEvent, screen, waitFor } from "@testing-library/react";
import { renderWithQuery } from "../../../test/renderWithQuery";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";
import { Code, ConnectError } from "@connectrpc/connect";
import { RecentChanges } from "../RecentChanges";
import { FileChangeStatus } from "../../../gen/reliant/v1/common_pb";
import { DaemonStatus, DaemonWakingSchema } from "../../../gen/reliant/v1/daemon_registry_pb";
import { machineWakeInterceptor } from "../../../api/machineWakeInterceptor";
import { clearWaking } from "../../../lib/machineWake";
import { useProjectStore } from "../../../store/projectStore";

// The Changes panel of a workspace whose machine is asleep. The server wakes
// the machine for the panel's own request and says so (a DaemonWaking detail);
// the panel must say "Waking up…" and finish what it was doing once the
// machine is back — not "Couldn't load changes", and not a failed commit.

const getChangesMock = vi.fn();
const git = {
  getExistingPR: vi.fn(),
  commitChanges: vi.fn(),
  pushChanges: vi.fn(),
  pullChanges: vi.fn(),
  stageFiles: vi.fn(),
  unstageFiles: vi.fn(),
  revertFiles: vi.fn(),
};

vi.mock("../../../../src/api/worktree-grpc", () => ({
  worktreeGrpc: {
    getChanges: (...args: unknown[]) => getChangesMock(...args),
  },
}));

vi.mock("../../../../src/api/project-grpc", () => ({
  projectGrpc: { getChanges: vi.fn() },
}));

vi.mock("../../../../src/api/git", () => ({
  getExistingPR: (...args: unknown[]) => git.getExistingPR(...args),
  commitChanges: (...args: unknown[]) => git.commitChanges(...args),
  pushChanges: (...args: unknown[]) => git.pushChanges(...args),
  pullChanges: (...args: unknown[]) => git.pullChanges(...args),
  stageFiles: (...args: unknown[]) => git.stageFiles(...args),
  unstageFiles: (...args: unknown[]) => git.unstageFiles(...args),
  revertFiles: (...args: unknown[]) => git.revertFiles(...args),
}));

// A cloud deployment whose registry still reads the woken machine as
// SUSPENDED — the moment right after a wake, before the control plane has
// caught up. Without the wake record this is "Your machine is suspended",
// which stops retrying.
vi.mock("@/services/controlPlane/capabilities", () => ({
  capabilities: { cloudDaemons: true },
}));

vi.mock("@/api/grpc-client", () => ({
  grpcClient: {
    daemonRegistry: () => ({
      listDaemons: async () => ({
        daemons: [{ daemonId: "daemon-b", hostname: "cloud-b", status: DaemonStatus.SUSPENDED }],
      }),
    }),
  },
}));

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => vi.fn(),
}));

vi.mock("../../../../src/store/viewerStore", () => ({
  useViewerStore: (selector: (state: { openDiffViewer: () => void }) => unknown) =>
    selector({ openDiffViewer: vi.fn() }),
}));

vi.mock("../../ui/FileIcon", () => ({
  FileIcon: () => <div data-testid="file-icon" />,
}));

vi.mock("../../SourceControl/PRDialog", () => ({
  PRDialog: () => null,
}));

vi.mock("../../Git/GitNotInitialized", () => ({
  GitNotInitialized: () => <div>Git not initialized</div>,
}));

vi.mock("../../ui/Tooltip", () => ({
  Tooltip: ({ children }: { children: ReactNode }) => <>{children}</>,
}));

let worktreeSeq = 0;
const nextWorktreeId = () => `wt-wake-${++worktreeSeq}`;

function changes(files: Array<{ path: string; status: FileChangeStatus }>) {
  return {
    branch: "feature/x",
    files: files.map((f) => ({ diff: "", is_new: false, ...f })),
    total_files: files.length,
    ahead: 0,
    behind: 0,
    default_branch: "main",
  };
}

/**
 * The server's answer to a request whose machine it just woke, delivered
 * through the real transport interceptor so the wake is recorded the way the
 * app records it.
 */
function wokeTheMachine(): Promise<never> {
  const err = new ConnectError("your machine is waking up: no daemon connected yet", Code.Unavailable, undefined, [
    { desc: DaemonWakingSchema, value: { daemonId: "daemon-b" } },
  ]);
  return machineWakeInterceptor(() => Promise.reject(err))({} as never) as Promise<never>;
}

describe("RecentChanges against an asleep machine", () => {
  beforeEach(() => {
    getChangesMock.mockReset();
    Object.values(git).forEach((fn) => fn.mockReset());
    git.getExistingPR.mockResolvedValue({ exists: false });
    useProjectStore.setState({
      currentProject: {
        id: "project-1",
        name: "Project",
        path: "/tmp/project",
        is_git_repo: true,
        default_branch: "main",
        worktree_count: 1,
        last_active: new Date().toISOString(),
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
      },
    });
  });

  afterEach(() => {
    clearWaking("daemon-b");
    vi.restoreAllMocks();
  });

  it("says Waking up… instead of failing, and shows the changes once the machine is up", async () => {
    getChangesMock
      .mockImplementationOnce(wokeTheMachine)
      .mockResolvedValue(changes([{ path: "back.ts", status: FileChangeStatus.MODIFIED }]));

    renderWithQuery(<RecentChanges worktreeId={nextWorktreeId()} projectId="project-1" onClose={() => {}} />);

    expect(await screen.findByText("Waking up…")).toBeInTheDocument();
    expect(screen.queryByText("Couldn't load changes")).not.toBeInTheDocument();
    expect(screen.queryByText(/is suspended/)).not.toBeInTheDocument();

    // The wait retries on its own cadence; no click needed.
    expect(await screen.findByText("back.ts", undefined, { timeout: 5_000 })).toBeInTheDocument();
    expect(screen.queryByText("Waking up…")).not.toBeInTheDocument();
  });

  it("finishes a commit that found the machine asleep, saying so meanwhile", async () => {
    getChangesMock.mockResolvedValue(changes([{ path: "staged.ts", status: FileChangeStatus.STAGED }]));
    git.commitChanges.mockImplementationOnce(wokeTheMachine).mockResolvedValue({ message: "ok" });

    renderWithQuery(<RecentChanges worktreeId={nextWorktreeId()} projectId="project-1" onClose={() => {}} />);
    await screen.findByText("staged.ts");

    fireEvent.change(screen.getByLabelText("Commit message"), { target: { value: "While asleep" } });
    fireEvent.click(screen.getByRole("button", { name: "Commit 1 file" }));

    expect(await screen.findByText(/Waking up… this finishes once your machine is back/)).toBeInTheDocument();
    await waitFor(() => expect(git.commitChanges).toHaveBeenCalledTimes(2), { timeout: 5_000 });
    await waitFor(() => expect(screen.queryByText(/Waking up…/)).not.toBeInTheDocument());
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });
});
