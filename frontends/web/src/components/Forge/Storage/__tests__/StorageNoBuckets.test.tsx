// Copyright (c) 2025 Reliant Labs

/**
 * The bucket list's three states, kept apart: loading, a failed read, and a
 * project that declares no bucket in this environment. "No buckets" is said
 * only after ListBrowsableBuckets SUCCEEDED with none — never while it is in
 * flight and never in place of an error. In an audited environment the
 * reason is still asked before any object is read, and the empty state is no
 * read: listing the buckets writes no audit row, and with none there is
 * nothing to ask a reason for.
 */

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Code, ConnectError } from "@connectrpc/connect";

import { createStorageFixtureApi, type StorageFixtureApi } from "../__fixtures__/fixtureStorageApi";
import { STORAGE_BUCKETS_DOCS_URL } from "../StorageStates";
import { callsOf, PROD, renderBrowser, STAGING } from "./storageTestUtils";

vi.mock("@tanstack/react-router", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-router")>()),
  useNavigate: () => vi.fn(),
}));

afterEach(() => window.sessionStorage.clear());

/** Hold ListBrowsableBuckets until `release`, so the loading state can be looked at. */
function deferBuckets(api: StorageFixtureApi) {
  const discover = api.listBrowsableBuckets;
  let release!: () => void;
  const pending = new Promise<void>((resolve) => {
    release = resolve;
  });
  api.listBrowsableBuckets = async (...args) => {
    await pending;
    return discover(...args);
  };
  return () => release();
}

/** A rate limit: offered a retry, and (unlike Unavailable) not retried on its own, so the test sees it at once. */
const rateLimited = () => new ConnectError("slow down", Code.ResourceExhausted, new Headers({ "x-forge-error-reason": "rate_limited" }));

/** Only the bucket list was asked for: no listing, metadata, preview or download. */
function readNothing(api: StorageFixtureApi) {
  for (const method of ["listObjects", "getObjectMetadata", "previewObject", "presignObjectDownload"] as const) {
    expect(callsOf(api, method), method).toHaveLength(0);
  }
}

describe("a project that declares no bucket", () => {
  it("says so, names the environment, explains what is listed and links to how to declare one", async () => {
    const api = createStorageFixtureApi({ noBuckets: true });
    renderBrowser(api, {}, STAGING);
    const empty = await screen.findByTestId("storage-no-buckets");
    expect(empty).toHaveTextContent("This project declares no storage buckets in staging");
    expect(empty).toHaveTextContent("Buckets an app declares in its config appear here: each forge.Bucket in this environment's last recorded release.");
    expect(empty).toHaveTextContent("Platform storage (container images, static sites, backups) isn't listed here.");
    const docs = screen.getByRole("link", { name: "How to declare a bucket" });
    expect(docs).toHaveAttribute("href", STORAGE_BUCKETS_DOCS_URL);
    expect(docs).toHaveAttribute("target", "_blank");
    expect(docs).toHaveAttribute("rel", "noreferrer");
    // Not a failure: a status, not an alert, and no bucket chrome around it.
    expect(empty).toHaveAttribute("role", "status");
    expect(screen.queryByTestId("storage-buckets")).not.toBeInTheDocument();
    expect(screen.queryByTestId("storage-buckets-error")).not.toBeInTheDocument();
    readNothing(api);
  });
});

describe("loading", () => {
  it("shows the skeleton, not the empty state, until the bucket list resolves", async () => {
    const api = createStorageFixtureApi({ noBuckets: true });
    const release = deferBuckets(api);
    renderBrowser(api, {}, STAGING);
    expect(screen.getByTestId("storage-buckets-loading")).toHaveAttribute("aria-busy", "true");
    expect(screen.getByTestId("storage-buckets-loading")).toHaveAttribute("aria-label", "Loading staging's buckets");
    expect(screen.queryByTestId("storage-no-buckets")).not.toBeInTheDocument();
    expect(screen.queryByTestId("storage-buckets-error")).not.toBeInTheDocument();
    release();
    expect(await screen.findByTestId("storage-no-buckets")).toBeVisible();
    expect(screen.queryByTestId("storage-buckets-loading")).not.toBeInTheDocument();
  });
});

describe("a failed bucket list", () => {
  it("shows the error with a retry, never the empty state, and the empty state once a retry succeeds with none", async () => {
    const api = createStorageFixtureApi({ noBuckets: true });
    const discover = api.listBrowsableBuckets;
    let failing = true;
    api.listBrowsableBuckets = async (...args) => {
      if (failing) throw rateLimited();
      return discover(...args);
    };
    renderBrowser(api, {}, STAGING);
    const error = await screen.findByTestId("storage-buckets-error");
    expect(error).toHaveAttribute("data-kind", "busy");
    expect(error).toHaveTextContent("Too many reads at once");
    expect(screen.queryByTestId("storage-no-buckets")).not.toBeInTheDocument();

    failing = false;
    fireEvent.click(screen.getByRole("button", { name: /try again/i }));
    expect(await screen.findByTestId("storage-no-buckets")).toBeVisible();
    expect(screen.queryByTestId("storage-buckets-error")).not.toBeInTheDocument();
  });

  it("says an unrecorded environment's buckets aren't known yet, not that it has none", async () => {
    renderBrowser(createStorageFixtureApi({ fail: { method: "listBrowsableBuckets", code: Code.FailedPrecondition, reason: "shape_unknown" } }), {}, STAGING);
    expect(await screen.findByTestId("storage-buckets-error")).toHaveAttribute("data-kind", "shape-unknown");
    expect(screen.queryByTestId("storage-no-buckets")).not.toBeInTheDocument();
  });
});

describe("an audited environment", () => {
  it("shows the empty state only after the bucket list succeeded, asks no reason and reads nothing", async () => {
    const api = createStorageFixtureApi({ noBuckets: true });
    const release = deferBuckets(api);
    renderBrowser(api, {}, PROD);
    expect(screen.getByTestId("storage-buckets-loading")).toBeInTheDocument();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.queryByTestId("storage-no-buckets")).not.toBeInTheDocument();

    release();
    expect(await screen.findByTestId("storage-no-buckets")).toHaveTextContent("This project declares no storage buckets in prod");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(screen.queryByTestId("storage-needs-reason")).not.toBeInTheDocument();
    expect(api.calls.map((call) => call.method)).toEqual(["listBrowsableBuckets"]);
  });

  it("with buckets, still asks the reason before any read, and never shows the empty state", async () => {
    const api = createStorageFixtureApi();
    const release = deferBuckets(api);
    renderBrowser(api, { bucket: "uploads" }, PROD);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    release();

    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent("Reading prod's files is audited");
    expect(screen.queryByTestId("storage-no-buckets")).not.toBeInTheDocument();
    readNothing(api);

    await userEvent.type(screen.getByTestId("storage-access-reason"), "Checking #4412");
    fireEvent.click(screen.getByTestId("storage-access-confirm"));
    await screen.findByTestId("storage-table");
    expect(callsOf(api, "listObjects")[0]).toMatchObject({ access: { confirmEnvId: "env-prod", reason: "Checking #4412" } });
    expect(screen.queryByTestId("storage-no-buckets")).not.toBeInTheDocument();
  });

  it("shows a failed bucket list as an error, asking no reason", async () => {
    const api = createStorageFixtureApi({ fail: { method: "listBrowsableBuckets", code: Code.PermissionDenied } });
    renderBrowser(api, {}, PROD);
    expect(await screen.findByTestId("storage-buckets-error")).toHaveAttribute("data-kind", "denied");
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(screen.queryByTestId("storage-no-buckets")).not.toBeInTheDocument();
    readNothing(api);
  });
});
