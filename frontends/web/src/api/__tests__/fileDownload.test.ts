import { Code, ConnectError } from "@connectrpc/connect";
import { FolderArchiveState } from "../../gen/reliant/v1/filesystem_pb";
import {
  createFileHandleSink,
  createMemorySink,
  DownloadCancelledError,
  FOLDER_DOWNLOAD_UNAVAILABLE_MESSAGE,
  MEMORY_DOWNLOAD_LIMIT_BYTES,
  runFolderDownload,
  runRangedDownload,
  type FileDownloadClient,
} from "../fileDownload";

const scope = { projectId: "project-1", worktreeId: "wt-1", chatId: "chat-1" };

function reasoned(message: string, code: Code, reason: string): ConnectError {
  return new ConnectError(message, code, new Headers({ "x-forge-error-reason": reason }));
}

const bytesOf = (size: number) => new Uint8Array(size).map((_, i) => i % 251);

type RangeReq = { path: string; archiveId?: string; offset: bigint; ifVersion?: string };

// A fake machine serving `content` in pieces of `piece` bytes, whose version
// can change between pieces.
function fakeClient(content: Uint8Array, piece = 4) {
  const state = { version: "v1" };
  const client = {
    readFileRange: vi.fn(async (req: RangeReq, _opts?: { signal?: AbortSignal }) => {
      if (req.ifVersion !== undefined && req.ifVersion !== state.version) {
        throw reasoned("report.bin changed while it was downloading", Code.FailedPrecondition, "file_changed_during_download");
      }
      const start = Number(req.offset);
      const data = content.slice(start, start + piece);
      return {
        data,
        totalSize: BigInt(content.length),
        version: state.version,
        filename: req.archiveId ? "docs.zip" : "report.bin",
        contentType: req.archiveId ? "application/zip" : "application/octet-stream",
        eof: start + data.length >= content.length,
      };
    }),
    downloadFile: vi.fn(async (_req: { path: string }) => ({
      content, contentType: "text/plain", filename: "report.bin", size: BigInt(content.length),
    })),
    prepareFolderArchive: vi.fn(async (_req: { path: string }) => ({
      archiveId: "ar-1", filename: "docs.zip", totalFiles: 3n, totalBytes: 30n, maxSize: 1n << 31n,
    })),
    getFolderArchiveStatus: vi.fn(async (_req: { archiveId: string }) => ({
      state: FolderArchiveState.READY, filesDone: 3n, filesTotal: 3n, bytesDone: 30n, bytesTotal: 30n,
      archiveSize: BigInt(content.length), error: "", skipped: 0n,
    })),
    releaseFolderArchive: vi.fn(async (_req: { archiveId: string }) => ({})),
  };
  return { client, state, asClient: client as unknown as FileDownloadClient };
}

// A FileSystemWritableFileStream that records what it was given.
function fakeHandle() {
  const written: number[] = [];
  const stream = {
    write: vi.fn(async (chunk: Uint8Array) => {
      written.push(...chunk);
    }),
    close: vi.fn(async () => undefined),
    abort: vi.fn(async () => undefined),
  };
  return { written, stream, handle: { createWritable: vi.fn(async () => stream) } };
}

async function blobBytes(blob: Blob): Promise<Uint8Array> {
  const buffer = await new Promise<ArrayBuffer>((resolve) => {
    const reader = new FileReader();
    reader.onload = () => resolve(reader.result as ArrayBuffer);
    reader.readAsArrayBuffer(blob);
  });
  return new Uint8Array(buffer);
}

describe("runRangedDownload", () => {
  it.each([false, true])("aborts instead of committing when cancelled in a sink operation (fallback=%s)", async (fallback) => {
    for (const stage of ["begin", "write"] as const) {
      const fake = fakeClient(bytesOf(1));
      if (fallback) fake.client.readFileRange.mockRejectedValue(reasoned("unsupported", Code.Unimplemented, "ranged_download_unsupported"));
      const controller = new AbortController();
      let entered!: () => void;
      const started = new Promise<void>((resolve) => { entered = resolve; });
      let resume!: () => void;
      const blocked = new Promise<void>((resolve) => { resume = resolve; });
      const sink = {
        begin: vi.fn(async () => undefined),
        write: vi.fn(async () => undefined),
        close: vi.fn(async () => undefined),
        abort: vi.fn(async () => undefined),
      };
      sink[stage].mockImplementation(async () => { entered(); await blocked; });
      const transfer = runRangedDownload({ client: fake.asClient, scope, source: { path: "report.bin" }, sink, signal: controller.signal });
      const rejected = expect(transfer).rejects.toBeInstanceOf(DownloadCancelledError);
      await started;
      controller.abort();
      resume();
      await rejected;
      expect(sink.abort).toHaveBeenCalledOnce();
      expect(sink.close).not.toHaveBeenCalled();
    }
  });

  it("streams every piece into the picked file, pinning the first piece's version", async () => {
    const content = bytesOf(10);
    const fake = fakeClient(content);
    const { handle, stream, written } = fakeHandle();
    const progress: Array<[number, number]> = [];

    const result = await runRangedDownload({
      client: fake.asClient,
      scope,
      source: { path: "docs/report.bin" },
      sink: createFileHandleSink(handle),
      onProgress: (done, total) => progress.push([done, total]),
    });

    expect(result).toEqual({ filename: "report.bin", totalSize: 10 });
    expect(new Uint8Array(written)).toEqual(content);
    expect(stream.close).toHaveBeenCalledTimes(1);
    expect(stream.abort).not.toHaveBeenCalled();
    expect(progress).toEqual([[4, 10], [8, 10], [10, 10]]);
    const calls = fake.client.readFileRange.mock.calls.map(([req]) => req);
    expect(calls.map((req) => req.offset)).toEqual([0n, 4n, 8n]);
    expect(calls[0]).toMatchObject({ ...scope, path: "docs/report.bin", ifVersion: undefined });
    expect(calls.slice(1).every((req) => req.ifVersion === "v1")).toBe(true);
  });

  it("fails cleanly when the file changes mid-download, discarding the partial file", async () => {
    const fake = fakeClient(bytesOf(10));
    const { handle, stream } = fakeHandle();
    fake.client.readFileRange.mockImplementationOnce(async (req) => {
      const first = await fakeClient(bytesOf(10)).client.readFileRange(req);
      fake.state.version = "v2"; // the file is rewritten after the first piece
      return first;
    });

    await expect(runRangedDownload({
      client: fake.asClient, scope, source: { path: "report.bin" }, sink: createFileHandleSink(handle),
    })).rejects.toThrow("report.bin changed while it was downloading");
    expect(stream.abort).toHaveBeenCalledTimes(1);
    expect(stream.close).not.toHaveBeenCalled();
  });

  it("stops when cancelled mid-download and aborts the file", async () => {
    const fake = fakeClient(bytesOf(12));
    const { handle, stream } = fakeHandle();
    const controller = new AbortController();

    const promise = runRangedDownload({
      client: fake.asClient,
      scope,
      source: { path: "report.bin" },
      sink: createFileHandleSink(handle),
      signal: controller.signal,
      onProgress: (done) => {
        if (done === 4) controller.abort();
      },
    });

    await expect(promise).rejects.toBeInstanceOf(DownloadCancelledError);
    expect(fake.client.readFileRange).toHaveBeenCalledTimes(1);
    expect(stream.abort).toHaveBeenCalled();
    expect(stream.close).not.toHaveBeenCalled();
  });

  it("collects the file in memory and hands it to the browser as one blob", async () => {
    const content = bytesOf(9);
    const save = vi.fn();
    await runRangedDownload({
      client: fakeClient(content).asClient, scope, source: { path: "report.bin" }, sink: createMemorySink("report.bin", save),
    });
    expect(save).toHaveBeenCalledTimes(1);
    const [blob, filename] = save.mock.calls[0] as [Blob, string];
    expect(filename).toBe("report.bin");
    expect(await blobBytes(blob)).toEqual(content);
  });

  it("refuses a file over the memory cap after its first piece, saying how to get it", async () => {
    const fake = fakeClient(bytesOf(4));
    fake.client.readFileRange.mockResolvedValueOnce({
      data: bytesOf(4), totalSize: 1_200_000_000n, version: "v1", filename: "video.mov",
      contentType: "video/quicktime", eof: false,
    });
    const save = vi.fn();
    await expect(runRangedDownload({
      client: fake.asClient, scope, source: { path: "video.mov" }, sink: createMemorySink("video.mov", save),
    })).rejects.toThrow(
      "video.mov is 1.2 GB. This browser can only download files up to 500 MB from the Files tab; " +
        "use Chrome, Edge or the Reliant desktop app for larger files.",
    );
    expect(MEMORY_DOWNLOAD_LIMIT_BYTES).toBe(500_000_000);
    expect(fake.client.readFileRange).toHaveBeenCalledTimes(1);
    expect(save).not.toHaveBeenCalled();
  });

  it.each([
    ["the machine predates ranged reads", reasoned("Update Reliant on laptop to download files larger than 25 MB", Code.FailedPrecondition, "ranged_download_unsupported")],
    ["the API server predates ranged reads", new ConnectError("unimplemented", Code.Unimplemented)],
  ])("falls back to DownloadFile when %s", async (_, refusal) => {
    const content = bytesOf(7);
    const fake = fakeClient(content);
    fake.client.readFileRange.mockRejectedValueOnce(refusal);
    const { handle, written, stream } = fakeHandle();

    const result = await runRangedDownload({
      client: fake.asClient, scope, source: { path: "report.bin" }, sink: createFileHandleSink(handle),
    });
    expect(result.totalSize).toBe(7);
    expect(fake.client.downloadFile).toHaveBeenCalledWith({ ...scope, path: "report.bin" }, expect.anything());
    expect(new Uint8Array(written)).toEqual(content);
    expect(stream.close).toHaveBeenCalled();
  });

  it("says to update Reliant when the fallback finds the file too large", async () => {
    const fake = fakeClient(bytesOf(7));
    fake.client.readFileRange.mockRejectedValueOnce(
      reasoned("Update Reliant on laptop to download files larger than 25 MB", Code.FailedPrecondition, "ranged_download_unsupported"),
    );
    fake.client.downloadFile.mockRejectedValueOnce(
      reasoned("file is larger than 25 MB (it is 40 MB)", Code.FailedPrecondition, "file_too_large_to_download"),
    );
    const save = vi.fn();
    const err = await runRangedDownload({
      client: fake.asClient, scope, source: { path: "big.iso" }, sink: createMemorySink("big.iso", save),
    }).catch((e: unknown) => e);
    expect((err as ConnectError).rawMessage).toBe("Update Reliant on laptop to download files larger than 25 MB");
    expect(save).not.toHaveBeenCalled();
  });

  it("does not fall back for an archive", async () => {
    const fake = fakeClient(bytesOf(4));
    fake.client.readFileRange.mockRejectedValueOnce(new ConnectError("unimplemented", Code.Unimplemented));
    await expect(runRangedDownload({
      client: fake.asClient, scope, source: { archiveId: "ar-1" }, sink: createMemorySink("docs.zip", vi.fn()),
    })).rejects.toThrow();
    expect(fake.client.downloadFile).not.toHaveBeenCalled();
  });
});

describe("runFolderDownload", () => {
  it("zips, polls until ready, reads the archive and releases it", async () => {
    const content = bytesOf(6);
    const fake = fakeClient(content);
    fake.client.getFolderArchiveStatus.mockResolvedValueOnce({
      state: FolderArchiveState.RUNNING, filesDone: 1n, filesTotal: 3n, bytesDone: 10n, bytesTotal: 30n,
      archiveSize: 0n, error: "", skipped: 0n,
    });
    fake.client.getFolderArchiveStatus.mockResolvedValueOnce({
      state: FolderArchiveState.READY, filesDone: 3n, filesTotal: 3n, bytesDone: 30n, bytesTotal: 30n,
      archiveSize: 6n, error: "", skipped: 2n,
    });
    const save = vi.fn();
    const zipping: number[] = [];

    const result = await runFolderDownload({
      client: fake.asClient, scope, path: "docs", sink: createMemorySink("docs.zip", save), pollIntervalMs: 1,
      onZipping: ({ filesDone }) => zipping.push(filesDone),
    });

    expect(result).toEqual({ filename: "docs.zip", totalSize: 6, skipped: 2 });
    expect(fake.client.prepareFolderArchive).toHaveBeenCalledWith({ ...scope, path: "docs" }, expect.anything());
    expect(zipping).toEqual([0, 1, 3]);
    expect(fake.client.readFileRange.mock.calls[0][0]).toMatchObject({ archiveId: "ar-1", path: "" });
    expect(await blobBytes(save.mock.calls[0][0] as Blob)).toEqual(content);
    expect(fake.client.releaseFolderArchive).toHaveBeenCalledWith({ ...scope, archiveId: "ar-1" });
  });

  it("fails with the machine's reason when zipping fails, and still releases", async () => {
    const fake = fakeClient(bytesOf(6));
    fake.client.getFolderArchiveStatus.mockResolvedValueOnce({
      state: FolderArchiveState.FAILED, filesDone: 1n, filesTotal: 3n, bytesDone: 0n, bytesTotal: 30n,
      archiveSize: 0n, error: "disk full", skipped: 0n,
    });
    const { handle, stream } = fakeHandle();
    await expect(runFolderDownload({
      client: fake.asClient, scope, path: "docs", sink: createFileHandleSink(handle), pollIntervalMs: 1,
    })).rejects.toThrow("disk full");
    expect(fake.client.readFileRange).not.toHaveBeenCalled();
    expect(fake.client.releaseFolderArchive).toHaveBeenCalledTimes(1);
    expect(stream.close).not.toHaveBeenCalled();
  });

  it("releases the archive when cancelled while zipping", async () => {
    const fake = fakeClient(bytesOf(6));
    fake.client.getFolderArchiveStatus.mockResolvedValue({
      state: FolderArchiveState.RUNNING, filesDone: 0n, filesTotal: 3n, bytesDone: 0n, bytesTotal: 30n,
      archiveSize: 0n, error: "", skipped: 0n,
    });
    const controller = new AbortController();
    const promise = runFolderDownload({
      client: fake.asClient, scope, path: "docs", sink: createMemorySink("docs.zip", vi.fn()),
      signal: controller.signal, pollIntervalMs: 10_000,
      onZipping: () => queueMicrotask(() => controller.abort()),
    });
    await expect(promise).rejects.toBeInstanceOf(DownloadCancelledError);
    expect(fake.client.releaseFolderArchive).toHaveBeenCalledWith({ ...scope, archiveId: "ar-1" });
  });

  it("names a too-large folder with the server's words and releases nothing", async () => {
    const fake = fakeClient(bytesOf(6));
    fake.client.prepareFolderArchive.mockRejectedValueOnce(
      reasoned("docs holds 3.1 GB; folders up to 2 GB can be downloaded", Code.FailedPrecondition, "folder_too_large_to_download"),
    );
    const err = await runFolderDownload({
      client: fake.asClient, scope, path: "docs", sink: createMemorySink("docs.zip", vi.fn()),
    }).catch((e: unknown) => e);
    expect((err as ConnectError).rawMessage).toBe("docs holds 3.1 GB; folders up to 2 GB can be downloaded");
    expect(fake.client.releaseFolderArchive).not.toHaveBeenCalled();
  });

  it("says the server is too old when it has no folder archives", async () => {
    const fake = fakeClient(bytesOf(6));
    fake.client.prepareFolderArchive.mockRejectedValueOnce(new ConnectError("unimplemented", Code.Unimplemented));
    await expect(runFolderDownload({
      client: fake.asClient, scope, path: "docs", sink: createMemorySink("docs.zip", vi.fn()),
    })).rejects.toThrow(FOLDER_DOWNLOAD_UNAVAILABLE_MESSAGE);
  });
});
