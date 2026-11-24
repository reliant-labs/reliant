// Copyright (c) 2025 Reliant Labs

/**
 * Env teardown in the env header (control-plane unified-shell-and-projects.md,
 * Lifecycle, L1): the data choice on delete (explicit, never downgraded), a
 * bound domain sending the person to the Domains page (no combined unbind),
 * the storage note on Stop, and who may do what (D19).
 * Every wait is on an event (a call, an element), never a fixed sleep.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";

import type { ForgeDomain } from "@/services/forge/domains";

const setEnvironmentRunState = vi.fn();
const deleteEnvironment = vi.fn();
const listDomains = vi.fn();
const unbindDomain = vi.fn();
const listRetainedDatabases = vi.fn();
let role: string | null = "admin";

vi.mock("@/services/forge/cloudEnvs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/services/forge/cloudEnvs")>();
  return {
    ...actual,
    setEnvironmentRunState: (_transport: unknown, ...args: unknown[]) => setEnvironmentRunState(...args),
    deleteEnvironment: (_transport: unknown, ...args: unknown[]) => deleteEnvironment(...args),
    hasCloudControlPlane: () => true,
    listRetainedDatabases: (_transport: unknown, ...args: unknown[]) => listRetainedDatabases(...args),
  };
});

vi.mock("@/services/forge/domains", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/services/forge/domains")>();
  return {
    ...actual,
    hasControlPlane: () => true,
    listDomains: (_transport: unknown, ...args: unknown[]) => listDomains(...args),
    unbindDomain: (_transport: unknown, ...args: unknown[]) => unbindDomain(...args),
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

import { EnvLifecycleControls } from "../EnvPage/EnvLifecycleControls";
import { useKeptDataChecks } from "../Overview/keptDataChecks";
import { forgeCloudKeys } from "@/hooks/forge-queries";
import type { CloudEnvStatus } from "@/services/forge/cloudEnvs";

const STATUS: CloudEnvStatus = {
  verdict: "converged",
  currentPromotion: null,
  workloads: [
    { name: "api", tier: "backend", deployment_id: "dep_1", declared_run_state: "running", observed_state: "ready", verdict: "converged" },
    { name: "web", tier: "static", deployment_id: "dep_2", declared_run_state: "running", observed_state: "ready", verdict: "converged" },
    { name: "db", tier: "database", deployment_id: "dep_3", declared_run_state: "running", observed_state: "ready", verdict: "converged" },
  ],
};

function domain(id: string, hostname: string, environmentId: string | null): ForgeDomain {
  return {
    id,
    hostname,
    state: "live",
    origin: "external",
    requiredRecords: [],
    lastError: "",
    binding: environmentId ? { id: `b_${id}`, domainId: id, environmentId, target: "web", redirectTo: "" } : null,
  };
}

const DOMAIN_REFUSAL = new ConnectError(
  "that environment is still the target of custom domain(s) app.acme.dev; rebind or unbind them first (force does not override this)",
  Code.FailedPrecondition
);

let client: QueryClient;
let rerenderWith: (status: CloudEnvStatus) => void;
const onManageDomains = vi.fn();

function wrap(onDeleted = vi.fn(), status: CloudEnvStatus = STATUS) {
  client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const ui = (s: CloudEnvStatus) => (
    <QueryClientProvider client={client}>
      <EnvLifecycleControls
        environmentId="env_1"
        envName="prod"
        project="shop"
        status={s}
        onDeleted={onDeleted}
        onManageDomains={onManageDomains}
      />
    </QueryClientProvider>
  );
  const view = render(ui(status));
  rerenderWith = (next) => view.rerender(ui(next));
  return onDeleted;
}

/** Re-reads the kept-databases list, as a focus or an invalidation would. */
async function refetchRetained() {
  await act(async () => {
    await client.refetchQueries({ queryKey: forgeCloudKeys.retainedDatabases.all });
  });
}

function withDatabases(...names: string[]): CloudEnvStatus {
  return {
    ...STATUS,
    workloads: [
      ...STATUS.workloads.filter((w) => w.tier !== "database"),
      ...names.map((name, i) => ({
        name,
        tier: "database",
        deployment_id: `dep_db${i}`,
        declared_run_state: "running" as const,
        observed_state: "ready",
        verdict: "converged",
      })),
    ],
  };
}

function confirmButton() {
  return screen.getByTestId("delete-environment-confirm") as HTMLButtonElement;
}

/** No destructive or domain write was sent. */
function expectNoWrites() {
  expect(deleteEnvironment).not.toHaveBeenCalled();
  expect(unbindDomain).not.toHaveBeenCalled();
}

async function openDelete() {
  await userEvent.click(screen.getByTestId("env-danger-menu"));
  await userEvent.click(await screen.findByTestId("env-action-delete"));
  return screen.findByTestId("delete-environment-dialog");
}

async function typeName() {
  await userEvent.type(screen.getByTestId("delete-environment-confirm-input"), "prod");
}

beforeEach(() => {
  role = "admin";
  setEnvironmentRunState.mockReset().mockResolvedValue(undefined);
  deleteEnvironment.mockReset().mockResolvedValue(undefined);
  listDomains.mockReset().mockResolvedValue([domain("d_other", "other.acme.dev", "env_2")]);
  unbindDomain.mockReset().mockResolvedValue(undefined);
  onManageDomains.mockReset();
  listRetainedDatabases.mockReset().mockResolvedValue([]);
  useKeptDataChecks.setState({ checks: [] });
});

/** The Keep data option is enabled once the control plane is known to list kept databases. */
async function keepOffered() {
  await waitFor(() => expect((screen.getByTestId("delete-environment-data-keep") as HTMLInputElement).disabled).toBe(false));
}

describe("delete: what goes, what stays, and Keep data", () => {
  it("names what is deleted and what is kept", async () => {
    wrap(vi.fn(), {
      ...STATUS,
      workloads: STATUS.workloads.map((w) => (w.name === "web" ? { ...w, url: "https://shop-acme.reliantapps.dev" } : w)),
    });
    await openDelete();
    const removed = screen.getByTestId("delete-environment-removed").textContent ?? "";
    expect(removed).toContain("api, web, db");
    expect(removed).toContain("namespace");
    expect(removed).toContain("generated hostnames");
    expect(removed).toContain("https://shop-acme.reliantapps.dev");
    expect(removed).toContain("charges stop once the teardown finishes");
    const kept = screen.getByTestId("delete-environment-kept").textContent ?? "";
    expect(kept).toContain("Releases and their images");
    expect(kept).toContain("Custom domains");
  });

  // L3: a re-created environment does NOT get the old secrets back (design §11.3).
  it("says secrets must be set again, and never that a new environment gets them back", async () => {
    wrap();
    const dialog = await openDelete();
    expect(screen.getByTestId("delete-environment-removed").textContent).toContain(
      "A new prod does not get its secrets back: they have to be set again."
    );
    expect(screen.getByTestId("delete-environment-kept").textContent).not.toMatch(/secret/i);
    expect(dialog.textContent).not.toContain("gets them back");
  });

  it("destroys data only on an explicit Delete: nothing is preselected", async () => {
    const onDeleted = wrap();
    await openDelete();
    await keepOffered();
    expect((screen.getByTestId("delete-environment-data-delete") as HTMLInputElement).checked).toBe(false);
    expect((screen.getByTestId("delete-environment-data-keep") as HTMLInputElement).checked).toBe(false);
    await typeName();
    expect(confirmButton().disabled).toBe(true);
    await userEvent.click(confirmButton());
    expectNoWrites();

    await userEvent.click(screen.getByTestId("delete-environment-data-delete"));
    expect(confirmButton().disabled).toBe(false);
    await userEvent.click(confirmButton());
    await waitFor(() => expect(deleteEnvironment).toHaveBeenCalledWith("env_1", { keepData: false }));
    await waitFor(() => expect(onDeleted).toHaveBeenCalled());
  });

  it("Keep data sends keep_data and says the databases keep billing", async () => {
    wrap();
    await openDelete();
    const note = screen.getByTestId("delete-environment-data-note").textContent ?? "";
    expect(note).toContain("keep billing as storage");
    expect(note).toContain("Kept databases");
    await keepOffered();
    await userEvent.click(screen.getByTestId("delete-environment-data-keep"));
    await typeName();
    await userEvent.click(screen.getByTestId("delete-environment-confirm"));
    await waitFor(() => expect(deleteEnvironment).toHaveBeenCalledWith("env_1", { keepData: true }));
    // Not trusted: the delete leaves a check that the databases get listed as kept.
    await waitFor(() =>
      expect(useKeptDataChecks.getState().checks).toEqual([
        expect.objectContaining({ environmentId: "env_1", envName: "prod", project: "shop", databases: ["db"] }),
      ])
    );
  });

  it("offers no Keep data when the control plane does not serve kept databases, and says why", async () => {
    listRetainedDatabases.mockRejectedValue(new ConnectError("not configured", Code.Unimplemented));
    wrap();
    await openDelete();
    const note = await screen.findByTestId("delete-environment-keep-unavailable");
    expect(note.getAttribute("data-reason")).toBe("unsupported");
    expect(note.textContent).toContain("Keep data isn't offered");
    expect(screen.getByTestId("delete-environment-data-note").textContent).toContain("deleted with it, data included");
    expect((screen.getByTestId("delete-environment-data-keep") as HTMLInputElement).disabled).toBe(true);
    await typeName();
    expect(confirmButton().disabled).toBe(true);
    await userEvent.click(screen.getByTestId("delete-environment-data-delete"));
    await userEvent.click(screen.getByTestId("delete-environment-confirm"));
    await waitFor(() => expect(deleteEnvironment).toHaveBeenCalledWith("env_1", { keepData: false }));
    expect(useKeptDataChecks.getState().checks).toEqual([]);
  });

  // Any failed read hides Keep data, not only a missing rpc: a kept database
  // this person cannot list could be neither confirmed nor purged here.
  it.each([
    ["PermissionDenied", Code.PermissionDenied, "no-access", "You can't view kept databases here."],
    ["Unauthenticated", Code.Unauthenticated, "no-access", "You can't view kept databases here."],
    ["Unavailable", Code.Unavailable, "unavailable", "Reliant couldn't check the kept databases. Try again."],
  ])("offers no Keep data when the kept-databases read fails with %s", async (_name, code, reason, copy) => {
    listRetainedDatabases.mockRejectedValue(new ConnectError("refused", code));
    wrap();
    await openDelete();
    // Unavailable is asked again a bounded number of times first.
    const note = await screen.findByTestId("delete-environment-keep-unavailable", {}, { timeout: 10_000 });
    expect(note.getAttribute("data-reason")).toBe(reason);
    expect(note.textContent).toContain("Keep data isn't offered");
    expect(note.textContent).toContain(copy);
    expect((screen.getByTestId("delete-environment-data-keep") as HTMLInputElement).disabled).toBe(true);
    // A refusal is the same answer on a retry: asked once. A transient failure: three times.
    expect(listRetainedDatabases).toHaveBeenCalledTimes(reason === "unavailable" ? 3 : 1);
    expect(!!screen.queryByTestId("delete-environment-keep-retry")).toBe(reason === "unavailable");

    await typeName();
    await userEvent.click(screen.getByTestId("delete-environment-data-delete"));
    await userEvent.click(screen.getByTestId("delete-environment-confirm"));
    await waitFor(() => expect(deleteEnvironment).toHaveBeenCalledWith("env_1", { keepData: false }));
    expect(useKeptDataChecks.getState().checks).toEqual([]);
  });

  it("offers Keep data again once an unavailable read succeeds on Check again", async () => {
    listRetainedDatabases.mockRejectedValue(new ConnectError("busy", Code.Unavailable));
    wrap();
    await openDelete();
    const note = await screen.findByTestId("delete-environment-keep-unavailable", {}, { timeout: 10_000 });
    expect(note.textContent).toContain("(busy)");
    listRetainedDatabases.mockResolvedValue([]);
    await userEvent.click(screen.getByTestId("delete-environment-keep-retry"));
    await keepOffered();
    expect(screen.queryByTestId("delete-environment-keep-unavailable")).toBeNull();
  });

  it("keeps Keep data disabled while the kept-databases read is loading", async () => {
    listRetainedDatabases.mockReturnValue(new Promise(() => {}));
    wrap();
    await openDelete();
    await waitFor(() => expect(listRetainedDatabases).toHaveBeenCalled());
    const keep = screen.getByTestId("delete-environment-data-keep") as HTMLInputElement;
    expect(keep.disabled).toBe(true);
    expect(screen.getByTestId("delete-environment-data-note").textContent).toContain("Checking whether");
  });

  it("asks nothing about data for an environment that runs no database", async () => {
    wrap(vi.fn(), { ...STATUS, workloads: STATUS.workloads.filter((w) => w.tier !== "database") });
    await openDelete();
    expect(screen.getByTestId("delete-environment-no-databases")).toBeTruthy();
    expect(screen.queryByTestId("delete-environment-data-keep")).toBeNull();
    await typeName();
    await userEvent.click(confirmButton());
    await waitFor(() => expect(deleteEnvironment).toHaveBeenCalledWith("env_1", { keepData: false }));
  });
});

// L1: a selected Keep is never turned into a Delete. When what it was chosen
// against changes, the choice is void: nothing is sent until the person
// chooses again, and destroying data needs a fresh, explicit Delete.
describe("a chosen Keep is never downgraded", () => {
  async function chooseKeep() {
    await openDelete();
    await keepOffered();
    await userEvent.click(screen.getByTestId("delete-environment-data-keep"));
    await typeName();
    expect(confirmButton().disabled).toBe(false);
  }

  it.each([
    ["PermissionDenied", Code.PermissionDenied, "You can't view kept databases here."],
    ["Unavailable", Code.Unavailable, "Reliant couldn't check the kept databases."],
  ])("voids Keep when the kept-databases read then fails with %s", async (_name, code, copy) => {
    wrap();
    await chooseKeep();
    listRetainedDatabases.mockRejectedValue(new ConnectError("refused", code));
    await refetchRetained();

    const stale = await screen.findByTestId("delete-environment-choice-stale", {}, { timeout: 10_000 });
    expect(stale.getAttribute("data-kind")).toBe("keep");
    expect(stale.textContent).toContain("can't be honoured now");
    expect(stale.textContent).toContain(copy);
    expect(stale.textContent).toContain("Nothing has been deleted. Choose again.");
    expect(confirmButton().disabled).toBe(true);
    await userEvent.click(confirmButton());
    expect((screen.getByTestId("delete-environment-data-keep") as HTMLInputElement).disabled).toBe(true);
    expect((screen.getByTestId("delete-environment-data-delete") as HTMLInputElement).checked).toBe(false);
    expectNoWrites();

    // Only a fresh, explicit Delete destroys the data.
    await userEvent.click(screen.getByTestId("delete-environment-data-delete"));
    await userEvent.click(confirmButton());
    await waitFor(() => expect(deleteEnvironment).toHaveBeenCalledWith("env_1", { keepData: false }));
  });

  it("voids Keep when the database inventory changes, and keeps it when chosen again", async () => {
    wrap();
    await chooseKeep();
    rerenderWith(withDatabases("db", "cache"));

    const stale = await screen.findByTestId("delete-environment-choice-stale");
    expect(stale.textContent).toContain("the databases this environment runs changed since you chose");
    expect(confirmButton().disabled).toBe(true);
    await userEvent.click(confirmButton());
    expectNoWrites();

    await userEvent.click(screen.getByTestId("delete-environment-data-keep"));
    await userEvent.click(confirmButton());
    await waitFor(() => expect(deleteEnvironment).toHaveBeenCalledWith("env_1", { keepData: true }));
    expect(deleteEnvironment).not.toHaveBeenCalledWith("env_1", { keepData: false });
  });

  it("voids Keep when the inventory then shows no databases, rather than deleting", async () => {
    wrap();
    await chooseKeep();
    rerenderWith(withDatabases());
    expect(await screen.findByTestId("delete-environment-choice-stale")).toBeTruthy();
    // The choice stays on screen to be made again; it does not collapse to "no data to keep".
    expect(screen.queryByTestId("delete-environment-no-databases")).toBeNull();
    expect(confirmButton().disabled).toBe(true);
    await userEvent.click(confirmButton());
    expectNoWrites();
  });

  it("voids a Delete too when the inventory changes after it was chosen", async () => {
    wrap();
    await openDelete();
    await userEvent.click(screen.getByTestId("delete-environment-data-delete"));
    await typeName();
    rerenderWith(withDatabases("db", "cache"));
    const stale = await screen.findByTestId("delete-environment-choice-stale");
    expect(stale.getAttribute("data-kind")).toBe("delete");
    await userEvent.click(confirmButton());
    expectNoWrites();
  });

  // The retry after a delete refused for a bound domain, once the person has
  // unbound some domains elsewhere ("partial unbind").
  it("retries a domain-refused delete with Keep still sent", async () => {
    deleteEnvironment.mockRejectedValueOnce(DOMAIN_REFUSAL);
    const onDeleted = wrap();
    await chooseKeep();
    await userEvent.click(confirmButton());
    await screen.findByTestId("delete-environment-domain-refusal");
    await userEvent.click(confirmButton());
    await waitFor(() => expect(onDeleted).toHaveBeenCalled());
    expect(deleteEnvironment.mock.calls).toEqual([
      ["env_1", { keepData: true }],
      ["env_1", { keepData: true }],
    ]);
    expect(unbindDomain).not.toHaveBeenCalled();
  });

  it("sends no retry of a domain-refused Keep delete once Keep can no longer be honoured", async () => {
    deleteEnvironment.mockRejectedValueOnce(DOMAIN_REFUSAL);
    wrap();
    await chooseKeep();
    await userEvent.click(confirmButton());
    await screen.findByTestId("delete-environment-domain-refusal");
    expect(deleteEnvironment).toHaveBeenCalledTimes(1);

    listRetainedDatabases.mockRejectedValue(new ConnectError("refused", Code.PermissionDenied));
    await refetchRetained();
    await screen.findByTestId("delete-environment-choice-stale");
    await userEvent.click(confirmButton());
    // Still only the first, refused, Keep request: no Delete was sent, no domain touched.
    expect(deleteEnvironment.mock.calls).toEqual([["env_1", { keepData: true }]]);
    expect(unbindDomain).not.toHaveBeenCalled();
  });
});

// L2: there is no combined "unbind domains and delete". The backend unbinds
// by domain id and a rebind updates the binding in place, so unbinding from
// here could take down a domain since rebound to another environment.
describe("delete with a custom domain bound", () => {
  it("lists the bound domains and sends the person to the Domains page; it never unbinds", async () => {
    listDomains.mockResolvedValue([
      domain("d_www", "www.acme.dev", "env_1"),
      domain("d_app", "app.acme.dev", "env_1"),
      domain("d_other", "other.acme.dev", "env_2"),
    ]);
    wrap();
    await openDelete();
    const bound = await screen.findByTestId("delete-environment-bound-domains");
    expect(bound.textContent).toContain("app.acme.dev");
    expect(bound.textContent).toContain("www.acme.dev");
    expect(bound.textContent).not.toContain("other.acme.dev");
    expect(bound.textContent).toContain("Unbind these domains on the Domains page");

    expect(screen.queryByText(/Unbind domains and delete/)).toBeNull();
    expect(confirmButton().textContent).toBe("Delete environment");
    await userEvent.click(screen.getByTestId("delete-environment-open-domains"));
    expect(onManageDomains).toHaveBeenCalledTimes(1);
    expect(unbindDomain).not.toHaveBeenCalled();
  });

  it("on a domain refusal, says to unbind on the Domains page and offers no combined action", async () => {
    // The dialog's own read missed the binding: the server's refusal reveals it.
    deleteEnvironment.mockRejectedValueOnce(DOMAIN_REFUSAL);
    const onDeleted = wrap();
    await openDelete();
    await userEvent.click(screen.getByTestId("delete-environment-data-delete"));
    await typeName();
    await userEvent.click(confirmButton());

    const refusal = await screen.findByTestId("delete-environment-domain-refusal");
    expect(refusal.textContent).toContain("app.acme.dev");
    expect(refusal.textContent).toContain("Unbind these domains on the Domains page");
    expect(confirmButton().textContent).toBe("Delete environment");
    expect(screen.queryByText(/Unbind domains and delete/)).toBeNull();
    expect(screen.queryByTestId("delete-environment-progress")).toBeNull();
    expect(unbindDomain).not.toHaveBeenCalled();
    expect(onDeleted).not.toHaveBeenCalled();
  });
});

describe("who may do what", () => {
  it("disables Delete for a member, saying who can", async () => {
    role = "member";
    wrap();
    await userEvent.click(screen.getByTestId("env-danger-menu"));
    const item = await screen.findByTestId("env-action-delete");
    expect(item.getAttribute("aria-disabled")).toBe("true");
    expect(item.textContent).toContain("Owner or admin");
    await userEvent.click(item);
    expect(screen.queryByTestId("delete-environment-dialog")).toBeNull();
    // Stop stays: a member may hold deploy:write bound to this environment.
    expect((screen.getByTestId("env-action-stop") as HTMLButtonElement).disabled).toBe(false);
  });

  it("leaves Delete enabled while the role is unknown, and words a PermissionDenied", async () => {
    role = null;
    deleteEnvironment.mockRejectedValue(new ConnectError("insufficient role", Code.PermissionDenied));
    wrap();
    await openDelete();
    await userEvent.click(screen.getByTestId("delete-environment-data-delete"));
    await typeName();
    await userEvent.click(screen.getByTestId("delete-environment-confirm"));
    const error = await screen.findByTestId("delete-environment-error");
    expect(error.textContent).toContain("Deleting an environment needs an org owner or admin.");
    expect(error.textContent).toContain("insufficient role");
  });

  it("words a refused Stop with the permission it needs", async () => {
    setEnvironmentRunState.mockRejectedValue(new ConnectError("missing deploy:write in target organization", Code.PermissionDenied));
    wrap();
    await userEvent.click(screen.getByTestId("env-action-stop"));
    await userEvent.click(screen.getByTestId("stop-environment-confirm"));
    const error = await screen.findByTestId("stop-environment-error");
    expect(error.textContent).toContain("Stopping or starting prod needs deploy:write on it.");
  });
});

describe("stop", () => {
  it("says storage keeps billing, without inventing an amount", async () => {
    wrap();
    await userEvent.click(screen.getByTestId("env-action-stop"));
    const note = screen.getByTestId("stop-environment-storage-note").textContent ?? "";
    expect(note).toContain("Storage keeps billing while it is stopped");
    expect(note).toContain("can't show that amount");
    expect(note).not.toMatch(/\$\d/);
  });
});
