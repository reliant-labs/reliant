// Copyright (c) 2025 Reliant Labs

/**
 * "Ask Reliant" for a forge app no Reliant project holds (an app built with
 * standalone forge): the dialog offers the step that gets a project, by what
 * the reader has, and never ends at "add one from the main screen". The
 * machine list, the recorded source remote, the pairing instructions and the
 * trip to billing are mocked at their module edges; the Add project dialog's
 * store is real.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import type { GitRepo } from "@/services/controlPlane/git";
import { useAddProjectDialogStore } from "@/store/addProjectDialogStore";
import { useProjectStore, type Project } from "@/store/projectStore";

const projectList = vi.hoisted(() => ({ projects: [] as unknown[] }));
vi.mock("@/hooks/project-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/project-queries")>()),
  fetchProjects: vi.fn(async () => projectList.projects),
}));
const startAskReliantChat = vi.hoisted(() => vi.fn());
vi.mock("../startAskReliantChat", () => ({ startAskReliantChat }));
vi.mock("@/lib/toast-manager", () => ({ toast: { error: vi.fn(), warning: vi.fn(), success: vi.fn(), info: vi.fn() } }));

const machines = vi.hoisted(() => ({ daemons: [] as { daemonId: string; daemonType: string }[], loading: false }));
vi.mock("@/hooks/useDaemonStatus", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/useDaemonStatus")>()),
  useDaemonStatus: () => ({ daemons: machines.daemons, loading: machines.loading, activeDaemon: undefined }),
}));
const source = vi.hoisted(() => ({ repo: null as GitRepo | null }));
const sourceReads = vi.hoisted(() => [] as (string | undefined)[]);
vi.mock("../askReliantSource", () => ({
  useForgeSourceRepo: (forgeProject?: string) => {
    sourceReads.push(forgeProject);
    return forgeProject ? source.repo : null;
  },
}));
// The pairing instructions are PairMachinePanel's tests'; here, that they are the
// shared panel, in which mode, and what a connected machine does.
const pairing = vi.hoisted(() => ({ mode: undefined as string | undefined }));
vi.mock("@/components/Machines", () => ({
  PairMachinePanel: ({ mode, onConnected }: { mode?: string; onConnected?: (daemonId: string) => void }) => {
    pairing.mode = mode;
    return (
      <button type="button" data-testid="machine-connected" onClick={() => onConnected?.("d1")}>
        pairing instructions
      </button>
    );
  },
  AddMachineSheet: () => {
    throw new Error("Ask Reliant must not open the Add machine sheet: it can create a paid machine");
  },
}));
const goToBilling = vi.hoisted(() => vi.fn());
const billingOrigins = vi.hoisted(() => [] as string[]);
vi.mock("@/hooks/useGoToBilling", () => ({
  useGoToBilling: (from: string) => {
    billingOrigins.push(from);
    return goToBilling;
  },
}));
const caps = vi.hoisted(() => ({ cloud: true }));
vi.mock("@/services/controlPlane/capabilities", async (importOriginal) => {
  const original = await importOriginal<typeof import("@/services/controlPlane/capabilities")>();
  return {
    ...original,
    capabilities: {
      ...original.capabilities,
      get cloudDaemons() {
        return caps.cloud;
      },
    },
  };
});

import { AskReliantButton } from "../AskReliant";

const shopRepo: GitRepo = {
  fullName: "acme/shop",
  cloneUrl: "https://github.com/acme/shop.git",
  defaultBranch: "main",
  description: "",
  private: false,
  language: "",
  updatedAt: "",
};

const project = (id: string, forge?: string): Project => ({
  id,
  name: `Project ${id}`,
  path: `/work/${id}`,
  is_git_repo: true,
  last_active: "",
  created_at: "",
  updated_at: "",
  is_forge: !!forge,
  forge_project_name: forge,
});

const issue = { kind: "issue", issueId: "ISS-7f3a", env: "prod" } as const;

async function openAsk() {
  render(<AskReliantButton subject={issue} forgeProject="shop" />);
  await userEvent.click(screen.getByTestId("ask-reliant"));
  return screen.findByTestId("ask-reliant-no-projects");
}

beforeEach(() => {
  projectList.projects = [];
  useProjectStore.setState({ currentProject: null });
  useAddProjectDialogStore.setState({ isOpen: false, method: null, repoUrl: null });
  startAskReliantChat.mockReset().mockResolvedValue({ worktreeId: "w1" });
  machines.daemons = [];
  machines.loading = false;
  source.repo = null;
  sourceReads.length = 0;
  pairing.mode = undefined;
  goToBilling.mockReset();
  billingOrigins.length = 0;
  caps.cloud = true;
});

describe("Ask Reliant with no project for the forge app", () => {
  it("with no machine, shows both ways to get one as instructions, and never the main screen", async () => {
    const body = await openAsk();
    expect(screen.getByText("Reliant has no project for shop yet")).toBeInTheDocument();
    expect(body).toHaveTextContent(/First, connect a machine/);
    expect(screen.queryByText(/main screen/)).toBeNull();
    // Your own computer: the shared install-and-pair instructions, waiting for the first machine.
    expect(screen.getByTestId("ask-reliant-pair-machine")).toHaveTextContent(/free/);
    expect(pairing.mode).toBe("bootstrap");
    // A cloud machine: needs a plan, so a way to the plans, and no machine created here.
    expect(screen.getByTestId("ask-reliant-cloud-machine")).toHaveTextContent(/needs a plan/);
    expect(screen.queryByTestId("ask-reliant-add-project")).toBeNull();
    expect(startAskReliantChat).not.toHaveBeenCalled();
  });

  it("sends the cloud path to the plans, as from Settings › Machines", async () => {
    await openAsk();
    await userEvent.click(screen.getByTestId("ask-reliant-see-plans"));
    expect(goToBilling).toHaveBeenCalledTimes(1);
    expect(new Set(billingOrigins)).toEqual(new Set(["machines"]));
    expect(screen.queryByTestId("ask-reliant-no-projects")).toBeNull();
  });

  it("offers no cloud path where the deployment has no cloud machines", async () => {
    caps.cloud = false;
    await openAsk();
    expect(screen.getByTestId("ask-reliant-pair-machine")).toBeInTheDocument();
    expect(screen.queryByTestId("ask-reliant-cloud-machine")).toBeNull();
  });

  it("does not read the app's repository while there is no machine to add it to", async () => {
    source.repo = shopRepo;
    const body = await openAsk();
    expect(body).not.toHaveTextContent("acme/shop");
    expect(sourceReads.length).toBeGreaterThan(0);
    expect(sourceReads.every((name) => name === undefined)).toBe(true);
  });

  it("starts the chat once a paired machine brings the project", async () => {
    await openAsk();
    // The machine was paired and its checkout added as a project that declares the app.
    const shop = project("p1", "shop");
    projectList.projects = [shop];
    await userEvent.click(screen.getByTestId("machine-connected"));
    await waitFor(() => expect(startAskReliantChat).toHaveBeenCalledTimes(1));
    expect(startAskReliantChat.mock.calls[0][0].project).toEqual(shop);
    expect(screen.queryByTestId("ask-reliant-no-projects")).toBeNull();
  });

  it("with a cloud machine, Add project opens the clone form filled with the app's repository", async () => {
    machines.daemons = [{ daemonId: "d1", daemonType: "managed" }];
    source.repo = shopRepo;
    await openAsk();
    await userEvent.click(screen.getByTestId("ask-reliant-add-project"));

    expect(useAddProjectDialogStore.getState()).toMatchObject({ isOpen: true, method: "clone", repoUrl: "https://github.com/acme/shop" });
    expect(screen.queryByTestId("ask-reliant-no-projects")).toBeNull();
  });

  it("with only the reader's own computer, Add project offers both ways, the clone form still filled", async () => {
    machines.daemons = [{ daemonId: "d2", daemonType: "external" }];
    source.repo = shopRepo;
    await openAsk();
    await userEvent.click(screen.getByTestId("ask-reliant-add-project"));
    expect(useAddProjectDialogStore.getState()).toMatchObject({ isOpen: true, method: null, repoUrl: "https://github.com/acme/shop" });
  });

  it("with no recorded repository, Add project opens the plain dialog", async () => {
    machines.daemons = [{ daemonId: "d1", daemonType: "managed" }];
    await openAsk();
    await userEvent.click(screen.getByTestId("ask-reliant-add-project"));
    expect(useAddProjectDialogStore.getState()).toMatchObject({ isOpen: true, method: null, repoUrl: null });
  });

  it("waits for the machine list before choosing a step", async () => {
    machines.loading = true;
    const body = await openAsk();
    expect(body).toHaveTextContent("Checking your machines");
    expect(screen.getByTestId("ask-reliant-add-project")).toBeDisabled();
  });

  it("hands the subject to the reader's own agent instead, without sending them to Reliant again", async () => {
    await openAsk();
    await userEvent.click(screen.getByTestId("ask-reliant-handoff"));

    expect(await screen.findByTestId("ask-agent-sheet")).toBeInTheDocument();
    expect(screen.getByTestId("ask-agent-prompt")).toHaveTextContent("ISS-7f3a");
    expect(screen.queryByTestId("ask-agent-open-in-reliant")).toBeNull();
    expect(screen.queryByTestId("ask-reliant-no-projects")).toBeNull();
  });
});

describe("Ask Reliant with projects, none of them declaring the forge app", () => {
  it("offers Add project beside the picker", async () => {
    projectList.projects = [project("a")];
    render(<AskReliantButton subject={issue} forgeProject="shop" />);
    await userEvent.click(screen.getByTestId("ask-reliant"));
    expect(await screen.findByTestId("ask-reliant-projects")).toBeInTheDocument();

    await userEvent.click(screen.getByTestId("ask-reliant-add-project"));
    expect(useAddProjectDialogStore.getState()).toMatchObject({ isOpen: true, method: null });
    expect(screen.queryByTestId("ask-reliant-projects")).toBeNull();
  });

  it("offers none when several declare it: the code is already here", async () => {
    projectList.projects = [project("a", "shop"), project("b", "shop")];
    render(<AskReliantButton subject={issue} forgeProject="shop" />);
    await userEvent.click(screen.getByTestId("ask-reliant"));
    expect(await screen.findByTestId("ask-reliant-projects")).toBeInTheDocument();
    expect(screen.queryByTestId("ask-reliant-add-project")).toBeNull();
  });
});
