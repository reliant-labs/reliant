/**
 * User-uploaded attachment chips stay 32px.
 *
 * That size (h-8/w-8, down from h-20/w-20) was an explicit UX request, and it is
 * the constant someone reaches for when asked to "make the generated image
 * bigger". Making generated images large was done by adding a SEPARATE component
 * (MessageGeneratedImages) rather than by touching this size, and this test is
 * what makes that separation enforceable instead of a comment.
 *
 * It also pins that the two surfaces still share ONE byte-loading path, since
 * there is no HTTP route serving attachment bytes.
 */

import { render, screen, waitFor } from "@testing-library/react";
import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import { MessageAttachments } from "../MessageAttachments";
import { MessageGeneratedImages } from "../MessageGeneratedImages";
import type { Attachment } from "../../../types/chat";

const getAttachmentAsBlob = vi.fn(
  async () => new Blob(["png-bytes"], { type: "image/png" }),
);

vi.mock("../../../api/attachment-grpc", () => ({
  attachmentGrpc: {
    getAttachmentAsBlob: (id: string) => getAttachmentAsBlob(id),
  },
}));

const originalCreateObjectURL = URL.createObjectURL;
const originalRevokeObjectURL = URL.revokeObjectURL;

beforeAll(() => {
  // jsdom implements neither.
  Object.defineProperty(URL, "createObjectURL", {
    configurable: true,
    writable: true,
    value: vi.fn(() => "blob:attachment"),
  });
  Object.defineProperty(URL, "revokeObjectURL", {
    configurable: true,
    writable: true,
    value: vi.fn(),
  });
});

afterAll(() => {
  Object.defineProperty(URL, "createObjectURL", {
    configurable: true,
    writable: true,
    value: originalCreateObjectURL,
  });
  Object.defineProperty(URL, "revokeObjectURL", {
    configurable: true,
    writable: true,
    value: originalRevokeObjectURL,
  });
});

const uploaded = [
  {
    id: "att-upload-1",
    filename: "screenshot.png",
    size: BigInt(123),
    mimeType: "image/png",
    url: "/api/attachments/att-upload-1",
  },
] as unknown as Attachment[];

describe("user-uploaded attachment chips", () => {
  it("renders the uploaded image preview at the requested 32px size", async () => {
    render(<MessageAttachments attachments={uploaded} />);

    const image = await waitFor(() =>
      screen.getByAltText<HTMLImageElement>("screenshot.png"),
    );

    expect(image.className).toContain("h-8");
    expect(image.className).toContain("w-8");
  });

  it("loads its bytes over the gRPC attachment path", async () => {
    render(<MessageAttachments attachments={uploaded} />);

    await waitFor(() => screen.getByAltText("screenshot.png"));
    expect(getAttachmentAsBlob).toHaveBeenCalledWith("att-upload-1");
  });

  it("is visibly smaller than a generated image", async () => {
    // The contrast is the whole point of keeping two components: same bytes,
    // same loader, deliberately different presentation.
    const { unmount } = render(<MessageAttachments attachments={uploaded} />);
    const chip = await waitFor(() => screen.getByAltText("screenshot.png"));
    const chipClasses = chip.className;
    unmount();

    render(<MessageGeneratedImages attachments={uploaded} />);
    const generated = await waitFor(() =>
      screen.getByAltText("screenshot.png"),
    );

    expect(chipClasses).toContain("h-8");
    expect(generated.className).not.toContain("h-8");
    expect(generated.className).toContain("w-full");
  });
});
