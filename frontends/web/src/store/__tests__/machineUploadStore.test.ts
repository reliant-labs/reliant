import { Code, ConnectError } from "@connectrpc/connect";
import { describe, expect, it, vi } from "vitest";
import { UploadConflictPolicy, UploadDestination } from "../../gen/reliant/v1/filesystem_pb";
import type { FileUploadClient } from "../../api/fileUpload";
import {
  createMachineUploadStore,
  goesToMachine,
  machineUploadContext,
  sanitizeUploadName,
  type MachineUpload,
} from "../machineUploadStore";

const target = {
  scope: { projectId: "project-1", worktreeId: "wt-1", chatId: "chat-1" },
  uploadsFolder: "chat-1",
};
const PLACED = "/home/me/.reliant/uploads/chat-1/report.xlsx";

function fileOf(size: number, name = "report.xlsx", type = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"): File {
  return new File([new Uint8Array(size).map((_, i) => i % 251)], name, { type });
}

// A fake machine that opens user uploads (unless told it predates them).
function fakeClient(opts: { serverEchoes?: boolean } = {}) {
  const begin = vi.fn(async (_req: Record<string, unknown>) => ({
    uploadId: "up-1",
    chunkSize: 4,
    path: PLACED,
    maxSize: 1n << 31n,
    destination: opts.serverEchoes === false ? UploadDestination.UNSPECIFIED : UploadDestination.USER_UPLOADS,
  }));
  const chunk = vi.fn(async (req: { offset: bigint; data: Uint8Array }) => ({
    bytesReceived: req.offset + BigInt(req.data.length),
  }));
  const commit = vi.fn(async () => ({ path: PLACED, absolutePath: PLACED, size: 10n, created: true }));
  const abort = vi.fn(async (_req: { uploadId: string }) => ({}));
  const client = {
    beginFileUpload: begin,
    uploadFileChunk: chunk,
    commitFileUpload: commit,
    abortFileUpload: abort,
    getFileUploadStatus: vi.fn(async () => ({ exists: false, bytesReceived: 0n, size: 0n })),
  } as unknown as FileUploadClient;
  return { client, begin, chunk, commit, abort };
}

async function settled(store: ReturnType<typeof createMachineUploadStore>): Promise<MachineUpload> {
  await vi.waitFor(() => expect(store.getState().uploads[0]?.status).not.toBe("uploading"));
  return store.getState().uploads[0];
}

describe("machine uploads from the chat composer", () => {
  it("sends the file to the machine's uploads folder and records its absolute path", async () => {
    const fake = fakeClient();
    const store = createMachineUploadStore({ client: fake.client });
    store.getState().add("chat-1", [fileOf(10)], target);

    const done = await settled(store);
    expect(done).toMatchObject({ status: "done", path: PLACED, name: "report.xlsx", bytesReceived: 10 });
    expect(fake.begin).toHaveBeenCalledWith(
      expect.objectContaining({
        ...target.scope,
        filename: "report.xlsx",
        size: 10n,
        destination: UploadDestination.USER_UPLOADS,
        uploadsFolder: "chat-1",
        onConflict: UploadConflictPolicy.RENAME,
      }),
      expect.anything(),
    );
    expect(fake.chunk).toHaveBeenCalledTimes(3);
    expect(machineUploadContext(store.getState().uploads)).toBe(
      `User uploaded file: ${PLACED} (report.xlsx, 10 B, application/vnd.openxmlformats-officedocument.spreadsheetml.sheet)`,
    );

    store.getState().clearDone("chat-1");
    expect(store.getState().uploads).toEqual([]);
  });

  it("aborts the upload when its chip is removed", async () => {
    const fake = fakeClient();
    let release!: () => void;
    fake.chunk.mockImplementationOnce(
      (req) => new Promise((resolve) => (release = () => resolve({ bytesReceived: req.offset + BigInt(req.data.length) }))),
    );
    const store = createMachineUploadStore({ client: fake.client });
    store.getState().add("chat-1", [fileOf(10)], target);
    await vi.waitFor(() => expect(fake.chunk).toHaveBeenCalledTimes(1));

    store.getState().remove(store.getState().uploads[0].id);
    release();
    await vi.waitFor(() => expect(fake.abort).toHaveBeenCalledWith(expect.objectContaining({ uploadId: "up-1" })));
    expect(store.getState().uploads).toEqual([]);
    expect(fake.commit).not.toHaveBeenCalled();
  });

  it("discards a workspace session an older server opened, before a byte is sent", async () => {
    const fake = fakeClient({ serverEchoes: false });
    const onFailed = vi.fn();
    const store = createMachineUploadStore({ client: fake.client, onFailed });
    store.getState().add("chat-1", [fileOf(10)], target);

    const failed = await settled(store);
    expect(failed.status).toBe("failed");
    expect(failed.error).toContain('File type ".xlsx" is not supported');
    expect(failed.error).toContain("Update Reliant");
    expect(fake.abort).toHaveBeenCalledWith(expect.objectContaining({ uploadId: "up-1" }));
    expect(fake.chunk).not.toHaveBeenCalled();
    expect(onFailed).toHaveBeenCalledTimes(1);
  });

  it("tells the user to update the machine when its daemon predates the uploads folder", async () => {
    const fake = fakeClient();
    const err = new ConnectError("Update Reliant on laptop to send it files chat can't attach", Code.FailedPrecondition);
    // How the reason arrives: the x-forge-error-reason header.
    err.metadata.set("x-forge-error-reason", "machine_needs_update_for_uploads");
    fake.begin.mockRejectedValueOnce(err);
    const store = createMachineUploadStore({ client: fake.client });
    store.getState().add("chat-1", [fileOf(10, "data.zip", "application/zip")], target);

    const failed = await settled(store);
    expect(failed.error).toBe(
      'File type ".zip" is not supported. Supported: images (jpg, png, gif, webp) and text files (md, txt, json, code files, etc.) Update Reliant on laptop to send it files chat can\'t attach.',
    );
  });
});

describe("routing and names", () => {
  it("sends unsupported types and oversized files to the machine", () => {
    expect(goesToMachine({ name: "report.xlsx", size: 10 })).toBe(true);
    expect(goesToMachine({ name: "archive.zip", size: 10 })).toBe(true);
    expect(goesToMachine({ name: "big.csv", size: 10 * 1024 * 1024 + 1 })).toBe(true);
    expect(goesToMachine({ name: "small.csv", size: 100 })).toBe(false);
    expect(goesToMachine({ name: "photo.png", size: 100 })).toBe(false);
  });

  it("makes a file name one plain path element", () => {
    expect(sanitizeUploadName("report.xlsx")).toBe("report.xlsx");
    expect(sanitizeUploadName("../../etc/passwd")).toBe(".._.._etc_passwd");
    expect(sanitizeUploadName("a\\b\u0000c\n.txt")).toBe("a_b_c_.txt");
    expect(sanitizeUploadName("..")).toBe("upload");
    expect(sanitizeUploadName("   ")).toBe("upload");
    const long = sanitizeUploadName(`${"é".repeat(200)}.xlsx`);
    expect(new TextEncoder().encode(long).length).toBeLessThanOrEqual(255);
    expect(long.endsWith(".xlsx")).toBe(true);
  });
});
