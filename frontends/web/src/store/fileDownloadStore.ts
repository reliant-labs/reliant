/**
 * The Files tab's downloads that are too large for one response: a file
 * over DownloadFile's 25 MB cap (or of unknown size), and a folder as a
 * .zip. What the transfers panel renders, one row per download.
 *
 * The protocol (ranged reads, zipping, the fallback) is api/fileDownload.ts;
 * this store owns only the rows and each one's abort handle. Downloads run
 * at once, side by side: each is the user's explicit click, and each writes
 * to its own destination. `createFileDownloadStore` takes the client, so
 * tests drive it against a fake one.
 */

import { create } from "zustand";
import { FileSystemService } from "../gen/reliant/v1/filesystem_pb";
import { connectClient } from "../lib/connect";
import { retryAcrossWake } from "../lib/daemon-retry";
import {
  describeDownloadError,
  DownloadCancelledError,
  runFolderDownload,
  runRangedDownload,
  type DownloadScope,
  type DownloadSink,
  type FileDownloadClient,
  type FirstCallRunner,
} from "../api/fileDownload";

export type DownloadStatus = "preparing" | "downloading" | "done" | "failed" | "cancelled";

/** One download row of the transfers panel. */
export interface DownloadItem {
  id: string;
  /** What is saved: the file's name, or the folder's name + ".zip". */
  name: string;
  kind: "file" | "folder";
  /** "preparing" is a folder being zipped on the machine. */
  status: DownloadStatus;
  bytesDone: number;
  /** The whole size; 0 while unknown. */
  totalBytes: number;
  /** Zipping progress, for a folder. */
  filesDone?: number;
  filesTotal?: number;
  /** Entries the machine left out of a folder's archive. */
  skipped?: number;
  /** Why it failed, for a failed row. */
  error?: string;
}

export interface StartFileDownload {
  name: string;
  /** The path the request names (normalizeFilePathForAPI). */
  path: string;
  scope: DownloadScope;
  sink: DownloadSink;
  /** The size the tree listed, shown until the first piece says. */
  size?: number;
}

export interface StartFolderDownload {
  /** The folder's name; the row reads "<name>.zip". */
  name: string;
  /** Workspace-relative folder. */
  path: string;
  scope: DownloadScope;
  sink: DownloadSink;
}

/** How a download ended, for a caller that waits on it. */
export type DownloadOutcome = { status: "done" } | { status: "cancelled" } | { status: "failed"; error: string };

export interface FileDownloadState {
  items: DownloadItem[];
  /** Starts a ranged download of one file; resolves when it ends. */
  startFileDownload: (download: StartFileDownload) => Promise<DownloadOutcome>;
  /** Zips a folder on the machine and downloads it; resolves when it ends. */
  startFolderDownload: (download: StartFolderDownload) => Promise<DownloadOutcome>;
  /** Stops a running download; its partial file is discarded. */
  cancel: (id: string) => void;
  /** Removes a row that is not running. */
  dismiss: (id: string) => void;
  /** Removes every done and cancelled row. */
  clearFinished: () => void;
}

export interface FileDownloadStoreDeps {
  /** The client, or a getter for it (resolved per download). */
  client: FileDownloadClient | (() => FileDownloadClient);
  /** Runs a download's first call (retryAcrossWake in the app). */
  firstCall?: FirstCallRunner;
  pollIntervalMs?: number;
}

const ACTIVE: ReadonlySet<DownloadStatus> = new Set(["preparing", "downloading"]);
const FINISHED: ReadonlySet<DownloadStatus> = new Set(["done", "cancelled"]);

export function isDownloadActive(item: DownloadItem): boolean {
  return ACTIVE.has(item.status);
}

export function createFileDownloadStore(deps: FileDownloadStoreDeps) {
  const getClient = () => (typeof deps.client === "function" ? deps.client() : deps.client);
  // The abort handles stay out of the store so the rows stay plain data.
  const controllers = new Map<string, AbortController>();
  let nextId = 1;

  return create<FileDownloadState>()((set, get) => {
    const patch = (id: string, changes: Partial<DownloadItem>) =>
      set((state) => ({
        items: state.items.map((item) => (item.id === id ? { ...item, ...changes } : item)),
      }));

    const run = async (
      row: Omit<DownloadItem, "id">,
      transfer: (signal: AbortSignal, id: string) => Promise<Partial<DownloadItem>>,
    ): Promise<DownloadOutcome> => {
      const id = `download-${nextId++}`;
      const controller = new AbortController();
      controllers.set(id, controller);
      set((state) => ({ items: [...state.items, { ...row, id }] }));
      try {
        const done = await transfer(controller.signal, id);
        patch(id, { status: "done", ...done });
        return { status: "done" };
      } catch (err) {
        if (err instanceof DownloadCancelledError) {
          patch(id, { status: "cancelled" });
          return { status: "cancelled" };
        }
        console.error("[fileDownload] download failed", err);
        const error = describeDownloadError(err);
        patch(id, { status: "failed", error });
        return { status: "failed", error };
      } finally {
        controllers.delete(id);
      }
    };

    const onProgress = (id: string) => (bytesDone: number, totalBytes: number) =>
      patch(id, { status: "downloading", bytesDone, totalBytes });

    return {
      items: [],

      startFileDownload: ({ name, path, scope, sink, size }) =>
        run(
          { name, kind: "file", status: "downloading", bytesDone: 0, totalBytes: size ?? 0 },
          async (signal, id) => {
            const result = await runRangedDownload({
              client: getClient(),
              scope,
              source: { path },
              sink,
              signal,
              onProgress: onProgress(id),
              firstCall: deps.firstCall,
            });
            return { bytesDone: result.totalSize, totalBytes: result.totalSize };
          },
        ),

      startFolderDownload: ({ name, path, scope, sink }) =>
        run(
          { name: `${name}.zip`, kind: "folder", status: "preparing", bytesDone: 0, totalBytes: 0 },
          async (signal, id) => {
            const result = await runFolderDownload({
              client: getClient(),
              scope,
              path,
              sink,
              signal,
              onZipping: ({ filesDone, filesTotal }) => patch(id, { filesDone, filesTotal }),
              onProgress: onProgress(id),
              firstCall: deps.firstCall,
              pollIntervalMs: deps.pollIntervalMs,
            });
            return { bytesDone: result.totalSize, totalBytes: result.totalSize, skipped: result.skipped };
          },
        ),

      cancel: (id) => {
        // The transfer sees the abort, discards the partial file, and the row
        // turns cancelled when it returns.
        controllers.get(id)?.abort();
      },

      dismiss: (id) => {
        const item = get().items.find((candidate) => candidate.id === id);
        if (!item || isDownloadActive(item)) return;
        set((state) => ({ items: state.items.filter((candidate) => candidate.id !== id) }));
      },

      clearFinished: () => set((state) => ({ items: state.items.filter((item) => !FINISHED.has(item.status)) })),
    };
  });
}

let defaultClient: FileDownloadClient | undefined;

export const useFileDownloadStore = createFileDownloadStore({
  client: () => (defaultClient ??= connectClient(FileSystemService)),
  firstCall: (action, signal) => retryAcrossWake(action, { signal }),
});
