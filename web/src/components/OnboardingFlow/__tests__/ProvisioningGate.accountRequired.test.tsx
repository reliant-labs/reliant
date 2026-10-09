import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";

vi.mock("@/components/Billing/LinkIdentityModal", () => ({
  LinkIdentityModal: ({ onLinked, onDismiss }: { onLinked: () => void; onDismiss: () => void }) => (
    <div data-testid="link-identity-modal">
      <button onClick={onLinked}>linked</button>
      <button onClick={onDismiss}>dismiss</button>
    </div>
  ),
}));
vi.mock("../DaemonConnectingGate", () => ({ DaemonConnectingGate: () => null }));

import { ProvisioningGate } from "../ProvisioningGate";
import type { CommitResult } from "../commitLaunchPlan";

const needsIdentity: CommitResult = {
  commitKey: "k",
  status: "failed",
  tasks: [
    { name: "grant_ai_access", status: "skipped", detail: "" },
    { name: "provision_daemon", status: "failed", detail: "Add an email", needsIdentity: true },
  ],
};

describe("ProvisioningGate — identity required", () => {
  it("asks for an email and re-runs the commit once linked", () => {
    const onRetry = vi.fn();
    render(<ProvisioningGate commit={needsIdentity} onContinue={vi.fn()} onRetry={onRetry} />);

    fireEvent.click(screen.getByText("linked"));

    expect(onRetry).toHaveBeenCalledTimes(1);
    expect(screen.queryByTestId("link-identity-modal")).toBeNull();
  });

  it("does not show the modal for an ordinary failure", () => {
    const failed: CommitResult = {
      ...needsIdentity,
      tasks: [{ name: "provision_daemon", status: "failed", detail: "boom" }],
    };
    render(<ProvisioningGate commit={failed} onContinue={vi.fn()} onRetry={vi.fn()} />);
    expect(screen.queryByTestId("link-identity-modal")).toBeNull();
  });
});
