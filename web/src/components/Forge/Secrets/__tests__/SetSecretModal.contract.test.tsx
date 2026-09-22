// Copyright (c) 2025 Reliant Labs

/**
 * The create/set flow — the user's explicit complaint ("i have no way to create
 * them?").
 *
 * What is pinned here is that the value is WRITE-ONLY by construction and that
 * the user is told so in terms they can act on. A form that quietly refused to
 * show a value back would produce exactly the "is this broken?" confusion this
 * screen exists to remove.
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ConnectError, Code } from "@connectrpc/connect";

import { SetSecretModal } from "../SetSecretModal";
import type { SetSecretModalProps } from "../SetSecretModal";

function props(overrides: Partial<SetSecretModalProps> = {}): SetSecretModalProps {
  return {
    open: true,
    onClose: vi.fn(),
    env: "dev",
    existing: null,
    takenNames: [],
    onSubmit: vi.fn().mockResolvedValue(undefined),
    isSubmitting: false,
    error: null,
    ...overrides,
  };
}

/** The value field, found the way a user finds it: by its label. */
function valueField(): HTMLInputElement {
  return screen.getByLabelText("Value") as HTMLInputElement;
}

describe("write-only is legible, not mysterious", () => {
  it("tells the user they cannot read it back, and why, next to the field", () => {
    render(<SetSecretModal {...props()} />);
    const notice = screen.getByTestId("write-only-notice");
    expect(notice.textContent).toMatch(/will not be able to read this back/i);
    // The WHY matters: a user told "we chose not to show you this" asks for the
    // setting that turns it on. A user told the credential cannot read it
    // understands there is no such setting.
    expect(notice.textContent).toMatch(/refuses it read access/i);
    expect(notice.textContent).toMatch(/no reveal to enable/i);
  });

  it("masks the value and keeps it out of the browser's password manager", () => {
    render(<SetSecretModal {...props()} />);
    const field = valueField();
    expect(field).toHaveAttribute("type", "password");
    // A secret belonging in the managed store must not be silently copied into
    // a second store nobody audits.
    expect(field).toHaveAttribute("autoComplete", "new-password");
  });

  it("never pre-fills the value, even when updating an existing secret", () => {
    // There is nothing to pre-fill it FROM — no RPC returns a value — so a
    // populated field could only be a fabrication.
    render(
      <SetSecretModal {...props({ existing: { name: "DATABASE_URL", currentVersion: 3 } })} />
    );
    expect(valueField().value).toBe("");
  });
});

describe("create versus update", () => {
  it("sends cas 0 on create, asserting the secret must not already exist", async () => {
    const onSubmit = vi.fn().mockResolvedValue(undefined);
    render(<SetSecretModal {...props({ onSubmit })} />);

    await userEvent.type(screen.getByLabelText("Name"), "API_KEY");
    await userEvent.type(valueField(), "s3cret");
    await userEvent.click(screen.getByTestId("set-secret-submit"));

    expect(onSubmit).toHaveBeenCalledWith({ name: "API_KEY", value: "s3cret", cas: 0 });
  });

  it("sends the current version as cas on update, so a concurrent write cannot be clobbered", async () => {
    const onSubmit = vi.fn().mockResolvedValue(undefined);
    render(
      <SetSecretModal
        {...props({ existing: { name: "API_KEY", currentVersion: 3 }, onSubmit })}
      />
    );

    await userEvent.type(valueField(), "rotated");
    await userEvent.click(screen.getByTestId("set-secret-submit"));

    expect(onSubmit).toHaveBeenCalledWith({ name: "API_KEY", value: "rotated", cas: 3 });
  });

  it("locks the name on update, because a name is the secret's identity", () => {
    render(<SetSecretModal {...props({ existing: { name: "API_KEY", currentVersion: 1 } })} />);
    expect(screen.getByLabelText("Name")).toBeDisabled();
  });
});

describe("refusals happen before the round trip", () => {
  it("refuses a name that already exists", async () => {
    const onSubmit = vi.fn();
    render(<SetSecretModal {...props({ takenNames: ["API_KEY"], onSubmit })} />);

    await userEvent.type(screen.getByLabelText("Name"), "API_KEY");
    await userEvent.type(valueField(), "x");
    await userEvent.click(screen.getByTestId("set-secret-submit"));

    expect(onSubmit).not.toHaveBeenCalled();
  });

  it("refuses a name that is not a valid environment variable", async () => {
    const onSubmit = vi.fn();
    render(<SetSecretModal {...props({ onSubmit })} />);

    await userEvent.type(screen.getByLabelText("Name"), "lower-case");
    await userEvent.type(valueField(), "x");
    await userEvent.click(screen.getByTestId("set-secret-submit"));

    expect(onSubmit).not.toHaveBeenCalled();
  });

  it("will not submit an empty value", async () => {
    const onSubmit = vi.fn();
    render(<SetSecretModal {...props({ onSubmit })} />);
    await userEvent.type(screen.getByLabelText("Name"), "API_KEY");
    await userEvent.click(screen.getByTestId("set-secret-submit"));
    expect(onSubmit).not.toHaveBeenCalled();
  });
});

describe("failure reporting", () => {
  it("explains a cas rejection as a concurrent edit rather than a precondition code", async () => {
    render(
      <SetSecretModal
        {...props({
          existing: { name: "API_KEY", currentVersion: 3 },
          error: new ConnectError("cas mismatch", Code.FailedPrecondition),
        })}
      />
    );
    const alert = screen.getByTestId("set-secret-error");
    expect(alert.textContent).toMatch(/someone else wrote a new version/i);
    expect(alert.textContent).toMatch(/nothing was saved/i);
  });

  it("never echoes the submitted value into an error message", async () => {
    const { container } = render(
      <SetSecretModal {...props({ error: new Error("upstream unavailable") })} />
    );
    await userEvent.type(valueField(), "SUPERSECRETVALUE");
    // The value exists in the input's state, but must appear nowhere in the
    // rendered TEXT — not in an error, a title, or a debug render.
    expect(container.textContent).not.toContain("SUPERSECRETVALUE");
  });
});

describe("environment safety", () => {
  it("names the environment being written to", () => {
    // Writing a production value into dev (or the reverse) is the expensive
    // mistake this form can cause; the only defence is saying which is selected.
    render(<SetSecretModal {...props({ env: "prod" })} />);
    expect(screen.getByTestId("set-secret-form").textContent).toMatch(/Writing to\s*prod/);
  });
});
