// Copyright (c) 2025 Reliant Labs

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";

const setEnvironmentRunState = vi.fn();
const scaleDeployment = vi.fn();
const deleteEnvironment = vi.fn();

vi.mock("@/services/forge/cloudEnvs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/services/forge/cloudEnvs")>();
  return {
    ...actual,
    setEnvironmentRunState: (...args: unknown[]) => setEnvironmentRunState(...args),
    scaleDeployment: (...args: unknown[]) => scaleDeployment(...args),
    deleteEnvironment: (...args: unknown[]) => deleteEnvironment(...args),
  };
});

import { EnvLifecycleControls } from "../EnvPage/EnvLifecycleControls";
import { HostedWorkloadList } from "../HostedWorkloads";
import { runStateOf } from "../runStateVocabulary";
import type { CloudEnvStatus } from "@/services/forge/cloudEnvs";

function wrap(ui: React.ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>);
}

function status(runState: "running" | "suspended", observed = "ready"): CloudEnvStatus {
  return {
    verdict: "converged",
    currentPromotion: null,
    workloads: [
      { name: "api", deployment_id: "dep_1", declared_run_state: runState, observed_state: observed, verdict: "converged" },
    ],
  };
}

beforeEach(() => {
  setEnvironmentRunState.mockReset().mockResolvedValue(undefined);
  scaleDeployment.mockReset().mockResolvedValue(undefined);
  deleteEnvironment.mockReset().mockResolvedValue(undefined);
});

describe("state vocabulary", () => {
  const w = (declared: string, observed: string, last_error = "") => ({
    declared_run_state: declared,
    observed_state: observed,
    last_error,
  });
  it("maps each reason", () => {
    expect(runStateOf(w("suspended", "suspended")).label).toBe("Stopped by you");
    expect(runStateOf(w("suspended", "ready")).label).toBe("Stopping…");
    expect(runStateOf(w("running", "suspended")).label).toBe("Starting…");
    expect(runStateOf(w("running", "pending")).label).toBe("Starting…");
    expect(runStateOf(w("running", "ready")).label).toBe("Running");
    const billing = runStateOf(w("running", "suspended", "no active compute plan"));
    expect(billing.label).toBe("Suspended — billing");
    expect(billing.detail).toContain("Subscribe to a compute plan");
    expect(billing.detail).toContain("no active compute plan");
  });
});

describe("environment controls", () => {
  it("stop confirms, then calls SetEnvironmentRunState SUSPENDED", async () => {
    wrap(<EnvLifecycleControls environmentId="env_1" envName="prod" status={status("running")} />);
    await userEvent.click(screen.getByTestId("env-action-stop"));
    expect(setEnvironmentRunState).not.toHaveBeenCalled();
    expect(screen.getByText(/Compute stops\. Data and URLs are kept\. Start brings it back\./)).toBeTruthy();
    await userEvent.click(screen.getByTestId("stop-environment-confirm"));
    await waitFor(() => expect(setEnvironmentRunState).toHaveBeenCalledWith("env_1", "suspended"));
  });

  it("shows Start when every workload is stopped, and a billing refusal inline", async () => {
    setEnvironmentRunState.mockRejectedValue(
      new ConnectError("no active compute plan; to resume: subscribe", Code.FailedPrecondition)
    );
    wrap(<EnvLifecycleControls environmentId="env_1" envName="prod" status={status("suspended", "suspended")} />);
    expect(screen.queryByTestId("env-action-stop")).toBeNull();
    await userEvent.click(screen.getByTestId("env-action-start"));
    await waitFor(() => expect(setEnvironmentRunState).toHaveBeenCalledWith("env_1", "running"));
    const refusal = await screen.findByTestId("env-start-refused");
    expect(refusal.textContent).toContain("no active compute plan; to resume: subscribe");
  });

  it("delete requires the typed name and states the data caveat", async () => {
    wrap(<EnvLifecycleControls environmentId="env_1" envName="prod" status={status("running")} />);
    await userEvent.click(screen.getByTestId("env-danger-menu"));
    await userEvent.click(await screen.findByTestId("env-action-delete"));
    const note = screen.getByTestId("delete-environment-data-note").textContent ?? "";
    expect(note).toContain("retained");
    expect(note).toContain("not reattach");
    expect(screen.getByTestId("delete-environment-dialog").textContent).toContain("forge env deploy prod");

    const confirm = screen.getByTestId("delete-environment-confirm") as HTMLButtonElement;
    expect(confirm.disabled).toBe(true);
    await userEvent.type(screen.getByTestId("delete-environment-confirm-input"), "pro");
    expect(confirm.disabled).toBe(true);
    await userEvent.type(screen.getByTestId("delete-environment-confirm-input"), "d");
    expect(confirm.disabled).toBe(false);
    await userEvent.click(confirm);
    await waitFor(() => expect(deleteEnvironment).toHaveBeenCalledWith("env_1"));
  });
});

describe("per-workload controls", () => {
  it("stop calls Scale SUSPENDED for that deployment", async () => {
    wrap(<HostedWorkloadList envName="prod" workloads={status("running").workloads} runControls />);
    await userEvent.click(screen.getByTestId("hosted-run-toggle-api"));
    await waitFor(() => expect(scaleDeployment).toHaveBeenCalledWith("dep_1", "suspended"));
  });

  it("a stopped workload offers Start and reads 'Stopped by you'", async () => {
    wrap(<HostedWorkloadList envName="prod" workloads={status("suspended", "suspended").workloads} runControls />);
    expect(screen.getByText("Stopped by you")).toBeTruthy();
    await userEvent.click(screen.getByTestId("hosted-run-toggle-api"));
    await waitFor(() => expect(scaleDeployment).toHaveBeenCalledWith("dep_1", "running"));
  });

  it("shows no controls without runControls", () => {
    wrap(<HostedWorkloadList envName="prod" workloads={status("running").workloads} />);
    expect(screen.queryByTestId("hosted-run-toggle-api")).toBeNull();
  });
});
