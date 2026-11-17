// Copyright (c) 2025 Reliant Labs

/**
 * Env teardown in the env header (control-plane unified-shell-and-projects.md,
 * Lifecycle, L1): Keep data on delete, "Unbind domains and delete" with its
 * progress and errors, the storage note on Stop, and who may do what (D19).
 * Every wait is on an event (a call, an element), never a fixed sleep.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";

import type { ForgeDomain } from "@/services/forge/domains";

const setEnvironmentRunState = vi.fn();
const deleteEnvironment = vi.fn();
const listDomains = vi.fn();
const unbindDomain = vi.fn();
let role: string | null = "admin";

vi.mock("@/services/forge/cloudEnvs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/services/forge/cloudEnvs")>();
  return {
    ...actual,
    setEnvironmentRunState: (...args: unknown[]) => setEnvironmentRunState(...args),
    deleteEnvironment: (...args: unknown[]) => deleteEnvironment(...args),
  };
});

vi.mock("@/services/forge/domains", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/services/forge/domains")>();
  return {
    ...actual,
    hasControlPlane: () => true,
    listDomains: (...args: unknown[]) => listDomains(...args),
    unbindDomain: (...args: unknown[]) => unbindDomain(...args),
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
import type { CloudEnvStatus } from "@/services/forge/cloudEnvs";

const STATUS: CloudEnvStatus = {
  verdict: "converged",
  currentPromotion: null,
  workloads: [
    { name: "api", deployment_id: "dep_1", declared_run_state: "running", observed_state: "ready", verdict: "converged" },
    { name: "web", deployment_id: "dep_2", declared_run_state: "running", observed_state: "ready", verdict: "converged" },
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

function wrap(onDeleted = vi.fn()) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <EnvLifecycleControls environmentId="env_1" envName="prod" status={STATUS} onDeleted={onDeleted} />
    </QueryClientProvider>
  );
  return onDeleted;
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
});

describe("delete: what goes, what stays, and Keep data", () => {
  it("names what is deleted and what is kept", async () => {
    wrap();
    await openDelete();
    const removed = screen.getByTestId("delete-environment-removed").textContent ?? "";
    expect(removed).toContain("api, web");
    expect(removed).toContain("namespace");
    expect(removed).toContain("generated hostnames");
    const kept = screen.getByTestId("delete-environment-kept").textContent ?? "";
    expect(kept).toContain("Releases and their images");
    expect(kept).toContain("Secrets in the store");
    expect(kept).toContain("Custom domains");
  });

  it("deletes the data by default", async () => {
    const onDeleted = wrap();
    await openDelete();
    expect((screen.getByTestId("delete-environment-data-delete") as HTMLInputElement).checked).toBe(true);
    await typeName();
    await userEvent.click(screen.getByTestId("delete-environment-confirm"));
    await waitFor(() => expect(deleteEnvironment).toHaveBeenCalledWith("env_1", { keepData: false }));
    await waitFor(() => expect(onDeleted).toHaveBeenCalled());
  });

  it("Keep data sends keep_data and says the databases keep billing", async () => {
    wrap();
    await openDelete();
    const note = screen.getByTestId("delete-environment-data-note").textContent ?? "";
    expect(note).toContain("keep billing as storage");
    expect(note).toContain("Kept databases");
    await userEvent.click(screen.getByTestId("delete-environment-data-keep"));
    await typeName();
    await userEvent.click(screen.getByTestId("delete-environment-confirm"));
    await waitFor(() => expect(deleteEnvironment).toHaveBeenCalledWith("env_1", { keepData: true }));
  });
});

describe("delete with a custom domain bound", () => {
  it("lists the bound domains, then unbinds each and deletes", async () => {
    listDomains.mockResolvedValue([
      domain("d_www", "www.acme.dev", "env_1"),
      domain("d_app", "app.acme.dev", "env_1"),
      domain("d_other", "other.acme.dev", "env_2"),
    ]);
    const onDeleted = wrap();
    await openDelete();
    const bound = await screen.findByTestId("delete-environment-bound-domains");
    expect(bound.textContent).toContain("app.acme.dev");
    expect(bound.textContent).toContain("www.acme.dev");
    expect(bound.textContent).not.toContain("other.acme.dev");

    const confirm = screen.getByTestId("delete-environment-confirm");
    expect(confirm.textContent).toBe("Unbind domains and delete");
    await userEvent.click(screen.getByTestId("delete-environment-data-keep"));
    await typeName();
    await userEvent.click(confirm);

    await waitFor(() => expect(onDeleted).toHaveBeenCalled());
    expect(unbindDomain.mock.calls).toEqual([["d_app"], ["d_www"]]);
    expect(unbindDomain).not.toHaveBeenCalledWith("d_other");
    expect(deleteEnvironment).toHaveBeenCalledWith("env_1", { keepData: true });
    // Every unbind finished before the delete was sent.
    expect(unbindDomain.mock.invocationCallOrder[1]).toBeLessThan(deleteEnvironment.mock.invocationCallOrder[0]);
  });

  it("offers Unbind domains and delete when the delete is refused for a bound domain", async () => {
    // The dialog's own read missed the binding (made after it loaded): the
    // server's refusal is what reveals it.
    deleteEnvironment.mockRejectedValueOnce(DOMAIN_REFUSAL);
    const onDeleted = wrap();
    await openDelete();
    await typeName();
    expect(screen.getByTestId("delete-environment-confirm").textContent).toBe("Delete environment");
    await userEvent.click(screen.getByTestId("delete-environment-confirm"));

    const refusal = await screen.findByTestId("delete-environment-domain-refusal");
    expect(refusal.textContent).toContain("app.acme.dev");
    expect(screen.getByTestId("delete-environment-confirm").textContent).toBe("Unbind domains and delete");

    listDomains.mockResolvedValue([domain("d_app", "app.acme.dev", "env_1")]);
    await userEvent.click(screen.getByTestId("delete-environment-confirm"));
    await waitFor(() => expect(onDeleted).toHaveBeenCalled());
    expect(unbindDomain).toHaveBeenCalledWith("d_app");
    expect(deleteEnvironment).toHaveBeenCalledTimes(2);
  });

  it("stops at a failed unbind, shows which step failed, and does not delete", async () => {
    listDomains.mockResolvedValue([domain("d_app", "app.acme.dev", "env_1"), domain("d_www", "www.acme.dev", "env_1")]);
    unbindDomain.mockImplementation(async (id: string) => {
      if (id === "d_www") throw new ConnectError("missing domain:write in target organization", Code.PermissionDenied);
    });
    const onDeleted = wrap();
    await openDelete();
    await screen.findByTestId("delete-environment-bound-domains");
    await typeName();
    await userEvent.click(screen.getByTestId("delete-environment-confirm"));

    const failed = await screen.findByTestId("delete-environment-progress-failed");
    expect(failed.textContent).toContain("prod was not deleted");
    expect(failed.textContent).toContain("already unbound stay unbound");
    const progress = screen.getByTestId("delete-environment-progress");
    expect(within(progress).getByTestId("teardown-step-unbind:d_app").getAttribute("data-state")).toBe("done");
    const www = within(progress).getByTestId("teardown-step-unbind:d_www");
    expect(www.getAttribute("data-state")).toBe("failed");
    expect(www.textContent).toContain("missing domain:write");
    expect(within(progress).getByTestId("teardown-step-delete").getAttribute("data-state")).toBe("pending");
    expect(deleteEnvironment).not.toHaveBeenCalled();
    expect(onDeleted).not.toHaveBeenCalled();
  });

  it("reports a delete that fails after the domains were unbound", async () => {
    listDomains.mockResolvedValue([domain("d_app", "app.acme.dev", "env_1")]);
    deleteEnvironment.mockRejectedValue(new ConnectError("the teardown could not be recorded", Code.Unavailable));
    wrap();
    await openDelete();
    await screen.findByTestId("delete-environment-bound-domains");
    await typeName();
    await userEvent.click(screen.getByTestId("delete-environment-confirm"));

    const failed = await screen.findByTestId("delete-environment-progress-failed");
    expect(failed.textContent).toContain("The domains were unbound, but prod was not deleted.");
    expect(screen.getByTestId("teardown-step-delete").textContent).toContain("the teardown could not be recorded");
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
