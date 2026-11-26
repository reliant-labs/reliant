// Copyright (c) 2025 Reliant Labs

import { ConnectError } from "@connectrpc/connect";
import { toast } from "sonner";
import { captureFileScope, downloadFile, normalizeFilePathForAPI } from "../../api/fileSystem";
import {
  createFileHandleSink,
  createMemorySink,
  type DownloadSink,
  type WritableFileHandleLike,
} from "../../api/fileDownload";
import { retryAcrossWake } from "../../lib/daemon-retry";
import { saveBlob } from "../../lib/saveBlob";
import { useFileDownloadStore, type DownloadOutcome } from "../../store/fileDownloadStore";

/**
 * DownloadFile's cap: one response carries the whole file. A file the tree
 * lists at or under it takes that path, which every machine serves.
 * Mirrors the server's maxDownloadFileBytes.
 */
export const DIRECT_DOWNLOAD_MAX_BYTES = 25 * 1024 * 1024;

/** What the user reads when a download fails: the server's words, without the code prefix. */
export function downloadErrorMessage(error: unknown): string {
  if (error instanceof ConnectError) return error.rawMessage;
  if (error instanceof Error) return error.message;
  return "Something went wrong.";
}

type SaveFilePicker = (options: { suggestedName?: string }) => Promise<WritableFileHandleLike>;

function saveFilePicker(): SaveFilePicker | undefined {
  const picker = (window as Window & { showSaveFilePicker?: SaveFilePicker }).showSaveFilePicker;
  return typeof picker === "function" ? picker.bind(window) : undefined;
}

/**
 * Where a large download goes. Where the browser lets us (Chromium,
 * Electron), a file the user picks, written piece by piece; elsewhere
 * memory, handed to the browser at the end (up to 500 MB).
 *
 * The picker opens at once — this must run in the click, before any
 * `await`, while the click's user activation lasts. Resolves null when the
 * user closed it.
 */
function chooseSink(suggestedName: string): Promise<DownloadSink | null> {
  const picker = saveFilePicker();
  const memory = () => createMemorySink(suggestedName, saveBlob);
  if (!picker) return Promise.resolve(memory());
  let picked: Promise<WritableFileHandleLike>;
  try {
    picked = picker({ suggestedName });
  } catch (err) {
    console.debug("[download] showSaveFilePicker threw; downloading through the browser", err);
    return Promise.resolve(memory());
  }
  return picked.then(
    (handle) => createFileHandleSink(handle),
    (err: unknown) => {
      if (err instanceof DOMException && err.name === "AbortError") return null;
      // Refused (no user activation, a sandboxed frame): the browser's own
      // download still works.
      console.debug("[download] showSaveFilePicker failed; downloading through the browser", err);
      return memory();
    },
  );
}

function toastFailure(name: string, outcome: DownloadOutcome) {
  if (outcome.status !== "failed") return;
  toast.error(`Couldn't download ${name}`, { id: `download-failed:${name}`, description: outcome.error });
}

/**
 * The Files tab's "Download": reads the file off the machine that holds it and
 * saves it on this device.
 *
 * A file the tree lists at 25 MB or less is read in one response, with one
 * toast following it, replaced in place: "Downloading…", "Waking up the
 * machine…" when the server had to wake it first, then gone on success or the
 * reason it failed (too large, the machine's Reliant needs an update, not
 * found).
 *
 * A larger file, or one of unknown size, is read piecewise into a file the
 * user picks (or memory), with a row in the Files tab's transfers panel.
 * Resolves when the download ends.
 */
export async function downloadWorkspaceFile(path: string, name: string, worktreeId?: string, size?: number): Promise<void> {
  if (size !== undefined && size <= DIRECT_DOWNLOAD_MAX_BYTES) {
    await directDownload(path, name, worktreeId);
    return;
  }
  // Before any await: the picker needs the click's user activation.
  const sinkChoice = chooseSink(name);
  const source = Promise.all([captureFileScope(worktreeId), normalizeFilePathForAPI(path, worktreeId)]);
  try {
    const [sink, [scope, apiPath]] = await Promise.all([sinkChoice, source]);
    if (!sink) return;
    const outcome = await useFileDownloadStore.getState().startFileDownload({ name, path: apiPath, scope, sink, size });
    toastFailure(name, outcome);
  } catch (error) {
    toast.error(`Couldn't download ${name}`, { description: downloadErrorMessage(error) });
  }
}

async function directDownload(path: string, name: string, worktreeId?: string): Promise<void> {
  const toastId = `download:${worktreeId ?? ""}:${path}`;
  toast.loading(`Downloading ${name}…`, { id: toastId });
  try {
    const { blob, filename } = await retryAcrossWake(() => downloadFile(path, worktreeId), {
      onWaking: () => toast.loading(`Waking up the machine to download ${name}…`, { id: toastId }),
    });
    saveBlob(blob, filename || name);
    toast.dismiss(toastId);
  } catch (error) {
    toast.error(`Couldn't download ${name}`, { id: toastId, description: downloadErrorMessage(error) });
  }
}

/**
 * A folder's "Download as .zip": the machine zips it, then the archive is
 * read piecewise like a large file, with a row in the transfers panel that
 * shows the zipping first. Resolves when the download ends.
 */
export async function downloadWorkspaceFolder(path: string, name: string, worktreeId?: string): Promise<void> {
  const zipName = `${name}.zip`;
  // Before any await: the picker needs the click's user activation.
  const sinkChoice = chooseSink(zipName);
  const source = Promise.all([captureFileScope(worktreeId), normalizeFilePathForAPI(path, worktreeId)]);
  try {
    const [sink, [scope, apiPath]] = await Promise.all([sinkChoice, source]);
    if (!sink) return;
    const outcome = await useFileDownloadStore.getState().startFolderDownload({ name, path: apiPath, scope, sink });
    toastFailure(zipName, outcome);
  } catch (error) {
    toast.error(`Couldn't download ${zipName}`, { description: downloadErrorMessage(error) });
  }
}
