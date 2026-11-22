// Copyright (c) 2026 Reliant Labs

/**
 * CI run and issue URLs arrive in uploaded run metadata, which anyone holding
 * a project's upload token writes. OutLink makes only an https URL a link;
 * anything else (javascript:, data:, plain http, credentials in the URL) is
 * shown as text.
 */

import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";

import { OutLink, safeOutUrl } from "../TestsParts";

describe("safeOutUrl", () => {
  it("accepts an https URL on any host", () => {
    expect(safeOutUrl("https://github.com/acme/shopfront/actions/runs/4411")?.host).toBe("github.com");
    expect(safeOutUrl("https://ci.internal.example:8443/job/7")?.host).toBe("ci.internal.example:8443");
  });

  it.each(["javascript:alert(1)", "JavaScript:alert(1)", "data:text/html,<script>1</script>", "http://ci.example/run/1", "https://user:pass@ci.example/", "//ci.example/run", "/forge/p/x", "not a url", ""])(
    "refuses %j",
    (href) => {
      expect(safeOutUrl(href)).toBeNull();
    }
  );
});

describe("OutLink", () => {
  it("links an https URL in a new tab, naming its host", () => {
    render(<OutLink href="https://github.com/acme/shopfront/actions/runs/4411">#4411</OutLink>);
    const link = screen.getByRole("link", { name: /#4411/ });
    expect(link).toHaveAttribute("href", "https://github.com/acme/shopfront/actions/runs/4411");
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", "noopener noreferrer");
    expect(link).toHaveAttribute("title", "Opens github.com");
  });

  it("renders a javascript: URL as text, never a link", () => {
    render(
      <OutLink href="javascript:alert(document.cookie)" testId="ci">
        #1
      </OutLink>
    );
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
    expect(screen.getByTestId("ci")).toHaveTextContent("#1");
    expect(screen.getByTestId("ci")).toHaveAttribute("data-unsafe-url");
  });
});
