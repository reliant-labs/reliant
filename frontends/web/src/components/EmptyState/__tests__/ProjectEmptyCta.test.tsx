// Copyright (c) 2026 Reliant Labs

import type React from "react";
import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";

vi.mock("@/components/Machines/AddMachineSheet", () => ({
  AddMachineSheet: ({ open, initialKind }: { open: boolean; initialKind?: string }) =>
    open ? <div role="dialog" aria-label="Add an agent sandbox" data-kind={initialKind} /> : null,
}));

const { ProjectEmptyCta, ProjectEmptyCtaRow, MIGRATE_LABEL, ASK_DEPLOY_LABEL } = await import("../ProjectEmptyCta");
const { ConnectSandboxCta } = await import("../ConnectSandboxCta");
const { learnUrl } = await import("@/lib/learnLinks");
const { useProjectStore } = await import("@/store/projectStore");
const { ForgeConsoleContext } = await import("@/components/Forge/Console/consoleContextValue");
const { GettingStarted } = await import("@/components/Forge/GettingStarted");
type ForgeConsole = NonNullable<React.ContextType<typeof ForgeConsoleContext>>;

/** A forge console whose app maps to `projectId` (null: no Reliant project declares it). */
const consoleOf = (projectId: string | null) => ({ projectId, page: { kind: "domains" } }) as unknown as ForgeConsole;

function withCurrentProject(id: string) {
  useProjectStore.setState({ currentProject: { id, name: id } as never });
}

describe("ProjectEmptyCta", () => {
  it("offers the page's action first, then deploy with forge, migrate and the guide", () => {
    render(
      <ProjectEmptyCta title="No custom domains yet" action={<button type="button">Add domain</button>} projectId="p-1" learn="domains">
        <p>Serve the app on a hostname you own.</p>
      </ProjectEmptyCta>,
    );
    const row = screen.getByTestId("project-empty-cta");
    const controls = Array.from(row.querySelectorAll("a, button"));
    expect(controls.map((el) => el.textContent?.trim())).toEqual(["Add domain", ASK_DEPLOY_LABEL, MIGRATE_LABEL, "Learn"]);
    expect(within(row).getByRole("link", { name: ASK_DEPLOY_LABEL })).toHaveAttribute("href", expect.stringContaining("/project/p-1"));
    expect(within(row).getByTestId("learn-link-domains")).toHaveAttribute("href", learnUrl("domains"));
  });

  it("never runs Migrate in the shell's current project when the caller says there is none", () => {
    withCurrentProject("p-unrelated");
    render(<ProjectEmptyCtaRow deploy={false} learn={false} projectId={null} />);
    // A link to the workflow's page, not the Run dialog's button in p-unrelated.
    expect(screen.getByRole("link", { name: MIGRATE_LABEL }).getAttribute("href")).toContain(encodeURIComponent("builtin://forge-migrate"));
    useProjectStore.setState({ currentProject: null });
  });

  it("never runs Migrate in the shell's current project when the console's app maps to no project", () => {
    withCurrentProject("p-unrelated");
    render(
      <ForgeConsoleContext.Provider value={consoleOf(null)}>
        <ProjectEmptyCtaRow deploy={false} learn={false} />
      </ForgeConsoleContext.Provider>,
    );
    expect(screen.getByRole("link", { name: MIGRATE_LABEL }).getAttribute("href")).toContain(encodeURIComponent("builtin://forge-migrate"));
    useProjectStore.setState({ currentProject: null });
  });

  it("runs Migrate in the console's project", () => {
    withCurrentProject("p-unrelated");
    render(
      <ForgeConsoleContext.Provider value={consoleOf("p-console")}>
        <ProjectEmptyCtaRow deploy={false} learn={false} />
      </ForgeConsoleContext.Provider>,
    );
    expect(screen.getByRole("button", { name: MIGRATE_LABEL })).toBeInTheDocument();
    useProjectStore.setState({ currentProject: null });
  });

  it("sends Migrate to the Move to Forge workflow's page when there is no project to run it in", () => {
    render(<ProjectEmptyCtaRow deploy={false} learn={false} projectId={null} />);
    expect(screen.getByRole("link", { name: MIGRATE_LABEL }).getAttribute("href")).toContain(encodeURIComponent("builtin://forge-migrate"));
    expect(screen.queryByRole("link", { name: /learn/i })).toBeNull();
  });
});

describe("GettingStarted's project-shell actions", () => {
  const props = { icon: null, title: "No releases yet", about: "Releases show here.", agent: <button type="button">Ask</button>, testId: "gs" };

  it("adds Migrate and Learn beside the agent on a console page", () => {
    render(
      <ForgeConsoleContext.Provider value={consoleOf("p-console")}>
        <GettingStarted {...props} />
      </ForgeConsoleContext.Provider>,
    );
    expect(screen.getByRole("button", { name: MIGRATE_LABEL })).toBeInTheDocument();
  });

  it("adds nothing beside the agent outside the project shell unless asked", () => {
    const { unmount } = render(<GettingStarted {...props} />);
    expect(screen.queryByText(MIGRATE_LABEL)).toBeNull();
    expect(screen.getByRole("button", { name: "Ask" })).toBeInTheDocument();
    unmount();
    render(<GettingStarted {...props} projectCta={{ learn: false }} />);
    expect(screen.getByText(MIGRATE_LABEL)).toBeInTheDocument();
  });
});

describe("ConnectSandboxCta", () => {
  it("opens the one Add sheet on your own machine or on a Reliant sandbox", async () => {
    const { default: userEvent } = await import("@testing-library/user-event");
    const user = userEvent.setup();
    render(<ConnectSandboxCta />);
    expect(screen.getByRole("heading", { name: "Connect an agent sandbox to start chats" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Use your own machine" }));
    expect(screen.getByRole("dialog")).toHaveAttribute("data-kind", "self");
    await user.click(screen.getByRole("button", { name: "Provision a Reliant sandbox" }));
    expect(screen.getByRole("dialog")).toHaveAttribute("data-kind", "cloud");
    expect(screen.getByTestId("learn-link-sandboxes")).toHaveAttribute("href", learnUrl("sandboxes"));
  });
});
