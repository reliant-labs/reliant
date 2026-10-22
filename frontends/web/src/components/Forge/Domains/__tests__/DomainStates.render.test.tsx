// Copyright (c) 2025 Reliant Labs

/**
 * Every acquisition state renders, and each one says something DIFFERENT
 * about whose turn it is.
 *
 * The bug this guards is not a crash. It is the far likelier one where a
 * screen collapses six states into "something is happening" — a tenant
 * staring at PENDING_DNS with no records, or at CONFLICT being told to
 * retry, is worse served than one with no UI at all, because they will wait
 * instead of acting. So the assertions are about the SENTENCES, not just the
 * presence of a badge.
 *
 * Also pinned here: the records table is rendered in every state including
 * LIVE (removing the records fails the next check), and the values shown are
 * the server's verbatim — never derived from the hostname's shape.
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";

import {
  DOMAIN_STATE_EXPLANATIONS,
  DOMAIN_STATE_LABELS,
  DOMAIN_STATE_NEXT_STEPS,
  type ForgeDomain,
} from "@/services/forge/domains";

import { DomainDetail } from "../DomainDetail";
import { DomainStateBadge } from "../DomainStateBadge";
import { DnsRecordsTable } from "../DnsRecordsTable";

/** An apex domain's records as the control plane actually returns them. */
const APEX_RECORDS = [
  { type: "A", name: "hounders.club", value: "34.63.203.181" },
  {
    type: "TXT",
    name: "_reliant-challenge.hounders.club",
    value: "reliant-verify-8f3a91c2e7b04d56",
  },
];

/** A subdomain's: a CNAME to the ingress name, not the address. */
const SUBDOMAIN_RECORDS = [
  { type: "CNAME", name: "www.hounders.club", value: "ingress.reliantlabs.dev" },
  {
    type: "TXT",
    name: "_reliant-challenge.www.hounders.club",
    value: "reliant-verify-1122334455667788",
  },
];

function domain(overrides: Partial<ForgeDomain> = {}): ForgeDomain {
  return {
    id: "dom-1",
    hostname: "hounders.club",
    state: "pending-dns",
    origin: "external",
    requiredRecords: APEX_RECORDS,
    lastError: "",
    binding: null,
    ...overrides,
  };
}

function renderDetail(d: ForgeDomain) {
  return render(
    <DomainDetail
      domain={d}
      envNameFor={(id) => (id === "env-prod" ? "prod" : id)}
      onVerify={vi.fn()}
      onRebind={vi.fn()}
      onUnbind={vi.fn()}
      onRemove={vi.fn()}
      isVerifying={false}
      actionError={null}
    />
  );
}

describe("domain state badges", () => {
  it.each([
    ["pending-dns"],
    ["verifying"],
    ["issuing"],
    ["live"],
    ["failed"],
    ["conflict"],
    ["unknown"],
  ] as const)("%s renders its own label", (state) => {
    render(<DomainStateBadge state={state} />);
    expect(screen.getByTestId(`domain-state-${state}`)).toHaveTextContent(
      DOMAIN_STATE_LABELS[state]
    );
  });

  it("gives every state a distinct label, so none of them collapse", () => {
    const labels = Object.values(DOMAIN_STATE_LABELS);
    expect(new Set(labels).size).toBe(labels.length);
  });
});

describe("PENDING_DNS", () => {
  it("tells the tenant to publish the records and that we check automatically", () => {
    renderDetail(domain({ state: "pending-dns" }));
    expect(screen.getByTestId("domain-next-step")).toHaveTextContent(
      /add these records at your dns provider/i
    );
    expect(screen.getByTestId("domain-next-step")).toHaveTextContent(/we check automatically/i);
  });

  it("shows the records, which are the whole of what to do next", () => {
    renderDetail(domain({ state: "pending-dns" }));
    const table = screen.getByTestId("dns-records-table");
    expect(within(table).getByText("34.63.203.181")).toBeInTheDocument();
    expect(within(table).getByText("_reliant-challenge.hounders.club")).toBeInTheDocument();
  });
});

describe("VERIFYING and ISSUING", () => {
  it("says the platform is acting, not the tenant", () => {
    renderDetail(domain({ state: "verifying" }));
    expect(screen.getByText(DOMAIN_STATE_EXPLANATIONS.verifying)).toBeInTheDocument();
    expect(screen.getByTestId("domain-next-step")).toHaveTextContent(/nothing to do/i);
  });

  it("ISSUING names the certificate as the thing being waited on", () => {
    renderDetail(domain({ state: "issuing" }));
    expect(screen.getByText(DOMAIN_STATE_EXPLANATIONS.issuing)).toHaveTextContent(/certificate/i);
  });
});

describe("LIVE", () => {
  it("still shows the records — removing them fails the next check", () => {
    renderDetail(
      domain({
        state: "live",
        liveSince: "2026-09-30T10:00:00.000Z",
        binding: {
          id: "bind-1",
          domainId: "dom-1",
          environmentId: "env-prod",
          target: "web",
          redirectTo: "",
        },
      })
    );
    expect(screen.getByTestId("dns-records-table")).toBeInTheDocument();
    expect(screen.getByTestId("domain-next-step")).toHaveTextContent(/keep the records published/i);
  });

  it("names the binding in the env name a human knows, not its id", () => {
    renderDetail(
      domain({
        state: "live",
        binding: {
          id: "bind-1",
          domainId: "dom-1",
          environmentId: "env-prod",
          target: "web",
          redirectTo: "",
        },
      })
    );
    expect(screen.getByTestId("domain-binding")).toHaveTextContent("web in prod");
  });

  it("describes a redirect binding as a redirect", () => {
    renderDetail(
      domain({
        hostname: "www.hounders.club",
        state: "live",
        requiredRecords: SUBDOMAIN_RECORDS,
        binding: {
          id: "bind-2",
          domainId: "dom-2",
          environmentId: "env-prod",
          target: "",
          redirectTo: "hounders.club",
        },
      })
    );
    expect(screen.getByTestId("domain-binding")).toHaveTextContent("Redirects to hounders.club");
  });
});

describe("FAILED", () => {
  it("quotes the checker's own reason and offers a retry", () => {
    renderDetail(
      domain({
        state: "failed",
        lastError: "no TXT record found at _reliant-challenge.hounders.club",
      })
    );
    expect(screen.getByTestId("domain-last-error")).toHaveTextContent(
      "no TXT record found at _reliant-challenge.hounders.club"
    );
    expect(screen.getByRole("button", { name: /check dns now/i })).toBeInTheDocument();
    expect(screen.getByTestId("domain-next-step")).toHaveTextContent(/check again/i);
  });
});

describe("CONFLICT", () => {
  it("explains that first-to-verify wins, and does NOT tell the tenant to retry", () => {
    renderDetail(domain({ state: "conflict" }));
    const explanation = screen.getByText(DOMAIN_STATE_EXPLANATIONS.conflict);
    expect(explanation).toHaveTextContent(/another organization proved ownership/i);
    // The distinction from FAILED is the whole reason CONFLICT is its own
    // state: waiting cannot fix it, so the next step must not suggest it.
    expect(screen.getByTestId("domain-next-step")).toHaveTextContent(/must remove it/i);
    expect(DOMAIN_STATE_NEXT_STEPS.conflict).not.toMatch(/try again|retry/i);
  });
});

describe("the records table", () => {
  it("renders exactly what the server sent, for an apex and a subdomain alike", () => {
    const { rerender } = render(<DnsRecordsTable records={APEX_RECORDS} />);
    expect(screen.getByTestId("dns-record-a")).toHaveTextContent("34.63.203.181");
    expect(screen.queryByTestId("dns-record-cname")).not.toBeInTheDocument();

    rerender(<DnsRecordsTable records={SUBDOMAIN_RECORDS} />);
    expect(screen.getByTestId("dns-record-cname")).toHaveTextContent("ingress.reliantlabs.dev");
    // The apex form must not survive the switch — nothing here is derived
    // from the hostname, so a stale A row would mean we computed it.
    expect(screen.queryByTestId("dns-record-a")).not.toBeInTheDocument();
  });

  it("explains why the TXT record exists, which is the one tenants skip", () => {
    render(<DnsRecordsTable records={APEX_RECORDS} />);
    expect(screen.getByTestId("dns-record-txt")).toHaveTextContent(/proves you own this domain/i);
  });

  it("offers a copy control for every name and value", () => {
    render(<DnsRecordsTable records={APEX_RECORDS} />);
    // Two records × (name + value).
    expect(screen.getAllByRole("button", { name: /^copy /i })).toHaveLength(4);
  });

  it("says so plainly when there are no records, rather than rendering an empty table", () => {
    render(<DnsRecordsTable records={[]} />);
    expect(screen.queryByTestId("dns-records-table")).not.toBeInTheDocument();
    expect(screen.getByText(/did not return any DNS records/i)).toBeInTheDocument();
  });
});
