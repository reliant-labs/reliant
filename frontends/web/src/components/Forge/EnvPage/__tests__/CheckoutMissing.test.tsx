// Copyright (c) 2025 Reliant Labs

/**
 * A project with no checkout on a machine that could answer is its own state,
 * with its own remedy — never "your daemon isn't answering", which is false
 * (the daemon answered) and which no retry fixes. The prod report: the forge
 * console asked a cloud machine about the owner's Mac path and painted the
 * answer as a daemon outage.
 */

import { describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";

import type { MessageInitShape } from "@bufbuild/protobuf";
import {
  ProjectCheckoutMissingSchema,
  ProjectCheckoutState,
  type ProjectCheckoutMissing,
} from "@/gen/reliant/v1/filesystem_pb";
import { DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";
import { daemonSideForTab, daemonSideOf } from "@/services/forge/environments";
import { renderWithQuery as render } from "@/test/renderWithQuery";

import { checkoutMissingCopy, checkoutMissingKind } from "../CheckoutMissing";
import { DAEMON_OFFLINE_COPY } from "../DaemonNeeded";
import { RunningTab } from "../EnvTabPanels";

const mocks = vi.hoisted(() => ({
  createProjectFromRepo: vi.fn(),
  daemons: [] as unknown[],
}));

vi.mock("@/hooks/project-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/project-queries")>()),
  createProjectFromRepo: mocks.createProjectFromRepo,
}));
vi.mock("@/hooks/useDaemonStatus", () => ({ useDaemonStatus: () => ({ daemons: mocks.daemons }) }));

function detail(fields: MessageInitShape<typeof ProjectCheckoutMissingSchema>): ProjectCheckoutMissing {
  return create(ProjectCheckoutMissingSchema, {
    projectId: "p-1",
    state: ProjectCheckoutState.ABSENT,
    remoteUrl: "https://github.com/reliant-labs/control-plane",
    ...fields,
  });
}

function missingError(d: ProjectCheckoutMissing): ConnectError {
  return new ConnectError(
    '"control-plane" is not checked out on any of your machines: clone it onto a machine to use forge with it',
    Code.FailedPrecondition,
    undefined,
    [{ desc: ProjectCheckoutMissingSchema, value: d }],
  );
}

const names = { project: "control-plane", machine: "Cloud box", cloneTarget: "Cloud box", what: "the topology" };

describe("checkoutMissingCopy", () => {
  it("nowhere, with a remote: clone it onto a machine", () => {
    const d = detail({});
    expect(checkoutMissingKind(d)).toBe("nowhere");
    const copy = checkoutMissingCopy(d, names);
    expect(copy.headline).toBe("control-plane isn't checked out on any of your machines");
    expect(copy.action).toBe("Clone it onto Cloud box");
  });

  it("nowhere, a plain folder: connect the machine that holds it", () => {
    const copy = checkoutMissingCopy(detail({ remoteUrl: "" }), names);
    expect(copy.headline).toBe("control-plane isn't on any of your connected machines");
    expect(copy.body).toContain("Connect the machine that holds it");
    expect(copy.action).toBeNull();
  });

  it("nowhere, with no machine to clone onto: says to add one", () => {
    const copy = checkoutMissingCopy(detail({}), { ...names, cloneTarget: null });
    expect(copy.body).toContain("Add a machine");
    expect(copy.action).toBeNull();
  });

  it("a folder gone from the machine that had it names the machine and its path", () => {
    const d = detail({ daemonId: "cloud-1", path: "/home/workspace/projects/control-plane" });
    expect(checkoutMissingKind(d)).toBe("gone");
    const copy = checkoutMissingCopy(d, names);
    expect(copy.headline).toBe("control-plane's folder is missing on Cloud box");
    expect(copy.body).toContain("/home/workspace/projects/control-plane");
    expect(copy.action).toBe("Clone it onto Cloud box");
  });

  it("cloning waits; a failed clone offers to try again with its reason", () => {
    expect(checkoutMissingCopy(detail({ daemonId: "c", state: ProjectCheckoutState.CLONING }), names).action).toBeNull();
    const failed = checkoutMissingCopy(
      detail({ daemonId: "c", state: ProjectCheckoutState.CLONE_FAILED, installError: "repository not found" }),
      names,
    );
    expect(failed.body).toBe("repository not found");
    expect(failed.action).toBe("Try the clone again");
  });
});

describe("daemonSideOf", () => {
  it("files a missing checkout as its own side, not as an offline daemon", () => {
    expect(daemonSideOf(undefined, missingError(detail({})))).toBe("no-checkout");
  });

  it("an awake machine does not upgrade it: the machine being up is not the problem", () => {
    expect(daemonSideForTab("no-checkout", true)).toBe("no-checkout");
  });
});

describe("RunningTab", () => {
  it("shows the checkout-missing state with its action, never the daemon-offline copy", async () => {
    mocks.daemons = [{ daemonId: "cloud-1", name: "Cloud box", status: DaemonStatus.ACTIVE }];
    mocks.createProjectFromRepo.mockResolvedValue({});
    const onRetry = vi.fn();
    const d = detail({});
    render(
      <RunningTab
        envName="dev"
        daemon="no-checkout"
        envStatus={undefined}
        envStatusLoading={false}
        envStatusError={null}
        onRetry={onRetry}
        retrying={false}
        projectName="control-plane"
        checkoutMissing={d}
      />,
    );

    const panel = screen.getByTestId("forge-checkout-missing");
    expect(panel).toHaveTextContent("control-plane isn't checked out on any of your machines");
    expect(panel).not.toHaveTextContent(DAEMON_OFFLINE_COPY);
    expect(screen.queryByTestId("daemon-needed")).toBeNull();

    fireEvent.click(screen.getByTestId("forge-checkout-clone"));
    await waitFor(() =>
      expect(mocks.createProjectFromRepo).toHaveBeenCalledWith({
        cloneUrl: "https://github.com/reliant-labs/control-plane",
        daemonId: "cloud-1",
        name: "control-plane",
        path: undefined,
      }),
    );
    await waitFor(() => expect(onRetry).toHaveBeenCalled());
  });

  it("reads the detail off the tab's own error too", () => {
    mocks.daemons = [];
    render(
      <RunningTab
        envName="dev"
        daemon="ok"
        envStatus={undefined}
        envStatusLoading={false}
        envStatusError={missingError(detail({ remoteUrl: "" }))}
        onRetry={vi.fn()}
        retrying={false}
        projectName="notes"
      />,
    );
    expect(screen.getByTestId("forge-checkout-missing")).toHaveTextContent("notes isn't on any of your connected machines");
    expect(screen.getByTestId("forge-checkout-retry")).toBeInTheDocument();
  });
});
