// Copyright (c) 2025 Reliant Labs

/**
 * Adding a domain is ONE step: the hostname and what it serves, together.
 *
 * The regression this pins is the two-step version — claim now, bind later —
 * which leaves the common case in the claimed-but-unbound state that serves
 * nothing and looks finished.
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { AddDomainDialog, type DomainTargetEnv } from "../AddDomainDialog";

const ENVS: DomainTargetEnv[] = [
  { name: "prod", environmentId: "env-prod", targets: ["web", "api"] },
  { name: "staging", environmentId: "env-staging", targets: ["web"] },
];

function setup(overrides: Partial<React.ComponentProps<typeof AddDomainDialog>> = {}) {
  const onSubmit = vi.fn().mockResolvedValue(undefined);
  render(
    <AddDomainDialog
      open
      onClose={vi.fn()}
      envs={ENVS}
      takenHostnames={["taken.example"]}
      onSubmit={onSubmit}
      isSubmitting={false}
      error={null}
      {...overrides}
    />
  );
  return { onSubmit };
}

describe("AddDomainDialog", () => {
  it("submits the hostname and the binding in one action", async () => {
    const user = userEvent.setup();
    const { onSubmit } = setup();

    await user.type(screen.getByLabelText(/hostname/i), "hounders.club");
    await user.selectOptions(screen.getByLabelText(/^target$/i), "web");
    await user.click(screen.getByRole("button", { name: /add domain/i }));

    expect(onSubmit).toHaveBeenCalledWith({
      hostname: "hounders.club",
      environmentId: "env-prod",
      target: "web",
      redirectTo: undefined,
    });
  });

  it("normalises a pasted hostname — case and a trailing dot", async () => {
    const user = userEvent.setup();
    const { onSubmit } = setup();

    await user.type(screen.getByLabelText(/hostname/i), "Hounders.Club.");
    await user.selectOptions(screen.getByLabelText(/^target$/i), "api");
    await user.click(screen.getByRole("button", { name: /add domain/i }));

    expect(onSubmit).toHaveBeenCalledWith(
      expect.objectContaining({ hostname: "hounders.club", target: "api" })
    );
  });

  it("offers redirect as a target kind, not a separate flow", async () => {
    const user = userEvent.setup();
    const { onSubmit } = setup();

    await user.type(screen.getByLabelText(/hostname/i), "www.hounders.club");
    await user.click(screen.getByLabelText(/redirect to another domain/i));
    await user.type(screen.getByLabelText(/^redirect to$/i), "hounders.club");
    await user.click(screen.getByRole("button", { name: /add domain/i }));

    expect(onSubmit).toHaveBeenCalledWith({
      hostname: "www.hounders.club",
      environmentId: "env-prod",
      target: undefined,
      redirectTo: "hounders.club",
    });
  });

  it("allows claiming with no target — a parked domain is a real case", async () => {
    const user = userEvent.setup();
    const { onSubmit } = setup();

    await user.type(screen.getByLabelText(/hostname/i), "later.example");
    await user.click(screen.getByLabelText(/nothing yet/i));
    await user.click(screen.getByRole("button", { name: /add domain/i }));

    expect(onSubmit).toHaveBeenCalledWith({
      hostname: "later.example",
      environmentId: undefined,
      target: undefined,
      redirectTo: undefined,
    });
  });

  it("refuses a duplicate before the round trip", async () => {
    const user = userEvent.setup();
    const { onSubmit } = setup();

    await user.type(screen.getByLabelText(/hostname/i), "taken.example");
    await user.tab();

    expect(screen.getByText(/already holds this domain/i)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /add domain/i }));
    expect(onSubmit).not.toHaveBeenCalled();
  });

  it("refuses a URL pasted in place of a hostname", async () => {
    const user = userEvent.setup();
    const { onSubmit } = setup();

    await user.type(screen.getByLabelText(/hostname/i), "https://hounders.club/app");
    await user.tab();

    expect(screen.getByText(/does not look like a hostname/i)).toBeInTheDocument();
    expect(onSubmit).not.toHaveBeenCalled();
  });

  it("refuses a self-redirect", async () => {
    const user = userEvent.setup();
    const { onSubmit } = setup();

    await user.type(screen.getByLabelText(/hostname/i), "hounders.club");
    await user.click(screen.getByLabelText(/redirect to another domain/i));
    await user.type(screen.getByLabelText(/^redirect to$/i), "hounders.club");
    await user.tab();

    expect(screen.getByText(/cannot redirect to itself/i)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /add domain/i }));
    expect(onSubmit).not.toHaveBeenCalled();
  });

  it("still lets a domain be claimed when no environment can be bound to", async () => {
    const user = userEvent.setup();
    const { onSubmit } = setup({ envs: [] });

    expect(screen.getByText(/you can still claim the domain/i)).toBeInTheDocument();
    await user.type(screen.getByLabelText(/hostname/i), "hounders.club");
    await user.click(screen.getByRole("button", { name: /add domain/i }));

    expect(onSubmit).toHaveBeenCalledWith(
      expect.objectContaining({ hostname: "hounders.club", environmentId: undefined })
    );
  });

  it("falls back to free text when an environment has no deployed targets", async () => {
    const user = userEvent.setup();
    const { onSubmit } = setup({
      envs: [{ name: "prod", environmentId: "env-prod", targets: [] }],
    });

    // A binding names a target that need not exist yet, so an env with
    // nothing deployed must still be bindable rather than a dead end.
    await user.type(screen.getByLabelText(/hostname/i), "hounders.club");
    await user.type(screen.getByLabelText(/^target$/i), "web");
    await user.click(screen.getByRole("button", { name: /add domain/i }));

    expect(onSubmit).toHaveBeenCalledWith(
      expect.objectContaining({ target: "web", environmentId: "env-prod" })
    );
  });
});
