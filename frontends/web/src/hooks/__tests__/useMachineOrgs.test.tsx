/**
 * Machines and orgs: a machine belongs to one org (controlplane Daemon.org_id).
 * With several orgs, pickers and the machines list offer the active org's
 * machines, each labelled with its org; a machine whose org is unknown is
 * never hidden; one recorded before machines had an org is the default org's.
 * A person with one org sees no label, no filter, and no extra request.
 */
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { renderWithQuery } from "@/test/renderWithQuery";
import { useActiveOrgStore } from "@/store/activeOrgStore";
import { useAuthStore } from "@/store/authStore";
import { DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";

const listMyOrganizations = vi.fn();
const listMachineOrgs = vi.fn();
/** Whether each ListDaemons call spanned every org (its acrossAllOrgs context value). */
const acrossAll: boolean[] = [];
vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));

import { machineOrgId, machinesInOrg } from "../useMachineOrgs";
import { MachineSelect } from "@/components/Projects/MachineSelect";
import { MachinePicker } from "@/components/Chat/MachinePicker";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { myOrgsHandlers } from "@/test/orgsTransport";
import { acrossAllOrgsKey } from "@/api/orgInterceptor";
import type { Transport } from "@connectrpc/connect";

// The machine → org map is the control plane's ListDaemons, answered from MAP-shaped fakes.
beforeEach(() => {
  acrossAll.length = 0;
  const base = mockTransport({
    ...myOrgsHandlers({ listMyOrganizations: () => listMyOrganizations() }),
    "DaemonService.ListDaemons": async () => ({
      daemons: [...((await listMachineOrgs()) as Map<string, string>)].map(([id, orgId]) => ({ id, orgId })),
    }),
  });
  const transport: Transport = {
    ...base,
    unary: (method, signal, timeoutMs, header, input, contextValues) => {
      if (method.name === "ListDaemons") acrossAll.push(contextValues?.get(acrossAllOrgsKey) ?? false);
      return base.unary(method, signal, timeoutMs, header, input, contextValues);
    },
  };
  setTransport(transport);
});
afterEach(() => setTransport(null));

const org = (id: string, name: string, isDefault = false) => ({ id, name, slug: id, role: "owner", isOwner: true, isDefault });
const ACME = org("acme", "Acme", true);
const GLOBEX = org("globex", "Globex");
const MAP = new Map([["m-acme", "acme"], ["m-globex", "globex"], ["m-legacy", ""]]);
// A machine named by its owner: "m-acme" is called "acme-box". (A name equal
// to the id is a placeholder the UI does not show; lib/machineName pins that.)
const machine = (daemonId: string) => {
  const name = `${daemonId.replace(/^m-/, "")}-box`;
  return { daemonId, hostname: `${name}.local`, name, status: DaemonStatus.ACTIVE };
};

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
    await waitFor(() => expect(options()).toEqual(["globex-box · online · Globex"]));
    // The map names no org: it lists every org's machines.
    expect(acrossAll).toEqual([true]);
  });

  it("changes nothing for a person with one org, and asks the control plane nothing", async () => {
    listMyOrganizations.mockResolvedValue({ orgs: [ACME], defaultOrgId: "acme" });
    renderWithQuery(
      <MachineSelect machines={["m-acme", "m-globex"].map(machine)} value="m-acme" onChange={() => {}} />,
    );
    await waitFor(() => expect(listMyOrganizations).toHaveBeenCalled());
    expect(options()).toEqual(["acme-box · online", "globex-box · online"]);
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
