/**
 * The step palette (research/INTEGRATIONS_V1_BRIEF.md §3a): one list of
 * built-in steps and integration catalog entries. Before anything is typed it
 * browses — built-ins, then one row per integration that expands into its
 * actions; typing searches. The RPC client is the only mock, so browsing,
 * search, paging, facets and keyboard handling are asserted on what the
 * catalog RPCs returned and what the user did.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create } from "@bufbuild/protobuf";

import {
  CatalogEntryKind,
  CatalogEntrySummarySchema,
  CatalogIntegrationListingSchema,
  CatalogIntegrationSchema,
  ListCatalogIntegrationsResponseSchema,
  NodeInfoSchema,
  SearchCatalogResponseSchema,
  type ListCatalogIntegrationsRequest,
  type SearchCatalogRequest,
} from "@/gen/reliant/v1/catalog_pb";
import { renderWithQuery } from "@/test/renderWithQuery";

const searchCatalog = vi.fn();
const getCatalogEntry = vi.fn();
const listCatalogIntegrations = vi.fn();
const listNodes = vi.fn();

vi.mock("@/api/grpc-client", () => ({
  grpcClient: { catalog: () => ({ searchCatalog, getCatalogEntry, listCatalogIntegrations }) },
  getCatalogClient: () => ({ listNodes }),
  getGRPCBaseURLPublic: () => null,
}));

import { StepPalette, type StepPaletteProps } from "../StepPalette";

const DISPLAY: Record<string, string> = { github: "GitHub", slack: "Slack", gmail: "Gmail" };

function integration(id: string, category = "engineering") {
  return create(CatalogIntegrationSchema, { id, version: 1, displayName: DISPLAY[id] ?? id, icon: id, category });
}

function entry(ref: string, opts: { name?: string; connected?: boolean; mutates?: boolean; kind?: CatalogEntryKind; integration?: string; category?: string } = {}) {
  const id = opts.integration ?? ref.split("/")[0]!;
  return create(CatalogEntrySummarySchema, {
    ref,
    kind: opts.kind ?? CatalogEntryKind.ACTION,
    id: ref.split("/")[1]!.split("@")[0]!,
    displayName: opts.name ?? ref,
    summary: `Summary of ${ref}`,
    integration: integration(id, opts.category),
    connected: opts.connected ?? true,
    mutates: opts.mutates ?? false,
  });
}

function page(entries: ReturnType<typeof entry>[], opts: { next?: string; total?: number; facets?: Array<[string, number]> } = {}) {
  return create(SearchCatalogResponseSchema, {
    entries,
    nextPageToken: opts.next ?? "",
    totalSize: opts.total ?? entries.length,
    categoryFacets: (opts.facets ?? []).map(([value, count]) => ({ value, count })),
  });
}

function listing(id: string, opts: { connected?: boolean; count?: number; category?: string } = {}) {
  return create(CatalogIntegrationListingSchema, {
    integration: integration(id, opts.category),
    entryCount: opts.count ?? 2,
    connected: opts.connected ?? false,
  });
}

function integrationsPage(listings: ReturnType<typeof listing>[], opts: { next?: string; total?: number; facets?: Array<[string, number]> } = {}) {
  return create(ListCatalogIntegrationsResponseSchema, {
    integrations: listings,
    nextPageToken: opts.next ?? "",
    totalSize: opts.total ?? listings.length,
    categoryFacets: (opts.facets ?? []).map(([value, count]) => ({ value, count })),
  });
}

function renderPalette(props: Partial<StepPaletteProps> = {}) {
  const handlers = {
    onClose: vi.fn(),
    onChooseBuiltin: vi.fn(),
    onChooseAction: vi.fn(),
    onChooseTrigger: vi.fn(),
    onChooseBuiltinTrigger: vi.fn(),
    onConnect: vi.fn(),
  };
  renderWithQuery(<StepPalette open {...handlers} {...props} />);
  return handlers;
}

const input = () => screen.getByRole("combobox");
const activeOption = () => {
  const id = input().getAttribute("aria-activedescendant");
  return id ? document.getElementById(id) : null;
};
const optionNamed = (text: string) => screen.getByText(text).closest('[role="option"]') as HTMLElement;

beforeEach(() => {
  searchCatalog.mockReset();
  searchCatalog.mockResolvedValue(page([]));
  getCatalogEntry.mockReset();
  getCatalogEntry.mockReturnValue(new Promise(() => undefined));
  listCatalogIntegrations.mockReset();
  listCatalogIntegrations.mockResolvedValue(integrationsPage([]));
  listNodes.mockReset();
  listNodes.mockResolvedValue({
    nodes: [
      create(NodeInfoSchema, { id: "workflow", displayName: "Agent", description: "Invoke an agent or sub-workflow" }),
      create(NodeInfoSchema, { id: "call_llm", displayName: "Call LLM", description: "Ask a model" }),
      create(NodeInfoSchema, { id: "invoke_tool", displayName: "Run Tool", description: "Run one tool you pick" }),
      create(NodeInfoSchema, { id: "execute_tools", displayName: "Run LLM Tool Calls", description: "Run the tool calls a Call LLM returned" }),
      create(NodeInfoSchema, { id: "compact", displayName: "Compact", description: "Compact the thread" }),
      create(NodeInfoSchema, { id: "action", displayName: "Action", description: "Generic action" }),
    ],
  });
});

describe("StepPalette: browsing (nothing typed)", () => {
  it("lists built-ins, then one row per integration with its logo, without searching", async () => {
    listCatalogIntegrations.mockResolvedValue(
      integrationsPage([listing("slack", { connected: true, count: 3, category: "communication" }), listing("github", { count: 12 })]),
    );
    renderPalette();

    const list = screen.getByRole("listbox", { name: "Steps" });
    const integrations = await within(list).findByRole("group", { name: "Integrations" });
    const rows = within(integrations).getAllByRole("option");
    expect(rows).toHaveLength(2);
    expect(within(rows[0]!).getByText("Slack")).toBeInTheDocument();
    expect(within(rows[0]!).getByText("3 actions · Communication")).toBeInTheDocument();
    expect(within(rows[0]!).getByText("Connected")).toBeInTheDocument();
    expect(rows[0]!.querySelector('[data-integration-logo="slack"]')).not.toBeNull();
    expect(within(rows[1]!).getByText("GitHub")).toBeInTheDocument();
    expect(within(rows[1]!).getByText("Not connected")).toBeInTheDocument();
    expect(rows[1]!.querySelector('[data-integration-logo="github"]')).not.toBeNull();

    expect(within(list).getByText("Call LLM")).toBeInTheDocument();
    expect(within(list).getByText("Loop")).toBeInTheDocument();
    expect(within(list).queryByText("Generic action")).not.toBeInTheDocument();
    expect(listCatalogIntegrations).toHaveBeenCalledWith(
      expect.objectContaining({ kinds: [CatalogEntryKind.ACTION], pageSize: 20, pageToken: "" }),
      expect.anything(),
    );
    // Browsing lists integrations; it does not fetch a page of raw entries.
    expect(searchCatalog).not.toHaveBeenCalled();
    expect(screen.getByText("Type to search every action across 2 integrations.")).toBeInTheDocument();
  });

  it("expands an integration into its actions and adds one", async () => {
    const user = userEvent.setup();
    listCatalogIntegrations.mockResolvedValue(integrationsPage([listing("github", { connected: true })]));
    searchCatalog.mockImplementation((req: SearchCatalogRequest) =>
      Promise.resolve(
        req.integration === "github"
          ? page([entry("github/issue.create@1", { name: "Create issue", mutates: true }), entry("github/pr.get@1", { name: "Get pull request" })])
          : page([]),
      ),
    );
    const handlers = renderPalette();

    await user.click(await screen.findByText("GitHub"));
    expect(await screen.findByText("Create issue")).toBeInTheDocument();
    expect(searchCatalog).toHaveBeenCalledWith(
      expect.objectContaining({ query: "", integration: "github", kinds: [CatalogEntryKind.ACTION] }),
      expect.anything(),
    );
    expect(optionNamed("GitHub")).toHaveTextContent("expanded");
    // Nested under GitHub, so the row does not repeat GitHub's name or logo.
    const createIssue = optionNamed("Create issue");
    expect(within(createIssue).queryByText("GitHub")).not.toBeInTheDocument();
    expect(createIssue.querySelector("[data-integration-logo]")).toBeNull();
    expect(within(createIssue).getByText("Changes data")).toBeInTheDocument();

    await user.click(screen.getByText("Get pull request"));
    expect(handlers.onChooseAction).toHaveBeenCalledWith(expect.objectContaining({ ref: "github/pr.get@1" }));

    // Choosing the integration again collapses it.
    await user.click(screen.getByText("GitHub"));
    expect(screen.queryByText("Create issue")).not.toBeInTheDocument();
  });

  it("expands and collapses with → and ← and keeps one integration open at a time", async () => {
    const user = userEvent.setup();
    listCatalogIntegrations.mockResolvedValue(integrationsPage([listing("github", { connected: true }), listing("slack")]));
    searchCatalog.mockImplementation((req: SearchCatalogRequest) =>
      Promise.resolve(
        req.integration === "github"
          ? page([entry("github/issue.create@1", { name: "Create issue" })])
          : page([entry("slack/message.post@1", { name: "Post message", integration: "slack", connected: false })]),
      ),
    );
    const handlers = renderPalette();
    await screen.findByText("Slack");

    await user.keyboard("{End}");
    expect(activeOption()).toHaveTextContent("Slack");
    await user.keyboard("{ArrowUp}");
    expect(activeOption()).toHaveTextContent("GitHub");
    await user.keyboard("{ArrowRight}");
    expect(await screen.findByText("Create issue")).toBeInTheDocument();

    await user.keyboard("{ArrowDown}");
    expect(activeOption()).toHaveTextContent("Create issue");
    await user.keyboard("{ArrowLeft}");
    expect(screen.queryByText("Create issue")).not.toBeInTheDocument();
    expect(activeOption()).toHaveTextContent("GitHub");

    // Expanding Slack, below an open GitHub, closes GitHub and stays on Slack.
    await user.keyboard("{ArrowRight}");
    await screen.findByText("Create issue");
    await user.keyboard("{ArrowDown}{ArrowDown}");
    expect(activeOption()).toHaveTextContent("Slack");
    await user.keyboard("{Enter}");
    expect(await screen.findByText("Post message")).toBeInTheDocument();
    expect(screen.queryByText("Create issue")).not.toBeInTheDocument();
    expect(activeOption()).toHaveTextContent("Slack");

    // An entry under it still connects with ⇧↵.
    await user.keyboard("{ArrowDown}");
    expect(screen.getByText("Slack isn't connected.")).toBeInTheDocument();
    await user.keyboard("{Shift>}{Enter}{/Shift}");
    expect(handlers.onConnect).toHaveBeenCalledWith(expect.objectContaining({ ref: "slack/message.post@1" }));
  });

  it("offers the next page of a long integration as a row", async () => {
    const user = userEvent.setup();
    listCatalogIntegrations.mockResolvedValue(integrationsPage([listing("github", { connected: true, count: 3 })]));
    searchCatalog.mockImplementation((req: SearchCatalogRequest) =>
      Promise.resolve(
        req.pageToken === "p2"
          ? page([entry("github/c@1", { name: "Third" })], { total: 3 })
          : page([entry("github/a@1", { name: "First" }), entry("github/b@1", { name: "Second" })], { next: "p2", total: 3 }),
      ),
    );
    renderPalette({ initialFocus: { integration: "github" } });

    expect(await screen.findByText("Second")).toBeInTheDocument();
    await user.click(screen.getByText("Show 1 more"));
    expect(await screen.findByText("Third")).toBeInTheDocument();
    expect(screen.queryByText(/Show \d+ more/)).not.toBeInTheDocument();
  });

  it("opens on an integration, expanded and active, from the sidebar", async () => {
    listCatalogIntegrations.mockResolvedValue(integrationsPage([listing("github"), listing("slack")]));
    searchCatalog.mockResolvedValue(page([entry("slack/message.post@1", { name: "Post message", integration: "slack" })]));
    renderPalette({ initialFocus: { integration: "slack" } });

    expect(await screen.findByText("Post message")).toBeInTheDocument();
    expect(activeOption()).toHaveTextContent("Slack");
  });

  it("opens on the integrations list from the sidebar's All integrations", async () => {
    listCatalogIntegrations.mockResolvedValue(integrationsPage([listing("github"), listing("slack")]));
    renderPalette({ initialFocus: "integrations" });
    await waitFor(() => expect(activeOption()).toHaveTextContent("GitHub"));
  });

  it("folds the agent building blocks under Advanced, after the core steps", async () => {
    const user = userEvent.setup();
    const handlers = renderPalette();
    const builtIn = await screen.findByRole("group", { name: "Built-in" });
    await within(builtIn).findByText("Agent");

    expect(within(builtIn).queryByText("Run LLM Tool Calls")).not.toBeInTheDocument();
    expect(within(builtIn).queryByText("Compact")).not.toBeInTheDocument();
    expect(within(builtIn).getByText("Run Tool")).toBeInTheDocument();
    const labels = within(builtIn).getAllByRole("option").map((row) => row.querySelector(".font-medium")?.textContent);
    expect(labels).toEqual(["Agent", "Call LLM", "Run Tool", "Loop", "Switch", "Router", "Join", "Advanced: agent building blocks"]);

    await user.click(within(builtIn).getByText("Advanced: agent building blocks"));
    expect(within(builtIn).getByText("Run LLM Tool Calls")).toBeInTheDocument();
    await user.click(within(builtIn).getByText("Run LLM Tool Calls"));
    expect(handlers.onChooseBuiltin).toHaveBeenCalledWith("execute_tools");
  });

  it("filters the integrations by a category facet", async () => {
    const user = userEvent.setup();
    listCatalogIntegrations.mockImplementation((req: ListCatalogIntegrationsRequest) =>
      Promise.resolve(
        req.category === "communication"
          ? integrationsPage([listing("slack", { category: "communication" })], { facets: [["communication", 1], ["engineering", 1]] })
          : integrationsPage([listing("github"), listing("slack", { category: "communication" })], { facets: [["communication", 1], ["engineering", 1]] }),
      ),
    );
    renderPalette();
    await screen.findByText("GitHub");

    const facets = screen.getByRole("group", { name: "Category" });
    await user.click(within(facets).getByRole("button", { name: /Communication/ }));
    await waitFor(() => expect(screen.queryByText("GitHub")).not.toBeInTheDocument());
    expect(screen.getByText("Slack")).toBeInTheDocument();
    // A category narrows to integrations; built-ins have none.
    expect(screen.queryByText("Call LLM")).not.toBeInTheDocument();
  });

  it("shows designed loading and error-with-retry states for the integrations", async () => {
    const user = userEvent.setup();
    let reject!: (error: Error) => void;
    listCatalogIntegrations.mockReturnValueOnce(new Promise((_, r) => (reject = r)));
    renderPalette();

    expect(await screen.findByRole("status", { name: "Loading integrations" })).toBeInTheDocument();
    await act(async () => reject(new Error("down")));
    expect(await screen.findByRole("alert")).toHaveTextContent("Couldn't load integrations.");

    listCatalogIntegrations.mockResolvedValue(integrationsPage([listing("github")]));
    await user.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByText("GitHub")).toBeInTheDocument();
  });

  it("opens on triggers with built-in sources and integrations that have triggers", async () => {
    const user = userEvent.setup();
    listCatalogIntegrations.mockResolvedValue(integrationsPage([listing("github", { connected: true, count: 1 })]));
    searchCatalog.mockResolvedValue(page([entry("github/issue.opened@1", { name: "Issue opened", kind: CatalogEntryKind.TRIGGER })]));
    const handlers = renderPalette({ initialKind: "trigger" });

    const list = screen.getByRole("listbox", { name: "Triggers" });
    expect(within(list).getByText("Schedule")).toBeInTheDocument();
    expect(within(list).getByText("Webhook")).toBeInTheDocument();
    expect(within(list).getByText("When a workflow finishes")).toBeInTheDocument();
    expect(await within(list).findByText("1 trigger · Engineering")).toBeInTheDocument();
    expect(listCatalogIntegrations).toHaveBeenCalledWith(expect.objectContaining({ kinds: [CatalogEntryKind.TRIGGER] }), expect.anything());

    await user.keyboard("{Enter}");
    expect(handlers.onChooseBuiltinTrigger).toHaveBeenCalledWith("schedule");
    await user.keyboard("{End}{ArrowRight}");
    await screen.findByText("Issue opened");
    await user.keyboard("{ArrowDown}{Enter}");
    expect(handlers.onChooseTrigger).toHaveBeenCalledWith(expect.objectContaining({ ref: "github/issue.opened@1" }));
  });
});

describe("StepPalette: searching", () => {
  it("debounces the query and sends it to SearchCatalog, narrowing built-ins locally", async () => {
    const user = userEvent.setup();
    searchCatalog.mockImplementation((req: SearchCatalogRequest) =>
      Promise.resolve(page(req.query === "issue" ? [entry("github/issue.create@1", { name: "Create issue" })] : [])),
    );
    renderPalette();
    await screen.findByText("Call LLM");

    await user.type(input(), "issue");
    await screen.findByText("Create issue");
    const queries = searchCatalog.mock.calls.map(([req]) => (req as SearchCatalogRequest).query);
    // One request for the settled query: not one per keystroke, and none for the empty browse.
    expect(queries).toEqual(["issue"]);
    expect(screen.queryByText("Call LLM")).not.toBeInTheDocument();
    // A search result names its integration, since it is not under one.
    expect(within(optionNamed("Create issue")).getByText("GitHub")).toBeInTheDocument();
  });

  it("finds a renamed node by its old name and its YAML type", async () => {
    const user = userEvent.setup();
    renderPalette();
    await screen.findByText("Call LLM");

    await user.type(input(), "execute");
    expect(await screen.findByText("Run LLM Tool Calls")).toBeInTheDocument();
    await user.clear(input());
    await user.type(input(), "invoke");
    expect(await screen.findByText("Run Tool")).toBeInTheDocument();
  });

  it("moves with the arrow keys and adds the active item with Enter", async () => {
    const user = userEvent.setup();
    searchCatalog.mockResolvedValue(page([entry("github/issue.create@1", { name: "Create issue" })]));
    const handlers = renderPalette();
    await screen.findByText("Call LLM");
    await user.type(input(), "create");
    await screen.findByText("Create issue");

    expect(activeOption()).toHaveTextContent("Create issue");
    expect(activeOption()).toHaveAttribute("aria-selected", "true");
    await user.keyboard("{Enter}");
    expect(handlers.onChooseAction).toHaveBeenCalledWith(expect.objectContaining({ ref: "github/issue.create@1" }));
  });

  it("adds a built-in by type and closes on Escape", async () => {
    const user = userEvent.setup();
    const handlers = renderPalette();
    await screen.findByText("Agent");

    await user.keyboard("{Enter}");
    expect(handlers.onChooseBuiltin).toHaveBeenCalledWith("workflow");
    await user.keyboard("{Escape}");
    expect(handlers.onClose).toHaveBeenCalled();
  });

  it("loads the next page when the active option nears the end", async () => {
    const user = userEvent.setup();
    const first = Array.from({ length: 5 }, (_, i) => entry(`github/a${i}@1`, { name: `Action ${i}` }));
    searchCatalog.mockImplementation((req: SearchCatalogRequest) =>
      Promise.resolve(
        req.pageToken === "p2"
          ? page([entry("github/last@1", { name: "Last action" })], { total: 6 })
          : page(first, { next: "p2", total: 6 }),
      ),
    );
    renderPalette();
    await user.type(input(), "action");
    await screen.findByText("Action 0");
    expect(screen.getByText(/5 of 6/)).toBeInTheDocument();

    for (let i = 0; i < 5; i++) await user.keyboard("{ArrowDown}");
    await screen.findByText("Last action");
    expect(searchCatalog).toHaveBeenCalledWith(expect.objectContaining({ pageToken: "p2" }), expect.anything());
    expect(screen.getByText(/6 of 6/)).toBeInTheDocument();
  });

  it("offers Connect on an unconnected result, from the row and with Shift+Enter", async () => {
    const user = userEvent.setup();
    searchCatalog.mockResolvedValue(
      page([entry("slack/message.post@1", { name: "Post message", connected: false, mutates: true, integration: "slack" })]),
    );
    const handlers = renderPalette();
    await user.type(input(), "post");
    await screen.findByText("Post message");

    const row = optionNamed("Post message");
    expect(within(row).getByText("Connect")).toBeInTheDocument();
    expect(within(row).getByText("Changes data")).toBeInTheDocument();

    expect(screen.getByText("Slack isn't connected.")).toBeInTheDocument();
    await user.keyboard("{Shift>}{Enter}{/Shift}");
    expect(handlers.onConnect).toHaveBeenCalledWith(expect.objectContaining({ ref: "slack/message.post@1" }));
    expect(handlers.onChooseAction).not.toHaveBeenCalled();

    await user.click(within(row).getByText("Connect"));
    expect(handlers.onConnect).toHaveBeenCalledTimes(2);
  });

  it("shows designed error-with-retry and empty states", async () => {
    const user = userEvent.setup();
    searchCatalog.mockRejectedValueOnce(new Error("down"));
    renderPalette();
    await screen.findByText("Call LLM");

    await user.type(input(), "zzzz");
    expect(await screen.findByRole("alert")).toHaveTextContent("Couldn't search integrations.");
    searchCatalog.mockResolvedValue(page([]));
    await user.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByText("No steps match “zzzz”")).toBeInTheDocument();
  });
});
