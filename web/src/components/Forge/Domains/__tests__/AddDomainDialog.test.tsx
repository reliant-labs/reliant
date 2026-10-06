// Copyright (c) 2025 Reliant Labs

/**
 * Adding a domain is ONE step: the hostname and what it serves, together.
 *
 * The regression this pins is the two-step version — claim now, bind later —
 * which leaves the common case in the claimed-but-unbound state that serves
 * nothing and looks finished. It also pins that the target is PICKED from
 * what the environment runs, never typed.
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { AddDomainDialog, type DomainTargetEnv } from "../AddDomainDialog";

const ENVS: DomainTargetEnv[] = [
  {
    name: "prod",
    environmentId: "env-prod",
    targets: [
      { name: "api", kind: "service" },
      { name: "web", kind: "static-site" },
    ],
  },
  { name: "staging", environmentId: "env-staging", targets: [{ name: "web", kind: "static-site" }] },
];

function setup(overrides: Partial<React.ComponentProps<typeof AddDomainDialog>> = {}) {
  const onSubmit = vi.fn().mockResolvedValue(undefined);
  render(
    <AddDomainDialog
      open
      onClose={vi.fn()}
      envs={ENVS}
      takenHostnames={["taken.example.com"]}
      onSubmit={onSubmit}
      isSubmitting={false}
      error={null}
      {...overrides}
    />
  );
  return { onSubmit };
}

function optionLabels(select: HTMLElement): string[] {
  return within(select)
    .getAllByRole("option")
    .map((option) => option.textContent ?? "");
}

describe("AddDomainDialog", () => {
  it("submits the hostname and the binding in one action", async () => {
    const user = userEvent.setup();
    const { onSubmit } = setup();

    await user.type(screen.getByLabelText(/hostname/i), "example.com");
    await user.selectOptions(screen.getByLabelText(/^target$/i), "web");
    await user.click(screen.getByRole("button", { name: /add domain/i }));

    expect(onSubmit).toHaveBeenCalledWith({
      hostname: "example.com",
      environmentId: "env-prod",
      target: "web",
      redirectTo: undefined,
    });
  });

  it("offers environments and their targets as selects, each target labelled with its kind", () => {
    setup();

    expect(optionLabels(screen.getByLabelText(/^environment$/i))).toEqual(["prod", "staging"]);
    expect(optionLabels(screen.getByLabelText(/^target$/i))).toEqual([
      "Choose a service or static site…",
      "api — service",
      "web — static site",
    ]);
    // Picked, never typed.
    expect(screen.getByLabelText(/^target$/i).tagName).toBe("SELECT");
  });

  it("re-reads the targets when the environment changes, and submits against that env", async () => {
    const user = userEvent.setup();
    const { onSubmit } = setup();

    await user.type(screen.getByLabelText(/hostname/i), "staging.example.com");
    await user.selectOptions(screen.getByLabelText(/^environment$/i), "env-staging");
    expect(optionLabels(screen.getByLabelText(/^target$/i))).toEqual([
      "Choose a service or static site…",
      "web — static site",
    ]);
    await user.selectOptions(screen.getByLabelText(/^target$/i), "web");
    await user.click(screen.getByRole("button", { name: /add domain/i }));

    expect(onSubmit).toHaveBeenCalledWith(
      expect.objectContaining({ environmentId: "env-staging", target: "web" })
    );
  });

  it("will not submit until a target is chosen", async () => {
    const user = userEvent.setup();
    const { onSubmit } = setup();

    await user.type(screen.getByLabelText(/hostname/i), "example.com");
    expect(screen.getByRole("button", { name: /add domain/i })).toBeDisabled();
    await user.click(screen.getByRole("button", { name: /add domain/i }));
    expect(onSubmit).not.toHaveBeenCalled();
  });

  it("normalises a pasted hostname — case and a trailing dot", async () => {
    const user = userEvent.setup();
    const { onSubmit } = setup();

    await user.type(screen.getByLabelText(/hostname/i), "App.Example.COM.");
    await user.selectOptions(screen.getByLabelText(/^target$/i), "api");
    await user.click(screen.getByRole("button", { name: /add domain/i }));

    expect(onSubmit).toHaveBeenCalledWith(
      expect.objectContaining({ hostname: "app.example.com", target: "api" })
    );
  });

  it("offers redirect as a target kind, not a separate flow", async () => {
    const user = userEvent.setup();
    const { onSubmit } = setup();

    await user.type(screen.getByLabelText(/hostname/i), "www.example.com");
    await user.click(screen.getByLabelText(/redirect to another domain/i));
    await user.type(screen.getByLabelText(/^redirect to$/i), "example.com");
    await user.click(screen.getByRole("button", { name: /add domain/i }));

    expect(onSubmit).toHaveBeenCalledWith({
      hostname: "www.example.com",
      environmentId: "env-prod",
      target: undefined,
      redirectTo: "example.com",
    });
  });

  it("allows claiming with no target — a parked domain is a real case", async () => {
    const user = userEvent.setup();
    const { onSubmit } = setup();

    await user.type(screen.getByLabelText(/hostname/i), "later.example.com");
    await user.click(screen.getByLabelText(/nothing yet/i));
    await user.click(screen.getByRole("button", { name: /add domain/i }));

    expect(onSubmit).toHaveBeenCalledWith({
      hostname: "later.example.com",
      environmentId: undefined,
      target: undefined,
      redirectTo: undefined,
    });
  });

  it("refuses a duplicate before the round trip", async () => {
    const user = userEvent.setup();
    const { onSubmit } = setup();

    await user.type(screen.getByLabelText(/hostname/i), "taken.example.com");
    await user.tab();

    expect(screen.getByText(/already holds this domain/i)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /add domain/i }));
    expect(onSubmit).not.toHaveBeenCalled();
  });

  it("refuses a URL pasted in place of a hostname", async () => {
    const user = userEvent.setup();
    const { onSubmit } = setup();

    await user.type(screen.getByLabelText(/hostname/i), "https://example.com/app");
    await user.tab();

    expect(screen.getByText(/does not look like a hostname/i)).toBeInTheDocument();
    expect(onSubmit).not.toHaveBeenCalled();
  });

  it("refuses a self-redirect", async () => {
    const user = userEvent.setup();
    const { onSubmit } = setup();

    await user.type(screen.getByLabelText(/hostname/i), "example.com");
    await user.click(screen.getByLabelText(/redirect to another domain/i));
    await user.type(screen.getByLabelText(/^redirect to$/i), "example.com");
    await user.tab();

    expect(screen.getByText(/cannot redirect to itself/i)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /add domain/i }));
    expect(onSubmit).not.toHaveBeenCalled();
  });

  it("still lets a domain be claimed when no environment can be bound to", async () => {
    const user = userEvent.setup();
    const { onSubmit } = setup({ envs: [] });

    expect(screen.getByText(/you can still claim the domain/i)).toBeInTheDocument();
    await user.type(screen.getByLabelText(/hostname/i), "example.com");
    await user.click(screen.getByRole("button", { name: /add domain/i }));

    expect(onSubmit).toHaveBeenCalledWith(
      expect.objectContaining({ hostname: "example.com", environmentId: undefined })
    );
  });

  it("explains an environment with nothing to serve instead of offering a text box", async () => {
    const user = userEvent.setup();
    const { onSubmit } = setup({
      envs: [{ name: "prod", environmentId: "env-prod", targets: [] }],
    });

    const empty = screen.getByTestId("domain-targets-empty");
    expect(empty).toHaveTextContent("Nothing in prod serves HTTP yet.");
    expect(empty).toHaveTextContent(/exposed port or a static site/i);
    expect(screen.queryByLabelText(/^target$/i)).not.toBeInTheDocument();

    // Nothing to bind to, so the workload mode cannot submit…
    await user.type(screen.getByLabelText(/hostname/i), "example.com");
    expect(screen.getByRole("button", { name: /add domain/i })).toBeDisabled();

    // …but redirecting and parking still work.
    await user.click(screen.getByLabelText(/nothing yet/i));
    await user.click(screen.getByRole("button", { name: /add domain/i }));
    expect(onSubmit).toHaveBeenCalledWith(
      expect.objectContaining({ hostname: "example.com", environmentId: undefined })
    );
  });

  it("says the targets are loading rather than that the env serves nothing", () => {
    setup({
      envs: [{ name: "prod", environmentId: "env-prod", targets: [], targetsLoading: true }],
    });

    expect(screen.getByTestId("domain-targets-loading")).toHaveTextContent("Reading what prod runs");
    expect(screen.queryByTestId("domain-targets-empty")).not.toBeInTheDocument();
  });

  describe("re-binding an existing domain", () => {
    it("fixes the hostname, pre-selects the current binding and submits the new target", async () => {
      const user = userEvent.setup();
      const { onSubmit } = setup({
        hostname: "app.example.com",
        current: { environmentId: "env-prod", target: "api", redirectTo: "" },
      });

      expect(screen.getByText("Change what app.example.com serves")).toBeInTheDocument();
      expect(screen.queryByLabelText(/hostname/i)).not.toBeInTheDocument();
      // Taking it off the air is "Stop serving", not a mode of this picker.
      expect(screen.queryByLabelText(/nothing yet/i)).not.toBeInTheDocument();
      expect(screen.getByLabelText(/^environment$/i)).toHaveValue("env-prod");
      expect(screen.getByLabelText(/^target$/i)).toHaveValue("api");

      await user.selectOptions(screen.getByLabelText(/^target$/i), "web");
      await user.click(screen.getByRole("button", { name: /save target/i }));

      expect(onSubmit).toHaveBeenCalledWith({
        hostname: "app.example.com",
        environmentId: "env-prod",
        target: "web",
        redirectTo: undefined,
      });
    });

    it("pre-selects a redirect binding as a redirect", () => {
      setup({
        hostname: "www.example.com",
        current: { environmentId: "env-staging", target: "", redirectTo: "example.com" },
      });

      expect(screen.getByLabelText(/redirect to another domain/i)).toBeChecked();
      expect(screen.getByLabelText(/^redirect to$/i)).toHaveValue("example.com");
      expect(screen.getByLabelText(/^environment$/i)).toHaveValue("env-staging");
    });
  });
});
