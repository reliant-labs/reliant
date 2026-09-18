// Copyright (c) 2025 Reliant Labs

/**
 * The non-report outcomes and the env selector.
 *
 * Same guard as the topology screen's states test: every one of these is a
 * SUCCESSFUL RPC carrying data, and the failure to prevent is any of them
 * silently producing an empty screen, which a reader takes as "this environment
 * declares no secrets" — false in all four cases.
 *
 * The selector tests pin the other rule: the env list is derived from the
 * topology report, never hardcoded.
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { SecretsView } from "../SecretsView";
import { classifyForgeResponse } from "@/services/forge/topology";
import type { ForgeSecretsReport } from "@/services/forge/secrets";
import { ForgeReachability } from "@/gen/reliant/v1/forge_pb";

import { controlPlaneDevReport, meta, reportOutcome, topologyOutcome } from "./fixtures";

function renderOutcome(
  outcome: ReturnType<typeof reportOutcome> | undefined,
  extra: Partial<React.ComponentProps<typeof SecretsView>> = {}
) {
  return render(
    <SecretsView
      outcome={outcome}
      isLoading={false}
      topology={topologyOutcome(["dev"])}
      selectedEnv="dev"
      onSelectEnv={vi.fn()}
      projectName="some-project"
      {...extra}
    />
  );
}

describe("is_forge_project: false", () => {
  it("renders the shared non-error empty state, not a blank screen", () => {
    const outcome = classifyForgeResponse<ForgeSecretsReport>(meta({ isForgeProject: false }), "");
    expect(outcome.kind).toBe("not-forge-project");

    renderOutcome(outcome);
    const panel = screen.getByTestId("forge-not-project");
    expect(panel.textContent).toContain("forge.yaml");
    expect(panel.className).not.toMatch(/destructive/);
    expect(screen.queryByTestId("forge-secrets")).toBeNull();
  });
});

describe("supported: false", () => {
  it("names the forge version rather than showing zero secrets", () => {
    const outcome = classifyForgeResponse<ForgeSecretsReport>(
      meta({ supported: false, forgeVersion: "v0.4.2", unsupportedReason: "unknown command: secret list" }),
      ""
    );
    expect(outcome.kind).toBe("unsupported");

    renderOutcome(outcome);
    const panel = screen.getByTestId("forge-unsupported");
    expect(panel.textContent).toContain("v0.4.2");
    expect(panel.textContent).toContain("unknown command: secret list");
  });
});

describe("reachability: unreachable with no body", () => {
  it("renders as UNKNOWN, not as an error and not as zero missing", () => {
    const outcome = classifyForgeResponse<ForgeSecretsReport>(
      meta({ reachability: ForgeReachability.UNREACHABLE, unreachableReason: "daemon offline", exitCode: 2 }),
      ""
    );
    expect(outcome.kind).toBe("unreachable");

    renderOutcome(outcome);
    const panel = screen.getByTestId("forge-unreachable");
    expect(panel.className).toContain("border-dashed");
    expect(panel.className).not.toMatch(/destructive/);
    expect(panel.textContent).toContain("daemon offline");
    // Nothing that could be read as a verdict about secrets.
    expect(screen.queryByTestId("secrets-verdict")).toBeNull();
  });
});

describe("a malformed report", () => {
  it("says the document could not be read instead of crashing", () => {
    const outcome = classifyForgeResponse<ForgeSecretsReport>(meta(), "not json at all");
    expect(outcome.kind).toBe("malformed");

    renderOutcome(outcome);
    expect(screen.getByTestId("forge-malformed")).toBeTruthy();
    // The unparsed body is NOT echoed — on this path it could have been anything.
    expect(screen.getByTestId("forge-malformed").textContent).not.toContain("not json at all");
  });
});

describe("loading", () => {
  it("names the environment it is reading rather than going blank", () => {
    renderOutcome(undefined, { isLoading: true });
    expect(screen.getByTestId("secrets-loading").textContent).toContain("dev");
  });
});

describe("the environment selector", () => {
  it("derives its options from the topology report, not from a hardcoded list", () => {
    // Deliberately unusual names: a hardcoded dev/staging/prod triple would fail.
    render(
      <SecretsView
        outcome={reportOutcome(controlPlaneDevReport())}
        isLoading={false}
        topology={topologyOutcome(["sandbox", "preprod", "eu-prod"])}
        selectedEnv="sandbox"
        onSelectEnv={vi.fn()}
      />
    );

    expect(screen.getByTestId("env-tab-sandbox")).toBeTruthy();
    expect(screen.getByTestId("env-tab-preprod")).toBeTruthy();
    expect(screen.getByTestId("env-tab-eu-prod")).toBeTruthy();
    // And nothing invented.
    expect(screen.queryByTestId("env-tab-staging")).toBeNull();
    expect(screen.getByTestId("env-tab-sandbox").getAttribute("aria-selected")).toBe("true");
  });

  it("reports the chosen environment to its caller", async () => {
    const onSelectEnv = vi.fn();
    render(
      <SecretsView
        outcome={reportOutcome(controlPlaneDevReport())}
        isLoading={false}
        topology={topologyOutcome(["dev", "prod"])}
        selectedEnv="dev"
        onSelectEnv={onSelectEnv}
      />
    );

    await userEvent.click(screen.getByTestId("env-tab-prod"));
    expect(onSelectEnv).toHaveBeenCalledWith("prod");
  });

  it("says the project declares no environments rather than showing empty tabs", () => {
    render(
      <SecretsView
        outcome={undefined}
        isLoading={false}
        topology={topologyOutcome([])}
        selectedEnv={null}
        onSelectEnv={vi.fn()}
      />
    );
    expect(screen.getByTestId("secrets-no-envs")).toBeTruthy();
  });
});

describe("the screen's own framing", () => {
  it("tells the reader up front that only presence is shown", () => {
    renderOutcome(reportOutcome(controlPlaneDevReport()));
    const screenText = screen.getByTestId("forge-secrets").textContent ?? "";
    expect(screenText).toContain("presence only");
    // The stronger claim, which is the one that stops a feature request for a
    // reveal button: the value is not in the response at all.
    expect(screenText.toLowerCase()).toContain("never leave the daemon");
  });
});
