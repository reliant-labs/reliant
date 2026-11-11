import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { SidebarUpdateButton } from "../SidebarUpdateButton";
import { useAppUpdateStore } from "../../../store/appUpdateStore";

const initial = useAppUpdateStore.getState();

beforeEach(() => {
  useAppUpdateStore.setState(initial, true);
});

afterEach(cleanup);

describe("SidebarUpdateButton", () => {
  it("renders nothing while no update is waiting", () => {
    const { container } = render(<SidebarUpdateButton />);
    expect(container).toBeEmptyDOMElement();
  });

  it("names the waiting update and opens the update dialog", () => {
    useAppUpdateStore.getState().receiveStatus({ type: "available", version: "1.14.0" });
    render(<SidebarUpdateButton />);

    const button = screen.getByRole("button", { name: "Update available: Reliant 1.14.0" });
    fireEvent.click(button);

    const state = useAppUpdateStore.getState();
    expect(state.dialogOpen).toBe(true);
    expect(state.status).toEqual({ type: "available", version: "1.14.0" });
  });

  it("asks for a restart once the update is downloaded", () => {
    useAppUpdateStore.getState().receiveStatus({ type: "downloaded", version: "1.14.0" });
    render(<SidebarUpdateButton />);

    expect(screen.getByRole("button", { name: "Restart to update to Reliant 1.14.0" })).toHaveAttribute(
      "data-update-state",
      "downloaded",
    );
  });
});
