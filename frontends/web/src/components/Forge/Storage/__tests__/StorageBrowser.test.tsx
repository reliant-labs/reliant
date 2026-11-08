// Copyright (c) 2025 Reliant Labs

/**
 * The storage browser against the fixture buckets: which bucket opens, one
 * level listed folders first, the breadcrumb (odd keys included), paging by
 * opaque token, the once-per-tab open_bucket, the object pane's metadata and
 * previews, and Download — minted on the click, handed to the browser, never
 * shown or fetched ahead.
 */

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";

import { createStorageFixtureApi } from "../__fixtures__/fixtureStorageApi";
import { callsOf, renderBrowser, searchOf } from "./storageTestUtils";

vi.mock("@tanstack/react-router", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-router")>()),
  useNavigate: () => vi.fn(),
}));

afterEach(() => window.sessionStorage.clear());

/** Every listed entry, in the order shown: `p:` a folder, `o:` an object. */
function entries(): string[] {
  return Array.from(document.querySelectorAll<HTMLElement>('[data-testid="storage-folder"], [data-testid="storage-object"]')).map((row) =>
    row.dataset.testid === "storage-folder" ? `p:${row.dataset.prefix}` : `o:${row.dataset.key}`
  );
}

const listed = () => screen.findByTestId("storage-table");

describe("the bucket", () => {
  it("shows loading until bucket discovery resolves, then shows the empty state", async () => {
    const api = createStorageFixtureApi({ noBuckets: true });
    const discover = api.listBrowsableBuckets;
    let release!: () => void;
    const pending = new Promise<void>((resolve) => { release = resolve; });
    api.listBrowsableBuckets = async (...args) => {
      await pending;
      return discover(...args);
    };
    renderBrowser(api);
    expect(screen.getByTestId("storage-buckets-loading")).toHaveAttribute("aria-busy", "true");
    expect(screen.queryByTestId("storage-table")).not.toBeInTheDocument();
    release();
    expect(await screen.findByTestId("storage-no-buckets")).toBeVisible();
    expect(screen.queryByTestId("storage-buckets-loading")).not.toBeInTheDocument();
  });

  it("opens the first readable bucket on a bare link, and shows an unreachable one disabled with its reason", async () => {
    const view = renderBrowser(createStorageFixtureApi());
    await waitFor(() => expect(view.search().bucket).toBe("exports"));
    expect(view.navigations[0]).toEqual({ search: { bucket: "exports" }, replace: true });
    const buckets = screen.getByTestId("storage-buckets");
    expect(within(buckets).getByTestId("storage-bucket-media")).toBeDisabled();
    expect(within(buckets).getByTestId("storage-bucket-uploads")).toHaveAttribute("aria-checked", "false");
    expect(screen.getByTestId("storage-bucket-unreachable")).toHaveTextContent("media can't be browsed: self-managed MinIO: not reachable from the control plane.");
  });

  it("opens a bucket this environment does not declare at the default bucket's root", async () => {
    const api = createStorageFixtureApi();
    const view = renderBrowser(api, { bucket: "gone", prefix: "a/", object: "a/b" });
    await waitFor(() => expect(view.search()).toEqual({ bucket: "exports" }));
    await listed();
    expect(callsOf(api, "getObjectMetadata")).toEqual([]);
    expect(callsOf(api, "listObjects").every((call) => call.prefix === "")).toBe(true);
  });

  it("switches bucket from the selector, dropping the level", async () => {
    const view = renderBrowser(createStorageFixtureApi(), { bucket: "exports", prefix: "daily/" });
    await listed();
    fireEvent.click(screen.getByTestId("storage-bucket-uploads"));
    expect(view.search()).toEqual({ bucket: "uploads" });
  });

  it("says a chosen bucket cannot be browsed, and reads nothing", async () => {
    const api = createStorageFixtureApi();
    renderBrowser(api, { bucket: "media" });
    expect(await screen.findByTestId("storage-unreachable")).toHaveTextContent("media can't be browsed from Reliant");
    expect(callsOf(api, "listObjects")).toHaveLength(0);
  });
});

describe("a level", () => {
  it("lists folders first, then the objects directly here, with size, time and etag", async () => {
    renderBrowser(createStorageFixtureApi(), { bucket: "uploads" });
    await listed();
    expect(entries()).toEqual(["p:avatars/", "p:blobs/", "p:docs/", "p:odd names/", "p:reports/", "o:robots.txt"]);
    const robots = screen.getByTestId("storage-object");
    expect(robots).toHaveTextContent("robots.txt");
    expect(robots).toHaveTextContent("26 B");
    expect(within(robots).getByTitle(/^"[0-9a-f]+"$/)).toBeInTheDocument();
  });

  it("lists one level only, and links each folder to its own", async () => {
    const view = renderBrowser(createStorageFixtureApi(), { bucket: "uploads", prefix: "avatars/" });
    await listed();
    expect(entries()).toEqual(["p:avatars/2026/", "o:avatars/legacy.jpg"]);
    const folder = within(screen.getByTestId("storage-folder")).getByRole("link");
    expect(folder).toHaveTextContent("2026/");
    expect(searchOf(folder.getAttribute("href"))).toEqual({ bucket: "uploads", prefix: "avatars/2026/" });
    view.go(searchOf(folder.getAttribute("href")));
    await waitFor(() => expect(entries()).toEqual(["o:avatars/2026/ada.png", "o:avatars/2026/grace.webp", "o:avatars/2026/logo.svg"]));
  });

  it("names keys with odd characters as stored, and links them so they come back the same", async () => {
    renderBrowser(createStorageFixtureApi(), { bucket: "uploads", prefix: "odd names/" });
    await listed();
    expect(entries()).toEqual(["p:odd names/a/", "o:odd names/  leading spaces.txt", "o:odd names/100% done #1?.txt", "o:odd names/ünïcødé ✓.md"]);
    const odd = screen.getAllByTestId("storage-object").find((row) => row.dataset.key === "odd names/100% done #1?.txt")!;
    const link = within(odd).getByRole("link");
    expect(link).toHaveTextContent("100% done #1?.txt");
    expect(searchOf(link.getAttribute("href"))).toEqual({ bucket: "uploads", prefix: "odd names/", object: "odd names/100% done #1?.txt" });
  });

  it("draws the breadcrumb from the prefix, an empty segment included, and links each crumb", async () => {
    const view = renderBrowser(createStorageFixtureApi(), { bucket: "uploads", prefix: "odd names/a//" });
    await listed();
    expect(entries()).toEqual(["o:odd names/a//b.txt"]);
    const crumbs = screen.getAllByTestId("storage-crumb");
    expect(crumbs.map((crumb) => crumb.textContent)).toEqual(["odd names", "a", "(empty)"]);
    expect(crumbs[2]).toHaveAttribute("aria-current", "page");
    expect(searchOf(crumbs[1]!.getAttribute("href"))).toEqual({ bucket: "uploads", prefix: "odd names/a/" });
    expect(searchOf(screen.getByTestId("storage-crumb-root").getAttribute("href"))).toEqual({ bucket: "uploads" });
    view.go(searchOf(crumbs[0]!.getAttribute("href")));
    await waitFor(() => expect(entries()[0]).toBe("p:odd names/a/"));
  });

  it("narrows the level by a typed prefix, which the server lists", async () => {
    const api = createStorageFixtureApi();
    const view = renderBrowser(api, { bucket: "uploads", prefix: "docs/" });
    await listed();
    fireEvent.change(screen.getByTestId("storage-narrow-input"), { target: { value: "b" } });
    fireEvent.submit(screen.getByTestId("storage-narrow"));
    expect(view.search()).toEqual({ bucket: "uploads", prefix: "docs/b" });
    await waitFor(() => expect(entries()).toEqual(["o:docs/broken.json"]));
    expect(callsOf(api, "listObjects").at(-1)).toMatchObject({ prefix: "docs/b" });
    expect(screen.getAllByTestId("storage-crumb").at(-1)).toHaveTextContent("b…");
    fireEvent.click(screen.getByTestId("storage-narrow-clear"));
    expect(view.search()).toEqual({ bucket: "uploads", prefix: "docs/" });
  });

  it("says what an empty narrowing means", async () => {
    renderBrowser(createStorageFixtureApi(), { bucket: "uploads", prefix: "docs/zz" });
    expect(await screen.findByTestId("storage-empty")).toHaveTextContent("Nothing in docs/ starts with “zz”");
  });
});

describe("paging", () => {
  it("pages forward by token and back through the pages it walked", async () => {
    const api = createStorageFixtureApi();
    const view = renderBrowser(api, { bucket: "exports", prefix: "daily/" });
    await listed();
    expect(screen.getAllByTestId("storage-object")).toHaveLength(200);
    expect(screen.getByTestId("storage-page")).toHaveTextContent("Page 1");
    expect(screen.getByTestId("storage-page-prev")).toBeDisabled();

    fireEvent.click(screen.getByTestId("storage-page-next"));
    const second = view.search().token!;
    expect(second).toBeTruthy();
    await waitFor(() => expect(screen.getAllByTestId("storage-object")[0]).toHaveAttribute("data-key", "daily/2026-201.csv"));
    expect(screen.getByTestId("storage-page")).toHaveTextContent("Page 2");

    await waitFor(() => expect(screen.getByTestId("storage-page-next")).toBeEnabled());
    fireEvent.click(screen.getByTestId("storage-page-next"));
    await waitFor(() => expect(screen.getByTestId("storage-page")).toHaveTextContent("Page 3 (the last)"));
    expect(screen.getAllByTestId("storage-object")).toHaveLength(50);
    expect(screen.getByTestId("storage-page-next")).toBeDisabled();

    fireEvent.click(screen.getByTestId("storage-page-prev"));
    expect(view.search().token).toBe(second);
    await waitFor(() => expect(screen.getByTestId("storage-page")).toHaveTextContent("Page 2"));
    fireEvent.click(screen.getByTestId("storage-page-prev"));
    expect(view.search()).toEqual({ bucket: "exports", prefix: "daily/" });
    // Going back reads the pages already read (each is an audit row): none is listed twice.
    expect(callsOf(api, "listObjects").map((call) => call.pageToken)).toEqual(["", second, expect.any(String)]);
  });

  it("opens a deep link to a later page with a way to the first", async () => {
    const api = createStorageFixtureApi();
    const first = renderBrowser(api, { bucket: "exports", prefix: "daily/" });
    await listed();
    fireEvent.click(screen.getByTestId("storage-page-next"));
    const token = first.search().token!;
    first.unmount();

    const view = renderBrowser(api, { bucket: "exports", prefix: "daily/", token });
    await listed();
    expect(screen.getByTestId("storage-page")).toHaveTextContent("A later page");
    expect(screen.getByTestId("storage-page-prev")).toBeDisabled();
    fireEvent.click(screen.getByTestId("storage-page-first"));
    expect(view.search()).toEqual({ bucket: "exports", prefix: "daily/" });
  });

  it("opens the first page, and says why, when a page link belongs to another bucket or level", async () => {
    const view = renderBrowser(createStorageFixtureApi(), { bucket: "uploads", prefix: "docs/", token: "not-one-of-ours" });
    await waitFor(() => expect(view.search()).toEqual({ bucket: "uploads", prefix: "docs/" }));
    expect(view.navigations.at(-1)).toMatchObject({ replace: true });
    await listed();
    expect(screen.getByTestId("storage-token-dropped")).toBeInTheDocument();
  });
});

describe("open_bucket", () => {
  it("is sent on this tab's first listing of a bucket only, whatever the level", async () => {
    const api = createStorageFixtureApi();
    const view = renderBrowser(api, { bucket: "uploads", prefix: "docs/" });
    await listed();
    view.go({ bucket: "uploads", prefix: "avatars/" });
    await waitFor(() => expect(callsOf(api, "listObjects")).toHaveLength(2));
    view.go({ bucket: "exports" });
    await waitFor(() => expect(callsOf(api, "listObjects")).toHaveLength(3));
    expect(callsOf(api, "listObjects").map((call) => [call.bucket, call.prefix, call.openBucket])).toEqual([
      ["uploads", "docs/", true],
      ["uploads", "avatars/", false],
      ["exports", "", true],
    ]);
    view.unmount();

    // A reload in the same tab is not a second open.
    const again = createStorageFixtureApi();
    renderBrowser(again, { bucket: "uploads" });
    await listed();
    expect(callsOf(again, "listObjects")[0]).toMatchObject({ openBucket: false });
  });
});

describe("an object", () => {
  it("shows its metadata, and its JSON pretty-printed", async () => {
    const api = createStorageFixtureApi();
    renderBrowser(api, { bucket: "uploads", prefix: "docs/", object: "docs/config.json" });
    const pane = await screen.findByTestId("storage-object-pane");
    const metadata = await within(pane).findByTestId("storage-metadata");
    expect(metadata).toHaveTextContent("application/json");
    expect(metadata).toHaveTextContent("57 B");
    expect(metadata).toHaveTextContent("(57 bytes)");
    expect(within(pane).getByTestId("storage-user-metadata")).toHaveTextContent("cache-controlno-store");
    const text = await within(pane).findByTestId("storage-preview-text");
    expect(text).toHaveAttribute("data-pretty", "true");
    expect(text.textContent).toContain('"flags": [\n      "checkout-v2"');
    expect(screen.getAllByTestId("storage-object").find((row) => row.dataset.key === "docs/config.json")!.querySelector("a")).toHaveAttribute("aria-current", "true");
  });

  it("draws an image through the preview's URL in an <img>, never a frame", async () => {
    renderBrowser(createStorageFixtureApi(), { bucket: "uploads", prefix: "avatars/2026/", object: "avatars/2026/ada.png" });
    const image = await screen.findByTestId("storage-preview-image");
    expect(image.tagName).toBe("IMG");
    expect(image).toHaveAttribute("src", expect.stringMatching(/^data:image\/png;base64,/));
    expect(image).toHaveAttribute("alt", "ada.png");
    expect(document.querySelector("iframe, object, embed")).toBeNull();
  });

  it("offers a new image URL when the old one stops loading", async () => {
    const api = createStorageFixtureApi();
    renderBrowser(api, { bucket: "uploads", object: "avatars/2026/ada.png" });
    fireEvent.error(await screen.findByTestId("storage-preview-image"));
    const failed = await screen.findByTestId("storage-preview-image-failed");
    fireEvent.click(within(failed).getByRole("button", { name: /load again/i }));
    await waitFor(() => expect(callsOf(api, "previewObject")).toHaveLength(2));
  });

  it("previews no SVG and no PDF, and asks for neither", async () => {
    const api = createStorageFixtureApi();
    const view = renderBrowser(api, { bucket: "uploads", object: "avatars/2026/logo.svg" });
    expect(await screen.findByTestId("storage-preview-none")).toHaveTextContent("image/svg+xml");
    view.go({ bucket: "uploads", object: "reports/q1.pdf" });
    expect(await screen.findByTestId("storage-preview-none")).toHaveTextContent("application/pdf");
    expect(screen.getByTestId("storage-download")).toBeEnabled();
    expect(callsOf(api, "previewObject")).toHaveLength(0);
  });

  it("says when a text preview was cut at 1 MiB", async () => {
    renderBrowser(createStorageFixtureApi({ truncatedText: true }), { bucket: "uploads", object: "docs/server.log" });
    expect(await screen.findByTestId("storage-preview-note")).toHaveTextContent("Cut at 1 MiB: the first 1 MiB of 3 MiB is shown.");
    expect(screen.getByTestId("storage-preview-text")).toHaveAttribute("data-pretty", "false");
  });

  it("shows JSON that does not parse as stored", async () => {
    renderBrowser(createStorageFixtureApi(), { bucket: "uploads", object: "docs/broken.json" });
    expect(await screen.findByTestId("storage-preview-note")).toHaveTextContent("Not valid JSON");
    expect(screen.getByTestId("storage-preview-text")).toHaveTextContent('{"feature": [1, 2,, }');
  });

  it("says an object is not there", async () => {
    renderBrowser(createStorageFixtureApi(), { bucket: "uploads", object: "docs/deleted.txt" });
    const error = await screen.findByTestId("storage-metadata-error");
    expect(error).toHaveAttribute("data-kind", "not-found");
    expect(error).toHaveTextContent("deleted.txt isn't there");
  });

  it("closes back to the level", async () => {
    renderBrowser(createStorageFixtureApi(), { bucket: "uploads", prefix: "docs/", object: "docs/readme.txt" });
    const close = await screen.findByTestId("storage-object-close");
    expect(searchOf(close.getAttribute("href"))).toEqual({ bucket: "uploads", prefix: "docs/" });
  });
});

describe("Download", () => {
  it("mints a URL on each click and hands it to the browser, never ahead of the click and never on the page", async () => {
    const api = createStorageFixtureApi();
    const view = renderBrowser(api, { bucket: "uploads", prefix: "docs/", object: "docs/readme.txt" });
    await screen.findByTestId("storage-metadata");
    await screen.findByTestId("storage-preview-text");
    expect(callsOf(api, "presignObjectDownload")).toHaveLength(0);

    fireEvent.click(screen.getByTestId("storage-download"));
    await waitFor(() => expect(view.downloads).toHaveLength(1));
    expect(view.downloads[0]).toMatch(/^https:\/\/storage\.fixture\.test\/uploads\/docs%2Freadme\.txt\?X-Goog-Signature=fixture-1$/);
    expect(callsOf(api, "presignObjectDownload")[0]).toEqual({ environmentId: "env-staging", bucket: "uploads", key: "docs/readme.txt" });

    await waitFor(() => expect(screen.getByTestId("storage-download")).toBeEnabled());
    fireEvent.click(screen.getByTestId("storage-download"));
    await waitFor(() => expect(view.downloads).toHaveLength(2));
    expect(view.downloads[1]).not.toBe(view.downloads[0]);

    expect(document.body.innerHTML).not.toContain("storage.fixture.test");
    expect(view.client.getMutationCache().getAll().every((mutation) => mutation.state.data === undefined)).toBe(true);
  });
});
