/**
 * Settings › Members › Permissions as a people × environments grid, over the
 * real generated AccessTokenService client on a mock transport, so what is
 * asserted is the wire request the control plane receives.
 *
 * Pinned:
 *   - one ListOrgMemberGrants renders the rows, the Org-wide column and a
 *     column per environment, protected ones marked;
 *   - every save sends the member's COMPLETE environment_grants set, also when
 *     only an org-wide box moved and also when it is empty (unset means "leave
 *     environments alone", which a client showing them must never say);
 *   - a box the caller cannot grant (org-wide or on that environment) is
 *     disabled and says why; owners and admins are read-only;
 *   - a refusal is rolled back and explained in words;
 *   - someone holding only environment-bound grants is told they also need an
 *     org-wide read to use them from forge.
 */
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { Code, ConnectError } from "@connectrpc/connect";

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));

import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { useActiveOrgStore } from "@/store/activeOrgStore";
import type { UpdateOrgMemberGrantsRequest } from "@/gen/controlplane/services/access_token/v1/access_token_pb";

import { MemberPermissions } from "../MemberPermissions";

const ENV_SCOPES = ["deploy:write", "secret:write", "db:query", "storage:read", "deploy:config:read"];
const ADMIN_SET = [
  "deploy:read", "deploy:write", "deploy:config:read", "secret:read", "secret:write",
  "domain:read", "domain:write", "db:query", "storage:read", "obs:write", "billing:read",
  "cluster:manage", "token:read", "token:write", "identity:read", "identity:manage",
];

function grants(overrides: { grantable?: string[]; prodGrantable?: string[] } = {}) {
  return {
    members: [
      { userId: "u-admin", email: "admin@example.test", role: "admin", scopes: ADMIN_SET, environmentGrants: [] },
      {
        userId: "u-dev",
        email: "dev@example.test",
        role: "member",
        scopes: ["deploy:read"],
        environmentGrants: [{ environmentId: "env-prod", scopes: ["deploy:write"] }],
      },
      {
        userId: "u-rm",
        email: "rm@example.test",
        role: "member",
        scopes: [],
        environmentGrants: [{ environmentId: "env-prod", scopes: ["deploy:write"] }],
      },
    ],
    grantable: overrides.grantable ?? ADMIN_SET,
    environments: [
      { id: "env-staging", name: "staging", protected: false, grantable: ENV_SCOPES },
      { id: "env-prod", name: "prod", protected: true, grantable: overrides.prodGrantable ?? ENV_SCOPES },
    ],
    environmentScopes: ENV_SCOPES,
  };
}

type Update = (req: UpdateOrgMemberGrantsRequest) => unknown;

function setup(list = grants(), update?: Update) {
  const calls: UpdateOrgMemberGrantsRequest[] = [];
  const echo: Update = (req) => ({
    member: {
      userId: req.userId,
      email: `${req.userId}@echo`,
      role: "member",
      scopes: req.scopes,
      environmentGrants: req.environmentGrants?.environments ?? [],
    },
  });
  setTransport(
    mockTransport({
      "AccessTokenService.ListOrgMemberGrants": { response: list },
      "AccessTokenService.UpdateOrgMemberGrants": (req: unknown) => {
        calls.push(req as UpdateOrgMemberGrantsRequest);
        return (update ?? echo)(req as UpdateOrgMemberGrantsRequest);
      },
    }),
  );
  render(<MemberPermissions embedded />);
  return { calls };
}

const box = (name: string) => screen.getByRole("checkbox", { name });
// A disabled box cannot take focus, so the tooltip describes its wrapper.
const reasonFor = (el: HTMLElement) => el.closest("[aria-describedby]") as HTMLElement;
const envGrantsOf = (req: UpdateOrgMemberGrantsRequest) =>
  Object.fromEntries((req.environmentGrants?.environments ?? []).map((e) => [e.environmentId, [...e.scopes].sort()]));

beforeEach(() => {
  setTransport(null);
  useActiveOrgStore.setState({ userId: "u-admin", chosenId: "org-a", effectiveId: "org-a" });
});

describe("MemberPermissions grid", () => {
  it("renders members against Org-wide and every environment from one load", async () => {
    setup();
    await screen.findByText("dev@example.test");

    const headers = screen.getAllByRole("columnheader").map((h) => h.textContent ?? "");
    expect(headers[0]).toMatch(/Member/);
    expect(headers[1]).toMatch(/Org-wide/);
    expect(headers[2]).toMatch(/staging/);
    expect(headers[3]).toMatch(/prod/);
    // Protected environments are marked; unprotected ones are not.
    expect(within(screen.getByRole("columnheader", { name: /prod/ })).getByText("protected")).toBeInTheDocument();
    expect(within(screen.getByRole("columnheader", { name: /staging/ })).queryByText("protected")).toBeNull();

    // Org-wide cell and environment-bound cells read from their own fields.
    expect(box("dev@example.test deploy:read")).toBeChecked();
    expect(box("dev@example.test deploy:write")).not.toBeChecked();
    expect(box("dev@example.test on prod: deploy:write")).toBeChecked();
    expect(box("dev@example.test on staging: deploy:write")).not.toBeChecked();
    // Only the bindable scopes get an environment box.
    expect(screen.queryByRole("checkbox", { name: "dev@example.test on prod: token:write" })).toBeNull();
  });

  it("saves an environment toggle with the member's complete set", async () => {
    const { calls } = setup();
    await screen.findByText("dev@example.test");

    fireEvent.click(box("dev@example.test on staging: secret:write"));

    await waitFor(() => expect(calls).toHaveLength(1));
    const req = calls[0];
    expect(req.orgId).toBe("org-a");
    expect(req.userId).toBe("u-dev");
    expect(req.scopes).toEqual(["deploy:read"]);
    // The untouched prod grant travels with the new staging one.
    expect(envGrantsOf(req)).toEqual({ "env-prod": ["deploy:write"], "env-staging": ["secret:write"] });
    expect(box("dev@example.test on staging: secret:write")).toBeChecked();
  });

  it("sends environment_grants on an org-wide toggle too, so nothing bound is dropped", async () => {
    const { calls } = setup();
    await screen.findByText("dev@example.test");

    fireEvent.click(box("dev@example.test secret:read"));

    await waitFor(() => expect(calls).toHaveLength(1));
    expect([...calls[0].scopes].sort()).toEqual(["deploy:read", "secret:read"]);
    expect(envGrantsOf(calls[0])).toEqual({ "env-prod": ["deploy:write"] });
  });

  it("revoking the last environment grant sends an empty set, never an unset one", async () => {
    const { calls } = setup();
    await screen.findByText("dev@example.test");

    fireEvent.click(box("dev@example.test on prod: deploy:write"));

    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0].environmentGrants).toBeDefined();
    expect(calls[0].environmentGrants?.environments).toEqual([]);
    await waitFor(() => expect(box("dev@example.test on prod: deploy:write")).not.toBeChecked());
  });

  it("shows a permission held org-wide as covering every environment", async () => {
    setup();
    await screen.findByText("dev@example.test");
    // dev holds deploy:read org-wide, which is not bindable, so take
    // deploy:write org-wide from the admin-shaped list instead.
    const held = box("admin@example.test on staging: deploy:write");
    expect(held).toBeChecked();
    expect(held).toBeDisabled();
    expect(reasonFor(held)).toHaveAccessibleDescription(/Held org-wide, which covers staging/);
  });

  it("disables what the caller cannot grant, org-wide or on one environment, and says why", async () => {
    setup(grants({
      grantable: ADMIN_SET.filter((s) => s !== "cluster:manage" && s !== "secret:write"),
      prodGrantable: ["deploy:write"],
    }));
    await screen.findByText("dev@example.test");

    const orgWide = box("dev@example.test cluster:manage");
    expect(orgWide).toBeDisabled();
    expect(reasonFor(orgWide)).toHaveAccessibleDescription(/can't grant cluster:manage: you can only grant what you hold yourself/);

    const onProd = box("dev@example.test on prod: secret:write");
    expect(onProd).toBeDisabled();
    expect(reasonFor(onProd)).toHaveAccessibleDescription(/can't grant secret:write on prod: you hold it neither org-wide nor on prod/);

    // The same scope stays grantable where the caller holds it.
    expect(box("dev@example.test on staging: secret:write")).not.toBeDisabled();
    expect(box("dev@example.test on prod: deploy:write")).not.toBeDisabled();
  });

  it("shows owners and admins read-only: their set is the role's preset", async () => {
    setup();
    await screen.findByText("admin@example.test");
    const row = screen.getByTestId("permissions-row-u-admin");
    const boxes = within(row).getAllByRole("checkbox");
    expect(boxes.length).toBeGreaterThan(0);
    for (const b of boxes) expect(b).toBeDisabled();
    expect(reasonFor(box("admin@example.test token:write"))).toHaveAccessibleDescription(/Admins hold every permission org-wide/);
  });

  it("disables everything for a caller without token:write", async () => {
    setup(grants({ grantable: ["deploy:read"] }));
    await screen.findByText("dev@example.test");
    const b = box("dev@example.test on staging: deploy:write");
    expect(b).toBeDisabled();
    expect(reasonFor(b)).toHaveAccessibleDescription(/needs token:write/);
    expect(screen.getByText(/only.*view/i)).toBeInTheDocument();
  });

  it("rolls back a refused grant and names the permission the caller lacks", async () => {
    const { calls } = setup(grants(), () => {
      throw new ConnectError(
        "your organization permissions do not include secret:write; ask an organization admin to grant it",
        Code.PermissionDenied,
        { "x-forge-error-reason": "scope_missing", "x-forge-error-scopes": "secret:write" },
      );
    });
    await screen.findByText("dev@example.test");

    fireEvent.click(box("dev@example.test on staging: secret:write"));

    await waitFor(() => expect(calls).toHaveLength(1));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Could not update dev@example.test's permissions: you can only grant what you hold yourself, org-wide or on that environment, and you don't hold secret:write.",
    );
    await waitFor(() => expect(box("dev@example.test on staging: secret:write")).not.toBeChecked());
    expect(box("dev@example.test on prod: deploy:write")).toBeChecked();
  });

  it("shows the server's words for an invalid environment grant", async () => {
    setup(grants(), () => {
      throw new ConnectError(
        "environment_grants names an environment that is not in this organization",
        Code.InvalidArgument,
      );
    });
    await screen.findByText("dev@example.test");

    fireEvent.click(box("dev@example.test on staging: db:query"));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Could not update dev@example.test's permissions: environment_grants names an environment that is not in this organization",
    );
    await waitFor(() => expect(box("dev@example.test on staging: db:query")).not.toBeChecked());
  });

  it("warns when someone holds only environment-bound grants, and offers the org-wide read", async () => {
    const { calls } = setup();
    await screen.findByText("rm@example.test");

    // dev holds deploy:read org-wide, so their prod grant works from forge.
    expect(screen.queryByTestId("permissions-hint-u-dev")).toBeNull();
    const hint = screen.getByTestId("permissions-hint-u-rm");
    expect(hint).toHaveTextContent(/also needs one permission held org-wide; deploy:read is enough/);
    expect(hint).toHaveTextContent(/refused when it is minted/);

    fireEvent.click(within(hint).getByRole("button", { name: "Grant deploy:read org-wide" }));

    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0].userId).toBe("u-rm");
    expect(calls[0].scopes).toEqual(["deploy:read"]);
    expect(envGrantsOf(calls[0])).toEqual({ "env-prod": ["deploy:write"] });
    await waitFor(() => expect(screen.queryByTestId("permissions-hint-u-rm")).toBeNull());
  });
});
