import { act, renderHook } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { useComposerUploads } from "../useComposerUploads";

describe("composer uploads", () => {
  it("cancels unfinished work on unmount, removes late success with its original callback, and skips queued files", async () => {
    let resolve!: (value: { id: string }) => void;
    // Only the first upload is deferred. If cleanup regresses and starts the
    // queued file, it must settle so assertions report the extra call instead
    // of deadlocking act and poisoning every subsequent renderHook.
    const attach = vi.fn().mockResolvedValue({ id: "unexpected-queued" }).mockImplementationOnce(() => new Promise<{ id: string }>((r) => { resolve = r; }));
    const originalRemove = vi.fn();
    const replacementRemove = vi.fn();
    const { result, rerender, unmount } = renderHook(({ remove }) => useComposerUploads(attach, remove, "draft"), { initialProps: { remove: originalRemove } });
    let upload!: Promise<void>;
    act(() => { upload = result.current.add("draft", [new File(["a"], "a.txt"), new File(["b"], "b.txt")]); });
    rerender({ remove: replacementRemove });
    unmount();
    await act(async () => { resolve({ id: "late" }); await upload; });
    expect(originalRemove).toHaveBeenCalledWith("draft", "late");
    expect(replacementRemove).not.toHaveBeenCalled();
    expect(attach).toHaveBeenCalledTimes(1);
  });

  it("isolates failure rows and retry/cancel operations when the active draft changes", async () => {
    const attach = vi.fn().mockRejectedValueOnce(new Error("offline")).mockResolvedValue({ id: "done" });
    const remove = vi.fn();
    const { result, rerender } = renderHook(({ session }) => useComposerUploads(attach, remove, session), { initialProps: { session: "a" } });
    await act(() => result.current.add("a", [new File(["a"], "a.txt")]));
    const id = result.current.uploads[0].id;
    const stale = result.current;
    rerender({ session: "b" });
    expect(result.current.uploads).toEqual([]);
    expect(result.current.hasPending()).toBe(false);
    await act(async () => { stale.cancel(id); await stale.retry(id); await stale.add("a", [new File(["x"], "x.txt")]); });
    expect(attach).toHaveBeenCalledTimes(1);
    rerender({ session: "a" });
    expect(result.current.uploads[0]).toMatchObject({ id, status: "failed" });
    expect(result.current.hasPending()).toBe(true);
    await act(() => result.current.retry(id));
    expect(attach).toHaveBeenCalledTimes(2);
    expect(remove).not.toHaveBeenCalled();
  });

  it("allows an old draft upload to finish without blocking or attaching to the new draft", async () => {
    let resolve!: (value: { id: string }) => void;
    const attach = vi.fn(() => new Promise<{ id: string }>((r) => { resolve = r; }));
    const remove = vi.fn();
    const { result, rerender, unmount } = renderHook(({ session }) => useComposerUploads(attach, remove, session), { initialProps: { session: "a" } });
    let upload!: Promise<void>;
    act(() => { upload = result.current.add("a", [new File(["a"], "a.txt")]); });
    rerender({ session: "b" });
    expect(result.current.hasPending()).toBe(false);
    expect(result.current.uploads).toEqual([]);
    await act(async () => { resolve({ id: "kept-in-a" }); await upload; });
    expect(attach).toHaveBeenCalledWith("a", expect.any(File));
    unmount();
    expect(remove).not.toHaveBeenCalled();
  });
  it("keeps failed files and retries the same file", async () => {
    const file = new File(["hi"], "note.txt");
    const attach = vi.fn().mockRejectedValueOnce(new Error("offline")).mockResolvedValue({ id: "a" });
    const { result } = renderHook(() => useComposerUploads(attach, vi.fn(), "draft"));
    await act(() => result.current.add("draft", [file]));
    expect(result.current.uploads[0]).toMatchObject({ file, status: "failed" });
    expect(result.current.hasPending()).toBe(true);
    await act(() => result.current.retry(result.current.uploads[0].id));
    expect(attach).toHaveBeenLastCalledWith("draft", file);
    expect(result.current.uploads).toEqual([]);
    expect(result.current.hasPending()).toBe(false);
  });
  it("discards a late upload after cancel", async () => {
    let resolve!: (value: { id: string }) => void;
    const attach = vi.fn(() => new Promise<{ id: string }>((r) => { resolve = r; }));
    const remove = vi.fn();
    const { result } = renderHook(() => useComposerUploads(attach, remove, "draft"));
    let upload!: Promise<void>;
    act(() => { upload = result.current.add("draft", [new File(["hi"], "a.txt")]); });
    act(() => result.current.cancel(result.current.uploads[0].id));
    await act(async () => { resolve({ id: "late" }); await upload; });
    expect(remove).toHaveBeenCalledWith("draft", "late");
    expect(result.current.uploads).toEqual([]);
  });
});
