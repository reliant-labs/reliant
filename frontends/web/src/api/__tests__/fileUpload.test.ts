import { Code, ConnectError } from "@connectrpc/connect";
import { UploadConflictPolicy } from "../../gen/reliant/v1/filesystem_pb";
import {
  describeUploadError,
  newUploadSession,
  runUpload,
  UploadCancelledError,
  UPLOAD_UNAVAILABLE_MESSAGE,
  type ConflictChoice,
  type FileUploadClient,
  type UploadSession,
} from "../fileUpload";

const scope = { projectId: "project-1", worktreeId: "wt-1", chatId: "chat-1" };

function fileOf(size: number, name = "report.bin"): File {
  const bytes = new Uint8Array(size).map((_, i) => i % 251);
  return new File([bytes], name);
}

// A fake machine: chunks extend what it has (as the daemon does), commit
// answers with the destination.
function fakeClient() {
  const calls: string[] = [];
  const client = {
    beginFileUpload: vi.fn(async (_req: { onConflict: UploadConflictPolicy }) => {
      return { uploadId: "up-1", chunkSize: 4, path: "docs/report.bin", maxSize: 1n << 31n };
    }),
    uploadFileChunk: vi.fn(async (req: { offset: bigint; data: Uint8Array }) => {
      return { bytesReceived: req.offset + BigInt(req.data.length), committedPath: undefined as string | undefined };
    }),
    commitFileUpload: vi.fn(async (_req: { onConflict?: UploadConflictPolicy }) => {
      return { path: "docs/report.bin", size: 10n, created: true };
    }),
    abortFileUpload: vi.fn(async (_req: { uploadId: string }) => {
      return {};
    }),
    getFileUploadStatus: vi.fn(async (_req: { uploadId: string }) => {
      return { exists: true, bytesReceived: 0n, size: 10n };
    }),
  };
  // Every call is logged here, whatever a test's mockImplementationOnce does.
  type Req = { offset?: bigint };
  const logged = (name: string, fn: (req: never, opts?: never) => unknown) =>
    (req: Req, opts?: unknown) => {
      calls.push(name === "chunk" ? `chunk@${req.offset}` : name);
      return fn(req as never, opts as never);
    };
  const asClient = {
    beginFileUpload: logged("begin", client.beginFileUpload),
    uploadFileChunk: logged("chunk", client.uploadFileChunk),
    commitFileUpload: logged("commit", client.commitFileUpload),
    abortFileUpload: logged("abort", client.abortFileUpload),
    getFileUploadStatus: logged("status", client.getFileUploadStatus),
  } as unknown as FileUploadClient;
  return { client, calls, asClient };
}

function run(
  c: ReturnType<typeof fakeClient>,
  opts: {
    file?: File;
    session?: UploadSession;
    signal?: AbortSignal;
    choice?: ConflictChoice;
    onProgress?: (n: number) => void;
  } = {},
) {
  const resolveConflict = vi.fn(async () => opts.choice ?? "cancel");
  const promise = runUpload({
    client: c.asClient,
    scope,
    file: opts.file ?? fileOf(10),
    directory: "docs",
    session: opts.session ?? newUploadSession(),
    signal: opts.signal,
    onProgress: opts.onProgress,
    resolveConflict,
  });
  return { promise, resolveConflict };
}

const exists = () => new ConnectError("a file already exists at this path", Code.AlreadyExists);

describe("runUpload", () => {
  it("sends sequential chunks of the server's chunk size, then commits", async () => {
    const c = fakeClient();
    const progress: number[] = [];
    const { promise } = run(c, { onProgress: (n) => progress.push(n) });
    await expect(promise).resolves.toEqual({ path: "docs/report.bin" });

    expect(c.calls).toEqual(["begin", "chunk@0", "chunk@4", "chunk@8", "commit"]);
    expect(c.client.beginFileUpload.mock.calls[0][0]).toMatchObject({
      ...scope, directory: "docs", filename: "report.bin", size: 10n, onConflict: UploadConflictPolicy.FAIL,
    });
    const chunks = c.client.uploadFileChunk.mock.calls.map(([req]) => req as unknown as { uploadId: string; data: Uint8Array });
    expect(chunks.map((req) => req.data.length)).toEqual([4, 4, 2]);
    expect(chunks.every((req) => req.uploadId === "up-1")).toBe(true);
    // The bytes are the file's, in order.
    expect(Array.from(chunks[1].data)).toEqual([4, 5, 6, 7]);
    expect(c.client.commitFileUpload.mock.calls[0][0]).toMatchObject({ ...scope, uploadId: "up-1", onConflict: undefined });
    expect(progress).toEqual([0, 4, 8, 10]);
  });

  it("takes each offset from the previous reply's bytes_received", async () => {
    const c = fakeClient();
    // The machine kept only 3 of the first 4 bytes.
    c.client.uploadFileChunk.mockResolvedValueOnce({ bytesReceived: 3n, committedPath: undefined });
    await run(c).promise;
    expect(c.calls).toEqual(["begin", "chunk@0", "chunk@3", "chunk@7", "commit"]);
  });

  it("is done without a commit when a legacy machine's chunk placed the file", async () => {
    const c = fakeClient();
    c.client.beginFileUpload.mockResolvedValueOnce({ uploadId: "legacy.abc", chunkSize: 10, path: "docs/report.bin", maxSize: 0n });
    c.client.uploadFileChunk.mockResolvedValueOnce({ bytesReceived: 10n, committedPath: "docs/report.bin" });
    await expect(run(c).promise).resolves.toEqual({ path: "docs/report.bin" });
    expect(c.calls).toEqual(["begin", "chunk@0"]);
    expect((c.client.uploadFileChunk.mock.calls[0][0] as { data: Uint8Array }).data.length).toBe(10);
  });

  it("sends one empty chunk for an empty file", async () => {
    const c = fakeClient();
    await run(c, { file: fileOf(0) }).promise;
    expect(c.calls).toEqual(["begin", "chunk@0", "commit"]);
  });

  describe("when Begin says the file already exists", () => {
    it.each([
      ["replace", UploadConflictPolicy.OVERWRITE],
      ["keep-both", UploadConflictPolicy.RENAME],
    ] as const)("%s opens the upload again with that policy", async (choice, policy) => {
      const c = fakeClient();
      c.client.beginFileUpload.mockRejectedValueOnce(exists());
      const { promise, resolveConflict } = run(c, { choice });
      await promise;
      expect(resolveConflict).toHaveBeenCalledTimes(1);
      expect(c.client.beginFileUpload).toHaveBeenCalledTimes(2);
      expect(c.client.beginFileUpload.mock.calls[1][0].onConflict).toBe(policy);
      expect(c.calls.slice(-1)).toEqual(["commit"]);
    });

    it("cancel stops there and sends nothing more", async () => {
      const c = fakeClient();
      c.client.beginFileUpload.mockRejectedValueOnce(exists());
      await expect(run(c, { choice: "cancel" }).promise).rejects.toBeInstanceOf(UploadCancelledError);
      expect(c.client.beginFileUpload).toHaveBeenCalledTimes(1);
      expect(c.client.uploadFileChunk).not.toHaveBeenCalled();
      expect(c.client.commitFileUpload).not.toHaveBeenCalled();
      expect(c.client.abortFileUpload).not.toHaveBeenCalled();
    });
  });

  it("commits again with the user's choice when Commit says the file appeared, without re-sending", async () => {
    const c = fakeClient();
    c.client.commitFileUpload.mockRejectedValueOnce(exists());
    const { promise, resolveConflict } = run(c, { choice: "keep-both" });
    await promise;
    expect(resolveConflict).toHaveBeenCalledTimes(1);
    expect(c.calls).toEqual(["begin", "chunk@0", "chunk@4", "chunk@8", "commit", "commit"]);
    expect(c.client.commitFileUpload.mock.calls[1][0]).toMatchObject({ uploadId: "up-1", onConflict: UploadConflictPolicy.RENAME });
  });

  it("discards the session when the user cancels at a Commit clash", async () => {
    const c = fakeClient();
    c.client.commitFileUpload.mockRejectedValueOnce(exists());
    await expect(run(c, { choice: "cancel" }).promise).rejects.toBeInstanceOf(UploadCancelledError);
    expect(c.client.abortFileUpload.mock.calls[0][0]).toMatchObject({ uploadId: "up-1" });
  });

  it("cancel aborts the in-flight request and the session", async () => {
    const c = fakeClient();
    const controller = new AbortController();
    let chunkSignal: AbortSignal | undefined;
    c.client.uploadFileChunk.mockImplementationOnce((_req, opts?: { signal?: AbortSignal }) => {
      chunkSignal = opts?.signal;
      return new Promise((_, reject) => {
        opts?.signal?.addEventListener("abort", () => reject(new ConnectError("aborted", Code.Canceled)));
      });
    });
    const session = newUploadSession();
    const { promise } = run(c, { signal: controller.signal, session });
    await vi.waitFor(() => expect(c.client.uploadFileChunk).toHaveBeenCalled());
    controller.abort();
    await expect(promise).rejects.toBeInstanceOf(UploadCancelledError);
    expect(chunkSignal?.aborted).toBe(true);
    expect(c.client.abortFileUpload.mock.calls[0][0]).toMatchObject({ ...scope, uploadId: "up-1" });
    expect(c.client.commitFileUpload).not.toHaveBeenCalled();
    expect(session.uploadId).toBeUndefined();
  });

  describe("retry", () => {
    it("resumes from the machine's bytes_received when the session still exists", async () => {
      const c = fakeClient();
      c.client.getFileUploadStatus.mockResolvedValueOnce({ exists: true, bytesReceived: 4n, size: 10n });
      const session: UploadSession = { uploadId: "up-1", chunkSize: 4, policy: UploadConflictPolicy.FAIL, bytesReceived: 4 };
      await run(c, { session }).promise;
      expect(c.calls).toEqual(["status", "chunk@4", "chunk@8", "commit"]);
      expect(c.client.getFileUploadStatus.mock.calls[0][0]).toMatchObject({ ...scope, uploadId: "up-1" });
    });

    it("starts over with Begin when the machine no longer has the session", async () => {
      const c = fakeClient();
      c.client.getFileUploadStatus.mockResolvedValueOnce({ exists: false, bytesReceived: 0n, size: 0n });
      const session: UploadSession = { uploadId: "up-gone", chunkSize: 4, policy: UploadConflictPolicy.OVERWRITE, bytesReceived: 8 };
      await run(c, { session }).promise;
      expect(c.calls).toEqual(["status", "begin", "chunk@0", "chunk@4", "chunk@8", "commit"]);
      // The user's earlier answer to a clash still holds.
      expect(c.client.beginFileUpload.mock.calls[0][0].onConflict).toBe(UploadConflictPolicy.OVERWRITE);
    });

    it("keeps the session after a failure, so the next attempt can resume", async () => {
      const c = fakeClient();
      c.client.uploadFileChunk
        .mockImplementationOnce(async (req) => ({ bytesReceived: req.offset + 4n, committedPath: undefined }))
        .mockRejectedValueOnce(new ConnectError("daemon unreachable", Code.Unavailable));
      const session = newUploadSession();
      await expect(run(c, { session }).promise).rejects.toThrow("daemon unreachable");
      expect(session).toMatchObject({ uploadId: "up-1", bytesReceived: 4 });
    });
  });

  it("explains an API server without the upload RPCs", async () => {
    const c = fakeClient();
    const err = new ConnectError("not found", Code.Unimplemented);
    c.client.beginFileUpload.mockRejectedValueOnce(err);
    await expect(run(c).promise).rejects.toBe(err);
    expect(describeUploadError(err)).toBe(UPLOAD_UNAVAILABLE_MESSAGE);
    expect(describeUploadError(new ConnectError("file exceeds the 2 GiB limit", Code.InvalidArgument))).toBe(
      "file exceeds the 2 GiB limit",
    );
  });
});
