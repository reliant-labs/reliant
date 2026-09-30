/**
 * Two prod bugs reported against this component, both visible in one
 * screenshot.
 *
 * 1. CONTRAST. The install button is an <a>, and index.css carries a global
 *    `a:not(.no-color) { color: hsl(var(--primary)) }`. That selector's
 *    specificity (0,1,1) beats a utility class (0,1,0), so the anchor lost its
 *    `text-primary-foreground` and got repainted in --primary — the same colour
 *    as the `bg-primary` it sits on. In dark mode that is white text on a
 *    near-white pill: a button you can barely read.
 *
 *    jsdom does not apply stylesheets, so the assertion here is on the opt-out
 *    class rather than a computed colour. That is the actual contract: the
 *    escape hatch has to be present by name, and the same fix already appears
 *    in CopilotDevicePanel and OnboardingChecklist.
 *
 * 2. A FALSE NEGATIVE CLAIM. "No GitHub accounts connected — the Reliant GitHub
 *    App isn't installed on any account yet" rendered directly BELOW a
 *    populated list of the user's repositories. An empty installations array
 *    does not mean zero installations: the control plane only enumerates them
 *    for a GitHub App user token, and returns empty for an OAuth-App token, a
 *    PAT, or a failed /user/installations probe. Repos on screen are proof that
 *    access exists, so the negative claim is simply false there.
 */

import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { ManageGitHubAccess } from "../ManageGitHubAccess";
import type { GitAppInstallation } from "../../../services/controlPlane/git";

const INSTALL_URL = "https://github.com/apps/reliant-labs/installations/new";

function installation(login: string): GitAppInstallation {
  return {
    accountLogin: login,
    accountType: "Organization",
    avatarUrl: "",
    configureUrl: `https://github.com/organizations/${login}/settings/installations/1`,
    repositorySelection: "selected",
  };
}

describe("ManageGitHubAccess — the install CTA is readable", () => {
  it("opts the filled anchor out of the global link colour", () => {
    render(<ManageGitHubAccess installUrl={INSTALL_URL} installations={[]} />);

    const cta = screen.getByRole("link", { name: /install the github app/i });

    // Without this class the global `a:not(.no-color)` rule wins and repaints
    // the label in --primary, which is the colour of its own background.
    expect(cta).toHaveClass("no-color");
    expect(cta).toHaveClass("bg-primary");
    expect(cta).toHaveClass("text-primary-foreground");
  });

  it("opts the quieter outline variant out too", () => {
    render(
      <ManageGitHubAccess
        installUrl={INSTALL_URL}
        installations={[installation("reliant-labs")]}
      />,
    );

    const cta = screen.getByRole("link", {
      name: /add account or choose repositories/i,
    });

    expect(cta).toHaveClass("no-color");
    expect(cta).toHaveClass("text-foreground");
  });

  it("opts the per-installation Configure links out as well", () => {
    render(
      <ManageGitHubAccess
        installUrl={INSTALL_URL}
        installations={[installation("reliant-labs")]}
      />,
    );

    expect(screen.getByRole("link", { name: /configure/i })).toHaveClass(
      "no-color",
    );
  });
});

describe("ManageGitHubAccess — it never claims zero accounts without evidence", () => {
  // THE REPORTED BUG. installationsKnown=false is what a caller passes when
  // repos are on screen, and it must suppress the negative claim entirely.
  it("shows the neutral CTA, not 'no accounts', when installations are unknown", () => {
    render(
      <ManageGitHubAccess
        installUrl={INSTALL_URL}
        installations={[]}
        installationsKnown={false}
      />,
    );

    expect(screen.queryByText(/no github accounts connected/i)).toBeNull();
    expect(
      screen.queryByText(/isn't installed on any account yet/i),
    ).toBeNull();

    expect(screen.getByText(/missing a repository\?/i)).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: /add account or choose repositories/i }),
    ).toBeInTheDocument();
  });

  it("still states it plainly when an empty list IS trustworthy", () => {
    // The empty state has to keep working — the fix is about entitlement to
    // the claim, not about deleting it.
    render(
      <ManageGitHubAccess
        installUrl={INSTALL_URL}
        installations={[]}
        installationsKnown
      />,
    );

    expect(
      screen.getByText(/no github accounts connected/i),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: /install the github app/i }),
    ).toBeInTheDocument();
  });

  it("lists the accounts it does know about", () => {
    render(
      <ManageGitHubAccess
        installUrl={INSTALL_URL}
        installations={[installation("reliant-labs")]}
      />,
    );

    expect(screen.getByText("reliant-labs")).toBeInTheDocument();
    expect(screen.queryByText(/no github accounts connected/i)).toBeNull();
  });

  it("renders nothing without an install URL, rather than a dead link", () => {
    const { container } = render(
      <ManageGitHubAccess installations={[]} installationsKnown />,
    );

    expect(container).toBeEmptyDOMElement();
  });
});
