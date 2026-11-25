import { Code, ConnectError } from "@connectrpc/connect";
import { UploadConflictPolicy } from "../../gen/reliant/v1/filesystem_pb";
import type { FileUploadClient } from "../../api/fileUpload";
import { createFileUploadStore } from "../fileUploadStore";

vi.mock("../../lib/connect", () => ({ connectClient: vi.fn() }));
vi.mock("../gitStatusStore", () => ({ triggerGitStatusRefresh: vi.fn() }));

const scope = { projectId: "project-1", worktreeId: "wt-1", chatId: "chat-1" };

function fileOf(size: number, name: string): File {
  return new File([new Uint8Array(size)], name);
}

function fakeClient() {
  let n = 0;
  const log: string[] = [];
  const client = {
    beginFileUpload: vi.fn(async (req: { filename: string; onConflict: UploadConflictPolicy }) => {
      log.push(`begin ${req.filename}`);
      return { uploadId: `up-${++n}`, chunkSize: 4, path: req.filename, maxSize: 0n };
    }),
    uploadFileChunk: vi.fn(async (req: { uploadId: string; offset: bigint; data: Uint8Array }) => {
      log.push(`chunk ${req.uploadId}@${req.offset}`);
      return { bytesReceived: req.offset + BigInt(req.data.length) };
    }),
    commitFileUpload: vi.fn(async (req: { uploadId: string }) => {
      log.push(`commit ${req.uploadId}`);
      return { path: `docs/${req.uploadId}`, size: 0n, created: true };
    }),
    abortFileUpload: vi.fn(async (req: { uploadId: string }) => {
      log.push(`abort ${req.uploadId}`);
      return {};
    }),
    getFileUploadStatus: vi.fn(async (req: { uploadId: string }) => {
      log.push(`status ${req.uploadId}`);
      return { exists: true, bytesReceived: 4n, size: 6n };
    }),
  };
  return { client, log, asClient: client as unknown as FileUploadClient };
}

const statusOf = (store: ReturnType<typeof createFileUploadStore>) =>
  store.getState().items.map((item) => item.status);

describe("fileUploadStore", () => {
  it("uploads queued files one at a time and reports each landing", async () => {
    const c = fakeClient();
    const onCommitted = vi.fn();
    const store = createFileUploadStore({ client: c.asClient, onCommitted });
    store.getState().enqueue([fileOf(6, "a.txt"), fileOf(2, "b.txt")], "docs", scope);
    expect(store.getState().items.map((item) => item.directory)).toEqual(["docs", "docs"]);

    await vi.waitFor(() => expect(statusOf(store)).toEqual(["done", "done"]));
    // b's Begin waits for a's Commit.
    expect(c.log).toEqual([
      "begin a.txt", "chunk up-1@0", "chunk up-1@4", "commit up-1",
      "begin b.txt", "chunk up-2@0", "commit up-2",
    ]);
    expect(onCommitted).toHaveBeenCalledTimes(2);
    expect(onCommitted.mock.calls[0][0]).toMatchObject({ directory: "docs", projectId: "project-1", worktreeId: "wt-1" });
    expect(store.getState().lastCommitted?.path).toBe("docs/up-2");
    expect(store.getState().items[0]).toMatchObject({ bytesReceived: 6, path: "docs/up-1" });
  });

  it("asks about a clash through `conflict` and goes on with the answer", async () => {
    const c = fakeClient();
    c.client.beginFileUpload.mockRejectedValueOnce(new ConnectError("exists", Code.AlreadyExists));
    const store = createFileUploadStore({ client: c.asClient });
    store.getState().enqueue([fileOf(2, "a.txt")], "", scope);

    await vi.waitFor(() => expect(store.getState().conflict).toMatchObject({ name: "a.txt", directory: "" }));
    store.getState().resolveConflict("replace");
    await vi.waitFor(() => expect(statusOf(store)).toEqual(["done"]));
    expect(store.getState().conflict).toBeNull();
    expect(c.client.beginFileUpload.mock.calls[1][0].onConflict).toBe(UploadConflictPolicy.OVERWRITE);
  });

  it("cancelling at the clash prompt marks the row cancelled and moves on", async () => {
    const c = fakeClient();
    c.client.beginFileUpload.mockRejectedValueOnce(new ConnectError("exists", Code.AlreadyExists));
    const store = createFileUploadStore({ client: c.asClient });
    store.getState().enqueue([fileOf(2, "a.txt"), fileOf(2, "b.txt")], "", scope);

    await vi.waitFor(() => expect(store.getState().conflict).not.toBeNull());
    store.getState().cancel(store.getState().items[0].id);
    await vi.waitFor(() => expect(statusOf(store)).toEqual(["cancelled", "done"]));
  });

  it("fails a row with a readable error, and Retry resumes it", async () => {
    const c = fakeClient();
    c.client.uploadFileChunk
      .mockImplementationOnce(async (req) => ({ bytesReceived: req.offset + BigInt(req.data.length) }))
      .mockRejectedValueOnce(new ConnectError("the machine is offline", Code.Unavailable));
    const store = createFileUploadStore({ client: c.asClient });
    store.getState().enqueue([fileOf(6, "a.txt")], "docs", scope);

    await vi.waitFor(() => expect(statusOf(store)).toEqual(["failed"]));
    expect(store.getState().items[0].error).toBe("the machine is offline");

    c.log.length = 0;
    store.getState().retry(store.getState().items[0].id);
    await vi.waitFor(() => expect(statusOf(store)).toEqual(["done"]));
    expect(c.log).toEqual(["status up-1", "chunk up-1@4", "commit up-1"]);
  });

  it("shows the old-server message when the RPCs are missing", async () => {
    const c = fakeClient();
    c.client.beginFileUpload.mockRejectedValueOnce(new ConnectError("404", Code.Unimplemented));
    const store = createFileUploadStore({ client: c.asClient });
    store.getState().enqueue([fileOf(2, "a.txt")], "", scope);
    await vi.waitFor(() => expect(statusOf(store)).toEqual(["failed"]));
    expect(store.getState().items[0].error).toBe("Uploading isn't available on this server yet");
  });

  it("cancels a running upload, and dismiss / clear finished remove rows", async () => {
    const c = fakeClient();
    c.client.uploadFileChunk.mockImplementationOnce(
      (_req, opts?: { signal?: AbortSignal }) =>
        new Promise((_, reject) =>
          opts?.signal?.addEventListener("abort", () => reject(new ConnectError("aborted", Code.Canceled))),
        ),
    );
    const store = createFileUploadStore({ client: c.asClient });
    store.getState().enqueue([fileOf(6, "a.txt"), fileOf(2, "b.txt"), fileOf(2, "c.txt")], "", scope);
    await vi.waitFor(() => expect(c.client.uploadFileChunk).toHaveBeenCalled());
    store.getState().cancel(store.getState().items[0].id);
    await vi.waitFor(() => expect(statusOf(store)).toEqual(["cancelled", "done", "done"]));
    expect(c.log).toContain("abort up-1");

    store.getState().dismiss(store.getState().items[1].id);
    expect(store.getState().items.map((item) => item.name)).toEqual(["a.txt", "c.txt"]);
    store.getState().clearFinished();
    expect(store.getState().items).toEqual([]);
  });
});
