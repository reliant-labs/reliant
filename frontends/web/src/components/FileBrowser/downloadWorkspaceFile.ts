// Copyright (c) 2025 Reliant Labs

import { ConnectError } from "@connectrpc/connect";
import { toast } from "sonner";
import { downloadFile } from "../../api/fileSystem";
import { retryAcrossWake } from "../../lib/daemon-retry";
import { saveBlob } from "../../lib/saveBlob";

/** What the user reads when a download fails: the server's words, without the code prefix. */
export function downloadErrorMessage(error: unknown): string {
  if (error instanceof ConnectError) return error.rawMessage;
  if (error instanceof Error) return error.message;
  return "Something went wrong.";
}

/**
 * The Files tab's "Download": reads the file off the machine that holds it and
 * saves it on this device.
 *
 * One toast follows it, replaced in place: "Downloading…" (a 25 MB file over a
 * home uplink takes a while), "Waking up the machine…" when the server had to
 * wake it first, then gone on success or the reason it failed (too large, the
 * machine's Reliant needs an update, not found).
 */
export async function downloadWorkspaceFile(path: string, name: string, worktreeId?: string): Promise<void> {
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
