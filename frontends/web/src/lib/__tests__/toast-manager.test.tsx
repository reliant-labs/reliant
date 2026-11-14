// Copyright (c) 2025 Reliant Labs

/**
 * ToastManager over the real Sonner adapter: toasts that share an id replace
 * each other in place, whatever their kind, so a caller that names one id per
 * cause (an automation funding warning per org and model) shows one toast per
 * cause, not one per call.
 */

import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { Toaster, toast as sonnerToast } from "sonner";
import { afterEach, describe, expect, it } from "vitest";

import { toast } from "../toast-manager";

afterEach(() => {
  act(() => sonnerToast.dismiss());
  cleanup();
});

describe("ToastManager ids", () => {
  it("warnings with the same id replace each other; another id is another toast", async () => {
    render(<Toaster />);

    act(() => {
      toast.warning("first warning", { id: "funding:acme:opus" });
      toast.warning("second warning", { id: "funding:acme:opus" });
    });
    await waitFor(() => expect(screen.getByText("second warning")).toBeTruthy());
    expect(screen.queryByText("first warning")).toBeNull();

    act(() => toast.warning("another cause", { id: "funding:acme:sonnet" }));
    await waitFor(() => expect(screen.getByText("another cause")).toBeTruthy());
    expect(screen.getByText("second warning")).toBeTruthy();
  });

  it("an error with the id of a warning replaces it", async () => {
    render(<Toaster />);

    act(() => toast.warning("about to fail", { id: "same-cause" }));
    await waitFor(() => expect(screen.getByText("about to fail")).toBeTruthy());
    await act(async () => {
      await toast.error(new Error("it failed"), { id: "same-cause" });
    });
    await waitFor(() => expect(screen.queryByText("about to fail")).toBeNull());
  });
});
