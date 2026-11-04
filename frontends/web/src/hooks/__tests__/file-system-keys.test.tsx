// Copyright (c) 2025 Reliant Labs

/**
 * FileSystemService's two pull reads live on the generated keys
 * (hooks/file-system-queries.ts): content search and the directory picker's
 * listings. The rest of the service — tree, content, the writes — stays
 * hand-written in api/fileSystem.ts and never touches this cache. These pin:
 *
 *   1. the directory picker refetches its listing after a CreateDirectory;
 *   2. a search is keyed by its term, options, project and chat, is shared by
 *      every reader of one key, and is not refetched by unrelated writes;
 *   3. a ReplaceInFiles re-runs the mounted search.
 *
 * Answered at the transport.
 */

import type { ReactNode } from "react";
import { act, fireEvent, render, renderHook, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { createFile, replaceInFiles, saveFileContent } from "@/api/fileSystem";
import { DirectoryPicker } from "@/components/Projects/DirectoryPicker";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { useChatStore } from "@/store/chatStore";
import { useProjectStore } from "@/store/projectStore";
import { serviceHandlers } from "@/test/serviceTransport";
import { FILE_SEARCH_DEBOUNCE_MS, useFileSearch, type FileSearchOptions } from "../file-system-queries";

const PROJECT = "p-files";
const CHAT = "chat-files";

const server = {
  searchFiles: vi.fn(),
  listDirectory: vi.fn(),
  createDirectory: vi.fn(),
  saveFileContent: vi.fn(),
  createFileOrFolder: vi.fn(),
  replaceInFiles: vi.fn(),
};

let client: QueryClient;
function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

const OPTIONS: FileSearchOptions = { worktreeId: "wt-1", caseSensitive: false, maxResults: 100, contextLines: 2 };

function hit(path: string) {
  return { path, matches: [{ lineNumber: 1, lineContent: "needle", matchStart: 0, matchEnd: 6 }] };
}

beforeEach(() => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  for (const fn of Object.values(server)) fn.mockReset();
  server.searchFiles.mockImplementation(async (req: { query: string }) => ({
    results: [hit(`/ws/${req.query}.ts`)],
    totalMatches: 1,
  }));
  server.saveFileContent.mockResolvedValue({});
  server.createFileOrFolder.mockResolvedValue({});
  setTransport(mockTransport(serviceHandlers("FileSystemService", server)));
  useProjectStore.setState({ currentProject: { id: PROJECT, name: "p", path: "/ws" } } as never);
  useChatStore.setState({ activeChatId: CHAT });
});

afterEach(() => {
  vi.useRealTimers();
  setTransport(null);
  client.clear();
  useProjectStore.setState({ currentProject: null } as never);
  useChatStore.setState({ activeChatId: null });
});

describe("content search", () => {
  it("sends the debounced term with the project, chat and options", async () => {
    const { result } = renderHook(() => useFileSearch("needle", OPTIONS), { wrapper });

    await waitFor(() => expect(result.current.results.map((r) => r.path)).toEqual(["/ws/needle.ts"]));
    expect(server.searchFiles).toHaveBeenCalledTimes(1);
    expect(server.searchFiles.mock.calls[0][0]).toMatchObject({
      projectId: PROJECT,
      chatId: CHAT,
      worktreeId: "wt-1",
      query: "needle",
      caseSensitive: false,
      maxResults: 100,
      contextLines: 2,
    });
  });

  it("an empty query, or a closed search, sends nothing", async () => {
    const { result, rerender } = renderHook(
      ({ q, enabled }: { q: string; enabled: boolean }) => useFileSearch(q, { ...OPTIONS, enabled }),
      { wrapper, initialProps: { q: "  ", enabled: true } },
    );
    rerender({ q: "needle", enabled: false });
    await new Promise((r) => setTimeout(r, FILE_SEARCH_DEBOUNCE_MS + 50));
    expect(server.searchFiles).not.toHaveBeenCalled();
    expect(result.current.results).toEqual([]);
  });

  it("is keyed by term: two readers of one term share a request, a new term is a new one", async () => {
    const { result, rerender } = renderHook(
      ({ q }: { q: string }) => ({ a: useFileSearch(q, OPTIONS), b: useFileSearch(q, OPTIONS) }),
      { wrapper, initialProps: { q: "needle" } },
    );
    await waitFor(() => expect(result.current.b.results).toHaveLength(1));
    expect(server.searchFiles).toHaveBeenCalledTimes(1);

    // Each reader debounces on its own timer. Real timers can fire in two
    // different ticks on a loaded host, and the second reader then mounts on a
    // search that already resolved (stale, staleTime 0) and refetches it. Both
    // timers fire inside one act() here, so both readers move to the new key in
    // one render, which is what the test is about. shouldAdvanceTime keeps the
    // clock moving for React Query's own zero-delay timers; afterEach restores it.
    vi.useFakeTimers({ shouldAdvanceTime: true });
    rerender({ q: "pin" });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(FILE_SEARCH_DEBOUNCE_MS);
    });
    await waitFor(() => expect(result.current.a.results.map((r) => r.path)).toEqual(["/ws/pin.ts"]));
    expect(server.searchFiles).toHaveBeenCalledTimes(2);
    expect(server.searchFiles.mock.calls[1][0].query).toBe("pin");
  });

  it("an option is part of the key: changing one searches again", async () => {
    const { result, rerender } = renderHook(
      ({ caseSensitive }: { caseSensitive: boolean }) => useFileSearch("needle", { ...OPTIONS, caseSensitive }),
      { wrapper, initialProps: { caseSensitive: false } },
    );
    await waitFor(() => expect(result.current.results).toHaveLength(1));

    rerender({ caseSensitive: true });
    await waitFor(() => expect(server.searchFiles).toHaveBeenCalledTimes(2));
    expect(server.searchFiles.mock.calls[1][0].caseSensitive).toBe(true);
  });

  it("is not refetched by unrelated writes", async () => {
    const { result } = renderHook(() => useFileSearch("needle", OPTIONS), { wrapper });
    await waitFor(() => expect(result.current.results).toHaveLength(1));

    await act(async () => {
      await saveFileContent("/ws/needle.ts", "changed", undefined);
      await createFile("/ws/new.ts", "", undefined);
    });
    await new Promise((r) => setTimeout(r, 50));

    expect(server.saveFileContent).toHaveBeenCalledTimes(1);
    expect(server.createFileOrFolder).toHaveBeenCalledTimes(1);
    expect(server.searchFiles).toHaveBeenCalledTimes(1);
  });

  it("a ReplaceInFiles re-runs the mounted search", async () => {
    server.replaceInFiles.mockResolvedValue({
      results: [{ path: "/ws/needle.ts", replacements: 1, success: true }],
      totalReplacements: 1,
      filesModified: 1,
    });
    const { result } = renderHook(() => useFileSearch("needle", OPTIONS), { wrapper });
    await waitFor(() => expect(result.current.results).toHaveLength(1));

    server.searchFiles.mockResolvedValue({ results: [], totalMatches: 0 });
    await act(async () => {
      await replaceInFiles("needle", "pin", { worktreeId: "wt-1" }, client);
    });

    await waitFor(() => expect(result.current.results).toEqual([]));
    expect(server.searchFiles).toHaveBeenCalledTimes(2);
  });
});

describe("directory picker", () => {
  it("refetches the listing after a CreateDirectory, so the new folder appears", async () => {
    server.listDirectory.mockResolvedValue({
      path: "/home/u",
      entries: [{ name: "code", path: "/home/u/code", isDirectory: true }],
    });
    server.createDirectory.mockResolvedValue({ path: "/home/u/fresh" });

    render(<DirectoryPicker isOpen onClose={() => {}} onSelect={() => {}} />, { wrapper });
    expect(await screen.findByText("code")).toBeInTheDocument();
    expect(server.listDirectory).toHaveBeenCalledTimes(1);
    expect(server.listDirectory.mock.calls[0][0].path).toBe("");

    server.listDirectory.mockResolvedValue({
      path: "/home/u",
      entries: [
        { name: "code", path: "/home/u/code", isDirectory: true },
        { name: "fresh", path: "/home/u/fresh", isDirectory: true },
      ],
    });
    fireEvent.click(screen.getByText("New folder"));
    fireEvent.change(screen.getByPlaceholderText("New folder name"), { target: { value: "fresh" } });
    fireEvent.click(screen.getByText("Create"));

    expect(await screen.findByText("fresh")).toBeInTheDocument();
    expect(server.createDirectory).toHaveBeenCalledTimes(1);
    expect(server.createDirectory.mock.calls[0][0].path).toBe("/home/u/fresh");
    expect(server.listDirectory).toHaveBeenCalledTimes(2);
    expect(screen.queryByPlaceholderText("New folder name")).not.toBeInTheDocument();
  });

  it("a failed create shows its error inline and lists nothing new", async () => {
    server.listDirectory.mockResolvedValue({ path: "/home/u", entries: [] });
    server.createDirectory.mockRejectedValue(new Error("permission denied"));

    render(<DirectoryPicker isOpen onClose={() => {}} onSelect={() => {}} />, { wrapper });
    expect(await screen.findByText("Empty directory")).toBeInTheDocument();

    fireEvent.click(screen.getByText("New folder"));
    fireEvent.change(screen.getByPlaceholderText("New folder name"), { target: { value: "x" } });
    fireEvent.click(screen.getByText("Create"));

    expect(await screen.findByText(/permission denied/)).toBeInTheDocument();
    expect(server.listDirectory).toHaveBeenCalledTimes(1);
  });

  it("navigating into a folder lists it, and a failed listing keeps the way back", async () => {
    server.listDirectory.mockImplementation(async (req: { path: string }) => {
      if (req.path === "/home/u/locked") throw new Error("access denied");
      return { path: "/home/u", entries: [{ name: "locked", path: "/home/u/locked", isDirectory: true }] };
    });

    render(<DirectoryPicker isOpen onClose={() => {}} onSelect={() => {}} />, { wrapper });
    fireEvent.click(await screen.findByText("locked"));

    expect(await screen.findByRole("alert")).toHaveTextContent("access denied");
    // The last listed directory is still the one "Up" and the crumbs work from.
    expect(screen.getByTitle("/home/u")).toBeInTheDocument();
  });
});
