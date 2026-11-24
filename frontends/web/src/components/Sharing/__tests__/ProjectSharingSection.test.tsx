/**
 * A project's Settings › Sharing: private or shared with its org (and since
 * when), the button that opens the disclosure dialog, and nothing at all
 * without a control plane, for a project of no org, or for a viewer.
 */

import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { Project } from "@/store/projectStore";

const config = vi.hoisted(() => ({ hasControlPlane: true }));
vi.mock("@/services/controlPlane/config", () => ({
  get hasControlPlane() {
    return config.hasControlPlane;
  },
}));
vi.mock("@/hooks/useOrgs", () => ({ useActiveOrg: () => ({ orgs: [{ id: "acme", name: "Acme", role: "member" }] }) }));
vi.mock("../ShareProjectDialog", () => ({
  ShareProjectDialog: ({ open, project }: { open: boolean; project: Project }) =>
    open ? <div data-testid="share-project-dialog" data-visibility={project.visibility ?? "private"} /> : null,
}));

const { ProjectSharingSection } = await import("../ProjectSharingSection");

function project(over: Partial<Project> = {}): Project {
  return {
    id: "p1",
    name: "shop",
    path: "/home/me/shop",
    is_git_repo: true,
    is_forge: false,
    last_active: "2026-01-01T00:00:00Z",
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    org_id: "acme",
    ...over,
  };
}

beforeEach(() => {
  config.hasControlPlane = true;
});

describe("ProjectSharingSection", () => {
  it("says a private project is private and opens the dialog to share it", () => {
    render(<ProjectSharingSection project={project()} />);
    expect(screen.getByTestId("project-sharing-state")).toHaveTextContent("Private");
    expect(screen.queryByTestId("share-project-dialog")).toBeNull();
    fireEvent.click(screen.getByTestId("project-sharing-toggle"));
    expect(screen.getByTestId("share-project-dialog")).toHaveAttribute("data-visibility", "private");
  });

  it("says a shared project is shared with its org, and since when", () => {
    render(<ProjectSharingSection project={project({ visibility: "org", shared_at: "2026-01-02T03:04:05Z" })} />);
    expect(screen.getByTestId("project-sharing-state")).toHaveTextContent("Shared with Acme");
    expect(screen.getByTestId("project-sharing").querySelector("time")).toHaveAttribute("dateTime", "2026-01-02T03:04:05Z");
    expect(screen.getByTestId("project-sharing-toggle")).toHaveTextContent("Stop sharing");
  });

  it("shows nothing without a control plane, for a project of no org, or for a viewer", () => {
    config.hasControlPlane = false;
    const { container, rerender } = render(<ProjectSharingSection project={project()} />);
    expect(container).toBeEmptyDOMElement();
    config.hasControlPlane = true;
    rerender(<ProjectSharingSection project={project({ org_id: undefined })} />);
    expect(container).toBeEmptyDOMElement();
    rerender(<ProjectSharingSection project={project({ viewer_access: true })} />);
    expect(container).toBeEmptyDOMElement();
  });
});
