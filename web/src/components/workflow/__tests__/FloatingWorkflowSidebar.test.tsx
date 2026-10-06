/**
 * The builder's left shelf. It lists integrations too — the user's usable ones
 * first, with their logos — so adding a GitHub step does not depend on
 * knowing to search for it; the agent building blocks sit folded under
 * Advanced; and control flow is listed once.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create } from "@bufbuild/protobuf";

import {
  CatalogIntegrationListingSchema,
  CatalogIntegrationSchema,
  ListCatalogIntegrationsResponseSchema,
  NodeInfoSchema,
} from "@/gen/reliant/v1/catalog_pb";
import { renderWithQuery } from "@/test/renderWithQuery";

const listCatalogIntegrations = vi.fn();
const listNodes = vi.fn();

vi.mock("@/api/grpc-client", () => ({
  grpcClient: { catalog: () => ({ listCatalogIntegrations }) },
  getCatalogClient: () => ({ listNodes }),
  getGRPCBaseURLPublic: () => null,
}));

import { FloatingWorkflowSidebar } from "../FloatingWorkflowSidebar";

function listing(id: string, displayName: string, connected: boolean) {
  return create(CatalogIntegrationListingSchema, {
    integration: create(CatalogIntegrationSchema, { id, displayName, icon: id, category: "engineering", version: 1 }),
    entryCount: 2,
    connected,
  });
}

beforeEach(() => {
  listNodes.mockReset();
  listNodes.mockResolvedValue({
    nodes: [
      create(NodeInfoSchema, { id: "workflow", displayName: "Agent", description: "Invoke an agent", category: "agentic" }),
      create(NodeInfoSchema, { id: "execute_tools", displayName: "Run LLM Tool Calls", description: "Run tool calls", category: "agentic" }),
      create(NodeInfoSchema, { id: "loop", displayName: "Loop", description: "Repeat", category: "flow" }),
      create(NodeInfoSchema, { id: "action", displayName: "Action", description: "Generic", category: "utility" }),
    ],
  });
  listCatalogIntegrations.mockReset();
  listCatalogIntegrations.mockResolvedValue(
    create(ListCatalogIntegrationsResponseSchema, {
      integrations: [listing("github", "GitHub", true), listing("slack", "Slack", false)],
      totalSize: 5,
    }),
  );
});

function renderSidebar() {
  const props = { onAddStep: vi.fn(), onAddSwitch: vi.fn(), onOpenPalette: vi.fn(), onOpenIntegration: vi.fn() };
  renderWithQuery(<FloatingWorkflowSidebar {...props} />);
  return props;
}

describe("FloatingWorkflowSidebar", () => {
  it("lists integrations with their logos and opens the palette on one", async () => {
    const user = userEvent.setup();
    const props = renderSidebar();

    const github = (await screen.findByText("GitHub")).closest("button")!;
    expect(github.querySelector('[data-integration-logo="github"]')).not.toBeNull();
    expect(within(github).getByRole("img", { name: "Connected" })).toBeInTheDocument();
    expect(within(screen.getByText("Slack").closest("button")!).queryByRole("img", { name: "Connected" })).not.toBeInTheDocument();
    expect(listCatalogIntegrations).toHaveBeenCalledWith(expect.objectContaining({ pageSize: 4 }), expect.anything());

    await user.click(github);
    expect(props.onOpenIntegration).toHaveBeenCalledWith("github");

    await user.click(screen.getByRole("button", { name: "All 5 integrations…" }));
    expect(props.onOpenIntegration).toHaveBeenLastCalledWith();
  });

  it("folds the agent building blocks under Advanced and lists control flow once", async () => {
    const user = userEvent.setup();
    const props = renderSidebar();
    await screen.findByText("Agent");

    expect(screen.queryByText("Run LLM Tool Calls")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Advanced" }));
    await user.click(screen.getByText("Run LLM Tool Calls"));
    expect(props.onAddStep).toHaveBeenCalledWith("execute_tools");

    expect(screen.getAllByRole("button", { name: "Control Flow" })).toHaveLength(1);
    expect(screen.getAllByText("Loop")).toHaveLength(1);
    expect(screen.queryByText("Action")).not.toBeInTheDocument();
  });
});
