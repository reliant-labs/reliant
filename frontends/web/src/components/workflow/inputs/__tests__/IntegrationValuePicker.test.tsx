// Copyright (c) 2025 Reliant Labs

/**
 * IntegrationValuePicker: the nine states, the keyboard model and what it
 * announces. ListInputOptions is the only mock,
 * answering with the real generated messages; until the server implements it
 * (T6) these are the options the picker is exercised against.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { create } from "@bufbuild/protobuf";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";

import {
  InputOptionSchema,
  InputOptionsConnectionState,
  ListInputOptionsResponseSchema,
  type ListInputOptionsRequest,
} from "@/gen/reliant/v1/catalog_pb";

import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";

const listInputOptions = vi.fn();

import { IntegrationValuePicker, type IntegrationValuePickerProps } from "../IntegrationValuePicker";

function option(value: string, group = "acme", warnings: string[] = []) {
  return create(InputOptionSchema, { value, label: value, group, warnings });
}

function page(options: ReturnType<typeof option>[], extra: { next?: string; state?: InputOptionsConnectionState } = {}) {
  return create(ListInputOptionsResponseSchema, {
    options,
    nextPageToken: extra.next ?? "",
    connectionState: extra.state ?? InputOptionsConnectionState.CONNECTED,
  });
}

const REPOS = [option("acme/api"), option("acme/web"), option("acme/docs", "acme", ["The Reliant bot can't write here: you have read access only."]), option("other/lib", "other")];

function Harness(props: Partial<IntegrationValuePickerProps> & { initial?: string[] | string; onValue?: (v: unknown) => void }) {
  const [value, setValue] = useState<string[] | string>(props.initial ?? (props.multi === false ? "" : []));
  return (
    <form onSubmit={(e) => e.preventDefault()}>
      <span id="lbl">Repositories</span>
      <IntegrationValuePicker
        id="repos"
        labelId="lbl"
        label="Repositories"
        type="github.repository"
        multi
        integrationName="GitHub"
        {...props}
        value={value}
        onChange={(next) => {
          setValue(next);
          props.onValue?.(next);
        }}
      />
      <input aria-label="Next field" />
    </form>
  );
}

function renderPicker(props: Parameters<typeof Harness>[0] = {}) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <Harness {...props} />
    </QueryClientProvider>,
  );
}

const field = () => screen.getByRole("combobox", { name: "Repositories" });
const search = () => screen.getByRole("combobox", { name: /^Search / });
const options = () => within(screen.getByRole("listbox")).getAllByRole("option");
const activeOption = () => {
  const id = search().getAttribute("aria-activedescendant");
  return id ? document.getElementById(id) : null;
};

beforeEach(() => {
  setTransport(mockTransport({ "CatalogService.ListInputOptions": (req) => listInputOptions(req) }));
  listInputOptions.mockReset();
  listInputOptions.mockImplementation(async (req: ListInputOptionsRequest) => {
    if (req.resolve.length > 0) return page([]);
    return page(REPOS.filter((o) => !req.query || o.value.includes(req.query)));
  });
});

describe("IntegrationValuePicker states", () => {
  it("1 · loading: says what it is loading and shows skeleton rows", async () => {
    let resolve!: (v: unknown) => void;
    listInputOptions.mockImplementation(() => new Promise((r) => (resolve = r)));
    const user = userEvent.setup();
    renderPicker();
    expect(field()).toHaveTextContent("Loading repositories…");
    expect(field()).toHaveAttribute("aria-busy", "true");
    await user.click(field());
    expect(screen.getByTestId("picker-skeleton")).toBeInTheDocument();
    expect(screen.getByRole("listbox")).toHaveAttribute("aria-busy", "true");
    await act(async () => resolve(page(REPOS)));
  });

  it("2 · not connected: the field is blocked and offers Connect", async () => {
    listInputOptions.mockResolvedValue(page([], { state: InputOptionsConnectionState.NEEDS_CONNECTION }));
    const onConnect = vi.fn();
    const user = userEvent.setup();
    renderPicker({ onConnect });
    expect(await screen.findByText("Connect GitHub to choose repositories.")).toBeInTheDocument();
    expect(field()).toHaveAttribute("aria-disabled", "true");
    await user.click(field());
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /Connect GitHub/ }));
    expect(onConnect).toHaveBeenCalled();
  });

  it("3 · waiting on scope: focusable, disabled, and says where its options come from", async () => {
    const user = userEvent.setup();
    renderPicker({ type: "github.label", scopeMissing: true, scopeLabel: "Repositories", scopePlural: "repositories" });
    expect(field()).toHaveTextContent("Pick a repository first");
    expect(field()).toHaveAttribute("aria-disabled", "true");
    expect(screen.getByTestId("picker-scope-hint")).toHaveTextContent("Labels come from the repositories you choose under Repositories.");
    expect(field()).toHaveAttribute("aria-describedby", expect.stringContaining(screen.getByTestId("picker-scope-hint").id));
    await user.tab();
    expect(field()).toHaveFocus();
    expect(listInputOptions).not.toHaveBeenCalled();
  });

  it("4 · no options: an empty panel with Refresh", async () => {
    listInputOptions.mockResolvedValue(page([]));
    const user = userEvent.setup();
    renderPicker();
    await waitFor(() => expect(field()).toHaveTextContent("Choose repositories…"));
    await user.click(field());
    expect(screen.getByTestId("picker-empty")).toHaveTextContent("Nothing to choose from yet.");
    await user.click(screen.getByRole("button", { name: "Refresh" }));
    await waitFor(() => expect(listInputOptions.mock.calls.length).toBeGreaterThanOrEqual(2));
  });

  it("5 · options failed: says why, retries, and still lets a typed value in", async () => {
    listInputOptions.mockRejectedValue(new ConnectError("not implemented", Code.Unimplemented));
    const onValue = vi.fn();
    const user = userEvent.setup();
    renderPicker({ onValue });
    await waitFor(() => expect(listInputOptions).toHaveBeenCalled());
    await user.click(field());
    expect(await screen.findByTestId("picker-error")).toHaveTextContent("Couldn't load repositories: this server can't list them yet.");
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
    await user.type(search(), "acme/new");
    const use = await screen.findByTestId("picker-use-query");
    expect(use).toHaveTextContent("Use “acme/new” · Not checked yet. It is checked when you save.");
    await user.keyboard("{Enter}");
    expect(onValue).toHaveBeenLastCalledWith(["acme/new"]);
  });

  it("6 · no match: says nothing matches the query", async () => {
    const user = userEvent.setup();
    renderPicker();
    await waitFor(() => expect(field()).toHaveTextContent("Choose repositories…"));
    await user.click(field());
    await user.type(search(), "zzz");
    expect(await screen.findByTestId("picker-no-match")).toHaveTextContent("No repositories match “zzz”.");
  });

  it("7 · a stored value the list no longer has is marked, removable, and listed apart", async () => {
    const user = userEvent.setup();
    renderPicker({ initial: ["acme/api", "gone/repo"] });
    const chips = await screen.findAllByTestId("picker-chip");
    await waitFor(() => expect(chips[1]).toHaveAttribute("data-unreachable", "true"));
    expect(chips[1]).toHaveTextContent("gone/repo · no longer reachable");
    await user.click(field());
    const group = screen.getByRole("group", { name: "No longer reachable" });
    expect(within(group).getByRole("option", { name: /gone\/repo/ })).toHaveAttribute("aria-selected", "true");
    await user.click(screen.getByRole("button", { name: "Remove gone/repo" }));
    expect(screen.getAllByTestId("picker-chip")).toHaveLength(1);
  });

  it("8 · free text: offers to use a value that does not exist yet", async () => {
    const user = userEvent.setup();
    renderPicker({ type: "github.label", freeText: true, multi: false, initial: "" });
    await waitFor(() => expect(field()).toHaveTextContent("Choose a label…"));
    await user.click(field());
    await user.type(search(), "needs-review");
    expect(await screen.findByTestId("picker-use-query")).toHaveTextContent("Use “needs-review” · created on first use");
  });

  it("9 · over the cap: says only the first page is shown, and loads more at the end", async () => {
    listInputOptions.mockImplementation(async (req: ListInputOptionsRequest) => {
      if (req.resolve.length > 0) return page([]);
      if (!req.pageToken) return page([option("acme/a"), option("acme/b")], { next: "p2" });
      return page([option("acme/c")]);
    });
    const user = userEvent.setup();
    renderPicker();
    await waitFor(() => expect(field()).toHaveTextContent("Choose repositories…"));
    await user.click(field());
    expect(screen.getByTestId("picker-footer")).toHaveTextContent("Showing the first 2 repositories. Type to search the rest.");
    // PageDown past the end asks for the next page.
    await waitFor(() => expect(search()).toHaveFocus());
    await user.keyboard("{PageDown}");
    await waitFor(() => expect(options().map((o) => o.textContent)).toContain("acme/c"));
    expect(listInputOptions.mock.calls.some(([req]) => (req as ListInputOptionsRequest).pageToken === "p2")).toBe(true);
  });

  it("shows an option's warning under it and on its chip, without blocking it", async () => {
    const user = userEvent.setup();
    renderPicker();
    await waitFor(() => expect(field()).toHaveTextContent("Choose repositories…"));
    await user.click(field());
    const docs = screen.getByRole("option", { name: /acme\/docs/ });
    expect(docs).toHaveAccessibleDescription("The Reliant bot can't write here: you have read access only.");
    await user.click(docs);
    expect(screen.getByTestId("picker-chip")).toHaveTextContent("acme/docs");
    expect(screen.getByTestId("picker-chip")).toHaveAttribute("title", "The Reliant bot can't write here: you have read access only.");
  });
});

describe("IntegrationValuePicker keyboard", () => {
  async function opened(props: Parameters<typeof Harness>[0] = {}) {
    const user = userEvent.setup();
    renderPicker(props);
    await waitFor(() => expect(field()).not.toHaveTextContent("Loading"));
    field().focus();
    return user;
  }

  it("Down opens and focuses the search; Up opens on the last option", async () => {
    const user = await opened();
    await user.keyboard("{ArrowDown}");
    await waitFor(() => expect(search()).toHaveFocus());
    expect(field()).toHaveAttribute("aria-expanded", "true");
    expect(activeOption()).toHaveTextContent("Select all 3 in acme");
    await user.keyboard("{Escape}");
    await waitFor(() => expect(field()).toHaveFocus());
    await user.keyboard("{ArrowUp}");
    await waitFor(() => expect(activeOption()).toHaveTextContent("other/lib"));
  });

  it("Enter and Space open the closed field", async () => {
    const user = await opened();
    await user.keyboard("{Enter}");
    await waitFor(() => expect(search()).toHaveFocus());
    await user.keyboard("{Escape}");
    await waitFor(() => expect(field()).toHaveFocus());
    await user.keyboard(" ");
    await waitFor(() => expect(search()).toHaveFocus());
  });

  it("arrows, Home and End move the active option; Enter toggles it and stays open (multi)", async () => {
    const onValue = vi.fn();
    const user = await opened({ onValue });
    await user.keyboard("{ArrowDown}");
    await waitFor(() => expect(search()).toHaveFocus());
    await user.keyboard("{ArrowDown}");
    expect(activeOption()).toHaveTextContent("acme/api");
    await user.keyboard("{End}");
    expect(activeOption()).toHaveTextContent("other/lib");
    await user.keyboard("{Home}{ArrowDown}{Enter}");
    expect(onValue).toHaveBeenLastCalledWith(["acme/api"]);
    expect(screen.getByRole("listbox")).toBeInTheDocument();
    expect(screen.getByRole("option", { name: /acme\/api/ })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByTestId("picker-live")).toHaveTextContent("acme/api selected, 1 selected");
  });

  it("Enter in single mode selects and closes, back on the field", async () => {
    const onValue = vi.fn();
    const user = await opened({ multi: false, initial: "", onValue });
    await user.keyboard("{ArrowDown}");
    await waitFor(() => expect(search()).toHaveFocus());
    await user.keyboard("{ArrowDown}{Enter}");
    expect(onValue).toHaveBeenLastCalledWith("acme/web");
    await waitFor(() => expect(field()).toHaveFocus());
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    expect(field()).toHaveTextContent("acme/web");
  });

  it("Select all adds every option of a group", async () => {
    const onValue = vi.fn();
    const user = await opened({ onValue });
    await user.keyboard("{ArrowDown}");
    await waitFor(() => expect(search()).toHaveFocus());
    await user.keyboard("{Enter}");
    expect(onValue).toHaveBeenLastCalledWith(["acme/api", "acme/web", "acme/docs"]);
  });

  it("Escape closes the popover only, and does not reach the dialog around it", async () => {
    const outer = vi.fn();
    document.addEventListener("keydown", outer);
    const user = await opened();
    await user.keyboard("{ArrowDown}");
    await waitFor(() => expect(search()).toHaveFocus());
    await user.keyboard("{Escape}");
    expect(outer).not.toHaveBeenCalledWith(expect.objectContaining({ key: "Escape" }));
    document.removeEventListener("keydown", outer);
    await waitFor(() => expect(field()).toHaveFocus());
  });

  it("Backspace on an empty search removes the last chip", async () => {
    const onValue = vi.fn();
    const user = await opened({ initial: ["acme/api", "acme/web"], onValue });
    await user.keyboard("{ArrowDown}");
    await waitFor(() => expect(search()).toHaveFocus());
    await user.keyboard("{Backspace}");
    expect(onValue).toHaveBeenLastCalledWith(["acme/api"]);
  });

  it("Tab closes, keeps the selection and moves to the next field", async () => {
    const user = await opened({ initial: ["acme/api"] });
    await user.keyboard("{ArrowDown}");
    await waitFor(() => expect(search()).toHaveFocus());
    await user.keyboard("{Tab}");
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "Next field" })).toHaveFocus();
    expect(screen.getAllByTestId("picker-chip")).toHaveLength(1);
  });

  it("chips: Left from the field reaches the last chip, arrows move, Backspace removes and steps back", async () => {
    const onValue = vi.fn();
    const user = await opened({ initial: ["acme/api", "acme/web"], onValue });
    await user.keyboard("{ArrowLeft}");
    expect(screen.getByRole("button", { name: "Remove acme/web" })).toHaveFocus();
    await user.keyboard("{ArrowLeft}");
    expect(screen.getByRole("button", { name: "Remove acme/api" })).toHaveFocus();
    await user.keyboard("{ArrowRight}{ArrowRight}");
    expect(field()).toHaveFocus();
    await user.keyboard("{ArrowLeft}{Backspace}");
    expect(onValue).toHaveBeenLastCalledWith(["acme/api"]);
    await waitFor(() => expect(screen.getByRole("button", { name: "Remove acme/api" })).toHaveFocus());
  });

  it("announces how many options a search settled on", async () => {
    const user = await opened();
    await user.keyboard("{ArrowDown}");
    await waitFor(() => expect(search()).toHaveFocus());
    await user.type(search(), "web");
    await waitFor(() => expect(screen.getByTestId("picker-live")).toHaveTextContent("1 repository"), { timeout: 2000 });
  });

  it("names the listbox and marks it multi-selectable", async () => {
    const user = await opened();
    await user.keyboard("{ArrowDown}");
    await waitFor(() => expect(search()).toHaveFocus());
    const listbox = screen.getByRole("listbox", { name: "Repositories" });
    expect(listbox).toHaveAttribute("aria-multiselectable", "true");
    expect(field()).toHaveAttribute("aria-controls", listbox.id);
  });
});
