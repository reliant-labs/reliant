import { fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Attachment } from "../../../types/chat";
import { MessageGeneratedImages } from "../MessageGeneratedImages";

vi.mock("../../../hooks/attachment-queries", () => ({
  fetchAttachmentBlob: async (id: string) => new Blob([id], { type: "image/png" }),
}));

const attachments = ["first", "second", "third"].map((id) => ({
  id,
  filename: `${id}.png`,
  mimeType: "image/png",
})) as Attachment[];

const share = vi.fn(async (_data: ShareData) => {});
const canShare = vi.fn(() => true);
const write = vi.fn(async (_items: ClipboardItem[]) => {});
const restoreProperties: (() => void)[] = [];

function setProperty(target: object, key: string, value: unknown) {
  const previous = Object.getOwnPropertyDescriptor(target, key);
  Object.defineProperty(target, key, { configurable: true, value });
  restoreProperties.push(() => {
    if (previous) Object.defineProperty(target, key, previous);
    else Reflect.deleteProperty(target, key);
  });
}

beforeEach(() => {
  let url = 0;
  setProperty(URL, "createObjectURL", () => `blob:attachment-${++url}`);
  setProperty(URL, "revokeObjectURL", vi.fn());
  setProperty(navigator, "share", share);
  setProperty(navigator, "canShare", canShare);
  setProperty(navigator, "clipboard", { write });
  setProperty(window, "ClipboardItem", class {
    readonly data: Record<string, Blob | Promise<Blob>>;

    constructor(data: Record<string, Blob | Promise<Blob>>) {
      this.data = data;
    }
  });
});

afterEach(() => {
  for (const restore of restoreProperties.splice(0).reverse()) restore();
  vi.clearAllMocks();
});

async function openImage(filename: string) {
  render(<MessageGeneratedImages attachments={attachments} />);
  fireEvent.click(await screen.findByRole("button", { name: filename }));
  return within(screen.getByRole("dialog", { name: filename }));
}

function swipe(image: HTMLElement, from: [number, number], to: [number, number]) {
  fireEvent.touchStart(image, { touches: [{ clientX: from[0], clientY: from[1] }] });
  fireEvent.touchEnd(image, { changedTouches: [{ clientX: to[0], clientY: to[1] }], touches: [] });
}

describe("presented image viewer", () => {
  it("starts at the tapped image and navigates horizontally without dismissing the viewer", async () => {
    const viewer = await openImage("second.png");
    expect(viewer.getByText("2 / 3")).toBeVisible();
    swipe(viewer.getByRole("img"), [130, 20], [120, 150]);
    expect(viewer.getByRole("img", { name: "second.png" })).toBeVisible();
    swipe(viewer.getByRole("img"), [130, 20], [20, 25]);
    expect(viewer.getByRole("img", { name: "third.png" })).toBeVisible();
    expect(viewer.getByRole("button", { name: "Next image" })).toBeDisabled();
    fireEvent.keyDown(window, { key: "ArrowLeft" });
    expect(viewer.getByRole("img", { name: "second.png" })).toBeVisible();
    fireEvent.click(viewer.getByRole("button", { name: "Previous image" }));
    expect(viewer.getByRole("img", { name: "first.png" })).toBeVisible();
    fireEvent.keyDown(window, { key: "Escape" });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "second.png" })).toHaveFocus();
  });

  it("shares the current file and copies its actual image bytes", async () => {
    const viewer = await openImage("first.png");
    fireEvent.click(viewer.getByRole("button", { name: "Next image" }));
    fireEvent.click(viewer.getByRole("button", { name: "Share image" }));
    const file = share.mock.calls[0]?.[0]?.files?.[0];
    expect(file).toBeInstanceOf(File);
    expect(file).toMatchObject({ name: "second.png", type: "image/png", size: 6 });

    fireEvent.click(viewer.getByRole("button", { name: "Copy image" }));
    const item = write.mock.calls[0]?.[0][0] as unknown as { data: Record<string, Blob> };
    expect(item.data["image/png"]).toMatchObject({ type: "image/png", size: 6 });
    expect(await viewer.findByText("Image copied")).toBeVisible();
  });

  it("leaves the viewer open when native sharing is canceled, and offers a fallback on failure", async () => {
    share.mockRejectedValueOnce(new DOMException("Canceled", "AbortError"));
    const viewer = await openImage("first.png");
    fireEvent.click(viewer.getByRole("button", { name: "Share image" }));
    await expect(share.mock.results[0]?.value).rejects.toMatchObject({ name: "AbortError" });
    expect(viewer.queryByRole("status")).not.toBeInTheDocument();
    share.mockRejectedValueOnce(new DOMException("Not allowed", "NotAllowedError"));
    fireEvent.click(viewer.getByRole("button", { name: "Share image" }));
    expect(await viewer.findByRole("status")).toHaveTextContent("Could not share image");
    expect(viewer.getByRole("link", { name: "Download image" })).toBeVisible();
  });

  it("offers a download when the browser has no image sharing or copying", async () => {
    setProperty(navigator, "share", undefined);
    setProperty(navigator, "clipboard", undefined);
    const viewer = await openImage("second.png");
    expect(viewer.queryByRole("button", { name: "Share image" })).not.toBeInTheDocument();
    expect(viewer.queryByRole("button", { name: "Copy image" })).not.toBeInTheDocument();
    expect(viewer.getByRole("link", { name: "Download image" })).toHaveAttribute("download", "second.png");
  });
});
