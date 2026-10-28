import { act, fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";
import type { Trigger } from "@/hooks/trigger-model";
import { TestEventPreview } from "../TestEventPreview";

vi.mock("@/hooks/trigger-service-hooks_gen", () => ({
  triggerServiceQueries: { previewReplay: () => ({ queryKey: ["preview"] }) },
}));

function mount() {
  const client = new QueryClient();
  let resolve!: (result: { prompt: string }) => void;
  let reject!: (error: Error) => void;
  const response = new Promise<{ prompt: string }>((done, fail) => { resolve = done; reject = fail; });
  const fetch = vi.spyOn(client, "fetchQuery").mockReturnValue(response);
  const trigger = { id: "trigger-1", source: { kind: "schedule" } } as Trigger;
  render(<QueryClientProvider client={client}><TestEventPreview trigger={trigger} /></QueryClientProvider>);
  fireEvent.click(screen.getByRole("button", { name: "Send a test event" }));
  return { resolve, reject, fetch };
}

describe("event preview response ownership", () => {
  it("does not present an old sample's result after the sample was edited", async () => {
    const { resolve } = mount();
    fireEvent.click(screen.getByRole("button", { name: "Preview message" }));
    fireEvent.change(screen.getByRole("textbox", { name: "Sample event (JSON)" }), { target: { value: '{"data":{"new":true}}' } });
    await act(async () => { resolve({ prompt: "OLD_SAMPLE_MAPPING" }); });
    expect(screen.queryByText("OLD_SAMPLE_MAPPING")).not.toBeInTheDocument();
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Preview message" })).toBeEnabled();
  });

  it("does not show a stale error or clear a newer preview's pending state", async () => {
    const { reject, fetch } = mount();
    fireEvent.click(screen.getByRole("button", { name: "Preview message" }));
    fireEvent.change(screen.getByRole("textbox", { name: "Sample event (JSON)" }), { target: { value: '{"data":{"new":true}}' } });
    let finish!: (result: { prompt: string }) => void;
    fetch.mockReturnValueOnce(new Promise<{ prompt: string }>((done) => { finish = done; }));
    fireEvent.click(screen.getByRole("button", { name: "Preview message" }));
    await act(async () => { reject(new Error("old request failed")); });
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Preview message" })).toBeDisabled();
    await act(async () => { finish({ prompt: "NEW_SAMPLE_MAPPING" }); });
    expect(screen.getByRole("status")).toHaveTextContent("NEW_SAMPLE_MAPPING");
    expect(screen.getByRole("button", { name: "Preview message" })).toBeEnabled();
  });
});
