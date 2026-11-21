// Copyright (c) 2025 Reliant Labs

import { fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { ProjectCatalogRow } from "@/services/projects/catalog";
import { useAddProjectDialogStore } from "@/store/addProjectDialogStore";

vi.mock("@tanstack/react-router", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-router")>()),
  useNavigate: () => vi.fn(),
}));

import { AppCodeView } from "../AppCodeCard";
import type { AppCode, AppCodeProject } from "../appCodeModel";

const row = (id: string, shared = false) => ({ kind: "project", key: id, name: id, shared, project: { id, name: id, path: `/p/${id}` } }) as unknown as ProjectCatalogRow;
const project = (id: string, link: AppCodeProject["link"], shared = false): AppCodeProject => ({ row: row(id, shared), link, subpath: "" });

const code = (over: Partial<AppCode> = {}): AppCode => ({
  state: "ok",
  sources: [{ remote: "github.com/acme/shop", subpath: "services/api", commit: "a1b2c3d4e5f6", lastSeenAt: "2026-11-18T12:00:00Z" }],
  warning: "",
  yours: [],
  shared: [],
  ...over,
});

describe("AppCodeView", () => {
  beforeEach(() => useAddProjectDialogStore.getState().closeAddProject());

  it("says where the app is deployed from, and lists the projects with their link and actions", () => {
    const onStartChat = vi.fn();
    render(<AppCodeView app="shop" code={code({ yours: [project("main", "source"), project("fork", "name")], shared: [project("sams", "name", true)] })} onStartChat={onStartChat} />);

    expect(screen.getByTestId("app-code-sources")).toHaveTextContent("github.com/acme/shop@services/api");
    expect(screen.getByTestId("app-code-sources")).toHaveTextContent("last deployed commit a1b2c3d");
    const yours = within(screen.getByTestId("app-code-yours"));
    expect(yours.getByTestId("app-code-project-main")).toHaveTextContent("deployed from here");
    expect(yours.getByTestId("app-code-project-fork")).toHaveTextContent("same name");
    fireEvent.click(yours.getByTestId("app-code-start-fork"));
    expect(onStartChat).toHaveBeenCalledWith("fork");
    // A teammate's project opens, and starts no chat.
    const shared = within(screen.getByTestId("app-code-shared"));
    expect(shared.getByRole("link", { name: "Open sams" })).toBeInTheDocument();
    expect(shared.queryByRole("button")).toBeNull();
    expect(screen.queryByTestId("app-code-get")).toBeNull();
  });

  it("shows the two-repositories warning", () => {
    render(<AppCodeView app="shop" code={code({ warning: "shop is deployed from two repositories." })} onStartChat={vi.fn()} />);
    expect(screen.getByTestId("app-code-warning")).toHaveTextContent("shop is deployed from two repositories.");
  });

  it("offers the code, prefilled from the recorded remote, when no project of the person's has it", () => {
    render(<AppCodeView app="shop" code={code()} onStartChat={vi.fn()} />);
    fireEvent.click(screen.getByTestId("app-code-get"));
    expect(useAddProjectDialogStore.getState()).toMatchObject({ isOpen: true, method: "clone", repoUrl: "https://github.com/acme/shop" });
  });

  it("says a local checkout, and opens the plain dialog to get code it cannot name", () => {
    render(<AppCodeView app="shop" code={code({ sources: [{ remote: "", subpath: "", commit: "abc1234", lastSeenAt: "" }] })} onStartChat={vi.fn()} />);
    expect(screen.getByTestId("app-code-sources")).toHaveTextContent("deployed from a local checkout");
    fireEvent.click(screen.getByTestId("app-code-get"));
    expect(useAddProjectDialogStore.getState()).toMatchObject({ isOpen: true, method: null, repoUrl: null });
  });

  it("says when the org's apps cannot be read", () => {
    const { rerender } = render(<AppCodeView app="shop" code={code({ state: "no_access", sources: [] })} onStartChat={vi.fn()} />);
    expect(screen.getByTestId("app-code-source-state")).toHaveTextContent("You can't read this org's apps");
    rerender(<AppCodeView app="shop" code={code({ state: "unavailable", sources: [] })} onStartChat={vi.fn()} />);
    expect(screen.getByTestId("app-code-source-state")).toHaveTextContent("unavailable right now");
  });
});
