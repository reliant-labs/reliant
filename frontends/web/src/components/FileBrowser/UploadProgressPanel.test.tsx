import { fireEvent, render, screen, within } from "@testing-library/react";
import { useFileDownloadStore, type DownloadItem } from "../../store/fileDownloadStore";
import { useFileUploadStore, type UploadItem } from "../../store/fileUploadStore";
import { UploadProgressPanel } from "./UploadProgressPanel";

const upload: UploadItem = {
  id: "upload-1", name: "notes.txt", directory: "docs", size: 10, bytesReceived: 5, status: "uploading",
  projectId: "project-1",
};

function download(changes: Partial<DownloadItem>): DownloadItem {
  return { id: "download-1", name: "docs.zip", kind: "folder", status: "preparing", bytesDone: 0, totalBytes: 0, ...changes };
}

beforeEach(() => {
  useFileUploadStore.setState({ items: [] });
  useFileDownloadStore.setState({ items: [] });
});

describe("UploadProgressPanel with downloads", () => {
  it("keeps the upload-only wording when nothing is downloading", () => {
    useFileUploadStore.setState({ items: [upload] });
    render(<UploadProgressPanel />);
    expect(screen.getByRole("heading", { name: "Uploading 1 file" })).toBeInTheDocument();
    expect(screen.getByTestId("upload-panel")).toHaveAccessibleName("Uploads");
  });

  it("shows a folder's zipping progress and cancels it", () => {
    const cancel = vi.fn();
    useFileDownloadStore.setState({ items: [download({ filesDone: 3, filesTotal: 12 })], cancel });
    render(<UploadProgressPanel />);

    expect(screen.getByRole("heading", { name: "Downloading 1 file" })).toBeInTheDocument();
    const row = screen.getByTestId("download-row");
    expect(within(row).getByText("Zipping… 3 of 12 files")).toBeInTheDocument();
    expect(within(row).getByRole("progressbar", { name: "Zipping docs.zip" })).toHaveAttribute("aria-valuenow", "25");
    fireEvent.click(within(row).getByRole("button", { name: "Cancel download of docs.zip" }));
    expect(cancel).toHaveBeenCalledWith("download-1");
  });

  it("lists uploads and downloads together under one heading", () => {
    useFileUploadStore.setState({ items: [upload] });
    useFileDownloadStore.setState({
      items: [download({ name: "movie.mov", kind: "file", status: "downloading", bytesDone: 50_000_000, totalBytes: 200_000_000 })],
    });
    render(<UploadProgressPanel />);
    expect(screen.getByRole("heading", { name: "Transferring 2 files" })).toBeInTheDocument();
    expect(screen.getByText("25% · 50 MB of 200 MB")).toBeInTheDocument();
  });

  it("says how many items a finished archive skipped, and dismisses the row", () => {
    const dismiss = vi.fn();
    useFileDownloadStore.setState({
      items: [download({ status: "done", bytesDone: 2_000, totalBytes: 2_000, skipped: 3 })],
      dismiss,
    });
    render(<UploadProgressPanel />);
    expect(screen.getByRole("heading", { name: "Downloads" })).toBeInTheDocument();
    expect(screen.getByText("Downloaded · 3 items skipped")).toBeInTheDocument();
    expect(screen.queryByRole("progressbar")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Dismiss docs.zip" }));
    expect(dismiss).toHaveBeenCalledWith("download-1");
  });

  it("shows why a download failed", () => {
    useFileDownloadStore.setState({
      items: [download({ status: "failed", error: "Update Reliant on laptop to download folders" })],
    });
    render(<UploadProgressPanel />);
    expect(screen.getByText("Update Reliant on laptop to download folders")).toBeInTheDocument();
  });

  it("clears finished uploads and downloads together", () => {
    useFileUploadStore.setState({ items: [{ ...upload, status: "done", bytesReceived: 10 }] });
    useFileDownloadStore.setState({ items: [download({ status: "cancelled" })] });
    render(<UploadProgressPanel />);
    fireEvent.click(screen.getByRole("button", { name: "Clear finished" }));
    expect(useFileUploadStore.getState().items).toEqual([]);
    expect(useFileDownloadStore.getState().items).toEqual([]);
  });
});
