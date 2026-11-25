/**
 * Moving a project is "add the new machine, then offer detach": the offer
 * appears only once the new checkout is INSTALLED, and the detach runs only
 * on the user's click.
 */
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { ConnectError, Code } from "@connectrpc/connect";
import { beforeEach, describe, expect, it, vi } from "vitest";

// Records each call; `refusal` makes the server refuse it. (A rejecting
// vi.fn reset in beforeEach is reported by Vitest 5's vm pool as the test's
// own failure even when the component catches it, so the refusal is thrown
// from a plain function.)
const detach = vi.hoisted(() => vi.fn());
const refusal = vi.hoisted(() => ({ current: null as Error | null }));
vi.mock("@/hooks/project-service-hooks_gen", () => ({
  useDetachProjectFromDaemon: () => ({
    mutateAsync: async (req: unknown) => {
      detach(req);
      if (refusal.current) throw refusal.current;
      return {};
    },
  }),
  usePreviewProjectMachineMove: () => ({ isLoading: false, error: null, data: undefined }),
}));

import { DetachOldMachineOffer } from "../DetachOldMachineOffer";

const laptop = { daemonId: "d-laptop", name: "laptop" };

function renderOffer(targetInstalled: boolean) {
  return render(
    <DetachOldMachineOffer
      projectId="p"
      projectName="api"
      targetName="cicd"
      targetInstalled={targetInstalled}
      oldMachines={[laptop]}
    />,
  );
}

beforeEach(() => {
  detach.mockReset();
  refusal.current = null;
});

describe("DetachOldMachineOffer", () => {
  it("offers nothing while the new checkout is not installed", () => {
    const { container } = renderOffer(false);
    expect(container).toBeEmptyDOMElement();
    expect(detach).not.toHaveBeenCalled();
  });

  it("offers the detach once installed, and never runs it without a click", () => {
    renderOffer(true);
    expect(screen.getByRole("button", { name: "Detach from laptop (keep files)" })).toBeInTheDocument();
    expect(detach).not.toHaveBeenCalled();
  });

  it("detaches the old machine on click, keeping its files", async () => {
    renderOffer(true);
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Detach from laptop (keep files)" }));
    });
    expect(detach).toHaveBeenCalledTimes(1);
    expect(detach).toHaveBeenCalledWith({ projectId: "p", daemonId: "d-laptop" });
    expect(screen.getByRole("status")).toHaveTextContent("Detached from laptop. Its files stay on disk.");
  });

  it("shows the server's refusal as it is", async () => {
    refusal.current = new ConnectError(
      "laptop still has 1 branch workspace of api (fix-login); archive it first.",
      Code.FailedPrecondition,
    );
    renderOffer(true);
    fireEvent.click(screen.getByRole("button", { name: "Detach from laptop (keep files)" }));
    await waitFor(() =>
      expect(screen.getByRole("alert")).toHaveTextContent(
        "laptop still has 1 branch workspace of api (fix-login); archive it first.",
      ),
    );
  });

  it("can be dismissed", () => {
    const { container } = renderOffer(true);
    fireEvent.click(screen.getByRole("button", { name: "Not now" }));
    expect(container).toBeEmptyDOMElement();
    expect(detach).not.toHaveBeenCalled();
  });
});
