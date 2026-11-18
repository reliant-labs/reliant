// Copyright (c) 2025 Reliant Labs

/**
 * Kept databases on an app's Environments page (L1): the rows are this app's
 * only, Purge needs the database name typed and an org owner or admin, a
 * requested purge stays listed as requested, and a control plane that cannot
 * answer (no access, no rpc) shows nothing rather than an error.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";

import type { RetainedDatabase } from "@/services/forge/cloudEnvs";

const listRetainedDatabases = vi.fn();
const purgeRetainedDatabase = vi.fn();
let role: string | null = "owner";

vi.mock("@/services/forge/cloudEnvs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/services/forge/cloudEnvs")>();
  return {
    ...actual,
    hasCloudControlPlane: () => true,
    listRetainedDatabases: (...args: unknown[]) => listRetainedDatabases(...args),
    purgeRetainedDatabase: (...args: unknown[]) => purgeRetainedDatabase(...args),
  };
});

vi.mock("@/hooks/useOrgs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/hooks/useOrgs")>();
  return {
    ...actual,
    useActiveOrg: () => ({
      orgs: [],
      active: role ? { id: "org_1", name: "Acme", slug: "acme", role, isOwner: role === "owner", isDefault: true } : null,
      activeOrgId: role ? "org_1" : null,
      defaultOrgId: "org_1",
      canSwitch: false,
      isLoading: false,
      error: null,
      select: () => {},
    }),
  };
});

import { RetainedDatabases } from "../RetainedDatabases";
import { CONFIRM_WINDOW_MS, startKeptDataCheck, useKeptDataChecks } from "../keptDataChecks";

function row(id: string, project: string, databaseName: string, environmentName = "staging"): RetainedDatabase {
  return {
    id,
    environmentId: `env_${id}`,
    environmentName,
    project,
    databaseName,
    storageGib: 10,
    retainedAt: "2026-11-10T12:00:00.000Z",
  };
}

function wrap() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const view = render(
    <QueryClientProvider client={client}>
      <RetainedDatabases project="shop" />
    </QueryClientProvider>
  );
  /** Resolves once the list read has answered (data or error). */
  const settled = () =>
    waitFor(() => {
      const queries = client.getQueryCache().getAll();
      expect(queries.length).toBeGreaterThan(0);
      expect(queries.every((query) => query.state.status !== "pending")).toBe(true);
    });
  return { ...view, settled };
}

beforeEach(() => {
  role = "owner";
  listRetainedDatabases.mockReset().mockResolvedValue([
    row("r1", "shop", "shop-db", "staging"),
    row("r2", "blog", "blog-db", "prod"),
  ]);
  purgeRetainedDatabase.mockReset().mockResolvedValue(undefined);
  useKeptDataChecks.setState({ checks: [] });
});

describe("kept databases", () => {
  it("lists this app's kept databases only, with size and where they came from", async () => {
    wrap();
    const card = await screen.findByTestId("retained-databases");
    expect(card.textContent).toContain("shop-db");
    expect(card.textContent).toContain("staging");
    expect(card.textContent).toContain("10 GiB");
    expect(card.textContent).toContain("keep billing as storage");
    expect(card.textContent).not.toContain("blog-db");
  });

  it("purges only after the database name is typed, then shows the purge as requested", async () => {
    wrap();
    await userEvent.click(await screen.findByTestId("retained-database-purge-r1"));
    const dialog = await screen.findByTestId("purge-retained-database-dialog");
    expect(dialog.textContent).toContain("cannot be undone");

    const confirm = screen.getByTestId("purge-retained-database-confirm") as HTMLButtonElement;
    expect(confirm.disabled).toBe(true);
    await userEvent.type(screen.getByTestId("purge-retained-database-input"), "shop-d");
    expect(confirm.disabled).toBe(true);
    await userEvent.type(screen.getByTestId("purge-retained-database-input"), "b");
    expect(confirm.disabled).toBe(false);
    await userEvent.click(confirm);

    await waitFor(() => expect(purgeRetainedDatabase).toHaveBeenCalledWith("r1"));
    expect(await screen.findByTestId("retained-database-purging-r1")).toHaveTextContent("Purge requested");
    expect(screen.queryByTestId("retained-database-purge-r1")).toBeNull();
  });

  it("words a refused purge", async () => {
    purgeRetainedDatabase.mockRejectedValue(new ConnectError("insufficient role", Code.PermissionDenied));
    wrap();
    await userEvent.click(await screen.findByTestId("retained-database-purge-r1"));
    await userEvent.type(await screen.findByTestId("purge-retained-database-input"), "shop-db");
    await userEvent.click(screen.getByTestId("purge-retained-database-confirm"));
    const error = await screen.findByTestId("purge-retained-database-error");
    expect(error.textContent).toContain("Purging a kept database needs an org owner or admin.");
    expect(screen.queryByTestId("retained-database-purging-r1")).toBeNull();
  });

  it("shows a member the list without Purge, and says who can", async () => {
    role = "member";
    wrap();
    await screen.findByTestId("retained-databases");
    expect(screen.queryByTestId("retained-database-purge-r1")).toBeNull();
    expect(screen.getByTestId("retained-databases-admin-only").textContent).toContain("org owner or admin");
  });

  it("renders nothing when the app has no kept databases", async () => {
    listRetainedDatabases.mockResolvedValue([row("r2", "blog", "blog-db")]);
    const { container, settled } = wrap();
    await settled();
    expect(container.textContent).toBe("");
  });

  it("renders nothing when the control plane does not serve the list", async () => {
    listRetainedDatabases.mockRejectedValue(new ConnectError("not configured", Code.Unimplemented));
    const { container, settled } = wrap();
    await settled();
    expect(container.textContent).toBe("");
  });

  it("says so when the list could not be read", async () => {
    listRetainedDatabases.mockRejectedValue(new ConnectError("database unavailable", Code.Unavailable));
    wrap();
    // Unavailable is retried once before it is shown.
    const notice = await screen.findByTestId("retained-databases-unavailable", {}, { timeout: 10_000 });
    expect(notice.textContent).toContain("database unavailable");
  });
});

describe("confirming a keep-data delete", () => {
  const check = (since = Date.now()) =>
    startKeptDataCheck({ environmentId: "env_gone", envName: "staging", project: "shop", databases: ["shop-db"], since });

  it("says it is confirming, then clears once the databases are listed as kept", async () => {
    check();
    listRetainedDatabases.mockResolvedValue([]);
    wrap();
    expect(await screen.findByTestId("kept-data-pending-env_gone")).toHaveTextContent("Confirming that staging's databases were kept");

    listRetainedDatabases.mockResolvedValue([{ ...row("r9", "shop", "shop-db"), environmentId: "env_gone" }]);
    // The page re-reads on its poll; the listed row ends the check.
    await waitFor(() => expect(useKeptDataChecks.getState().checks).toEqual([]), { timeout: 15_000 });
    expect(screen.queryByTestId("kept-data-pending-env_gone")).toBeNull();
    expect(await screen.findByTestId("retained-database-r9")).toBeTruthy();
  });

  it("warns when a server ignored Keep data: nothing is listed once the window has passed", async () => {
    check(Date.now() - CONFIRM_WINDOW_MS - 1_000);
    listRetainedDatabases.mockResolvedValue([row("r2", "blog", "blog-db")]);
    wrap();
    const warning = await screen.findByTestId("kept-data-warning-env_gone");
    expect(warning).toHaveAttribute("role", "alert");
    expect(warning.textContent).toContain("can't confirm shop-db was kept");
    expect(warning.textContent).toContain("the data may have been deleted");

    await userEvent.click(screen.getByTestId("kept-data-dismiss-env_gone"));
    expect(useKeptDataChecks.getState().checks).toEqual([]);
  });

  it("warns at once when the control plane cannot list kept databases", async () => {
    check();
    listRetainedDatabases.mockRejectedValue(new ConnectError("not configured", Code.Unimplemented));
    wrap();
    const warning = await screen.findByTestId("kept-data-warning-env_gone");
    expect(warning.textContent).toContain("can't list kept databases");
    expect(screen.queryByTestId("retained-databases")).toBeNull();
  });
});
