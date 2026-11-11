// Copyright (c) 2025 Reliant Labs
import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { DemoBrowserProviders } from "../DemoBrowserProviders";
import { DataBrowser } from "../../Data/DataBrowser";
import { StorageBrowser } from "../../Storage/StorageBrowser";
import { getReaderSession } from "../../AuditedAccess/readerSession";
import { ObsReadOnlyProvider } from "../../Observability/obsReadOnly";
vi.mock("@tanstack/react-router", async (original) => ({ ...await original<typeof import("@tanstack/react-router")>(), useNavigate: () => vi.fn() }));

describe("synthetic browsers across demo shells", () => {
  it("mounts real Data and Storage while signed out, without changing the production principal", async () => {
    const before = getReaderSession();
    const fetch = vi.spyOn(globalThis, "fetch").mockRejectedValue(new Error("network forbidden"));
    try {
      render(<QueryClientProvider client={new QueryClient()}><DemoBrowserProviders><ObsReadOnlyProvider value={{ readOnly: true, reason: "Read-only demo" }}>
        <DataBrowser env={{ id: "env-staging", name: "staging" }} search={{ db: "shop", table: "public.customers" }} onNavigate={() => {}} href={() => "#"} />
        <StorageBrowser env={{ id: "env-staging", name: "staging" }} search={{ bucket: "uploads", object: "README.txt" }} onNavigate={() => {}} href={() => "#"} />
      </ObsReadOnlyProvider></DemoBrowserProviders></QueryClientProvider>);
      await waitFor(() => expect(screen.getByTestId("data-grid")).toHaveAttribute("data-state", "ready"));
      expect(screen.getByText("lena.fischer@example.com")).toBeInTheDocument();
      expect(await screen.findByTestId("storage-object-pane")).toBeInTheDocument();
      await waitFor(() => expect(screen.getByTestId("storage-download")).toBeDisabled());
      expect(getReaderSession()).toBe(before);
      expect(fetch).not.toHaveBeenCalled();
    } finally { fetch.mockRestore(); }
  });

  it("shows shop's dev as a project that declares no storage buckets", async () => {
    render(<QueryClientProvider client={new QueryClient()}><DemoBrowserProviders>
      <StorageBrowser env={{ id: "env-dev", name: "dev" }} search={{}} onNavigate={() => {}} href={() => "#"} />
    </DemoBrowserProviders></QueryClientProvider>);
    expect(await screen.findByTestId("storage-no-buckets")).toHaveTextContent("This project declares no storage buckets in dev");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });
});
