// Copyright (c) 2025 Reliant Labs

/**
 * Kept databases on an app's Environments page (L1): the rows are this app's
 * only, Purge needs the database name typed and an org owner or admin, a
 * requested purge stays listed as requested, and a control plane that cannot
 * answer (no access, no rpc) shows nothing rather than an error.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";

import type { RetainedDatabase } from "@/services/forge/cloudEnvs";

const listRetainedDatabases = vi.fn();

// One test runs with a control plane, so the org scope follows the active org
// store (lib/orgScope); the rest run without one (no org, ready at once).
const cp = vi.hoisted(() => ({ on: false }));
vi.mock("@/services/controlPlane/config", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/controlPlane/config")>()),
  get hasControlPlane() {
    return cp.on;
  },
}));
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

import { useActiveOrgStore } from "@/store/activeOrgStore";

import { RetainedDatabases } from "../RetainedDatabases";
import { CONFIRM_WINDOW_MS, keptDataProgress, startKeptDataCheck, useKeptDataChecks } from "../keptDataChecks";

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
  cp.on = false;
  useActiveOrgStore.setState({ userId: null, chosenId: null, effectiveId: null, resolved: false, failure: null });
});

describe("kept databases", () => {
  it("reads in the tab's org, and not before the org is known", async () => {
    cp.on = true;
    useActiveOrgStore.setState({ userId: "u1", chosenId: null, effectiveId: null, resolved: false, failure: null });
    wrap();
    // Unresolved: nothing is sent, so no unscoped (every-org) list is asked for.
    expect(listRetainedDatabases).not.toHaveBeenCalled();
    act(() => {
      useActiveOrgStore.setState({ chosenId: "org-a", effectiveId: "org-a", resolved: true });
    });
    expect(await screen.findByTestId("retained-databases")).toBeTruthy();
    expect(listRetainedDatabases.mock.calls).toEqual([["org-a"]]);
  });

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
    // Unavailable is retried a bounded number of times before it is shown.
    const notice = await screen.findByTestId("retained-databases-unavailable", {}, { timeout: 10_000 });
    expect(notice.textContent).toContain("database unavailable");
  });
});

describe("confirming a keep-data delete", () => {
  // A fixed clock: the window is measured against it, never the wall clock.
  // Only Date is faked, so the page's poll and userEvent keep real timers.
  const NOW = new Date("2026-11-18T12:00:00.000Z").getTime();
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(NOW);
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  const check = (since = NOW, databases = ["shop-db"]) =>
    startKeptDataCheck({ environmentId: "env_gone", envName: "staging", project: "shop", databases, since });
  const kept = (id: string, databaseName: string) => ({ ...row(id, "shop", databaseName), environmentId: "env_gone" });

  it("says it is confirming, then clears once the databases are listed as kept", async () => {
    check();
    listRetainedDatabases.mockResolvedValue([]);
    wrap();
    expect(await screen.findByTestId("kept-data-pending-env_gone")).toHaveTextContent("Confirming that staging's databases were kept");

    listRetainedDatabases.mockResolvedValue([kept("r9", "shop-db")]);
    // The page re-reads on its poll; the listed row ends the check.
    await waitFor(() => expect(useKeptDataChecks.getState().checks).toEqual([]), { timeout: 15_000 });
    expect(screen.queryByTestId("kept-data-pending-env_gone")).toBeNull();
    expect(await screen.findByTestId("retained-database-r9")).toBeTruthy();
  });

  it("warns when a server ignored Keep data: nothing is listed once the window has passed", async () => {
    check(NOW - CONFIRM_WINDOW_MS - 1_000);
    listRetainedDatabases.mockResolvedValue([row("r2", "blog", "blog-db")]);
    wrap();
    const warning = await screen.findByTestId("kept-data-warning-env_gone");
    expect(warning).toHaveAttribute("role", "alert");
    expect(warning.textContent).toContain("can't confirm shop-db was kept");
    expect(warning.textContent).toContain("No kept database from staging is listed");
    expect(warning.textContent).toContain("the data of shop-db may have been deleted");

    await userEvent.click(screen.getByTestId("kept-data-dismiss-env_gone"));
    expect(useKeptDataChecks.getState().checks).toEqual([]);
  });

  it("warns at once when the control plane cannot list kept databases", async () => {
    check();
    listRetainedDatabases.mockRejectedValue(new ConnectError("not configured", Code.Unimplemented));
    wrap();
    const warning = await screen.findByTestId("kept-data-warning-env_gone");
    expect(warning.getAttribute("data-reason")).toBe("unsupported");
    expect(warning.textContent).toContain("can't list kept databases");
    expect(screen.queryByTestId("retained-databases")).toBeNull();
  });

  // Any failed read makes an outstanding check unverifiable, naming why;
  // none leaves it pending on a list it cannot read.
  it.each([
    ["PermissionDenied", Code.PermissionDenied, "no-access", "You can't view kept databases here."],
    ["Unauthenticated", Code.Unauthenticated, "no-access", "You can't view kept databases here."],
    ["Unavailable", Code.Unavailable, "unavailable", "Reliant couldn't check the kept databases."],
  ])("warns that a check cannot be confirmed when the read fails with %s", async (_name, code, reason, copy) => {
    check();
    listRetainedDatabases.mockRejectedValue(new ConnectError("refused", code));
    wrap();
    const warning = await screen.findByTestId("kept-data-warning-env_gone", {}, { timeout: 10_000 });
    expect(warning).toHaveAttribute("role", "alert");
    expect(warning.getAttribute("data-reason")).toBe(reason);
    expect(warning.textContent).toContain("can't confirm shop-db was kept");
    expect(warning.textContent).toContain(copy);
    expect(warning.textContent).toContain("Whether the data was kept is unknown");
    expect(screen.queryByTestId("kept-data-pending-env_gone")).toBeNull();
    // Bounded: a refusal is asked once, a transient failure three times, before the warning.
    expect(listRetainedDatabases).toHaveBeenCalledTimes(reason === "unavailable" ? 3 : 1);
    expect(useKeptDataChecks.getState().checks).toHaveLength(1);
  });

  it("goes back to confirming when an unavailable read recovers, and ends the check", async () => {
    check();
    listRetainedDatabases.mockRejectedValue(new ConnectError("busy", Code.Unavailable));
    wrap();
    const warning = await screen.findByTestId("kept-data-warning-env_gone", {}, { timeout: 10_000 });
    expect(warning.textContent).toContain("(busy)");
    listRetainedDatabases.mockResolvedValue([kept("r9", "shop-db")]);
    // The poll asks again; the listed database ends the check.
    await waitFor(() => expect(useKeptDataChecks.getState().checks).toEqual([]), { timeout: 15_000 });
    expect(screen.queryByTestId("kept-data-warning-env_gone")).toBeNull();
  });

  it("keeps confirming while only some of several databases are listed", async () => {
    check(NOW, ["shop-db", "shop-cache"]);
    listRetainedDatabases.mockResolvedValue([kept("r9", "shop-db")]);
    wrap();
    // The kept one is listed, and the check stays open for the other.
    expect(await screen.findByTestId("retained-database-r9")).toBeTruthy();
    expect(screen.getByTestId("kept-data-pending-env_gone")).toHaveTextContent("1 of 2 listed so far");
    expect(useKeptDataChecks.getState().checks).toHaveLength(1);
  });

  it("warns past the window, naming the databases that were not kept", async () => {
    check(NOW - CONFIRM_WINDOW_MS - 1_000, ["shop-db", "shop-cache", "shop-search"]);
    listRetainedDatabases.mockResolvedValue([kept("r9", "shop-db")]);
    wrap();
    const warning = await screen.findByTestId("kept-data-warning-env_gone");
    expect(warning.textContent).toContain("can't confirm shop-cache, shop-search were kept");
    expect(warning.textContent).toContain("Only shop-db is listed as kept");
    expect(warning.textContent).toContain("the data of shop-cache, shop-search may have been deleted");
    expect(useKeptDataChecks.getState().checks).toHaveLength(1);
  });
});

describe("keptDataProgress", () => {
  const base = { environmentId: "env_1", envName: "prod", project: "shop", since: 0 };
  const listed = [
    { environmentId: "env_1", databaseName: "a" },
    { environmentId: "env_2", databaseName: "b" },
  ];

  it("is done only when every recorded database is listed for that environment", () => {
    expect(keptDataProgress({ ...base, databases: ["a"] }, listed)).toEqual({ kept: ["a"], missing: [], done: true });
    // "b" is listed, but for another environment.
    expect(keptDataProgress({ ...base, databases: ["a", "b"] }, listed)).toEqual({ kept: ["a"], missing: ["b"], done: false });
    expect(keptDataProgress({ ...base, databases: ["a"] }, [])).toEqual({ kept: [], missing: ["a"], done: false });
  });

  it("with no names recorded, asks only for some database of the environment", () => {
    expect(keptDataProgress({ ...base, databases: [] }, listed).done).toBe(true);
    expect(keptDataProgress({ ...base, environmentId: "env_3", databases: [] }, listed).done).toBe(false);
  });
});
