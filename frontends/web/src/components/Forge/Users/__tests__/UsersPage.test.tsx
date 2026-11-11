// Copyright (c) 2025 Reliant Labs

/**
 * The Users page is the Identity tab, scoped by the top bar: it resolves the
 * scope to one environment and hands it over in the page layout.
 */

import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { UsersPage } from "../UsersPage";

const state = vi.hoisted(() => ({
  console: {} as Record<string, unknown>,
  tab: vi.fn(),
}));

vi.mock("../../Console/consoleContext", () => ({ useForgeConsole: () => state.console }));
vi.mock("../../SourceNotices", () => ({ CloudNotice: () => null }));
vi.mock("@/components/forge-ui/link", () => ({ default: ({ href, children, className }: { href: string; children: ReactNode; className?: string }) => <a href={href} className={className}>{children}</a> }));
vi.mock("../../EnvPage/IdentityTab", () => ({
  IdentityTab: (props: Record<string, unknown>) => {
    state.tab(props);
    return <div data-testid="identity-tab-stub">{String(props.envName)}</div>;
  },
  Frame: ({ title, children, testId }: { title: string; children: ReactNode; testId: string }) => (
    <div data-testid={testId}>
      <p>{title}</p>
      {children}
    </div>
  ),
}));

const PROD = { id: "env-prod-id", name: "prod" };
const roster = (overrides: Record<string, unknown> = {}) => ({
  isLoading: false,
  resolvingName: false,
  live: { isLoading: false, data: { availability: "ok", envs: [PROD] } },
  envs: [
    { name: "prod", lifecycle: "deployed", live: PROD, forge: null, source: "backend" },
    { name: "dev", lifecycle: "local", live: null, forge: null, source: "checkout" },
  ],
  ...overrides,
});

beforeEach(() => {
  state.tab.mockReset();
  state.console = { env: "prod", roster: roster(), setEnv: vi.fn(), href: (page: { kind: string }) => `/forge/p/shop/${page.kind}` };
});

describe("UsersPage", () => {
  it("hands the scoped environment to the Identity tab in its page layout", () => {
    render(<UsersPage />);
    expect(screen.getByRole("heading", { name: "Users" })).toBeInTheDocument();
    expect(screen.getByTestId("identity-tab-stub")).toHaveTextContent("prod");
    const props = state.tab.mock.calls.at(-1)![0] as Record<string, unknown> & { envNameById: (id: string) => string | undefined };
    expect(props).toMatchObject({ envName: "prod", lifecycle: "deployed", live: PROD, layout: "page" });
    expect(props.envNameById("env-prod-id")).toBe("prod");
  });

  it("asks for one environment when the scope is all of them", async () => {
    const setEnv = vi.fn();
    state.console = { ...state.console, env: "all", setEnv };
    render(<UsersPage />);
    expect(screen.getByTestId("users-choose-env")).toBeInTheDocument();
    expect(state.tab).not.toHaveBeenCalled();
    await userEvent.click(screen.getByTestId("users-env-prod"));
    expect(setEnv).toHaveBeenCalledWith("prod");
    // A local environment is offered too: its tab says where its IdP is.
    expect(screen.getByTestId("users-env-dev")).toBeInTheDocument();
  });

  it("says so when the scope names no environment of this project", () => {
    state.console = { ...state.console, env: "qa" };
    render(<UsersPage />);
    expect(screen.getByTestId("users-env-unknown")).toHaveTextContent("There is no environment named qa");
    expect(state.tab).not.toHaveBeenCalled();
  });
});
