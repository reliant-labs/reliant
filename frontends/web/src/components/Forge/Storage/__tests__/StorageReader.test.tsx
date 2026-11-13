import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { setReaderPrincipal, getReaderSession } from "../../AuditedAccess/readerSession";
import { readStorageAccess, wasBucketOpened } from "../storageAccess-model";
import { createStorageFixtureApi } from "../__fixtures__/fixtureStorageApi";
import { callsOf, PROD, renderBrowser } from "./storageTestUtils";

vi.mock("@tanstack/react-router", async (original) => ({ ...(await original<typeof import("@tanstack/react-router")>()), useNavigate: () => vi.fn() }));
afterEach(() => window.sessionStorage.clear());
async function confirm(reason: string) {
  await screen.findByRole("dialog");
  fireEvent.change(screen.getByTestId("storage-access-reason"), { target: { value: reason } });
  fireEvent.click(screen.getByTestId("storage-access-confirm"));
}

it.each([true, false])("requires B's own confirmation and bucket Activity after account transition (logout=%s)", async (logout) => {
  setReaderPrincipal("A");
  const api = createStorageFixtureApi();
  const view = renderBrowser(api, { bucket: "uploads", prefix: "docs/", object: "docs/readme.txt" }, PROD);
  await confirm("A investigation");
  await screen.findByTestId("storage-preview-text");
  expect(wasBucketOpened(PROD.id, "uploads")).toBe(true);
  const aEpoch = getReaderSession().epoch;
  act(() => { if (logout) setReaderPrincipal(null); setReaderPrincipal("B"); });
  await screen.findByRole("dialog");
  expect(readStorageAccess(PROD.id)).toBeNull();
  expect(screen.queryByTestId("storage-preview-text")).not.toBeInTheDocument();
  expect(screen.queryByTestId("storage-table")).not.toBeInTheDocument();
  expect(wasBucketOpened(PROD.id, "uploads")).toBe(false);
  expect(view.client.getQueryCache().getAll().some((query) => JSON.stringify(query.queryKey).includes(aEpoch))).toBe(false);
  const before = callsOf(api, "listObjects").length;
  await confirm("B investigation");
  await screen.findByTestId("storage-preview-text");
  expect(callsOf(api, "listObjects").slice(before)).toEqual([expect.objectContaining({ openBucket: true, access: { confirmEnvId: PROD.id, reason: "B investigation" } })]);
});

it("never displays A's deferred preview after B's own response", async () => {
  setReaderPrincipal("A");
  const api = createStorageFixtureApi();
  const preview = api.previewObject;
  let release!: () => void;
  let started!: () => void;
  let finished!: () => void;
  const begun = new Promise<void>((resolve) => { started = resolve; });
  const completed = new Promise<void>((resolve) => { finished = resolve; });
  const gate = new Promise<void>((resolve) => { release = resolve; });
  let calls = 0;
  api.previewObject = async (...args) => {
    const first = calls++ === 0;
    if (first) { started(); await gate; }
    const result = await preview(...args);
    if (first) finished();
    return { ...result, content: new TextEncoder().encode(first ? "A PRIVATE CONTENT" : "B CONTENT") };
  };
  renderBrowser(api, { bucket: "uploads", object: "docs/readme.txt" });
  await act(async () => { await begun; });
  act(() => setReaderPrincipal("B"));
  expect(await screen.findByTestId("storage-preview-text")).toHaveTextContent("B CONTENT");
  await act(async () => { release(); await completed; });
  expect(screen.getByTestId("storage-preview-text")).toHaveTextContent("B CONTENT");
  expect(screen.queryByText("A PRIVATE CONTENT")).not.toBeInTheDocument();
});

it("drops A's deferred listing and download after B replaces the reader, even when abort is ignored", async () => {
  setReaderPrincipal("A");
  const api = createStorageFixtureApi();
  const list = api.listObjects;
  const mint = api.presignObjectDownload;
  let finishList!: () => void;
  let finishDownload!: () => void;
  const listGate = new Promise<void>((resolve) => { finishList = resolve; });
  const downloadGate = new Promise<void>((resolve) => { finishDownload = resolve; });
  api.listObjects = async (...args) => { await listGate; return list(...args); };
  api.presignObjectDownload = async (...args) => { await downloadGate; return mint(...args); };
  const view = renderBrowser(api, { bucket: "uploads", object: "docs/readme.txt" });
  fireEvent.click(await screen.findByTestId("storage-download"));
  act(() => setReaderPrincipal("B"));
  await waitFor(() => expect(getReaderSession().principalId).toBe("B"));
  await act(async () => { finishList(); finishDownload(); await listGate; await downloadGate; });
  await screen.findByTestId("storage-table");
  expect(view.downloads).toEqual([]);
  const lists = callsOf(api, "listObjects");
  expect(lists).toHaveLength(2);
  expect(lists.every((req) => req.openBucket === true)).toBe(true);
  expect(wasBucketOpened("env-staging", "uploads")).toBe(true);
});
