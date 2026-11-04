// Copyright (c) 2025 Reliant Labs

/**
 * The shared "Ask Reliant" button: which project it starts in, what it asks the
 * orchestration to do, and what it hands back. startAskReliantChat is mocked at
 * its module edge — its own test pins what it does to the stores.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { useProjectStore, type Project } from "@/store/projectStore";

const projectList = vi.hoisted(() => ({ projects: [] as unknown[] }));
vi.mock("@/hooks/project-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/project-queries")>()),
  fetchProjects: vi.fn(async () => projectList.projects),
}));
const startAskReliantChat = vi.hoisted(() => vi.fn());
vi.mock("../startAskReliantChat", () => ({ startAskReliantChat }));
const toastError = vi.hoisted(() => vi.fn());
const toastWarning = vi.hoisted(() => vi.fn());
vi.mock("@/lib/toast-manager", () => ({ toast: { error: toastError, warning: toastWarning, success: vi.fn(), info: vi.fn() } }));

import { ForgeConsoleContext, type ForgeConsole } from "../../../Console/consoleContext";
import { AskReliantActions, AskReliantButton, AskReliantMenu, useAskReliant } from "../AskReliant";
import { ObsAskContextValue } from "../askReliantContext";

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

const consoleFor = (forgeProject: string, projectId: string | null): ForgeConsole => ({
  forgeProject,
  projectId,
  page: { kind: "overview" },
  env: "prod",
  setEnv: vi.fn(),
  roster: { envs: [{ name: "dev" }, { name: "prod" }] } as unknown as ForgeConsole["roster"],
  href: () => "",
});

const issue = { kind: "issue", issueId: "ISS-7f3a", env: "prod" } as const;

function setProjects(projects: Project[], current: Project | null) {
  projectList.projects = projects;
  useProjectStore.setState({ currentProject: current });
}

describe("Ask Reliant", () => {
  beforeEach(() => {
    startAskReliantChat.mockReset().mockResolvedValue({ worktreeId: "w1" });
    toastError.mockReset();
    toastWarning.mockReset();
  });

  it("starts a branched chat in the project that owns the forge project, with the references-only prompt", async () => {
    const shop = project("a", "shop");
    setProjects([project("b", "blog"), shop], project("b", "blog"));
    render(
      <ForgeConsoleContext.Provider value={consoleFor("shop", "a")}>
        <AskReliantButton subject={issue} />
      </ForgeConsoleContext.Provider>
    );
    await userEvent.click(screen.getByTestId("ask-reliant"));

    await waitFor(() => expect(startAskReliantChat).toHaveBeenCalledTimes(1));
    const args = startAskReliantChat.mock.calls[0][0];
    expect(args.project).toBe(shop);
    expect(args.prompt).toBe(
      "Issue ISS-7f3a in environment prod (forge project shop).\n\n" +
        "Fetch the details yourself with the forge CLI, starting with `reliant forge obs issue ISS-7f3a prod`."
    );
    expect(args.workspaceName).toMatch(/^ask-issue-iss-7f3a-[a-z0-9]+$/);
    expect(screen.queryByTestId("ask-reliant-projects")).toBeNull();
  });

  it("asks which project when none declares the forge project, preselecting the current one", async () => {
    const [one, two] = [project("a", "blog"), project("b")];
    setProjects([one, two], two);
    render(
      <ForgeConsoleContext.Provider value={consoleFor("shop", null)}>
        <AskReliantButton subject={issue} />
      </ForgeConsoleContext.Provider>
    );
    await userEvent.click(screen.getByTestId("ask-reliant"));

    expect(await screen.findByText(/No project here declares this forge project/)).toBeInTheDocument();
    expect(startAskReliantChat).not.toHaveBeenCalled();
    expect(screen.getByRole("radio", { name: /Project b/ })).toBeChecked();

    await userEvent.click(screen.getByRole("radio", { name: /Project a/ }));
    await userEvent.click(screen.getByTestId("ask-reliant-start"));
    await waitFor(() => expect(startAskReliantChat).toHaveBeenCalledTimes(1));
    expect(startAskReliantChat.mock.calls[0][0].project).toBe(one);
    await waitFor(() => expect(screen.queryByTestId("ask-reliant-projects")).toBeNull());
  });

  it("keeps the picker open and says so when the start fails, so the reader can try again", async () => {
    setProjects([project("a")], project("a"));
    startAskReliantChat.mockRejectedValueOnce(new Error("machine asleep"));
    render(<AskReliantButton subject={issue} forgeProject="shop" />);
    await userEvent.click(screen.getByTestId("ask-reliant"));
    await userEvent.click(await screen.findByTestId("ask-reliant-start"));

    await waitFor(() => expect(toastError).toHaveBeenCalledWith(expect.objectContaining({ message: "machine asleep" })));
    expect(screen.getByTestId("ask-reliant-projects")).toBeInTheDocument();
  });

  it("hands the new chat's id to the surface that offered it", async () => {
    setProjects([project("a", "shop")], null);
    const onChatCreated = vi.fn();
    render(
      <ForgeConsoleContext.Provider value={consoleFor("shop", "a")}>
        <AskReliantButton subject={issue} onChatCreated={onChatCreated} />
      </ForgeConsoleContext.Provider>
    );
    await userEvent.click(screen.getByTestId("ask-reliant"));
    await waitFor(() => expect(startAskReliantChat).toHaveBeenCalled());

    // The composer calls this when the reader sends, after this component is gone.
    startAskReliantChat.mock.calls[0][0].onChatCreated("chat-9");
    expect(onChatCreated).toHaveBeenCalledWith("chat-9");
  });

  it("lists the project's environments when the subject's own is unknown", async () => {
    setProjects([project("a", "shop")], null);
    render(
      <ForgeConsoleContext.Provider value={consoleFor("shop", "a")}>
        <AskReliantButton subject={{ kind: "trace", traceId: "abc123", env: null }} />
      </ForgeConsoleContext.Provider>
    );
    await userEvent.click(screen.getByTestId("ask-reliant"));
    await waitFor(() => expect(startAskReliantChat).toHaveBeenCalled());
    expect(startAskReliantChat.mock.calls[0][0].prompt).toContain("one of dev, prod");
  });

  it("works from a menu: the hook returns ask() and the dialog to render", async () => {
    setProjects([project("a", "shop")], null);
    const onChatCreated = vi.fn();
    function Menu() {
      const { ask, dialog } = useAskReliant();
      return (
        <>
          <button type="button" onClick={() => void ask({ kind: "alert", rule: "HighErrorRate", fingerprint: "f1", env: "prod" }, onChatCreated)}>
            ask
          </button>
          {dialog}
        </>
      );
    }
    render(
      <ObsAskContextValue.Provider value={{ environments: [{ id: "e1", name: "prod" }], forgeProject: "shop" }}>
        <Menu />
      </ObsAskContextValue.Provider>
    );
    await userEvent.click(screen.getByText("ask"));
    await waitFor(() => expect(startAskReliantChat).toHaveBeenCalled());
    expect(startAskReliantChat.mock.calls[0][0].prompt).toContain("Alert rule HighErrorRate (fingerprint f1) in environment prod (forge project shop).");
    expect(startAskReliantChat.mock.calls[0][0].onChatCreated).toBe(onChatCreated);
  });
});

describe("Investigate in chat / Fix with agent", () => {
  beforeEach(() => {
    startAskReliantChat.mockReset().mockResolvedValue({ worktreeId: "w1" });
    toastError.mockReset();
    toastWarning.mockReset();
    setProjects([project("a", "shop")], null);
  });

  const renderActions = (props: Partial<Parameters<typeof AskReliantActions>[0]> = {}) =>
    render(
      <ForgeConsoleContext.Provider value={consoleFor("shop", "a")}>
        <AskReliantActions subject={issue} {...props} />
      </ForgeConsoleContext.Provider>
    );

  it("investigate leaves an investigation prompt unsent on an ask- branch", async () => {
    const onChatCreated = vi.fn();
    renderActions({ onChatCreated });
    await userEvent.click(screen.getByTestId("ask-reliant-investigate"));

    await waitFor(() => expect(startAskReliantChat).toHaveBeenCalledTimes(1));
    const args = startAskReliantChat.mock.calls[0][0];
    expect(args.send).toBe(false);
    expect(args.workspaceName).toMatch(/^ask-issue-iss-7f3a-/);
    expect(args.prompt).toMatch(/Don't change any code yet/);
    expect(args.onChatCreated).toBe(onChatCreated);
  });

  it("fix sends a fix prompt at once, on a fix- branch", async () => {
    renderActions();
    await userEvent.click(screen.getByTestId("ask-reliant-fix"));

    await waitFor(() => expect(startAskReliantChat).toHaveBeenCalledTimes(1));
    const args = startAskReliantChat.mock.calls[0][0];
    expect(args.send).toBe(true);
    expect(args.workspaceName).toMatch(/^fix-issue-iss-7f3a-/);
    expect(args.prompt).toMatch(/Then fix it\./);
    expect(toastWarning).not.toHaveBeenCalled();
  });

  it("says the agent did not start, and where its prompt is, when the send fails", async () => {
    startAskReliantChat.mockResolvedValueOnce({ worktreeId: "w1", sendError: new Error("machine asleep") });
    renderActions();
    await userEvent.click(screen.getByTestId("ask-reliant-fix"));

    await waitFor(() => expect(toastWarning).toHaveBeenCalledWith(expect.stringMatching(/machine asleep.*waiting in the composer/)));
    expect(toastError).not.toHaveBeenCalled();
  });

  it("leads with a chat already started from the subject, and keeps a new one a menu away", async () => {
    const open = vi.fn();
    renderActions({ linkedChat: { label: "Fix the deadline", open } });
    expect(screen.queryByTestId("ask-reliant-investigate")).toBeNull();

    await userEvent.click(screen.getByTestId("ask-reliant-open-chat"));
    expect(open).toHaveBeenCalledTimes(1);

    await userEvent.click(screen.getByTestId("ask-reliant-menu"));
    await userEvent.click(await screen.findByTestId("ask-reliant-fix"));
    await waitFor(() => expect(startAskReliantChat).toHaveBeenCalledTimes(1));
    expect(startAskReliantChat.mock.calls[0][0].send).toBe(true);
  });

  it("the row menu offers both", async () => {
    render(
      <ForgeConsoleContext.Provider value={consoleFor("shop", "a")}>
        <AskReliantMenu subject={issue} testId="row-ask" />
      </ForgeConsoleContext.Provider>
    );
    await userEvent.click(screen.getByTestId("row-ask"));
    await userEvent.click(await screen.findByTestId("ask-reliant-investigate"));
    await waitFor(() => expect(startAskReliantChat).toHaveBeenCalledTimes(1));
    expect(startAskReliantChat.mock.calls[0][0].send).toBe(false);
  });
});
