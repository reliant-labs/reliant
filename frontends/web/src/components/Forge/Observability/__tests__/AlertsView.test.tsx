// Copyright (c) 2025 Reliant Labs

/**
 * The Alerts area against the stateful alerting fixture: what fires, history,
 * silences, the rules each environment declares (as code), receivers, the
 * notifications bell, and the two answers alerting gives on purpose — "not
 * configured" and "you are not an admin".
 */

import { describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { FIXTURE_DECLARED } from "../__fixtures__/fixtureAlerting";
import { createFixtureApi, FIXTURE_ENVS, FIXTURE_PROJECT } from "../__fixtures__/fixtureApi";
import type { ObsPlace } from "../obsPlace";
import { renderPanel } from "./obsTestUtils";

vi.mock("uplot", async () => (await import("./obsMocks")).obsMocks.uplot());
vi.mock("@tanstack/react-virtual", async () => (await import("./obsMocks")).obsMocks.virtual());

const ENVIRONMENTS = FIXTURE_ENVS.map((env) => ({ ...env, declared: FIXTURE_DECLARED[env.id] }));
const PROJECT: ObsPlace = { kind: "project", project: FIXTURE_PROJECT, environments: ENVIRONMENTS };
const PROD: ObsPlace = { kind: "env", env: ENVIRONMENTS[2] };

describe("firing", () => {
  it("lists what fires across the project with its labels, annotations and who wrote the rule", async () => {
    const user = userEvent.setup();
    renderPanel(createFixtureApi(), { view: "alerts" }, PROJECT);
    // The file's first render pays for the panel's cold imports.
    const checkout = await screen.findByTestId("obs-alert-CheckoutP95High", {}, { timeout: 10_000 });
    expect(checkout).toHaveAttribute("data-state", "firing");
    expect(checkout).toHaveTextContent("checkout p95 is 1.42s");
    expect(checkout).toHaveTextContent("team=payments");
    expect(within(checkout).getByTestId("obs-rule-source")).toHaveTextContent("Your rule");
    expect(within(screen.getByTestId("obs-alert-WorkloadOOMKilled")).getByTestId("obs-rule-source")).toHaveTextContent("forge default · oom-killed");
    expect(screen.getByTestId("obs-alert-ServiceErrorRateHigh")).toHaveAttribute("data-state", "pending");
    expect(screen.getByTestId("obs-alerts-tab-firing")).toHaveTextContent("2");

    await user.click(within(checkout).getByRole("button", { name: "Show details of CheckoutP95High" }));
    const details = within(checkout).getByTestId("obs-alert-details");
    expect(within(details).getByRole("link", { name: /wiki\.example\.com/ })).toHaveAttribute("href", "https://wiki.example.com/runbooks/checkout");

    // Attribute filters and group-by are telemetry concepts; the env picker stays.
    expect(screen.getByTestId("obs-env-picker")).toBeInTheDocument();
    expect(screen.queryByTestId("obs-add-filter")).not.toBeInTheDocument();
  });

  it("pinned to one environment, shows only what fires there", async () => {
    renderPanel(createFixtureApi(), { view: "alerts" }, PROD);
    await screen.findByTestId("obs-alert-CheckoutP95High");
    expect(screen.queryByTestId("obs-alert-ServiceErrorRateHigh")).not.toBeInTheDocument();
  });

  it("says nothing fires, and how many rules are watching, when it is quiet", async () => {
    renderPanel(createFixtureApi({ alerting: "quiet" }), { view: "alerts" }, PROJECT);
    // staging's three defaults and prod's four rules; dev has observability off.
    expect(await screen.findByTestId("obs-alerts-none")).toHaveTextContent("7 rules are evaluated across 3 environments");
  });

  it("silences one alert from its row, then expires the silence", async () => {
    const user = userEvent.setup();
    const harness = renderPanel(createFixtureApi(), { view: "alerts" }, PROJECT);
    await user.click(await screen.findByTestId("obs-alert-silence-CheckoutP95High"));
    const dialog = await screen.findByTestId("obs-silence-dialog");
    const names = within(dialog)
      .getAllByRole("textbox", { name: /label$/ })
      .map((input) => (input as HTMLInputElement).value);
    // The rule first, then its own labels; never a reliant_* one.
    expect(names).toEqual(["alertname", "service", "severity", "team"]);
    await user.click(within(dialog).getByTestId("obs-silence-create"));
    expect(within(dialog).getByTestId("obs-silence-problems")).toHaveTextContent("Say why");
    await user.type(within(dialog).getByTestId("obs-silence-comment"), "rolling back checkout");
    await user.click(within(dialog).getByTestId("obs-silence-create"));

    await waitFor(() => expect(harness.current().alerts).toBe("silences"));
    const made = await screen.findByTestId("obs-silence-sil-3");
    expect(made).toHaveAttribute("data-state", "active");
    expect(made).toHaveTextContent('alertname="CheckoutP95High"');
    expect(made).toHaveTextContent("this organization only");
    expect(made).not.toHaveTextContent("reliant_org_id=");

    await user.click(within(made).getByTestId("obs-silence-expire-sil-3"));
    await waitFor(() => expect(screen.queryByTestId("obs-silence-sil-3")).not.toBeInTheDocument());
  });
});

describe("history", () => {
  it("lists state changes newest first", async () => {
    renderPanel(createFixtureApi(), { view: "alerts", alerts: "history", range: "24h" }, PROJECT);
    const rows = await screen.findAllByTestId("obs-alert-event");
    expect(rows[0]).toHaveAttribute("data-transition", "pending");
    expect(rows[0]).toHaveTextContent("ServiceErrorRateHigh");
    expect(rows.map((r) => r.getAttribute("data-transition"))).toContain("resolved");
  });

  it("offers a wider window when nothing changed", async () => {
    const user = userEvent.setup();
    const harness = renderPanel(createFixtureApi({ alerting: "quiet" }), { view: "alerts", alerts: "history" }, PROJECT);
    const empty = await screen.findByTestId("obs-alert-history-empty");
    await user.click(within(empty).getByRole("button", { name: "Look at the last 7 days" }));
    expect(harness.current().range).toBe("7d");
  });
});

describe("rules are code", () => {
  it("shows a project rule with its query and the KCL that declares it", async () => {
    renderPanel(createFixtureApi(), { view: "alerts", alerts: "rules", rule: "prod/CheckoutP95High" }, PROJECT);
    const detail = await screen.findByTestId("obs-rule-detail");
    expect(detail).toHaveTextContent("quantile(0.95)(Duration)");
    expect(detail).toHaveTextContent("holds 10m before firing");
    const [snippet] = within(detail).getAllByTestId("obs-code-snippet");
    expect(snippet).toHaveTextContent("deploy/kcl/prod/main.k");
    expect(snippet).toHaveTextContent('"for" = "10m"');
    expect(snippet).toHaveTextContent("forge.AlertRule {");
    expect(screen.getByTestId("obs-rules-observability-off")).toHaveTextContent("Observability is off for dev");
  });

  it("shows a forge default with how to turn it off and how to tune it", async () => {
    renderPanel(createFixtureApi(), { view: "alerts", alerts: "rules", rule: "staging/ServiceSilent" }, PROJECT);
    const detail = await screen.findByTestId("obs-rule-detail");
    const snippets = within(detail).getAllByTestId("obs-code-snippet");
    expect(snippets).toHaveLength(2);
    expect(snippets[0]).toHaveTextContent('"service-silent": forge.AlertDefault {enabled = False}');
    expect(snippets[1]).toHaveTextContent('"for" = "15m"');
  });

  it("stays readable when alerting is not running: the rules come from the release shape", async () => {
    const user = userEvent.setup();
    renderPanel(createFixtureApi({ alerting: "not-configured" }), { view: "alerts" }, PROJECT);
    const frame = await screen.findByTestId("obs-alerting-not-configured");
    expect(frame).toHaveTextContent("already declare 7 rules");
    await user.click(within(frame).getByRole("button", { name: "See the rules" }));
    expect(await screen.findByTestId("obs-rules")).toBeInTheDocument();
  });
});

describe("a member who is not an admin", () => {
  it("turns the area read-only after the first refusal, and says why", async () => {
    const user = userEvent.setup();
    renderPanel(createFixtureApi({ alerting: "viewer" }), { view: "alerts", alerts: "silences" }, PROJECT);
    await user.click(await screen.findByTestId("obs-silence-new"));
    const dialog = await screen.findByTestId("obs-silence-dialog");
    await user.type(within(dialog).getByRole("textbox", { name: "Matcher 1 value" }), "ServiceSilent");
    await user.type(within(dialog).getByTestId("obs-silence-comment"), "maintenance");
    await user.click(within(dialog).getByTestId("obs-silence-create"));
    expect(await within(dialog).findByTestId("obs-silence-problems")).toHaveTextContent("needs the organization admin role");
    expect(screen.getByTestId("obs-alerts-read-only")).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(screen.getByTestId("obs-silence-new")).toBeDisabled();
    // Reading is untouched.
    await user.click(screen.getByTestId("obs-alerts-tab-receivers"));
    expect(await screen.findByTestId("obs-receiver-oncall-slack")).toBeInTheDocument();
    expect(screen.getByTestId("obs-receiver-new")).toBeDisabled();
  });
});

describe("receivers", () => {
  it("lists each receiver with its destination, whether a secret is stored, and who routes to it", async () => {
    renderPanel(createFixtureApi(), { view: "alerts", alerts: "receivers" }, PROJECT);
    const slack = await screen.findByTestId("obs-receiver-oncall-slack");
    expect(slack).toHaveTextContent("#oncall");
    expect(within(slack).getByTestId("obs-receiver-secret")).toHaveTextContent("secret set");
    expect(within(slack).getByTestId("obs-receiver-routed")).toHaveTextContent("Routed from prod");
    expect(within(screen.getByTestId("obs-receiver-reliant-inapp")).getByTestId("obs-receiver-routed")).toHaveTextContent("Routed from staging and prod");
    expect(within(screen.getByTestId("obs-receiver-team-email")).getByTestId("obs-receiver-routed")).toHaveTextContent("No environment routes here yet");
    // The in-app receiver is built in: it can be tested, not changed.
    expect(screen.queryByTestId("obs-receiver-edit-reliant-inapp")).not.toBeInTheDocument();
    // staging declares no routes, so it is on forge's pair — and nothing is named "slack".
    expect(screen.getByTestId("obs-receiver-missing-slack")).toHaveTextContent("staging sends alerts to slack");
  });

  it("sets up a receiver a route already names, with its secret write-only, and sends it a test", async () => {
    const user = userEvent.setup();
    renderPanel(createFixtureApi(), { view: "alerts", alerts: "receivers" }, PROJECT);
    const missing = await screen.findByTestId("obs-receiver-missing-slack");
    await user.click(within(missing).getByRole("button", { name: "Set up slack" }));
    const dialog = await screen.findByTestId("obs-receiver-dialog");
    expect(within(dialog).getByTestId("obs-receiver-name")).toHaveValue("slack");
    expect(within(dialog).getByTestId("obs-receiver-secret-input")).toHaveAttribute("type", "password");
    await user.type(within(dialog).getByTestId("obs-receiver-secret-input"), "http://hooks.slack.com/x");
    await user.click(within(dialog).getByTestId("obs-receiver-save"));
    expect(within(dialog).getByTestId("obs-receiver-problems")).toHaveTextContent("https URL");
    await user.clear(within(dialog).getByTestId("obs-receiver-secret-input"));
    await user.type(within(dialog).getByTestId("obs-receiver-secret-input"), "https://hooks.slack.com/services/T/B/x");
    await user.type(within(dialog).getByTestId("obs-receiver-target"), "#alerts");
    await user.click(within(dialog).getByTestId("obs-receiver-save"));

    const made = await screen.findByTestId("obs-receiver-slack");
    expect(made).toHaveTextContent("#alerts");
    expect(within(made).getByTestId("obs-receiver-routed")).toHaveTextContent("Routed from staging");
    expect(screen.queryByTestId("obs-receivers-missing")).not.toBeInTheDocument();

    // A receiver created a moment ago is not in Alertmanager's running config yet.
    await user.click(within(made).getByTestId("obs-receiver-test-slack"));
    const result = await within(made).findByTestId("obs-receiver-test-result");
    expect(result).toHaveAttribute("data-live", "false");
    expect(result).toHaveTextContent("isn't live yet");
    const old = screen.getByTestId("obs-receiver-oncall-slack");
    await user.click(within(old).getByTestId("obs-receiver-test-oncall-slack"));
    expect(await within(old).findByTestId("obs-receiver-test-result")).toHaveTextContent("It should reach #oncall");
  });

  it("will not delete a receiver a route still names, and says where", async () => {
    const user = userEvent.setup();
    renderPanel(createFixtureApi(), { view: "alerts", alerts: "receivers" }, PROJECT);
    const slack = await screen.findByTestId("obs-receiver-oncall-slack");
    await user.click(within(slack).getByTestId("obs-receiver-delete-oncall-slack"));
    await user.click(within(slack).getByTestId("obs-receiver-delete-confirm-oncall-slack"));
    expect(await within(slack).findByTestId("obs-receiver-problem")).toHaveTextContent("A route in the code still names oncall-slack (prod)");

    const email = screen.getByTestId("obs-receiver-team-email");
    await user.click(within(email).getByTestId("obs-receiver-delete-team-email"));
    await user.click(within(email).getByTestId("obs-receiver-delete-confirm-team-email"));
    await waitFor(() => expect(screen.queryByTestId("obs-receiver-team-email")).not.toBeInTheDocument());
  });

  it("edits settings and keeps the stored secret unless a new one is given", async () => {
    const user = userEvent.setup();
    const onCall = vi.fn();
    renderPanel(createFixtureApi({ onCall }), { view: "alerts", alerts: "receivers" }, PROJECT);
    await user.click(await screen.findByTestId("obs-receiver-edit-oncall-slack"));
    const dialog = await screen.findByTestId("obs-receiver-dialog");
    expect(within(dialog).queryByTestId("obs-receiver-name")).not.toBeInTheDocument();
    expect(within(dialog).getByTestId("obs-receiver-secret-input")).toHaveValue("");
    expect(within(dialog).getByTestId("obs-receiver-secret-input")).toHaveAttribute("placeholder", expect.stringContaining("leave empty to keep it"));
    await user.clear(within(dialog).getByTestId("obs-receiver-target"));
    await user.type(within(dialog).getByTestId("obs-receiver-target"), "#incidents");
    await user.click(within(dialog).getByTestId("obs-receiver-save"));
    expect(await screen.findByTestId("obs-receiver-oncall-slack")).toHaveTextContent("#incidents");
    const [, request] = onCall.mock.calls.find(([rpc]) => rpc === "UpdateReceiver")!;
    expect(request).toMatchObject({ settings: { channel: "#incidents" }, secret: "" });
  });
});

describe("notifications", () => {
  it("shows the unread count, marks one read and then all", async () => {
    const user = userEvent.setup();
    renderPanel(createFixtureApi(), { view: "overview" }, PROJECT);
    expect(await screen.findByTestId("obs-notifications-unread")).toHaveTextContent("2");
    await user.click(screen.getByTestId("obs-notifications"));
    const popover = screen.getByTestId("obs-notifications-popover");
    expect(within(popover).getByTestId("obs-notification-n2")).toHaveTextContent("checkout p95 is 1.42s");
    expect(within(popover).getByTestId("obs-notification-n2")).toHaveTextContent("prod");
    await user.click(within(popover).getByTestId("obs-notification-read-n3"));
    await waitFor(() => expect(screen.getByTestId("obs-notifications-unread")).toHaveTextContent("1"));
    await user.click(within(screen.getByTestId("obs-notifications-popover")).getByTestId("obs-notifications-mark-all"));
    await waitFor(() => expect(screen.queryByTestId("obs-notifications-unread")).not.toBeInTheDocument());
  });

  it("takes the reader to what is firing", async () => {
    const user = userEvent.setup();
    const harness = renderPanel(createFixtureApi(), { view: "logs", q: "timeout" }, PROJECT);
    await user.click(await screen.findByTestId("obs-notifications"));
    await user.click(screen.getByTestId("obs-notifications-open-alerts"));
    expect(harness.current()).toMatchObject({ view: "alerts" });
    expect(harness.current().q).toBeUndefined();
  });

  it("is not shown while alerting does not run for the org", async () => {
    renderPanel(createFixtureApi({ alerting: "not-configured" }), { view: "alerts" }, PROJECT);
    await screen.findByTestId("obs-alerting-not-configured");
    expect(screen.queryByTestId("obs-notifications")).not.toBeInTheDocument();
  });
});
