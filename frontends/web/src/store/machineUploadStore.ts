/**
 * Files the chat composer cannot attach, sent to the chat's machine instead.
 *
 * A spreadsheet, an archive, a Word file, a text file over the attachment cap:
 * chat attachments take none of them, so instead of refusing the drop the
 * composer uploads the file to the machine the chat runs on, into its uploads
 * folder outside the repo (~/.reliant/uploads/<chat id>/<name>, resolved by
 * the daemon), and the message that is sent names its absolute path, so the
 * agent opens it with its own tools. A clash keeps both ("report (1).xlsx").
 *
 * One row per file, per composer draft (`session`, the attachment bucket):
 * the chip shows its progress, Send waits for every row of the draft to
 * finish, and removing a row aborts its upload. The protocol is
 * api/fileUpload.ts with the USER_UPLOADS destination; this store owns only
 * the rows. Rows live for the page, not across a reload: the file itself
 * stays on the machine.
 */

import { create } from "zustand";
import { Code, ConnectError } from "@connectrpc/connect";
import { FileSystemService, UploadConflictPolicy, UploadDestination } from "../gen/reliant/v1/filesystem_pb";
import { connectClient } from "../lib/connect";
import { hasErrorReason } from "../lib/errorReasons";
import { getUnsupportedFileMessage, isFileTypeSupported } from "../lib/filetypes";
import { toast } from "../lib/toast-manager";
import {
  abortUploadSession,
  describeUploadError,
  newUploadSession,
  runUpload,
  UploadCancelledError,
  UserUploadsUnsupportedError,
  type FileUploadClient,
  type UploadScope,
  type UploadSession,
} from "../api/fileUpload";

/**
 * The largest file chat attaches directly: the API server's default
 * max_upload_size (internal/handlers/attachment). A bigger one goes to the
 * machine like an unsupported type.
 */
export const CHAT_ATTACHMENT_MAX_BYTES = 10 * 1024 * 1024;

/** Whether `file` goes to the chat's machine rather than into chat. */
export function goesToMachine(file: { name: string; size: number }): boolean {
  return !isFileTypeSupported(file.name) || file.size > CHAT_ATTACHMENT_MAX_BYTES;
}

/** What chat said about `file` before machine uploads existed. */
export function cannotAttachMessage(file: { name: string; size: number }): string {
  if (!isFileTypeSupported(file.name)) return getUnsupportedFileMessage(file.name);
  return `${file.name} is larger than chat attachments allow (10 MB).`;
}

/**
 * A browser file name made safe as one path element on any machine: no
 * separators, NUL or control characters, never "." or "..", at most 255
 * UTF-8 bytes with its extension kept. The daemon refuses anything else.
 */
export function sanitizeUploadName(name: string): string {
  // eslint-disable-next-line no-control-regex
  let clean = name.replace(/[/\\\u0000-\u001f\u007f]/g, "_").trim();
  if (clean === "" || clean === "." || clean === "..") clean = "upload";
  const encoder = new TextEncoder();
  if (encoder.encode(clean).length <= 255) return clean;
  const dot = clean.lastIndexOf(".");
  const ext = dot > 0 && clean.length - dot <= 16 ? clean.slice(dot) : "";
  let stem = Array.from(ext ? clean.slice(0, dot) : clean);
  while (stem.length > 0 && encoder.encode(stem.join("") + ext).length > 255) stem = stem.slice(0, -1);
  return (stem.join("") || "upload") + ext;
}

export type MachineUploadStatus = "uploading" | "done" | "failed";

/** One file of a draft, headed for the chat's machine. */
export interface MachineUpload {
  id: string;
  /** The composer draft it belongs to (the attachment bucket). */
  session: string;
  name: string;
  size: number;
  mimeType: string;
  bytesReceived: number;
  status: MachineUploadStatus;
  /** Why it failed, for a failed row. */
  error?: string;
  /** The file's absolute path on the machine, for a done row. */
  path?: string;
}

/** Where a draft's files go: the chat's scope, and its uploads folder. */
export interface MachineUploadTarget {
  scope: UploadScope;
  /** The folder under ~/.reliant/uploads/: the chat's id. */
  uploadsFolder: string;
}

export interface MachineUploadState {
  uploads: MachineUpload[];
  /** Starts uploading `files` to the machine `target` names. */
  add: (session: string, files: File[], target: MachineUploadTarget) => void;
  /** Runs a failed upload again, resuming where the machine has it. */
  retry: (id: string) => void;
  /** Removes a row; an upload still running is aborted. */
  remove: (id: string) => void;
  /** Removes the done rows of `session` (they went out with a message). */
  clearDone: (session: string) => void;
}

interface Job {
  file: File;
  target: MachineUploadTarget;
  session: UploadSession;
  controller?: AbortController;
}

/** The text a failed row shows, and the toast says. */
export function describeMachineUploadError(file: { name: string; size: number }, err: unknown): string {
  const outdated =
    err instanceof UserUploadsUnsupportedError ||
    (err instanceof ConnectError && err.code === Code.Unimplemented);
  if (outdated) {
    return `${cannotAttachMessage(file)} Update Reliant to send it to the chat's machine instead.`;
  }
  if (hasErrorReason(err, "machine_needs_update_for_uploads")) {
    // The server's message names the machine: "Update Reliant on <name> …".
    return `${cannotAttachMessage(file)} ${describeUploadError(err)}.`;
  }
  return describeUploadError(err);
}

export interface MachineUploadStoreDeps {
  client: FileUploadClient | (() => FileUploadClient);
  /** Told about each failure, once (a toast in the app). */
  onFailed?: (upload: MachineUpload) => void;
}

export function createMachineUploadStore(deps: MachineUploadStoreDeps) {
  const getClient = () => (typeof deps.client === "function" ? deps.client() : deps.client);
  const jobs = new Map<string, Job>();
  let nextId = 1;

  return create<MachineUploadState>()((set, get) => {
    const patch = (id: string, changes: Partial<MachineUpload>) =>
      set((state) => ({ uploads: state.uploads.map((u) => (u.id === id ? { ...u, ...changes } : u)) }));

    const run = async (id: string) => {
      const job = jobs.get(id);
      const row = get().uploads.find((u) => u.id === id);
      if (!job || !row || job.controller) return;
      const controller = new AbortController();
      job.controller = controller;
      patch(id, { status: "uploading", error: undefined });
      try {
        const result = await runUpload({
          client: getClient(),
          scope: job.target.scope,
          file: { name: row.name, size: job.file.size, slice: (start, end) => job.file.slice(start, end) },
          directory: "",
          destination: UploadDestination.USER_UPLOADS,
          uploadsFolder: job.target.uploadsFolder,
          session: job.session,
          signal: controller.signal,
          onProgress: (bytesReceived) => patch(id, { bytesReceived }),
          // A user upload always keeps both; the server never asks.
          resolveConflict: async () => "keep-both",
        });
        if (!jobs.has(id)) return; // removed while the commit answered
        patch(id, { status: "done", bytesReceived: row.size, path: result.absolutePath || result.path });
      } catch (err) {
        if (err instanceof UploadCancelledError || !jobs.has(id)) return;
        console.error("[machineUpload] upload failed", err);
        const error = describeMachineUploadError(job.file, err);
        patch(id, { status: "failed", error });
        const failed = get().uploads.find((u) => u.id === id);
        if (failed) deps.onFailed?.(failed);
      } finally {
        job.controller = undefined;
      }
    };

    return {
      uploads: [],

      add: (session, files, target) => {
        const added: MachineUpload[] = files.map((file) => {
          const id = `machine-upload-${nextId++}`;
          jobs.set(id, {
            file,
            target,
            session: { ...newUploadSession(), policy: UploadConflictPolicy.RENAME },
          });
          return {
            id,
            session,
            name: sanitizeUploadName(file.name),
            size: file.size,
            mimeType: file.type,
            bytesReceived: 0,
            status: "uploading",
          };
        });
        set((state) => ({ uploads: [...state.uploads, ...added] }));
        // Side by side: each file is its own session on the machine.
        for (const { id } of added) void run(id);
      },

      retry: (id) => {
        const row = get().uploads.find((u) => u.id === id);
        if (row?.status !== "failed") return;
        void run(id);
      },

      remove: (id) => {
        const job = jobs.get(id);
        jobs.delete(id);
        set((state) => ({ uploads: state.uploads.filter((u) => u.id !== id) }));
        if (!job) return;
        if (job.controller) {
          // runUpload sees the abort and discards what the machine staged.
          job.controller.abort();
        } else if (job.session.uploadId) {
          void abortUploadSession(getClient(), job.target.scope, job.session);
        }
      },

      clearDone: (session) => {
        const done = get().uploads.filter((u) => u.session === session && u.status === "done");
        if (done.length === 0) return;
        for (const u of done) jobs.delete(u.id);
        set((state) => ({ uploads: state.uploads.filter((u) => !(u.session === session && u.status === "done")) }));
      },
    };
  });
}

/** "1.2 MB": the size as the agent is told it. */
function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  const units = ["KB", "MB", "GB"];
  let value = bytes / 1024;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit++;
  }
  return `${value >= 10 ? Math.round(value) : value.toFixed(1)} ${units[unit]}`;
}

/**
 * The lines a message carries for its draft's finished machine uploads, one
 * per file: "User uploaded file: /abs/path (name, size, type)". Empty when
 * there are none.
 */
export function machineUploadContext(uploads: MachineUpload[]): string {
  return uploads
    .filter((u) => u.status === "done" && u.path)
    .map((u) => `User uploaded file: ${u.path} (${u.name}, ${formatBytes(u.size)}, ${u.mimeType || "unknown type"})`)
    .join("\n");
}

let defaultClient: FileUploadClient | undefined;

export const useMachineUploadStore = createMachineUploadStore({
  client: () => (defaultClient ??= connectClient(FileSystemService)),
  onFailed: (upload) => {
    toast.error(upload.error ?? `Couldn't send ${upload.name} to the chat's machine`, { duration: 8000 });
  },
});
