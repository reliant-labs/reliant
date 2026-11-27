// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ImagePreviewModal } from "./ImagePreviewModal";

vi.mock("../../hooks/useFocusManager", () => ({ focusChatInput: vi.fn() }));

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("ImagePreviewModal clipboard conversion", () => {
  it.each(["image/jpeg", "image/webp"])("copies %s directly from attachment bytes as PNG", async (type) => {
    const blob = new Blob(["attachment"], { type });
    const png = new Blob(["converted"], { type: "image/png" });
    const bitmap = { width: 32, height: 24, close: vi.fn() };
    const decode = vi.fn().mockResolvedValue(bitmap);
    vi.stubGlobal("createImageBitmap", decode);
    // A URL-based decode must not be needed (img-src may disallow blob:).
    vi.stubGlobal("Image", vi.fn(() => { throw new Error("URL decode blocked"); }));
    const drawImage = vi.fn();
    vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue({ drawImage } as unknown as CanvasRenderingContext2D);
    const encode = vi.spyOn(HTMLCanvasElement.prototype, "toBlob").mockImplementation((callback) => callback(png));
    let clipboardData: Record<string, Blob | Promise<Blob>> = {};
    vi.stubGlobal("ClipboardItem", class {
      constructor(data: Record<string, Blob | Promise<Blob>>) {
        clipboardData = data;
      }
    });
    const write = vi.fn(async () => { await clipboardData["image/png"]; });
    const clipboardNavigator = Object.create(navigator);
    Object.defineProperty(clipboardNavigator, "clipboard", { value: { write } });
    vi.stubGlobal("navigator", clipboardNavigator);

    render(<ImagePreviewModal isOpen onClose={vi.fn()} imageUrl="blob:attachment" filename="photo" imageBlob={blob} />);
    fireEvent.click(screen.getByRole("button", { name: "Copy image" }));

    // Start the write during the click, before asynchronous conversion finishes.
    expect(write).toHaveBeenCalledTimes(1);
    expect(clipboardData["image/png"]).toBeInstanceOf(Promise);
    expect(await screen.findByRole("status")).toHaveProperty("textContent", "Image copied");
    expect(decode).toHaveBeenCalledWith(blob);
    expect(drawImage).toHaveBeenCalledWith(bitmap, 0, 0);
    expect(encode).toHaveBeenCalledWith(expect.any(Function), "image/png");
    expect(await clipboardData["image/png"]).toBe(png);
    expect(bitmap.close).toHaveBeenCalledTimes(1);
  });
});
