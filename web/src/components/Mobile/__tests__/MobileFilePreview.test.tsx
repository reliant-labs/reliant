import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { MobileFilePreview } from "../MobileFilePreview";

const getFilePreviewInfo = vi.fn();
const getFileContent = vi.fn();
const getFilePreviewBlob = vi.fn();

vi.mock("../../../api/fileSystem", () => ({
  getFilePreviewInfo: (...args: unknown[]) => getFilePreviewInfo(...args),
  getFileContent: (...args: unknown[]) => getFileContent(...args),
  getFilePreviewBlob: (...args: unknown[]) => getFilePreviewBlob(...args),
}));

beforeEach(() => {
  getFilePreviewInfo.mockReset();
  getFileContent.mockReset();
  getFilePreviewBlob.mockReset();
  // jsdom has no createObjectURL/revokeObjectURL by default.
  if (!URL.createObjectURL) {
    URL.createObjectURL = vi.fn(() => "blob:mock");
  }
  if (!URL.revokeObjectURL) {
    URL.revokeObjectURL = vi.fn();
  }
});

describe("MobileFilePreview", () => {
  it("renders text content via the lightweight code viewer", async () => {
    getFilePreviewInfo.mockResolvedValue({ viewerKind: "text" });
    getFileContent.mockResolvedValue("const x = 1;");

    render(<MobileFilePreview path="src/index.ts" worktreeId="wt-1" />);

    await waitFor(() => {
      expect(screen.getByText(/const/)).toBeInTheDocument();
    });
    expect(getFileContent).toHaveBeenCalledWith("src/index.ts", "wt-1");
  });

  it("renders an image via a blob URL", async () => {
    getFilePreviewInfo.mockResolvedValue({ viewerKind: "image" });
    getFilePreviewBlob.mockResolvedValue(new Blob());

    render(<MobileFilePreview path="assets/logo.png" worktreeId="wt-1" />);

    await waitFor(() => {
      expect(screen.getByRole("img")).toBeInTheDocument();
    });
  });

  it("shows an unsupported message for binary files", async () => {
    getFilePreviewInfo.mockResolvedValue({ viewerKind: "binary" });

    render(<MobileFilePreview path="dist/bundle.wasm" worktreeId="wt-1" />);

    await waitFor(() => {
      expect(screen.getByText(/Preview unavailable/)).toBeInTheDocument();
    });
  });

  it("shows an error message when loading fails", async () => {
    getFilePreviewInfo.mockRejectedValue(new Error("boom"));

    render(<MobileFilePreview path="src/index.ts" worktreeId="wt-1" />);

    await waitFor(() => {
      expect(screen.getByText("boom")).toBeInTheDocument();
    });
  });
});
