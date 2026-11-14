// Copyright (c) 2025 Reliant Labs
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

const track = vi.fn();
vi.mock("@/lib/analytics", () => ({ trackEvent: (...a: unknown[]) => track(...a) }));
vi.mock("@reliantlabs/forge-web-runtime", () => ({
  // Like the real one: parses the URL and adds rid to its query, leaving the #anchor last.
  withAttribution: (u: string) => {
    const url = new URL(u);
    if (!url.searchParams.has("rid")) url.searchParams.set("rid", "v1");
    return url.toString();
  },
}));

import { resetShellForTests } from "@/lib/shell";
import { deployHref, DeployYoursButton, DemoCta, SignUpLink, signupHref } from "../DemoCta";

beforeEach(() => {
  vi.clearAllMocks();
  resetShellForTests("demo");
});
afterEach(() => {
  cleanup();
  resetShellForTests();
});

describe("the demo's call to action", () => {
  it("leads with Deploy yours free, pointing at the landing start checklist with UTMs", () => {
    render(<DemoCta placement="end_card" />);
    const deploy = screen.getByTestId("demo-cta-deploy");
    expect(deploy).toHaveTextContent("Deploy yours free");
    const url = new URL(deploy.getAttribute("href")!);
    expect(url.origin + url.pathname).toBe("https://reliantlabs.io/products/forge");
    expect(url.hash).toBe("#start");
    expect(url.searchParams.get("utm_source")).toBe("demo");
    expect(url.searchParams.get("utm_campaign")).toBe("forge_demo");
    expect(url.searchParams.get("utm_content")).toBe("end_card");
    // The visitor id rides along with the UTMs, before the anchor.
    expect(url.searchParams.get("rid")).toBe("v1");
  });

  it("keeps Sign up free as a secondary, quiet link", () => {
    render(<DemoCta placement="end_card" />);
    const signup = screen.getByTestId("demo-cta-signup");
    expect(signup).toHaveTextContent("Sign up free");
    expect(signup.className).not.toMatch(/bg-primary/);
    expect(screen.getByTestId("demo-cta-deploy").className).toMatch(/bg-primary/);
    expect(signup.getAttribute("href")).toBe(signupHref("end_card"));
    expect(signupHref("banner")).toContain("/auth?mode=signup&journey=forge&utm_source=demo&utm_campaign=forge_demo&utm_content=banner");
  });

  it("records which button was clicked and where", () => {
    render(<DemoCta placement="end_card" />);
    for (const id of ["demo-cta-deploy", "demo-cta-signup"]) {
      const a = screen.getByTestId(id);
      a.addEventListener("click", (e) => e.preventDefault());
      fireEvent.click(a);
    }
    expect(track).toHaveBeenCalledWith("demo_cta_clicked", { cta: "deploy", placement: "end_card" });
    expect(track).toHaveBeenCalledWith("demo_cta_clicked", { cta: "signup", placement: "end_card" });
  });

  it("deployHref and signupHref differ only by destination", () => {
    expect(new URL(deployHref("banner")).hash).toBe("#start");
    expect(deployHref("banner")).not.toBe(signupHref("banner"));
  });

  it("shows nothing inside page cards on the demo host (the action row carries it)", () => {
    render(
      <>
        <DemoCta placement="page_card" />
        <DeployYoursButton placement="toast" />
        <SignUpLink placement="corner" />
      </>,
    );
    expect(screen.queryByTestId("demo-cta-deploy")).toBeNull();
    expect(screen.queryByTestId("demo-cta-signup")).toBeNull();
  });
});
