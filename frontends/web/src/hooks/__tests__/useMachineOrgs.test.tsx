/**
 * Machines and orgs: a machine belongs to one org (controlplane Daemon.org_id).
 * With several orgs, pickers and the machines list offer the active org's
 * machines, each labelled with its org; a machine whose org is unknown is
 * never hidden; one recorded before machines had an org is the default org's.
 * A person with one org sees no label, no filter, and no extra request.
 */
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { renderWithQuery } from "@/test/renderWithQuery";
import { useActiveOrgStore } from "@/store/activeOrgStore";
import { useAuthStore } from "@/store/authStore";
import { DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";

const listMyOrganizations = vi.fn();
const listMachineOrgs = vi.fn();
vi.mock("@/services/controlPlane/orgs", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/controlPlane/orgs")>()),
  listMyOrganizations: () => listMyOrganizations(),
}));
vi.mock("@/services/controlPlane/machineOrgs", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/controlPlane/machineOrgs")>()),
  listMachineOrgs: () => listMachineOrgs(),
}));
vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));

import { machineOrgId, machinesInOrg } from "../useMachineOrgs";
import { MachineSelect } from "@/components/Projects/MachineSelect";
import { MachinePicker } from "@/components/Chat/MachinePicker";

const org = (id: string, name: string, isDefault = false) => ({ id, name, slug: id, role: "owner", isOwner: true, isDefault });
const ACME = org("acme", "Acme", true);
const GLOBEX = org("globex", "Globex");
const MAP = new Map([["m-acme", "acme"], ["m-globex", "globex"], ["m-legacy", ""]]);
const machine = (daemonId: string) => ({ daemonId, hostname: daemonId, name: daemonId, status: DaemonStatus.ACTIVE });

describe("machineOrgId / machinesInOrg", () => {
  it("names a machine's org, the default for one recorded before orgs, and nothing for an unknown one", () => {
    expect(machineOrgId("m-globex", MAP, "acme")).toBe("globex");
    expect(machineOrgId("m-legacy", MAP, "acme")).toBe("acme");
    expect(machineOrgId("m-local", MAP, "acme")).toBeUndefined();
    expect(machineOrgId("m-acme", undefined, "acme")).toBeUndefined();
  });

  it("offers the active org's machines, unknown ones, and the kept choice, in order", () => {
    const all = ["m-acme", "m-globex", "m-legacy", "m-local"].map(machine);
    const ids = (ms: { daemonId: string }[]) => ms.map((m) => m.daemonId);
    expect(ids(machinesInOrg(all, MAP, "acme", "acme"))).toEqual(["m-acme", "m-legacy", "m-local"]);
    expect(ids(machinesInOrg(all, MAP, "globex", "acme"))).toEqual(["m-globex", "m-local"]);
    expect(ids(machinesInOrg(all, MAP, "globex", "acme", ["m-acme"]))).toEqual(["m-acme", "m-globex", "m-local"]);
    // Not known yet, or no active org: everything, as before orgs.
    expect(ids(machinesInOrg(all, undefined, "globex", "acme"))).toEqual(ids(all));
    expect(ids(machinesInOrg(all, MAP, null, "acme"))).toEqual(ids(all));
  });
});

describe("MachineSelect with orgs", () => {
  beforeEach(() => {
    localStorage.clear();
    listMyOrganizations.mockReset();
    listMachineOrgs.mockReset().mockResolvedValue(MAP);
    useAuthStore.setState({ user: { id: "alice" } as never });
    useActiveOrgStore.setState({ userId: "alice", chosenId: null, effectiveId: null });
  });

  const options = () => screen.getAllByRole("option").map((o) => o.textContent);

  it("offers the active org's machines, each labelled with its org", async () => {
    listMyOrganizations.mockResolvedValue({ orgs: [ACME, GLOBEX], defaultOrgId: "acme" });
    useActiveOrgStore.setState({ chosenId: "globex", effectiveId: "globex" });
    renderWithQuery(
      <MachineSelect machines={["m-acme", "m-globex", "m-legacy"].map(machine)} value="m-globex" onChange={() => {}} />,
    );
    await waitFor(() => expect(options()).toEqual(["m-globex · online · Globex"]));
  });

  it("changes nothing for a person with one org, and asks the control plane nothing", async () => {
    listMyOrganizations.mockResolvedValue({ orgs: [ACME], defaultOrgId: "acme" });
    renderWithQuery(
      <MachineSelect machines={["m-acme", "m-globex"].map(machine)} value="m-acme" onChange={() => {}} />,
    );
    await waitFor(() => expect(listMyOrganizations).toHaveBeenCalled());
    expect(options()).toEqual(["m-acme · online", "m-globex · online"]);
    expect(listMachineOrgs).not.toHaveBeenCalled();
  });

  it("the new chat's machine picker offers the active org's machines with their org", async () => {
    listMyOrganizations.mockResolvedValue({ orgs: [ACME, GLOBEX], defaultOrgId: "acme" });
    useActiveOrgStore.setState({ chosenId: "acme", effectiveId: "acme" });
    renderWithQuery(
      <MachinePicker value="" onChange={() => {}} daemons={["m-acme", "m-globex", "m-legacy"].map(machine)} />,
    );
    await waitFor(() => expect(listMachineOrgs).toHaveBeenCalled());
    fireEvent.click(screen.getByTestId("machine-picker"));
    await waitFor(() => expect(screen.getByTestId("machine-org-m-acme")).toHaveTextContent("Acme"));
    expect(screen.getByTestId("machine-org-m-legacy")).toHaveTextContent("Acme");
    expect(screen.queryByTestId("machine-option-m-globex")).toBeNull();
  });
});
