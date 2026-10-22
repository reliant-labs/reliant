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
import { fireEvent, render, screen } from "@testing-library/react";
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

/**
 * THE FORM NEVER ASKS HOW THE ENVIRONMENT RUNS (#353, design §10).
 *
 * It used to, with a radio group, whenever nothing had stated the
 * environment's kind — and the answer was passed to onSubmit as
 * `controlPlaneKind` and written into the environment's row.
 *
 * The kind is IMMUTABLE once that row exists. So a wrong answer produced an
 * environment that could only be abandoned, and the control plane had to
 * guard against it afterwards with a FailedPrecondition. These tests pin the
 * question's ABSENCE, which is the only way to pin it: nothing else in the
 * suite would fail if a well-meaning change reintroduced the fieldset as a
 * "fallback for when we can't tell".
 */
describe("the kind question is gone, in every state", () => {
  it("renders no kind field and no 'how does it run' question", () => {
    render(<SetSecretModal {...props()} />);

    expect(screen.queryByTestId("environment-kind-field")).toBeNull();
    expect(screen.queryByTestId("environment-kind-persistent")).toBeNull();
    expect(screen.queryByTestId("environment-kind-local")).toBeNull();
    // No radio of any kind, however it might be labelled or named.
    expect(screen.queryAllByRole("radio")).toHaveLength(0);
    expect(screen.getByTestId("set-secret-form").textContent).not.toMatch(/how does/i);
  });

  it("submits a name, a value and a cas — and nothing resembling a kind", async () => {
    const onSubmit = vi.fn().mockResolvedValue(undefined);
    render(<SetSecretModal {...props({ onSubmit })} />);

    await userEvent.type(screen.getByLabelText("Name"), "API_KEY");
    await userEvent.type(valueField(), "s3cret");
    await userEvent.click(screen.getByTestId("set-secret-submit"));

    // Exact shape, not a subset: a `controlPlaneKind` smuggled back onto the
    // payload would pass a toMatchObject and fail here.
    expect(onSubmit).toHaveBeenCalledWith({ name: "API_KEY", value: "s3cret", cas: 0 });
  });
});

/**
 * §10 STATE 3: no control-plane row and no Preview render, so the value has
 * nowhere to go.
 *
 * DISABLED BECAUSE THE DESTINATION IS UNKNOWN, not because the daemon is
 * offline. Keep that asymmetry in mind when reading these: an environment
 * built once never returns to state 3, however often the daemon comes and
 * goes, so this is not a "degraded" mode waiting on a connection.
 */
describe("state 3: the form is disabled with the remedy, and cannot submit", () => {
  const REMEDY =
    "staging hasn't been built yet. Run `forge env build staging`, or open Preview with your daemon online.";

  function disabled(overrides: Partial<SetSecretModalProps> = {}) {
    return props({ env: "staging", disabledReason: REMEDY, ...overrides });
  }

  it("states the remedy verbatim — a command and a tab, not a cause", () => {
    render(<SetSecretModal {...disabled()} />);

    const notice = screen.getByTestId("set-secret-disabled");
    expect(notice.textContent).toBe(REMEDY);
    // NOT framed as a daemon problem: the form is inert because nothing has
    // stated how this environment runs.
    expect(notice.textContent).not.toMatch(/daemon (is )?offline/i);
  });

  it("disables every input, so there is nothing to fill in", () => {
    render(<SetSecretModal {...disabled()} />);

    expect(screen.getByLabelText("Name")).toBeDisabled();
    expect(valueField()).toBeDisabled();
    expect(screen.getByTestId("set-secret-submit")).toBeDisabled();
  });

  it("refuses a submit of an ALREADY-FILLED form that becomes disabled", async () => {
    // The disabled attributes are the affordance; `canSubmit` is the GUARD,
    // and this is the case that tells them apart.
    //
    // Asserting on an empty disabled form proves nothing: a blank name and
    // value are refused anyway, so the test passes with the guard deleted
    // (verified by mutation). The state that distinguishes them is a form
    // the user has ALREADY filled in which then loses its destination —
    // reachable in the product, because the control-plane row this form
    // writes against is refetched while the modal is open.
    const onSubmit = vi.fn();
    const view = render(<SetSecretModal {...props({ env: "staging", onSubmit })} />);

    await userEvent.type(screen.getByLabelText("Name"), "API_KEY");
    await userEvent.type(valueField(), "s3cret");
    // Filled and submittable, so the refusal below is attributable to the
    // disabled state and nothing else.
    expect(screen.getByTestId("set-secret-submit")).toBeEnabled();

    view.rerender(<SetSecretModal {...disabled({ onSubmit })} />);

    fireEvent.submit(screen.getByTestId("set-secret-form"));

    // The value stays in the browser. Writing it would mean creating the
    // environment's row, which means guessing its immutable kind (#353).
    expect(onSubmit).not.toHaveBeenCalled();
  });

  it("still does not ask the kind — a disabled form is not a reason to guess", () => {
    render(<SetSecretModal {...disabled()} />);
    expect(screen.queryByTestId("environment-kind-field")).toBeNull();
    expect(screen.queryAllByRole("radio")).toHaveLength(0);
  });

  it("is enabled in states 1 and 2, where something stated the kind", async () => {
    // The contrast that makes the above meaningful. With no disabledReason
    // the form is an ordinary working form — which is §10 state 1, the
    // overwhelmingly common case.
    const onSubmit = vi.fn().mockResolvedValue(undefined);
    render(<SetSecretModal {...props({ onSubmit })} />);

    expect(screen.queryByTestId("set-secret-disabled")).toBeNull();
    expect(screen.getByLabelText("Name")).toBeEnabled();
    expect(valueField()).toBeEnabled();

    await userEvent.type(screen.getByLabelText("Name"), "API_KEY");
    await userEvent.type(valueField(), "v");
    await userEvent.click(screen.getByTestId("set-secret-submit"));
    expect(onSubmit).toHaveBeenCalled();
  });
});
