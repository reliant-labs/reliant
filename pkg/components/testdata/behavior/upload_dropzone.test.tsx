// Behaviour of upload_dropzone in a real DOM (jsdom) against a fake
// XMLHttpRequest: validation before signing, the presigned PUT contract,
// per-file progress, failure and retry, and the concurrency bound. Run by
// TestComponentBehaviour.
import React, { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import UploadDropzone from "./upload_dropzone";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

/** Every request the component opens, newest last. */
let sent: FakeXHR[] = [];

class FakeXHR {
  method = "";
  url = "";
  headers: Record<string, string> = {};
  body: unknown = null;
  status = 0;
  aborted = false;
  upload: {
    onprogress: ((e: { loaded: number; total: number; lengthComputable: boolean }) => void) | null;
  } = {
    onprogress: null,
  };
  onload: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onabort: (() => void) | null = null;
  open(method: string, url: string) {
    this.method = method;
    this.url = url;
  }
  setRequestHeader(name: string, value: string) {
    this.headers[name] = value;
  }
  send(body: unknown) {
    this.body = body;
    sent.push(this);
  }
  abort() {
    this.aborted = true;
    this.onabort?.();
  }
  progress(loaded: number, total: number) {
    act(() => this.upload.onprogress?.({ loaded, total, lengthComputable: true }));
  }
  async respond(status: number) {
    this.status = status;
    await act(async () => {
      this.onload?.();
    });
  }
}

let host: HTMLDivElement;
let root: Root;
const realXHR = globalThis.XMLHttpRequest;

beforeEach(() => {
  sent = [];
  (globalThis as { XMLHttpRequest: unknown }).XMLHttpRequest = FakeXHR;
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  document.body.innerHTML = "";
  (globalThis as { XMLHttpRequest: unknown }).XMLHttpRequest = realXHR;
});

function render(ui: React.ReactElement) {
  act(() => root.render(ui));
}

const file = (name: string, size: number, type: string) =>
  new File([new Uint8Array(size)], name, { type });

/** Choose files through the hidden input, as the browse dialog would. */
async function choose(...files: File[]) {
  const input = document.querySelector('input[type="file"]') as HTMLInputElement;
  Object.defineProperty(input, "files", { value: files, configurable: true });
  await act(async () => {
    input.dispatchEvent(new Event("change", { bubbles: true }));
  });
}

const rows = () => [...document.querySelectorAll('[aria-label="Uploads"] li')];

test("accept and maxBytes reject before anything is signed", async () => {
  const getUploadUrl = vi.fn();
  render(<UploadDropzone getUploadUrl={getUploadUrl} accept="image/*,.pdf" maxBytes={1000} />);
  await choose(file("notes.txt", 10, "text/plain"), file("big.png", 5000, "image/png"));
  expect(getUploadUrl).not.toHaveBeenCalled();
  expect(rows()).toHaveLength(2);
  expect(rows()[0]?.textContent).toContain("isn't accepted");
  expect(rows()[1]?.textContent).toContain("over the 1000 B limit");
  expect(sent).toHaveLength(0);
});

test("an accepted file is signed, PUT with the target's headers, and reported with progress", async () => {
  const target = {
    url: "https://bucket.test/a.png?sig=1",
    headers: { "x-amz-acl": "private" },
    key: "a.png",
  };
  const getUploadUrl = vi.fn(async () => target);
  const onUploaded = vi.fn();
  render(<UploadDropzone getUploadUrl={getUploadUrl} onUploaded={onUploaded} accept=".png" />);
  const png = file("a.png", 200, "image/png");
  await choose(png);

  expect(getUploadUrl).toHaveBeenCalledWith(png);
  const xhr = sent[0] as FakeXHR;
  expect([xhr.method, xhr.url]).toEqual(["PUT", target.url]);
  expect(xhr.headers).toEqual({ "x-amz-acl": "private", "Content-Type": "image/png" });
  expect(xhr.body).toBe(png);

  xhr.progress(100, 200);
  expect(document.querySelector('[role="progressbar"]')?.getAttribute("aria-valuenow")).toBe("50");

  await xhr.respond(200);
  expect(onUploaded).toHaveBeenCalledWith(png, target);
  expect(rows()[0]?.textContent).toContain("Uploaded");
  expect(document.querySelector('[role="progressbar"]')).toBeNull();
});

test("a Content-Type the URL was signed with wins over the file's", async () => {
  render(
    <UploadDropzone
      getUploadUrl={async () => ({
        url: "https://b.test/x",
        headers: { "content-type": "image/webp" },
      })}
    />,
  );
  await choose(file("a.png", 10, "image/png"));
  expect(sent[0]?.headers).toEqual({ "content-type": "image/webp" });
});

test("a non-2xx response fails the file, and Retry signs and sends it again", async () => {
  const getUploadUrl = vi.fn(async () => ({ url: "https://b.test/x" }));
  const onError = vi.fn();
  render(<UploadDropzone getUploadUrl={getUploadUrl} onError={onError} />);
  await choose(file("a.png", 10, "image/png"));
  await (sent[0] as FakeXHR).respond(403);
  expect(onError).toHaveBeenCalledTimes(1);
  expect((onError.mock.calls[0]?.[1] as Error).message).toContain("HTTP 403");
  expect(rows()[0]?.textContent).toContain("HTTP 403");

  const retry = [...document.querySelectorAll("button")].find((b) => b.textContent === "Retry");
  await act(async () => retry?.click());
  expect(getUploadUrl).toHaveBeenCalledTimes(2);
  expect(sent).toHaveLength(2);
});

test("maxConcurrent bounds the uploads in flight", async () => {
  render(
    <UploadDropzone
      getUploadUrl={async (f) => ({ url: `https://b.test/${f.name}` })}
      maxConcurrent={1}
    />,
  );
  await choose(
    file("1.png", 1, "image/png"),
    file("2.png", 1, "image/png"),
    file("3.png", 1, "image/png"),
  );
  expect(sent.map((x) => x.url)).toEqual(["https://b.test/1.png"]);
  await (sent[0] as FakeXHR).respond(200);
  expect(sent.map((x) => x.url)).toEqual(["https://b.test/1.png", "https://b.test/2.png"]);
});

test("cancelling aborts the request and drops the row", async () => {
  const onError = vi.fn();
  render(
    <UploadDropzone getUploadUrl={async () => ({ url: "https://b.test/x" })} onError={onError} />,
  );
  await choose(file("a.png", 10, "image/png"));
  const cancel = document.querySelector('[aria-label="Cancel a.png"]') as HTMLButtonElement;
  await act(async () => cancel.click());
  expect(sent[0]?.aborted).toBe(true);
  expect(rows()).toHaveLength(0);
  expect(onError).not.toHaveBeenCalled();
});

test("dropping files onto the zone adds them", async () => {
  const getUploadUrl = vi.fn(async () => ({ url: "https://b.test/x" }));
  render(<UploadDropzone getUploadUrl={getUploadUrl} />);
  const zone = document.querySelector("button") as HTMLButtonElement;
  const drop = new Event("drop", { bubbles: true, cancelable: true });
  const png = file("a.png", 10, "image/png");
  Object.defineProperty(drop, "dataTransfer", { value: { files: [png], types: ["Files"] } });
  await act(async () => {
    zone.dispatchEvent(drop);
  });
  expect(getUploadUrl).toHaveBeenCalledWith(png);
});
