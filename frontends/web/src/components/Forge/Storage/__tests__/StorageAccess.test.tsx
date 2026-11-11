// Copyright (c) 2025 Reliant Labs

/**
 * Policy, the audited environment's confirmation, and refusals: nothing is
 * read in an audited environment until the reader gives a reason, once per
 * tab session, in a dialog naming the environment; every read (a download
 * included) then carries confirm_env_id and the reason. A deny policy, a
 * missing storage:read grant, an unrecorded environment and a rate limit
 * each say what they are.
 */

import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Code, ConnectError } from "@connectrpc/connect";

import { createStorageFixtureApi } from "../__fixtures__/fixtureStorageApi";
import { readStorageAccess, writeStorageAccess } from "../storageAccess-model";
import { callsOf, PROD, renderBrowser, STAGING } from "./storageTestUtils";

vi.mock("@tanstack/react-router", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-router")>()),
  useNavigate: () => vi.fn(),
}));

afterEach(() => window.sessionStorage.clear());

describe("an audited environment", () => {
  it("does not browse direct GCS even when its physical name is known", async () => {
    const api = createStorageFixtureApi();
    api.listBrowsableBuckets = async () => ({
      policy: "open",
      buckets: [{ name: "direct", runtime: "gcs", publicRead: false, reachable: false, unreachableReason: "Direct GCS browsing is not enabled" }],
    });
    renderBrowser(api, { bucket: "direct", object: "customer.txt" });
    expect(await screen.findByTestId("storage-unreachable")).toHaveTextContent("Direct GCS browsing is not enabled");
    for (const method of ["listObjects", "getObjectMetadata", "previewObject", "presignObjectDownload"] as const) {
      expect(callsOf(api, method)).toHaveLength(0);
    }
  });

  it("asks for a reason, naming the environment, before reading anything", async () => {
    const api = createStorageFixtureApi();
    renderBrowser(api, { bucket: "uploads", prefix: "docs/" }, PROD);
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent("Reading prod's files is audited");
    expect(screen.getByTestId("storage-access-confirm")).toHaveTextContent("Read prod's files");
    expect(screen.getByTestId("storage-access-confirm")).toBeDisabled();
    expect(api.calls.map((call) => call.method)).toEqual(["listBrowsableBuckets"]);

    await userEvent.type(screen.getByTestId("storage-access-reason"), "Checking #4412");
    fireEvent.click(screen.getByTestId("storage-access-confirm"));

    await screen.findByTestId("storage-table");
    const access = { confirmEnvId: "env-prod", reason: "Checking #4412" };
    expect(callsOf(api, "listObjects")[0]).toMatchObject({ environmentId: "env-prod", bucket: "uploads", prefix: "docs/", access, openBucket: true });
    expect(readStorageAccess("env-prod")).toEqual(access);
    expect(screen.getByTestId("storage-reason")).toHaveTextContent("Reading as: Checking #4412");
    expect(screen.getByTestId("storage-policy")).toHaveAttribute("data-policy", "audited");
  });

  it("asks once per tab session, and sends the kept reason on every read, a download included", async () => {
    writeStorageAccess("env-prod", "Kept");
    const api = createStorageFixtureApi();
    const view = renderBrowser(api, { bucket: "uploads", prefix: "docs/", object: "docs/readme.txt" }, PROD);
    await screen.findByTestId("storage-preview-text");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    fireEvent.click(screen.getByTestId("storage-download"));
    await waitFor(() => expect(view.downloads).toHaveLength(1));
    const access = { confirmEnvId: "env-prod", reason: "Kept" };
    for (const method of ["listObjects", "getObjectMetadata", "previewObject", "presignObjectDownload"] as const) {
      expect(callsOf(api, method)[0], method).toMatchObject({ access });
    }
  });

  it("waits, with a way back to the question, when the reader cancels", async () => {
    const api = createStorageFixtureApi();
    renderBrowser(api, { bucket: "uploads" }, PROD);
    await screen.findByRole("dialog");
    fireEvent.click(screen.getByTestId("storage-access-cancel"));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(screen.getByTestId("storage-needs-reason")).toHaveTextContent("prod is audited: reads need a reason");
    expect(callsOf(api, "listObjects")).toHaveLength(0);
    fireEvent.click(screen.getByTestId("storage-give-reason"));
    expect(await screen.findByRole("dialog")).toBeInTheDocument();
  });

  it("asks again when the server says the confirmation is missing, and does not count the refused read as the open", async () => {
    writeStorageAccess("env-prod", "Stale");
    const api = createStorageFixtureApi({ fail: { method: "listObjects", code: Code.FailedPrecondition, reason: "confirmation_required" } });
    renderBrowser(api, { bucket: "uploads" }, PROD);
    expect(await screen.findByRole("dialog")).toHaveTextContent("Reading prod's files is audited");
    expect(readStorageAccess("env-prod")).toBeNull();
    expect(window.sessionStorage.getItem("forge.storage.opened.env-prod.uploads")).toBeNull();
  });

  it("recovers from a policy that became audited after the bucket list was read", async () => {
    const api = createStorageFixtureApi();
    const realList = api.listObjects;
    let refused = false;
    api.listObjects = async (req, options) => {
      if (!refused) {
        refused = true;
        throw new ConnectError("Confirm this environment", Code.FailedPrecondition, new Headers({ "x-forge-error-reason": "confirmation_required" }));
      }
      return realList(req, options);
    };
    renderBrowser(api, { bucket: "uploads" }, STAGING);
    await screen.findByRole("dialog");
    fireEvent.change(screen.getByTestId("storage-access-reason"), { target: { value: "Checking the new policy" } });
    fireEvent.click(screen.getByTestId("storage-access-confirm"));
    await screen.findByTestId("storage-table");
    expect(callsOf(api, "listObjects").at(-1)).toMatchObject({ access: { confirmEnvId: "env-staging", reason: "Checking the new policy" } });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("keeps the files' reason apart from the data browser's", async () => {
    window.sessionStorage.setItem("forge.data.access.env-prod", JSON.stringify({ reason: "Reading rows" }));
    renderBrowser(createStorageFixtureApi(), { bucket: "uploads" }, PROD);
    expect(await screen.findByRole("dialog")).toBeInTheDocument();
  });

  it("never asks in an open environment", async () => {
    const api = createStorageFixtureApi();
    renderBrowser(api, { bucket: "uploads" }, STAGING);
    await screen.findByTestId("storage-table");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(callsOf(api, "listObjects")[0]).not.toHaveProperty("access");
  });
});

describe("refusals", () => {
  it("explains a deny policy, and reads nothing", async () => {
    const api = createStorageFixtureApi({ policy: { "env-staging": "deny" } });
    renderBrowser(api, { bucket: "uploads" }, STAGING);
    const denied = await screen.findByTestId("storage-policy-deny");
    expect(denied).toHaveTextContent("staging doesn't allow browsing its files");
    expect(denied).toHaveTextContent("forge.DataAccess");
    expect(denied).toHaveTextContent("db_query");
    expect(screen.getByTestId("storage-policy")).toHaveAttribute("data-policy", "deny");
    expect(callsOf(api, "listObjects")).toHaveLength(0);
  });

  it("refuses an unrecorded policy as deny", async () => {
    renderBrowser(createStorageFixtureApi({ policy: { "env-staging": "unknown" } }), { bucket: "uploads" }, STAGING);
    expect(await screen.findByTestId("storage-policy-deny")).toHaveTextContent("staging has no recorded storage access policy");
  });

  it("explains the storage:read grant on PermissionDenied", async () => {
    renderBrowser(createStorageFixtureApi({ fail: { method: "listBrowsableBuckets", code: Code.PermissionDenied } }), {}, STAGING);
    const error = await screen.findByTestId("storage-buckets-error");
    expect(error).toHaveAttribute("data-kind", "denied");
    expect(error).toHaveTextContent("You can't read staging's files");
    expect(error).toHaveTextContent("storage:read");
  });

  it("says an environment with no recorded release has buckets not known yet", async () => {
    renderBrowser(createStorageFixtureApi({ fail: { method: "listBrowsableBuckets", code: Code.FailedPrecondition, reason: "shape_unknown" } }), {}, STAGING);
    const error = await screen.findByTestId("storage-buckets-error");
    expect(error).toHaveAttribute("data-kind", "shape-unknown");
    expect(error).toHaveTextContent("reliant forge env deploy staging");
  });

  it("says a bucket the server cannot reach is unreachable", async () => {
    const api = createStorageFixtureApi({ fail: { method: "listObjects", code: Code.FailedPrecondition, reason: "bucket_unreachable" } });
    renderBrowser(api, { bucket: "uploads" }, STAGING);
    const error = await screen.findByTestId("storage-list-error");
    expect(error).toHaveAttribute("data-kind", "unreachable");
    expect(error).toHaveTextContent("Reliant can't reach the uploads bucket");
  });

  it("offers to try again after a rate limit, and does not retry on its own", async () => {
    const api = createStorageFixtureApi({ fail: { method: "listObjects", code: Code.ResourceExhausted, reason: "rate_limited" } });
    renderBrowser(api, { bucket: "uploads" }, STAGING);
    const error = await screen.findByTestId("storage-list-error");
    expect(error).toHaveAttribute("data-kind", "busy");
    expect(callsOf(api, "listObjects")).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: /try again/i }));
    await waitFor(() => expect(callsOf(api, "listObjects")).toHaveLength(2));
  });

  it("says a preview over its cap is too large", async () => {
    const api = createStorageFixtureApi({ fail: { method: "previewObject", code: Code.FailedPrecondition, reason: "too_large" } });
    renderBrowser(api, { bucket: "uploads", object: "docs/readme.txt" }, STAGING);
    const error = await screen.findByTestId("storage-preview-error");
    expect(error).toHaveAttribute("data-kind", "too-large");
    expect(screen.getByTestId("storage-download")).toBeEnabled();
  });

  it("shows a refused download beside the button, and mints nothing", async () => {
    const api = createStorageFixtureApi({ fail: { method: "presignObjectDownload", code: Code.ResourceExhausted, reason: "rate_limited" } });
    const view = renderBrowser(api, { bucket: "uploads", object: "reports/q1.pdf" }, STAGING);
    fireEvent.click(await screen.findByTestId("storage-download"));
    expect(await screen.findByTestId("storage-download-error")).toHaveAttribute("data-kind", "busy");
    expect(view.downloads).toEqual([]);
  });
});
