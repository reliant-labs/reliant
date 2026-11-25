// Copyright (c) 2025 Reliant Labs

/**
 * Uploading one file from the browser into a folder of the workspace.
 *
 * The server takes an upload piecewise, in unary RPCs (a single streaming
 * request cannot cross every proxy between a browser and a daemon):
 *
 *   BeginFileUpload      opens a session on the machine, names the chunk size
 *   UploadFileChunk ×N   one chunk at a time, each at the offset the previous
 *                        reply said had arrived
 *   CommitFileUpload     moves the staged file into place
 *
 * A name clash is the server's `ALREADY_EXISTS`, at Begin (the FAIL policy
 * checks early) or at Commit (the file appeared meanwhile). Either way the
 * caller decides — Replace, Keep both, or Cancel — and the upload goes on
 * with that policy; at Commit nothing is re-sent.
 *
 * A machine that predates staged uploads takes a small file in one chunk and
 * writes it at once: that reply carries `committedPath`, and there is no
 * Commit.
 *
 * This module is framework-free: it takes the client, the request scope and
 * the conflict prompt as arguments, so the whole flow is unit-tested against
 * a fake client (fileUpload.test.ts). The queue, its UI state and the prompt
 * live in store/fileUploadStore.ts.
 */

import { Code, ConnectError, type Client } from "@connectrpc/connect";
import { FileSystemService, UploadConflictPolicy } from "../gen/reliant/v1/filesystem_pb";

/** The five upload RPCs of FileSystemService (a real client, or a fake). */
export type FileUploadClient = Pick<
  Client<typeof FileSystemService>,
  "beginFileUpload" | "uploadFileChunk" | "commitFileUpload" | "abortFileUpload" | "getFileUploadStatus"
>;

/**
 * Which disk every call of one upload goes to. Captured once, when the upload
 * is queued: a chat or workspace switch mid-upload must not send the rest of
 * a file to a different machine.
 */
export interface UploadScope {
  projectId: string;
  worktreeId?: string;
  chatId?: string;
}

/** What the user chose when the file already exists at its destination. */
export type ConflictChoice = "replace" | "keep-both" | "cancel";

/** What an upload reads its bytes from. A browser `File` is one. */
export interface UploadSource {
  readonly name: string;
  readonly size: number;
  slice(start: number, end: number): Blob;
}

/**
 * The server-side state of one upload, kept between attempts so Retry
 * resumes instead of starting over. `runUpload` updates it as it goes.
 */
export interface UploadSession {
  /** The open session, once Begin answered. */
  uploadId?: string;
  chunkSize?: number;
  /** The policy Begin is (re)opened with: the user's answer to a clash sticks. */
  policy: UploadConflictPolicy;
  /** Bytes the machine has, contiguously from the start. */
  bytesReceived: number;
}

export function newUploadSession(): UploadSession {
  return { policy: UploadConflictPolicy.FAIL, bytesReceived: 0 };
}

export interface RunUploadOptions {
  client: FileUploadClient;
  scope: UploadScope;
  file: UploadSource;
  /** Workspace-relative folder; "" (or "/") is the workspace root. */
  directory: string;
  session: UploadSession;
  /** Aborts the in-flight request; the session is then discarded. */
  signal?: AbortSignal;
  /** Called after every chunk with the bytes the machine has. */
  onProgress?: (bytesReceived: number) => void;
  /** Asks the user what to do about an existing file of the same name. */
  resolveConflict: () => Promise<ConflictChoice>;
}

export interface UploadResult {
  /** Where the file landed, workspace-relative (a "Keep both" rename shows here). */
  path: string;
}

/** The upload stopped because the user cancelled it. Not a failure. */
export class UploadCancelledError extends Error {
  constructor() {
    super("Upload cancelled");
    this.name = "UploadCancelledError";
  }
}

/** Shown when the API server has no upload RPCs (an older release). */
export const UPLOAD_UNAVAILABLE_MESSAGE = "Uploading isn't available on this server yet";

function isAlreadyExists(err: unknown): boolean {
  return err instanceof ConnectError && err.code === Code.AlreadyExists;
}

function policyFor(choice: Exclude<ConflictChoice, "cancel">): UploadConflictPolicy {
  return choice === "replace" ? UploadConflictPolicy.OVERWRITE : UploadConflictPolicy.RENAME;
}

/** The text a failed upload's row shows. */
export function describeUploadError(err: unknown): string {
  if (err instanceof ConnectError) {
    if (err.code === Code.Unimplemented) return UPLOAD_UNAVAILABLE_MESSAGE;
    return err.rawMessage || err.message;
  }
  if (err instanceof Error) return err.message;
  return "Upload failed";
}

async function readChunk(blob: Blob): Promise<Uint8Array> {
  if (typeof blob.arrayBuffer === "function") {
    return new Uint8Array(await blob.arrayBuffer());
  }
  // Older engines (and some test DOMs) have Blob without arrayBuffer().
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(new Uint8Array(reader.result as ArrayBuffer));
    reader.onerror = () => reject(reader.error ?? new Error("Could not read the file"));
    reader.readAsArrayBuffer(blob);
  });
}

/**
 * Discards an upload's staged bytes on the machine. Best effort: a session
 * the machine no longer has is already gone, and one this misses is swept
 * after a day.
 */
export async function abortUploadSession(
  client: FileUploadClient,
  scope: UploadScope,
  session: UploadSession,
): Promise<void> {
  const uploadId = session.uploadId;
  session.uploadId = undefined;
  session.bytesReceived = 0;
  if (!uploadId) return;
  try {
    await client.abortFileUpload({ ...scope, uploadId });
  } catch (err) {
    console.debug("[fileUpload] AbortFileUpload failed", err);
  }
}

/**
 * Uploads `file` into `directory`, resuming `session` when it names an open
 * upload (Retry). Resolves with where the file landed; rejects with
 * UploadCancelledError when the user cancelled (at a clash prompt, or
 * through `signal`), or with the error that stopped it — the session then
 * still names what the machine has, so calling again resumes.
 */
export async function runUpload(options: RunUploadOptions): Promise<UploadResult> {
  const { client, scope, session, signal } = options;
  try {
    return await upload(options);
  } catch (err) {
    if (err instanceof UploadCancelledError || signal?.aborted) {
      // The request the abort interrupted may have been the one that opened
      // the session; whatever the machine staged is discarded either way.
      await abortUploadSession(client, scope, session);
      throw err instanceof UploadCancelledError ? err : new UploadCancelledError();
    }
    throw err;
  }
}

async function upload({
  client,
  scope,
  file,
  directory,
  session,
  signal,
  onProgress,
  resolveConflict,
}: RunUploadOptions): Promise<UploadResult> {
  const callOptions = { signal };
  const checkCancelled = () => {
    if (signal?.aborted) throw new UploadCancelledError();
  };
  const dir = directory === "/" ? "" : directory;

  // Retry: pick the session up where the machine has it, or start over when
  // the machine no longer knows it (committed, aborted, swept, or a legacy
  // single-chunk id, which is never "open").
  if (session.uploadId) {
    checkCancelled();
    const status = await client.getFileUploadStatus({ ...scope, uploadId: session.uploadId }, callOptions);
    if (status.exists) {
      session.bytesReceived = Number(status.bytesReceived);
      onProgress?.(session.bytesReceived);
    } else {
      session.uploadId = undefined;
      session.bytesReceived = 0;
    }
  }

  if (!session.uploadId) {
    for (;;) {
      checkCancelled();
      try {
        const begun = await client.beginFileUpload(
          {
            ...scope,
            directory: dir,
            filename: file.name,
            size: BigInt(file.size),
            onConflict: session.policy,
          },
          callOptions,
        );
        session.uploadId = begun.uploadId;
        session.chunkSize = begun.chunkSize;
        session.bytesReceived = 0;
        onProgress?.(0);
        break;
      } catch (err) {
        // Only a FAIL policy can clash; anything else that says "exists" is
        // a real error, never a second prompt.
        if (!isAlreadyExists(err) || session.policy !== UploadConflictPolicy.FAIL) throw err;
        const choice = await resolveConflict();
        if (choice === "cancel") throw new UploadCancelledError();
        session.policy = policyFor(choice);
      }
    }
  }

  const uploadId = session.uploadId;
  if (!uploadId) throw new Error("The upload has no session");
  const chunkSize = Math.max(1, session.chunkSize ?? file.size);

  // One chunk in flight at a time, each at the offset the machine reported.
  // An empty file still sends one (empty) chunk: a legacy machine writes the
  // file on that chunk, and a staged session accepts it as a no-op.
  let sentAny = false;
  while (session.bytesReceived < file.size || (file.size === 0 && !sentAny)) {
    checkCancelled();
    const offset = session.bytesReceived;
    const data = await readChunk(file.slice(offset, Math.min(offset + chunkSize, file.size)));
    checkCancelled();
    const reply = await client.uploadFileChunk(
      { ...scope, uploadId, offset: BigInt(offset), data },
      callOptions,
    );
    sentAny = true;
    const received = Number(reply.bytesReceived);
    if (data.length > 0 && received <= offset) {
      // The machine took nothing: looping would resend the same chunk forever.
      throw new Error("The machine stopped accepting this upload");
    }
    session.bytesReceived = received;
    onProgress?.(received);
    if (reply.committedPath) {
      session.uploadId = undefined;
      return { path: reply.committedPath };
    }
  }

  let override: UploadConflictPolicy | undefined;
  for (;;) {
    checkCancelled();
    try {
      const committed = await client.commitFileUpload(
        { ...scope, uploadId, onConflict: override },
        callOptions,
      );
      session.uploadId = undefined;
      return { path: committed.path };
    } catch (err) {
      // The file appeared after Begin checked (or Begin was told OVERWRITE /
      // RENAME and still clashed): ask, then commit again with the answer.
      // The staged bytes stay on the machine meanwhile.
      if (!isAlreadyExists(err) || override !== undefined) throw err;
      const choice = await resolveConflict();
      if (choice === "cancel") throw new UploadCancelledError();
      override = policyFor(choice);
      session.policy = override;
    }
  }
}
