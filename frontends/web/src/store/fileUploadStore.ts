/**
 * The Files tab's upload queue: what the progress panel and the clash dialog
 * render, and the one loop that uploads queued files one at a time.
 *
 * The protocol itself (Begin, chunks, Commit, resume) is api/fileUpload.ts;
 * this store owns only the queue, each row's state, and the open "already
 * exists" prompt. `createFileUploadStore` takes the client, so tests drive the
 * whole queue against a fake one.
 */

import { create } from "zustand";
import { FileSystemService } from "../gen/reliant/v1/filesystem_pb";
import { connectClient } from "../lib/connect";
import {
  abortUploadSession,
  describeUploadError,
  newUploadSession,
  runUpload,
  UploadCancelledError,
  type ConflictChoice,
  type FileUploadClient,
  type UploadScope,
  type UploadSession,
  type UploadSource,
} from "../api/fileUpload";
import { triggerGitStatusRefresh } from "./gitStatusStore";

export type UploadStatus = "queued" | "uploading" | "done" | "failed" | "cancelled";

/** One row of the progress panel. */
export interface UploadItem {
  id: string;
  name: string;
  /** Workspace-relative destination folder; "" is the workspace root. */
  directory: string;
  size: number;
  bytesReceived: number;
  status: UploadStatus;
  /** Why it failed, for a failed row. */
  error?: string;
  /** Where it landed, for a done row. */
  path?: string;
  projectId: string;
  worktreeId?: string;
}

/** The "already exists" prompt the queue is waiting on. */
export interface UploadConflict {
  itemId: string;
  name: string;
  directory: string;
}

/** The last upload that landed: the tree re-reads that folder. */
export interface UploadCommitted {
  seq: number;
  directory: string;
  path: string;
  projectId: string;
  worktreeId?: string;
}

export interface FileUploadState {
  items: UploadItem[];
  conflict: UploadConflict | null;
  lastCommitted: UploadCommitted | null;
  /** Queues `files` for upload into `directory` on the machine `scope` names. */
  enqueue: (files: UploadSource[], directory: string, scope: UploadScope) => void;
  /** Stops a queued or running upload and discards what the machine staged. */
  cancel: (id: string) => void;
  /** Runs a failed (or cancelled) upload again, resuming where the machine has it. */
  retry: (id: string) => void;
  /** Removes a row that is not running. */
  dismiss: (id: string) => void;
  /** Removes every done and cancelled row. */
  clearFinished: () => void;
  /** Answers the open "already exists" prompt. */
  resolveConflict: (choice: ConflictChoice) => void;
}

export interface FileUploadStoreDeps {
  /** The client, or a getter for it (resolved per upload). */
  client: FileUploadClient | (() => FileUploadClient);
  /** Called after each file lands. */
  onCommitted?: (committed: UploadCommitted) => void;
}

interface Job {
  file: UploadSource;
  scope: UploadScope;
  session: UploadSession;
  controller?: AbortController;
}

const FINISHED: ReadonlySet<UploadStatus> = new Set(["done", "cancelled"]);

export function createFileUploadStore(deps: FileUploadStoreDeps) {
  const getClient = () => (typeof deps.client === "function" ? deps.client() : deps.client);
  // Non-serializable per-row state (the File, the open session, the abort
  // handle) stays out of the store so the rows stay plain data.
  const jobs = new Map<string, Job>();
  let nextId = 1;
  let commitSeq = 0;
  let running = false;
  let pendingConflict: { itemId: string; resolve: (choice: ConflictChoice) => void } | null = null;

  return create<FileUploadState>()((set, get) => {
    const patch = (id: string, changes: Partial<UploadItem>) =>
      set((state) => ({
        items: state.items.map((item) => (item.id === id ? { ...item, ...changes } : item)),
      }));

    const answerConflict = (choice: ConflictChoice) => {
      const pending = pendingConflict;
      pendingConflict = null;
      set({ conflict: null });
      pending?.resolve(choice);
    };

    const runOne = async (item: UploadItem, job: Job) => {
      const controller = new AbortController();
      job.controller = controller;
      patch(item.id, { status: "uploading", error: undefined });
      try {
        const result = await runUpload({
          client: getClient(),
          scope: job.scope,
          file: job.file,
          directory: item.directory,
          session: job.session,
          signal: controller.signal,
          onProgress: (bytesReceived) => patch(item.id, { bytesReceived }),
          resolveConflict: () =>
            new Promise<ConflictChoice>((resolve) => {
              pendingConflict = { itemId: item.id, resolve };
              set({ conflict: { itemId: item.id, name: item.name, directory: item.directory } });
            }),
        });
        patch(item.id, { status: "done", bytesReceived: item.size, path: result.path });
        const committed: UploadCommitted = {
          seq: ++commitSeq,
          directory: item.directory,
          path: result.path,
          projectId: job.scope.projectId,
          worktreeId: job.scope.worktreeId,
        };
        set({ lastCommitted: committed });
        jobs.delete(item.id);
        deps.onCommitted?.(committed);
      } catch (err) {
        if (err instanceof UploadCancelledError) {
          patch(item.id, { status: "cancelled", bytesReceived: 0 });
        } else {
          console.error("[fileUpload] upload failed", err);
          patch(item.id, { status: "failed", error: describeUploadError(err) });
        }
      } finally {
        job.controller = undefined;
        if (pendingConflict?.itemId === item.id) answerConflict("cancel");
      }
    };

    // One file at a time, in the order queued.
    const pump = async () => {
      if (running) return;
      running = true;
      try {
        for (;;) {
          const item = get().items.find((candidate) => candidate.status === "queued");
          if (!item) return;
          const job = jobs.get(item.id);
          if (!job) {
            patch(item.id, { status: "failed", error: "The file is no longer available" });
            continue;
          }
          await runOne(item, job);
        }
      } finally {
        running = false;
      }
    };

    return {
      items: [],
      conflict: null,
      lastCommitted: null,

      enqueue: (files, directory, scope) => {
        if (files.length === 0) return;
        const dir = directory === "/" ? "" : directory;
        const added: UploadItem[] = files.map((file) => {
          const id = `upload-${nextId++}`;
          jobs.set(id, { file, scope, session: newUploadSession() });
          return {
            id,
            name: file.name,
            directory: dir,
            size: file.size,
            bytesReceived: 0,
            status: "queued",
            projectId: scope.projectId,
            worktreeId: scope.worktreeId,
          };
        });
        set((state) => ({ items: [...state.items, ...added] }));
        void pump();
      },

      cancel: (id) => {
        const item = get().items.find((candidate) => candidate.id === id);
        const job = jobs.get(id);
        if (!item) return;
        if (item.status === "uploading") {
          // runUpload sees the abort, discards the session, and the row
          // turns cancelled when it returns.
          job?.controller?.abort();
          if (pendingConflict?.itemId === id) answerConflict("cancel");
          return;
        }
        if (item.status === "queued" || item.status === "failed") {
          if (job) void abortUploadSession(getClient(), job.scope, job.session);
          patch(id, { status: "cancelled", bytesReceived: 0, error: undefined });
        }
      },

      retry: (id) => {
        const item = get().items.find((candidate) => candidate.id === id);
        if (!item || (item.status !== "failed" && item.status !== "cancelled")) return;
        if (!jobs.has(id)) return;
        patch(id, { status: "queued", error: undefined });
        void pump();
      },

      dismiss: (id) => {
        const item = get().items.find((candidate) => candidate.id === id);
        if (!item || item.status === "uploading") return;
        const job = jobs.get(id);
        // A failed row may still hold staged bytes on the machine.
        if (job?.session.uploadId) void abortUploadSession(getClient(), job.scope, job.session);
        jobs.delete(id);
        set((state) => ({ items: state.items.filter((candidate) => candidate.id !== id) }));
      },

      clearFinished: () => {
        const keep = get().items.filter((item) => !FINISHED.has(item.status));
        for (const item of get().items) if (FINISHED.has(item.status)) jobs.delete(item.id);
        set({ items: keep });
      },

      resolveConflict: (choice) => answerConflict(choice),
    };
  });
}

let defaultClient: FileUploadClient | undefined;

export const useFileUploadStore = createFileUploadStore({
  client: () => (defaultClient ??= connectClient(FileSystemService)),
  onCommitted: ({ worktreeId, projectId }) => triggerGitStatusRefresh(worktreeId, projectId),
});
