/**
 * The identity-link modal — the last navigation removed from the purchase path.
 *
 * What these tests are actually pinning, and why each one is written the way it
 * is rather than the obvious way:
 *
 *  1. **No guest affordance is reachable.** Asserted against a SPY on
 *     `signInAnonymously`, not only against the absence of the words "Skip for
 *     now". Copy changes; the dead end is the bug. A user who is already
 *     anonymous being offered "continue as guest" is returned to the exact
 *     state that blocked them.
 *
 *  2. **A successful email link dismisses without navigating.** Asserted
 *     against a spy on the router's `navigate` AND on `window.location`. The
 *     whole point of the modal is that dismissal IS the return path — a test
 *     that only checked the modal disappeared would pass against an
 *     implementation that navigated to `/upgrade` and unmounted everything.
 *
 *  3. **We LINK, we do not sign in.** `signUp` on an anonymous Supabase user
 *     upgrades it in place; `signIn` would strand the user's chats on the
 *     abandoned anonymous account. Pinned by asserting `signIn` is never
 *     called — the rendered output of the two is identical, so text cannot
 *     tell them apart.
 *
 *  4. **OAuth still round-trips `returnTo`, and still refuses an off-origin
 *     one.** OAuth is the honest limit: it redirects the whole window by
 *     nature, so it keeps `returnTo` rather than pretending otherwise.
 */

import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mockNavigate = vi.fn();

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => mockNavigate,
  useSearch: () => ({}),
}));

vi.mock("@/lib/logger", () => ({
  logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}));

const linkOAuthIdentity = vi.fn().mockResolvedValue(undefined);
const signUp = vi.fn();
const signIn = vi.fn();
const signInAnonymously = vi.fn();
const sendEmailVerificationOTP = vi.fn().mockResolvedValue(undefined);
const verifyEmailOTP = vi.fn().mockResolvedValue(undefined);

vi.mock("@/store/authStore", () => ({
  useAuthStore: () => ({
    user: { is_anonymous: true },
    initialized: true,
    loading: false,
    initialize: vi.fn(),
    linkOAuthIdentity,
    signUp,
    signIn,
    signInAnonymously,
    sendEmailVerificationOTP,
    verifyEmailOTP,
  }),
}));

const { LinkIdentityModal } = await import("../LinkIdentityModal");

const MESSAGE =
  "Before we take payment we need an account we can reach — a subscription tied to this browser session would be lost with it.";

function renderModal(props: Record<string, unknown> = {}) {
  return render(
    <LinkIdentityModal
      message={MESSAGE}
      returnTo="/settings/billing?tab=plans"
      onLinked={vi.fn()}
      onDismiss={vi.fn()}
      {...props}
    />,
  );
}

/** Fill the email + password fields with values that pass validation. */
function fillEmailForm() {
  fireEvent.change(screen.getByLabelText(/Email address/i), {
    target: { value: "someone@example.com" },
  });
  fireEvent.change(screen.getByLabelText("Password"), {
    target: { value: "Sup3rSecret!pass" },
  });
  fireEvent.change(screen.getByLabelText(/Confirm Password/i), {
    target: { value: "Sup3rSecret!pass" },
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  signUp.mockResolvedValue({ session: { access_token: "t" }, user: {} });
});

describe("LinkIdentityModal", () => {
  it("appears for an anonymous user, naming why they are being asked", () => {
    renderModal();

    // It is a real dialog, not an inline notice: the ask interrupts checkout.
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    // The owner's framing: they skipped sign-in earlier, and billing needs a
    // real account.
    expect(screen.getByText(/skipped sign/i)).toBeInTheDocument();
    expect(screen.getByText(MESSAGE)).toBeInTheDocument();
  });

  it("promises the existing account is kept, in the same words /upgrade uses", () => {
    // Two surfaces making the same promise differently is how a user starts
    // wondering which one is telling the truth.
    renderModal();

    expect(
      screen.getByText(/chats and projects stay exactly as they are/i),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/account you already have/i),
    ).toBeInTheDocument();
  });

  it("offers NO way back into an anonymous session", () => {
    renderModal();

    // The spy is the real assertion — copy is not a contract, and this is a
    // dead end rather than a cosmetic issue: the user is ALREADY anonymous, so
    // "continue as guest" returns them to the state that blocked them.
    for (const button of screen.getAllByRole("button")) {
      fireEvent.click(button);
    }
    expect(signInAnonymously).not.toHaveBeenCalled();

    expect(screen.queryByText(/skip for now/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/continue as guest/i)).not.toBeInTheDocument();
  });

  it("LINKS the email onto the current account rather than signing in", async () => {
    // signUp on an anonymous Supabase user upgrades it in place. signIn would
    // move them to a different account and strand the chats they came with —
    // and the two render identically, so only the spy can tell them apart.
    renderModal();
    fillEmailForm();
    fireEvent.click(screen.getByRole("button", { name: /Save my account/i }));

    await waitFor(() => expect(signUp).toHaveBeenCalled());
    expect(signUp).toHaveBeenCalledWith("someone@example.com", "Sup3rSecret!pass");
    expect(signIn).not.toHaveBeenCalled();
  });

  it("dismisses on a successful email link WITHOUT navigating anywhere", async () => {
    // Dismissal IS the return path. If this ever navigates, the user pays a
    // cold SPA boot in the middle of a purchase and the modal was pointless.
    const assign = vi.fn();
    const originalLocation = window.location;
    Object.defineProperty(window, "location", {
      configurable: true,
      writable: true,
      value: { ...originalLocation, assign },
    });

    const onLinked = vi.fn();
    renderModal({ onLinked });
    fillEmailForm();
    fireEvent.click(screen.getByRole("button", { name: /Save my account/i }));

    await waitFor(() => expect(onLinked).toHaveBeenCalled());
    expect(mockNavigate).not.toHaveBeenCalled();
    expect(assign).not.toHaveBeenCalled();

    Object.defineProperty(window, "location", {
      configurable: true,
      writable: true,
      value: originalLocation,
    });
  });

  it("verifies the emailed code in place, then dismisses — still no navigation", async () => {
    // Supabase with confirmations on returns no session until the OTP is
    // verified. The standalone EmailVerification screen navigates to "/" on
    // success, which inside a modal would tear down the checkout behind it —
    // so the modal owns this step.
    signUp.mockResolvedValue({ session: null, user: {} });

    const onLinked = vi.fn();
    renderModal({ onLinked });
    fillEmailForm();
    fireEvent.click(screen.getByRole("button", { name: /Save my account/i }));

    const codeInput = await screen.findByLabelText(/Verification code/i);
    expect(onLinked).not.toHaveBeenCalled(); // not linked until verified

    fireEvent.change(codeInput, { target: { value: "123456" } });
    fireEvent.click(screen.getByRole("button", { name: /Verify/i }));

    await waitFor(() => expect(verifyEmailOTP).toHaveBeenCalled());
    expect(verifyEmailOTP).toHaveBeenCalledWith("123456", "someone@example.com");
    await waitFor(() => expect(onLinked).toHaveBeenCalled());
    expect(mockNavigate).not.toHaveBeenCalled();
  });

  it("does not dismiss when the link fails", async () => {
    // A modal that closes on failure drops the user back at a checkout that
    // will refuse them again, with no explanation of what went wrong.
    signUp.mockRejectedValue(new Error("That email is already registered"));

    const onLinked = vi.fn();
    renderModal({ onLinked });
    fillEmailForm();
    fireEvent.click(screen.getByRole("button", { name: /Save my account/i }));

    await waitFor(() =>
      expect(screen.getByText(/already attached to another account/i)).toBeInTheDocument(),
    );
    expect(onLinked).not.toHaveBeenCalled();
  });

  it("keeps returnTo for OAuth, which genuinely redirects the window", async () => {
    // The honest limit. GitHub/Google take the whole window by nature, so the
    // user must be brought back — that is what returnTo is for, and it is NOT
    // used by the email path.
    renderModal({ returnTo: "/settings/billing?tab=plans" });

    fireEvent.click(screen.getByRole("button", { name: /Continue with GitHub/i }));

    await waitFor(() => expect(linkOAuthIdentity).toHaveBeenCalled());
    expect(linkOAuthIdentity).toHaveBeenCalledWith("github", {
      source: "link",
      returnTo: "/settings/billing?tab=plans",
    });
  });

  it("drops an off-origin returnTo rather than handing it to the provider", async () => {
    // `//evil.com` is protocol-relative: a browser reads it as another origin,
    // so `startsWith('/')` alone would wave it through.
    renderModal({ returnTo: "//evil.example.com/steal" });

    fireEvent.click(screen.getByRole("button", { name: /Continue with Google/i }));

    await waitFor(() => expect(linkOAuthIdentity).toHaveBeenCalled());
    expect(linkOAuthIdentity).toHaveBeenCalledWith("google", { source: "link" });
  });
});
