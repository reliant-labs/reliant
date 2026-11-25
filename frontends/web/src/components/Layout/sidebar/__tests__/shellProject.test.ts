import { describe, expect, it } from "vitest";

import type { AppChipModel, CatalogRow, ProjectCatalog, ProjectCatalogRow } from "@/services/projects/catalog";
import type { Project } from "@/store/projectStore";

import { pickTarget } from "../ProjectSwitcher";
import { deployedAppOf, routeProjectOf, rowForRef, sectionsAppOf } from "../shellProject";

const chip = (name: string, state: AppChipModel["state"], link: AppChipModel["link"] = "source"): AppChipModel => ({
  name,
  link,
  state,
  isStaticSite: false,
  environments: [],
});

const projectRow = (id: string, name: string, apps: AppChipModel[]): ProjectCatalogRow => ({
  kind: "project",
  key: id,
  name,
  activityAt: 0,
  project: { id, name, path: `/code/${name}`, is_git_repo: true, last_active: "", created_at: "", updated_at: "", is_forge: apps.length > 0 } as Project,
  shared: false,
  repos: [],
  apps,
  checkouts: [],
  hasCheckout: true,
});

const shop = projectRow("p-shop", "shopfront", [chip("shopfront", "live"), chip("shopfront-admin", "not_deployed", "not_hosted")]);
const docs = projectRow("p-docs", "docs-site", []);
const statusPage: CatalogRow = { kind: "app", key: "app:status-page", name: "status-page", activityAt: 0, app: chip("status-page", "live", null) };
const catalog: ProjectCatalog = { orgId: "org", hostedAppsState: "ok", rows: [shop, docs, statusPage] };

describe("the project shell's current project", () => {
  it("reads a chat route as its chat, and a console route as its app", () => {
    expect(routeProjectOf("/c/chat-1", {})).toEqual({ chatId: "chat-1" });
    expect(routeProjectOf("/o/acme/c/chat-1/t/th-1", {})).toEqual({ chatId: "chat-1" });
    expect(routeProjectOf("/forge/p/shopfront/data", {})).toEqual({ ref: { kind: "app", name: "shopfront" }, explicit: false });
    expect(routeProjectOf("/o/acme/apps/shopfront", {})).toEqual({ ref: { kind: "app", name: "shopfront" }, explicit: false });
    expect(routeProjectOf("/project/p-docs", {})).toEqual({ ref: { kind: "project", id: "p-docs" }, explicit: true });
    expect(routeProjectOf("/projects/p-docs/settings", {})).toEqual({ ref: { kind: "project", id: "p-docs" }, explicit: true });
    expect(routeProjectOf("/new", { project: "p-shop" })).toEqual({ ref: { kind: "project", id: "p-shop" }, explicit: false });
    expect(routeProjectOf("/inbox", {})).toBeNull();
    expect(routeProjectOf("/projects", {})).toBeNull();
  });

  it("resolves an app to the project that deploys it, else to its own row", () => {
    expect(rowForRef(catalog, { kind: "app", name: "shopfront" })).toBe(shop);
    // A forge.yaml that names an app the org does not host is not that app's project.
    expect(rowForRef(catalog, { kind: "app", name: "shopfront-admin" })).toBeUndefined();
    expect(rowForRef(catalog, { kind: "app", name: "status-page" })).toBe(statusPage);
    expect(rowForRef(catalog, { kind: "project", id: "p-docs" })).toBe(docs);
  });

  it("gives every project app sections, and knows which ones have something deployed", () => {
    expect(deployedAppOf(shop)).toBe("shopfront");
    expect(sectionsAppOf(shop)).toBe("shopfront");
    expect(deployedAppOf(docs)).toBeNull();
    expect(sectionsAppOf(docs)).toBe("docs-site");
    expect(sectionsAppOf(statusPage)).toBe("status-page");
  });

  // The 2026-11-23 incident's shape: a parent checkout (reliant-labs) whose nested
  // `control-plane/` repo declares the control-plane app, beside the
  // control-plane checkout itself, which declares it at its root.
  describe("a multi-repo project and a nested repository's app", () => {
    const repo = (id: string, relativePath: string) => ({ id, name: id, relativePath, remote: "" });
    const at = (app: AppChipModel, repoId: string, subpath = ""): AppChipModel => ({ ...app, repoId, subpath });
    const parent: ProjectCatalogRow = {
      ...projectRow("p-parent", "reliant-labs", [at(chip("control-plane", "live"), "r-cp-nested"), at(chip("reliant", "not_deployed", "not_hosted"), "r-reliant")]),
      repos: [repo("r-root", ""), repo("r-cp-nested", "control-plane"), repo("r-reliant", "reliant")],
    };
    const checkout: ProjectCatalogRow = {
      ...projectRow("p-cp", "control-plane", [at(chip("control-plane", "live"), "r-cp")]),
      repos: [repo("r-cp", ".")],
    };
    const mono: ProjectCatalogRow = {
      ...projectRow("p-mono", "mono", [at(chip("web", "not_deployed"), "r-mono", "apps/web"), at(chip("vendored", "live"), "r-vendor")]),
      repos: [repo("r-mono", ""), repo("r-vendor", "third_party/vendored")],
    };
    const multi: ProjectCatalog = { orgId: "org", hostedAppsState: "ok", rows: [parent, checkout, mono] };

    it("does not adopt the nested repo's app as the parent's own", () => {
      expect(deployedAppOf(parent)).toBeNull();
      expect(sectionsAppOf(parent)).toBe("reliant-labs");
      expect(deployedAppOf(checkout)).toBe("control-plane");
    });

    it("keeps an app in a directory of the project's own repo, but not a vendored repo's", () => {
      expect(sectionsAppOf(mono)).toBe("web");
      expect(deployedAppOf(mono)).toBeNull();
    });

    it("adopts a nested repo's app when the project declares it", () => {
      const declared: ProjectCatalogRow = { ...parent, project: { ...parent.project, is_forge: true, forge_project_name: "control-plane" } };
      expect(sectionsAppOf(declared)).toBe("control-plane");
    });

    it("resolves the app to the checkout whose own code it is, whatever the list order", () => {
      expect(rowForRef(multi, { kind: "app", name: "control-plane" })).toBe(checkout);
      // With no project owning it, a project holding it anywhere still answers.
      expect(rowForRef({ ...multi, rows: [parent] }, { kind: "app", name: "control-plane" })).toBe(parent);
    });
  });

  it("moves to the same section of the picked project's app, else to its chats", () => {
    expect(pickTarget({ kind: "project", row: docs }, "/forge/p/shopfront/observability/errors").href).toBe("/forge/p/docs-site/observability/errors");
    expect(pickTarget({ kind: "project", row: shop }, "/forge/p/docs-site/releases/v1").href).toBe("/forge/p/shopfront/releases");
    expect(pickTarget({ kind: "project", row: docs }, "/c/chat-1")).toEqual({ key: "p-docs", href: "/project/p-docs" });
    expect(pickTarget({ kind: "app", app: chip("status-page", "live", null) }, "/inbox")).toEqual({ key: "app:status-page", href: "/forge/p/status-page" });
  });
});
