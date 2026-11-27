import { SAVE_BLOB_URL_TTL_MS, saveBlob } from "./saveBlob";

describe("saveBlob", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    URL.createObjectURL = vi.fn(() => "blob:saved");
    URL.revokeObjectURL = vi.fn();
  });
  afterEach(() => vi.useRealTimers());

  it("clicks a download link named after the file, then releases the URL once the download has begun", () => {
    const clicked: HTMLAnchorElement[] = [];
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
      clicked.push(this);
    });
    const blob = new Blob(["x"], { type: "application/zip" });

    saveBlob(blob, "archive.zip");

    expect(URL.createObjectURL).toHaveBeenCalledWith(blob);
    expect(clicked).toHaveLength(1);
    expect(clicked[0].download).toBe("archive.zip");
    expect(clicked[0].href).toBe("blob:saved");
    expect(document.body.contains(clicked[0])).toBe(false);
    // Not revoked in the click's turn: Electron's Save dialog reads it later.
    expect(URL.revokeObjectURL).not.toHaveBeenCalled();
    vi.advanceTimersByTime(SAVE_BLOB_URL_TTL_MS);
    expect(URL.revokeObjectURL).toHaveBeenCalledWith("blob:saved");
    click.mockRestore();
  });
});
