// Copyright (c) 2025 Reliant Labs

/**
 * Downloading a file, or a folder as a .zip, from the workspace to this
 * device, however large.
 *
 * DownloadFile carries a whole file in one response, so the server caps it
 * at 25 MB. Anything larger is read piecewise, in unary RPCs (a single
 * streaming response cannot cross every proxy between a daemon and a
 * browser):
 *
 *   ReadFileRange ×N     up to 1 MiB a piece, from offset 0 until `eof`; every
 *                        piece after the first names the first one's
 *                        `version`, so a file that changes mid-download fails
 *                        cleanly (`file_changed_during_download`) instead of
 *                        saving half of one version and half of another
 *
 * A folder is zipped on the machine first, then read the same way:
 *
 *   PrepareFolderArchive        starts zipping, names the archive
 *   GetFolderArchiveStatus ×N   polled until READY (or FAILED)
 *   ReadFileRange ×N            the archive, by `archiveId`
 *   ReleaseFolderArchive        always, once an archive exists
 *
 * Where the bytes go is a DownloadSink: a file the user picked with the File
 * System Access API (Chromium, Electron), written piece by piece so a 2 GB
 * file never sits in memory, or — elsewhere — memory, handed to the browser
 * as one Blob at the end (capped at 500 MB).
 *
 * A machine that predates ranged reads (`ranged_download_unsupported`), or an
 * API server that predates them (`Unimplemented`), still serves DownloadFile:
 * a file falls back to it, up to its 25 MB cap.
 *
 * This module is framework-free: it takes the client, the request scope and
 * the sink as arguments, so the whole flow is unit-tested against a fake
 * client (fileDownload.test.ts). The rows and their UI state live in
 * store/fileDownloadStore.ts.
 */

import { Code, ConnectError, type Client } from "@connectrpc/connect";
import { FileSystemService, FolderArchiveState } from "../gen/reliant/v1/filesystem_pb";
import { hasErrorReason } from "../lib/errorReasons";
import { formatBytes } from "../lib/formatBytes";
import type { UploadScope } from "./fileUpload";

/** The download RPCs of FileSystemService (a real client, or a fake). */
export type FileDownloadClient = Pick<
  Client<typeof FileSystemService>,
  "readFileRange" | "downloadFile" | "prepareFolderArchive" | "getFolderArchiveStatus" | "releaseFolderArchive"
>;

/**
 * Which disk every call of one download goes to, captured once when it
 * starts (captureFileScope): a chat switch mid-download must not read the
 * rest of a file from another machine.
 */
export type DownloadScope = UploadScope;

/** The largest file the memory sink holds before handing it to the browser. */
export const MEMORY_DOWNLOAD_LIMIT_BYTES = 500 * 1000 * 1000;

/** How often a folder archive's progress is polled while it is zipped. */
export const FOLDER_ARCHIVE_POLL_MS = 750;

/** Shown when the API server has no folder archive RPCs (an older release). */
export const FOLDER_DOWNLOAD_UNAVAILABLE_MESSAGE = "Downloading folders needs a newer Reliant server";

/** What the server says the download is, from its first piece. */
export interface DownloadInfo {
  totalSize: number;
  filename: string;
  contentType: string;
}

/**
 * Where a download's bytes go. `begin` is called once, before the first
 * write, and may refuse (throw); `close` finishes the file; `abort` discards
 * it, leaving no half-written file. `abort` may be called more than once,
 * and after a refused `begin`.
 */
export interface DownloadSink {
  begin(info: DownloadInfo): void | Promise<void>;
  write(chunk: Uint8Array): Promise<void>;
  close(): Promise<void>;
  abort(): Promise<void>;
}

/** The download stopped because the user cancelled it. Not a failure. */
export class DownloadCancelledError extends Error {
  constructor() {
    super("Download cancelled");
    this.name = "DownloadCancelledError";
  }
}

/** The text a failed download's row shows. */
export function describeDownloadError(err: unknown): string {
  if (err instanceof ConnectError) return err.rawMessage || err.message;
  if (err instanceof Error) return err.message;
  return "Download failed";
}

/**
 * Whether ranged reads are unavailable: the machine's Reliant predates them,
 * or the API server does.
 */
export function isRangedDownloadUnsupported(err: unknown): boolean {
  if (err instanceof ConnectError && err.code === Code.Unimplemented) return true;
  return hasErrorReason(err, "ranged_download_unsupported");
}

// ── Sinks ──────────────────────────────────────────────────────────────

/** The part of a FileSystemWritableFileStream a sink writes through. */
export interface WritableFileStreamLike {
  write(data: Uint8Array): Promise<void>;
  close(): Promise<void>;
  abort(reason?: unknown): Promise<void>;
}

/** The part of a FileSystemFileHandle (from `showSaveFilePicker`) a sink needs. */
export interface WritableFileHandleLike {
  createWritable(): Promise<WritableFileStreamLike>;
}

/**
 * Writes the download into a file the user picked, piece by piece. The
 * browser stages the writes and swaps them in only at `close`, so `abort`
 * leaves the destination as it was.
 */
export function createFileHandleSink(handle: WritableFileHandleLike): DownloadSink {
  let writable: WritableFileStreamLike | undefined;
  return {
    async begin() {
      writable = await handle.createWritable();
    },
    async write(chunk) {
      if (!writable) throw new Error("The download was not started");
      await writable.write(chunk);
    },
    async close() {
      const stream = writable;
      writable = undefined;
      await stream?.close();
    },
    async abort() {
      const stream = writable;
      writable = undefined;
      try {
        await stream?.abort();
      } catch (err) {
        console.debug("[fileDownload] aborting the file write failed", err);
      }
    },
  };
}

/** Why a browser without file access refuses a download too large to hold in memory. */
export function memoryDownloadLimitMessage(name: string, totalSize: number): string {
  return (
    `${name} is ${formatBytes(totalSize)}. This browser can only download files up to ` +
    `${formatBytes(MEMORY_DOWNLOAD_LIMIT_BYTES)} from the Files tab; use Chrome, Edge or the ` +
    `Reliant desktop app for larger files.`
  );
}

/**
 * Collects the download in memory and hands it to `save` as one Blob at the
 * end (saveBlob: the browser's own download). For browsers without the File
 * System Access API; refuses anything over MEMORY_DOWNLOAD_LIMIT_BYTES
 * before reading a byte past the first piece.
 */
export function createMemorySink(name: string, save: (blob: Blob, filename: string) => void): DownloadSink {
  let pieces: Uint8Array[] = [];
  let info: DownloadInfo | undefined;
  return {
    begin(next) {
      if (next.totalSize > MEMORY_DOWNLOAD_LIMIT_BYTES) {
        throw new Error(memoryDownloadLimitMessage(next.filename || name, next.totalSize));
      }
      info = next;
    },
    async write(chunk) {
      pieces.push(chunk);
    },
    async close() {
      const blob = new Blob(pieces as BlobPart[], { type: info?.contentType || "application/octet-stream" });
      pieces = [];
      save(blob, info?.filename || name);
    },
    async abort() {
      pieces = [];
    },
  };
}

// ── Ranged download ────────────────────────────────────────────────────

/**
 * Runs the first call of a transfer. The store passes retryAcrossWake, so a
 * machine the server had to wake is waited for instead of failing.
 */
export type FirstCallRunner = <T>(action: () => Promise<T>, signal?: AbortSignal) => Promise<T>;

const runDirectly: FirstCallRunner = (action) => action();

export type DownloadSource = { path: string } | { archiveId: string };

export interface RunRangedDownloadOptions {
  client: FileDownloadClient;
  scope: DownloadScope;
  source: DownloadSource;
  sink: DownloadSink;
  /** Aborts the in-flight read; the sink is then aborted. */
  signal?: AbortSignal;
  /** Called after every piece with the bytes written and the whole size. */
  onProgress?: (bytesDone: number, totalBytes: number) => void;
  firstCall?: FirstCallRunner;
}

export interface DownloadResult {
  filename: string;
  totalSize: number;
}

function cancelledIfAborted(err: unknown, signal: AbortSignal | undefined): unknown {
  if (err instanceof DownloadCancelledError || signal?.aborted) return new DownloadCancelledError();
  return err;
}

async function abortQuietly(sink: DownloadSink): Promise<void> {
  try {
    await sink.abort();
  } catch (err) {
    console.debug("[fileDownload] aborting the sink failed", err);
  }
}

/**
 * Reads the source piece by piece into the sink. Resolves with what was
 * saved; rejects with DownloadCancelledError when `signal` aborted it, or
 * with the error that stopped it. Either way the sink is aborted, so no half
 * file is left behind.
 */
export async function runRangedDownload(options: RunRangedDownloadOptions): Promise<DownloadResult> {
  const { sink, signal } = options;
  try {
    return await rangedDownload(options);
  } catch (err) {
    await abortQuietly(sink);
    throw cancelledIfAborted(err, signal);
  }
}

async function rangedDownload({
  client,
  scope,
  source,
  sink,
  signal,
  onProgress,
  firstCall = runDirectly,
}: RunRangedDownloadOptions): Promise<DownloadResult> {
  const callOptions = { signal };
  const target = "path" in source ? { path: source.path } : { path: "", archiveId: source.archiveId };
  let offset = 0n;
  let version: string | undefined;
  let info: DownloadInfo | undefined;

  for (;;) {
    if (signal?.aborted) throw new DownloadCancelledError();
    const request = { ...scope, ...target, offset, length: 0, ifVersion: version };
    let piece;
    if (!info) {
      try {
        piece = await firstCall(() => client.readFileRange(request, callOptions), signal);
      } catch (err) {
        if ("path" in source && !signal?.aborted && isRangedDownloadUnsupported(err)) {
          return await wholeFileDownload(client, scope, source.path, sink, err, callOptions, onProgress);
        }
        throw err;
      }
      version = piece.version;
      info = { totalSize: Number(piece.totalSize), filename: piece.filename, contentType: piece.contentType };
      await sink.begin(info);
    } else {
      piece = await client.readFileRange(request, callOptions);
    }

    if (piece.data.length > 0) await sink.write(piece.data);
    offset += BigInt(piece.data.length);
    onProgress?.(Number(offset), Number(piece.totalSize));
    if (piece.eof) break;
    // A piece short of the end must carry bytes, or the loop never ends.
    if (piece.data.length === 0) throw new Error("The machine stopped sending the file before its end");
  }

  await sink.close();
  return { filename: info.filename, totalSize: Number(offset) };
}

/**
 * The fallback for a machine or server without ranged reads: DownloadFile,
 * one response up to its 25 MB cap. When that refuses the file as too large,
 * the ranged refusal says what fixes it ("Update Reliant on …").
 */
async function wholeFileDownload(
  client: FileDownloadClient,
  scope: DownloadScope,
  path: string,
  sink: DownloadSink,
  rangedError: unknown,
  callOptions: { signal?: AbortSignal },
  onProgress?: (bytesDone: number, totalBytes: number) => void,
): Promise<DownloadResult> {
  let response;
  try {
    response = await client.downloadFile({ ...scope, path }, callOptions);
  } catch (err) {
    if (hasErrorReason(err, "file_too_large_to_download") && hasErrorReason(rangedError, "ranged_download_unsupported")) {
      throw rangedError;
    }
    throw err;
  }
  const totalSize = response.content.length;
  await sink.begin({
    totalSize,
    filename: response.filename,
    contentType: response.contentType || "application/octet-stream",
  });
  await sink.write(response.content);
  await sink.close();
  onProgress?.(totalSize, totalSize);
  return { filename: response.filename, totalSize };
}

// ── Folder as a .zip ───────────────────────────────────────────────────

/** How far zipping has come, from GetFolderArchiveStatus. */
export interface ArchiveProgress {
  filesDone: number;
  filesTotal: number;
  bytesDone: number;
  bytesTotal: number;
}

export interface RunFolderDownloadOptions {
  client: FileDownloadClient;
  scope: DownloadScope;
  /** Workspace-relative folder; "" is the workspace root. */
  path: string;
  sink: DownloadSink;
  signal?: AbortSignal;
  /** Called while the machine zips the folder. */
  onZipping?: (progress: ArchiveProgress) => void;
  /** Called after every piece of the archive. */
  onProgress?: (bytesDone: number, totalBytes: number) => void;
  firstCall?: FirstCallRunner;
  pollIntervalMs?: number;
}

export interface FolderDownloadResult extends DownloadResult {
  /** Entries the machine left out of the archive (unreadable, credentials, outside links). */
  skipped: number;
}

function delay(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(new DownloadCancelledError());
      return;
    }
    const onAbort = () => {
      clearTimeout(timer);
      reject(new DownloadCancelledError());
    };
    const timer = setTimeout(() => {
      signal?.removeEventListener("abort", onAbort);
      resolve();
    }, ms);
    signal?.addEventListener("abort", onAbort, { once: true });
  });
}

/**
 * Zips `path` on the machine and downloads the archive into the sink. The
 * archive is released on the machine however this ends — saved, failed or
 * cancelled — once one exists.
 */
export async function runFolderDownload(options: RunFolderDownloadOptions): Promise<FolderDownloadResult> {
  const { client, scope, sink, signal } = options;
  let archiveId: string | undefined;
  try {
    return await folderDownload(options, (id) => {
      archiveId = id;
    });
  } catch (err) {
    await abortQuietly(sink);
    throw cancelledIfAborted(err, signal);
  } finally {
    if (archiveId) {
      // No signal: a cancelled download still frees the archive's disk.
      try {
        await client.releaseFolderArchive({ ...scope, archiveId });
      } catch (err) {
        console.debug("[fileDownload] ReleaseFolderArchive failed", err);
      }
    }
  }
}

async function folderDownload(
  {
    client,
    scope,
    path,
    sink,
    signal,
    onZipping,
    onProgress,
    firstCall = runDirectly,
    pollIntervalMs = FOLDER_ARCHIVE_POLL_MS,
  }: RunFolderDownloadOptions,
  setArchiveId: (id: string) => void,
): Promise<FolderDownloadResult> {
  const callOptions = { signal };
  const folder = path === "/" ? "" : path;
  let prepared;
  try {
    prepared = await firstCall(() => client.prepareFolderArchive({ ...scope, path: folder }, callOptions), signal);
  } catch (err) {
    if (err instanceof ConnectError && err.code === Code.Unimplemented) {
      throw new Error(FOLDER_DOWNLOAD_UNAVAILABLE_MESSAGE);
    }
    throw err;
  }
  const archiveId = prepared.archiveId;
  setArchiveId(archiveId);
  onZipping?.({
    filesDone: 0,
    filesTotal: Number(prepared.totalFiles),
    bytesDone: 0,
    bytesTotal: Number(prepared.totalBytes),
  });

  let skipped = 0;
  for (;;) {
    if (signal?.aborted) throw new DownloadCancelledError();
    const status = await client.getFolderArchiveStatus({ ...scope, archiveId }, callOptions);
    onZipping?.({
      filesDone: Number(status.filesDone),
      filesTotal: Number(status.filesTotal),
      bytesDone: Number(status.bytesDone),
      bytesTotal: Number(status.bytesTotal),
    });
    if (status.state === FolderArchiveState.READY) {
      skipped = Number(status.skipped);
      break;
    }
    if (status.state === FolderArchiveState.FAILED) {
      throw new Error(status.error || "The machine could not zip the folder");
    }
    await delay(pollIntervalMs, signal);
  }

  const result = await runRangedDownload({ client, scope, source: { archiveId }, sink, signal, onProgress });
  return { ...result, skipped };
}
