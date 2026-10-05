/**
 * The step palette (research/INTEGRATIONS_V1_BRIEF.md §3a): one searchable
 * list of built-in steps and integration catalog entries. The RPC client is
 * the only mock, so search, paging, facets and keyboard handling are asserted
 * on what SearchCatalog returned and what the user did.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create } from "@bufbuild/protobuf";

import {
  CatalogEntryKind,
  CatalogEntrySummarySchema,
  CatalogIntegrationSchema,
  NodeInfoSchema,
  SearchCatalogResponseSchema,
  type SearchCatalogRequest,
} from "@/gen/reliant/v1/catalog_pb";
import { renderWithQuery } from "@/test/renderWithQuery";

const searchCatalog = vi.fn();
const getCatalogEntry = vi.fn();
const listNodes = vi.fn();

vi.mock("@/api/grpc-client", () => ({
  grpcClient: { catalog: () => ({ searchCatalog, getCatalogEntry }) },
  getCatalogClient: () => ({ listNodes }),
  getGRPCBaseURLPublic: () => null,
}));

import { StepPalette, type StepPaletteProps } from "../StepPalette";

function entry(ref: string, opts: { name?: string; connected?: boolean; mutates?: boolean; kind?: CatalogEntryKind; integration?: string; category?: string } = {}) {
  const integration = opts.integration ?? ref.split("/")[0]!;
  return create(CatalogEntrySummarySchema, {
    ref,
    kind: opts.kind ?? CatalogEntryKind.ACTION,
    id: ref.split("/")[1]!.split("@")[0]!,
    displayName: opts.name ?? ref,
    summary: `Summary of ${ref}`,
    integration: create(CatalogIntegrationSchema, {
      id: integration,
      version: 1,
      displayName: integration === "github" ? "GitHub" : integration === "slack" ? "Slack" : integration,
      icon: integration,
      category: opts.category ?? "engineering",
    }),
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

beforeEach(() => {
  searchCatalog.mockReset();
  getCatalogEntry.mockReset();
  getCatalogEntry.mockReturnValue(new Promise(() => undefined));
  listNodes.mockReset();
  listNodes.mockResolvedValue({
    nodes: [
      create(NodeInfoSchema, { id: "call_llm", displayName: "Call LLM", description: "Ask a model" }),
      create(NodeInfoSchema, { id: "action", displayName: "Action", description: "Generic action" }),
    ],
  });
});

describe("StepPalette", () => {
  it("lists built-ins and integrations in one listbox, without the generic action node", async () => {
    searchCatalog.mockResolvedValue(page([entry("github/issue.create@1", { name: "Create issue" })]));
    renderPalette();

    const list = screen.getByRole("listbox", { name: "Steps" });
    await within(list).findByText("Create issue");
    expect(within(list).getByText("Call LLM")).toBeInTheDocument();
    expect(within(list).getByText("Loop")).toBeInTheDocument();
    expect(within(list).queryByText("Generic action")).not.toBeInTheDocument();
    expect(searchCatalog).toHaveBeenCalledWith(
      expect.objectContaining({ query: "", kinds: [CatalogEntryKind.ACTION], pageSize: 20, pageToken: "" }),
      expect.anything(),
    );
  });

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
    // One request for the empty browse and one for the settled query — not one per keystroke.
    expect(queries).toEqual(["", "issue"]);
    expect(screen.queryByText("Call LLM")).not.toBeInTheDocument();
  });

  it("moves with the arrow keys and adds the active item with Enter", async () => {
    const user = userEvent.setup();
    searchCatalog.mockResolvedValue(page([entry("github/issue.create@1", { name: "Create issue" })]));
    const handlers = renderPalette();
    await screen.findByText("Create issue");

    expect(activeOption()).toHaveTextContent("Call LLM");
    expect(activeOption()).toHaveAttribute("aria-selected", "true");
    await user.keyboard("{End}");
    expect(activeOption()).toHaveTextContent("Create issue");
    await user.keyboard("{ArrowUp}");
    expect(activeOption()).toHaveTextContent("Join");
    await user.keyboard("{ArrowDown}{Enter}");
    expect(handlers.onChooseAction).toHaveBeenCalledWith(expect.objectContaining({ ref: "github/issue.create@1" }));
  });

  it("adds a built-in by type and closes on Escape", async () => {
    const user = userEvent.setup();
    searchCatalog.mockResolvedValue(page([]));
    const handlers = renderPalette();
    await screen.findByText("Call LLM");

    await user.keyboard("{Enter}");
    expect(handlers.onChooseBuiltin).toHaveBeenCalledWith("call_llm");
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
    renderPalette({ initialKind: "trigger", allowKindSwitch: true });
    // Triggers have only 3 built-ins, so the catalog rows are reached quickly.
    await user.click(screen.getByRole("button", { name: "Steps" }));
    await screen.findByText("Action 0");
    expect(screen.getByText(/5 of 6/)).toBeInTheDocument();

    await user.keyboard("{End}");
    await screen.findByText("Last action");
    expect(searchCatalog).toHaveBeenCalledWith(expect.objectContaining({ pageToken: "p2" }), expect.anything());
    expect(screen.getByText(/6 of 6/)).toBeInTheDocument();
  });

  it("filters by a category facet and offers a way back from an empty category", async () => {
    const user = userEvent.setup();
    searchCatalog.mockImplementation((req: SearchCatalogRequest) =>
      Promise.resolve(
        req.category === "communication"
          ? page([], { facets: [["engineering", 1], ["communication", 0]] })
          : page([entry("github/issue.create@1", { name: "Create issue" })], { facets: [["engineering", 1], ["communication", 2]] }),
      ),
    );
    renderPalette();
    await screen.findByText("Create issue");

    const facets = screen.getByRole("group", { name: "Category" });
    await user.click(within(facets).getByRole("button", { name: /Communication/ }));
    expect(within(facets).getByRole("button", { name: /Communication/ })).toHaveAttribute("aria-pressed", "true");
    expect(await screen.findByText(/Try another category/)).toBeInTheDocument();
    // A category narrows to integrations; built-ins have none.
    expect(screen.queryByText("Call LLM")).not.toBeInTheDocument();
    expect(searchCatalog).toHaveBeenCalledWith(expect.objectContaining({ category: "communication" }), expect.anything());

    await user.click(screen.getByRole("button", { name: "Search all categories" }));
    await screen.findByText("Create issue");
  });

  it("offers Connect on an unconnected entry, from the row and with Shift+Enter", async () => {
    const user = userEvent.setup();
    searchCatalog.mockResolvedValue(
      page([entry("slack/message.post@1", { name: "Post message", connected: false, mutates: true, integration: "slack" })]),
    );
    const handlers = renderPalette();
    await screen.findByText("Post message");

    const row = screen.getByText("Post message").closest('[role="option"]') as HTMLElement;
    expect(within(row).getByText("Connect")).toBeInTheDocument();
    expect(within(row).getByText("Changes data")).toBeInTheDocument();

    await user.keyboard("{End}");
    expect(screen.getByText("Slack isn't connected.")).toBeInTheDocument();
    await user.keyboard("{Shift>}{Enter}{/Shift}");
    expect(handlers.onConnect).toHaveBeenCalledWith(expect.objectContaining({ ref: "slack/message.post@1" }));
    expect(handlers.onChooseAction).not.toHaveBeenCalled();

    await user.click(within(row).getByText("Connect"));
    expect(handlers.onConnect).toHaveBeenCalledTimes(2);
  });

  it("shows designed loading, error-with-retry and empty states", async () => {
    const user = userEvent.setup();
    let reject!: (error: Error) => void;
    searchCatalog.mockReturnValueOnce(new Promise((_, r) => (reject = r)));
    renderPalette();

    expect(await screen.findByRole("status", { name: "Searching integrations" })).toBeInTheDocument();
    await act(async () => reject(new Error("down")));
    expect(await screen.findByRole("alert")).toHaveTextContent("Couldn't search integrations.");

    searchCatalog.mockResolvedValue(page([]));
    await user.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(screen.queryByRole("alert")).not.toBeInTheDocument());

    await user.type(input(), "zzzz");
    expect(await screen.findByText("No steps match “zzzz”")).toBeInTheDocument();
  });

  it("opens on triggers with built-in sources and routes each choice", async () => {
    const user = userEvent.setup();
    searchCatalog.mockResolvedValue(
      page([entry("github/issue.opened@1", { name: "Issue opened", kind: CatalogEntryKind.TRIGGER })]),
    );
    const handlers = renderPalette({ initialKind: "trigger" });

    const list = screen.getByRole("listbox", { name: "Triggers" });
    await within(list).findByText("Issue opened");
    expect(within(list).getByText("Schedule")).toBeInTheDocument();
    expect(within(list).getByText("Webhook")).toBeInTheDocument();
    expect(within(list).getByText("When a workflow finishes")).toBeInTheDocument();
    expect(searchCatalog).toHaveBeenCalledWith(expect.objectContaining({ kinds: [CatalogEntryKind.TRIGGER] }), expect.anything());

    await user.keyboard("{Enter}");
    expect(handlers.onChooseBuiltinTrigger).toHaveBeenCalledWith("schedule");
    await user.keyboard("{End}{Enter}");
    expect(handlers.onChooseTrigger).toHaveBeenCalledWith(expect.objectContaining({ ref: "github/issue.opened@1" }));
  });
});
