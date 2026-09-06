/**
 * ProjectPickerStep — a brand-new user must not hit a dead end.
 *
 * THE BUG THIS CLOSES: on a fresh install the project list settles empty and
 * the step printed "No existing projects. Create one above to continue." —
 * a message whose only job is to tell the user to click a button we could
 * have clicked for them. The create modal is the entire point of the step in
 * that state, so it opens itself.
 *
 * The three things that make it correct rather than merely automatic:
 *   - it waits for the list to SETTLE, so it never flashes open over a fetch
 *     that was about to return projects;
 *   - it fires ONCE, so closing it leaves the user on the list instead of
 *     fighting a modal that keeps reappearing;
 *   - it stays out of the way entirely for a user who already has projects.
 */
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { LaunchPlan } from "../types";

// ── Mocks ────────────────────────────────────────────────────────────────

const projectState = vi.hoisted(() => ({
  current: {
    projects: [] as any[],
    isLoading: false,
    currentProject: null as any,
    loadProjects: vi.fn(async () => undefined),
    selectProject: vi.fn(async () => undefined),
  },
}));

vi.mock("@/store/projectStore", () => ({
  useProjectStore: Object.assign(
    (selector?: any) =>
      selector ? selector(projectState.current) : projectState.current,
    {
      getState: () => projectState.current,
      setState: vi.fn(),
      subscribe: vi.fn(() => () => undefined),
    },
  ),
}));

// Stand in for the real create-project modal. Rendering a marker only when
// open is what lets us assert on the auto-open behavior, and the close button
// is what proves the modal does not re-open behind the user.
vi.mock("@/components/Projects/ProjectPickerModal", () => ({
  ProjectPickerModal: ({ isOpen, onClose }: any) =>
    isOpen ? (
      <div data-testid="create-project-modal">
        <button type="button" onClick={onClose}>
          Close create modal
        </button>
      </div>
    ) : null,
}));

vi.mock("@/hooks/useOnboardingQueries", () => ({
  useCompleteOnboarding: () => ({ mutateAsync: vi.fn(async () => ({})) }),
}));

vi.mock("../useOnboardingComplete", () => ({
  finalizeOnboardingSideEffects: vi.fn(async () => undefined),
}));

vi.mock("../leaveOnboarding", () => ({
  leaveOnboarding: vi.fn(async () => undefined),
}));

vi.mock("../analytics", () => ({ markOnboardingFinalized: vi.fn() }));

vi.mock("../DaemonConnectingGate", () => ({
  DaemonConnectingGate: () => <div data-testid="daemon-gate" />,
}));

vi.mock("@/lib/logger", () => ({
  logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}));

import { ProjectPickerStep } from "../steps/ProjectPickerStep";

// ── Helpers ──────────────────────────────────────────────────────────────

const PLAN: Partial<LaunchPlan> = {
  compute: "local_daemon",
  modelProvider: "anthropic",
};

function renderStep() {
  return render(
    <ProjectPickerStep
      plan={PLAN}
      updatePlan={vi.fn()}
      onNext={vi.fn()}
      onBack={vi.fn()}
    />,
  );
}

/** A promise we resolve by hand, so a fetch can be held mid-flight. */
function deferred() {
  let resolve!: () => void;
  const promise = new Promise<void>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

beforeEach(() => {
  vi.clearAllMocks();
  projectState.current = {
    ...projectState.current,
    projects: [],
    isLoading: false,
    currentProject: null,
    loadProjects: vi.fn(async () => undefined),
    selectProject: vi.fn(async () => undefined),
  };
});

// ── Tests ────────────────────────────────────────────────────────────────

describe("ProjectPickerStep — empty project list opens the create modal", () => {
  it("opens the create modal once the list settles with no projects", async () => {
    renderStep();

    await waitFor(() =>
      expect(screen.getByTestId("create-project-modal")).toBeInTheDocument(),
    );
  });

  // The flash guard. `isLoading` starts false — before the effect has even
  // run — so keying off it alone would pop the modal open on the first
  // render of every user, including one with a dozen projects.
  it("does not open while the first fetch is still in flight", async () => {
    const gate = deferred();
    projectState.current.loadProjects = vi.fn(() => gate.promise);

    renderStep();

    // Give React a few turns to flush effects; the modal must stay shut.
    await new Promise((r) => setTimeout(r, 30));
    expect(screen.queryByTestId("create-project-modal")).toBeNull();

    gate.resolve();
    await waitFor(() =>
      expect(screen.getByTestId("create-project-modal")).toBeInTheDocument(),
    );
  });

  it("stays closed after the user dismisses it", async () => {
    renderStep();

    const closeButton = await waitFor(() =>
      screen.getByRole("button", { name: /close create modal/i }),
    );
    await userEvent.click(closeButton);

    expect(screen.queryByTestId("create-project-modal")).toBeNull();
    // A re-render must not re-trigger the auto-open — the user said no.
    await new Promise((r) => setTimeout(r, 30));
    expect(screen.queryByTestId("create-project-modal")).toBeNull();
  });

  it("does not open when the user already has projects", async () => {
    projectState.current.projects = [
      {
        id: "p1",
        name: "existing",
        path: "/Users/someone/code/existing",
        last_active: new Date().toISOString(),
        is_git_repo: true,
      },
    ];

    renderStep();

    await waitFor(() => expect(screen.getByText("existing")).toBeInTheDocument());
    expect(screen.queryByTestId("create-project-modal")).toBeNull();
  });

  // The manual path has to keep working — auto-open is an addition, not a
  // replacement for the button that has always been there.
  it("still opens the modal from the 'Create a new project' button", async () => {
    projectState.current.projects = [
      {
        id: "p1",
        name: "existing",
        path: "/Users/someone/code/existing",
        last_active: new Date().toISOString(),
      },
    ];

    renderStep();

    await waitFor(() => expect(screen.getByText("existing")).toBeInTheDocument());
    expect(screen.queryByTestId("create-project-modal")).toBeNull();

    await userEvent.click(
      screen.getByRole("button", { name: /create a new project/i }),
    );
    expect(screen.getByTestId("create-project-modal")).toBeInTheDocument();
  });

  // The copy that sent the user nowhere.
  it("no longer tells the user to click a button we opened for them", async () => {
    renderStep();

    await waitFor(() =>
      expect(screen.getByTestId("create-project-modal")).toBeInTheDocument(),
    );
    expect(
      screen.queryByText(/create one above to continue/i),
    ).toBeNull();
  });
});
