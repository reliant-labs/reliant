/**
 * Tools (MCP) card: servers are named, not picked (they live on each
 * machine), a name that cannot be a server is refused before it is added, and
 * the card says plainly what an older machine still does.
 */
import { create } from "@bufbuild/protobuf";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));

import { McpPolicySchema, OrgPolicyMode, type McpPolicy } from "@/gen/controlplane/controlplane/v1/org_policy_pb";

import { isMcpServerName, McpCard } from "../cards/McpCard";
import { renderCard } from "./cardHarness";

describe("McpCard", () => {
  it("lists the allowed servers for a member, and the older-machine caveat", () => {
    renderCard<McpPolicy>(McpCard, create(McpPolicySchema, { mode: OrgPolicyMode.ENFORCE, allowedServers: ["linear", "sentry"] }), { editable: false });
    expect(screen.getByTestId("policy-summary-mcp")).toHaveTextContent("Agents may use the tools of linear and sentry only.");
    expect(screen.getByTestId("policy-mcp-allowed")).toHaveTextContent("linear");
    expect(screen.getByText(/older version still starts the servers/)).toBeInTheDocument();
  });

  it("adds a server by name, refuses one that cannot be a server, and removes one", async () => {
    const { last } = renderCard<McpPolicy>(McpCard, create(McpPolicySchema, { mode: OrgPolicyMode.AUDIT, allowedServers: ["sentry"] }));
    const input = screen.getByRole("textbox", { name: "MCP server name" });
    await userEvent.type(input, "bad__name");
    expect(screen.getByRole("button", { name: "Allow server" })).toBeDisabled();
    await userEvent.clear(input);
    await userEvent.type(input, "linear{Enter}");
    expect(last()?.allowedServers).toEqual(["linear", "sentry"]);
    expect(input).toHaveValue("");

    await userEvent.click(screen.getByRole("button", { name: "Remove sentry" }));
    expect(last()?.allowedServers).toEqual(["linear"]);
  });

  it("knows a server name when it sees one", () => {
    expect(isMcpServerName("linear")).toBe(true);
    expect(isMcpServerName("my.server-1")).toBe(true);
    expect(isMcpServerName("a__b")).toBe(false);
    expect(isMcpServerName("has space")).toBe(false);
    expect(isMcpServerName("-lead")).toBe(false);
  });
});
