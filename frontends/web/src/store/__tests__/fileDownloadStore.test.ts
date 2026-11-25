import { Code, ConnectError } from "@connectrpc/connect";
import { FolderArchiveState } from "../../gen/reliant/v1/filesystem_pb";
import { createMemorySink, type FileDownloadClient, type FirstCallRunner } from "../../api/fileDownload";
import { createFileDownloadStore } from "../fileDownloadStore";

const scope = { projectId: "project-1", worktreeId: "wt-1", chatId: "chat-1" };

// A machine serving 10 bytes in pieces of 4; `gate` holds a piece back until released.
function fakeClient() {
  let release: (() => void) | undefined;
  const held = { hold: false };
  const client = {
    readFileRange: vi.fn(async (req: { offset: bigint; archiveId?: string }, opts?: { signal?: AbortSignal }) => {
      if (held.hold && req.offset > 0n) {
        await new Promise<void>((resolve, reject) => {
          release = resolve;
          opts?.signal?.addEventListener("abort", () => reject(new ConnectError("aborted", Code.Canceled)));
        });
      }
      const start = Number(req.offset);
      const size = Math.min(4, 10 - start);
      return {
        data: new Uint8Array(size), totalSize: 10n, version: "v1",
        filename: req.archiveId ? "docs.zip" : "big.bin", contentType: "application/octet-stream",
        eof: start + size >= 10,
      };
    }),
    downloadFile: vi.fn(),
    prepareFolderArchive: vi.fn(async () => ({
      archiveId: "ar-1", filename: "docs.zip", totalFiles: 4n, totalBytes: 40n, maxSize: 1n << 31n,
    })),
    getFolderArchiveStatus: vi.fn(async () => ({
      state: FolderArchiveState.READY, filesDone: 4n, filesTotal: 4n, bytesDone: 40n, bytesTotal: 40n,
      archiveSize: 10n, error: "", skipped: 1n,
    })),
    releaseFolderArchive: vi.fn(async () => ({})),
  };
  return { client, held, release: () => release?.(), asClient: client as unknown as FileDownloadClient };
}

describe("fileDownloadStore", () => {
  it("shows a file download's progress and ends done", async () => {
    const fake = fakeClient();
    const store = createFileDownloadStore({ client: fake.asClient });
    const save = vi.fn();
    const outcome = await store.getState().startFileDownload({
      name: "big.bin", path: "big.bin", scope, sink: createMemorySink("big.bin", save), size: 10,
    });
    expect(outcome).toEqual({ status: "done" });
    expect(store.getState().items).toEqual([
      expect.objectContaining({ name: "big.bin", kind: "file", status: "done", bytesDone: 10, totalBytes: 10 }),
    ]);
    expect(save).toHaveBeenCalledTimes(1);
  });

  it("runs a folder through zipping to done, counting what was skipped", async () => {
    const fake = fakeClient();
    const store = createFileDownloadStore({ client: fake.asClient, pollIntervalMs: 1 });
    const pending = store.getState().startFolderDownload({
      name: "docs", path: "docs", scope, sink: createMemorySink("docs.zip", vi.fn()),
    });
    expect(store.getState().items[0]).toMatchObject({ name: "docs.zip", kind: "folder", status: "preparing" });
    await pending;
    expect(store.getState().items[0]).toMatchObject({
      status: "done", filesDone: 4, filesTotal: 4, skipped: 1, totalBytes: 10,
    });
    expect(fake.client.releaseFolderArchive).toHaveBeenCalledTimes(1);
  });

  it("cancels a running download, and only then lets it be dismissed", async () => {
    const fake = fakeClient();
    fake.held.hold = true;
    const store = createFileDownloadStore({ client: fake.asClient });
    const pending = store.getState().startFileDownload({
      name: "big.bin", path: "big.bin", scope, sink: createMemorySink("big.bin", vi.fn()),
    });
    await vi.waitFor(() => expect(store.getState().items[0].bytesDone).toBe(4));
    const id = store.getState().items[0].id;

    store.getState().dismiss(id); // running: kept
    expect(store.getState().items).toHaveLength(1);

    store.getState().cancel(id);
    expect(await pending).toEqual({ status: "cancelled" });
    expect(store.getState().items[0].status).toBe("cancelled");
    store.getState().dismiss(id);
    expect(store.getState().items).toEqual([]);
  });

  it("keeps several downloads side by side, and a failure with its reason", async () => {
    const fake = fakeClient();
    fake.client.readFileRange.mockRejectedValueOnce(new ConnectError("no such file", Code.NotFound));
    const store = createFileDownloadStore({ client: fake.asClient });
    const [failed, done] = await Promise.all([
      store.getState().startFileDownload({ name: "gone.bin", path: "gone.bin", scope, sink: createMemorySink("gone.bin", vi.fn()) }),
      store.getState().startFileDownload({ name: "big.bin", path: "big.bin", scope, sink: createMemorySink("big.bin", vi.fn()) }),
    ]);
    expect(failed).toEqual({ status: "failed", error: "no such file" });
    expect(done).toEqual({ status: "done" });
    expect(store.getState().items.map((item) => item.status)).toEqual(["failed", "done"]);

    store.getState().clearFinished();
    expect(store.getState().items).toEqual([expect.objectContaining({ name: "gone.bin", error: "no such file" })]);
  });

  it("runs the first call through the wake-aware runner", async () => {
    const fake = fakeClient();
    const firstCall = vi.fn((action: () => Promise<unknown>) => action());
    const store = createFileDownloadStore({ client: fake.asClient, firstCall: firstCall as FirstCallRunner });
    await store.getState().startFileDownload({ name: "big.bin", path: "big.bin", scope, sink: createMemorySink("big.bin", vi.fn()) });
    expect(firstCall).toHaveBeenCalledTimes(1);
    expect(fake.client.readFileRange).toHaveBeenCalledTimes(3);
  });
});
