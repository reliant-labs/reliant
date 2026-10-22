// Copyright (c) 2025 Reliant Labs

/**
 * ReleasesPages adapts the console to the pages: the roster becomes the
 * pages' environments (prod first, previews apart, local ones dropped), and a
 * promote request becomes the plan dialog — titled for what it does, naming
 * the env (D1) — or, with no Reliant project to plan against, a blocked
 * reason and no dialog. The pages themselves are stubbed to their props.
 */

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import type { PromoteControl, ReleasesListProps } from "../ReleasesList";
import type { ReleaseDetailProps } from "../ReleaseDetail";

const consoleState: { projectId: string | null } = { projectId: "proj-1" };

const liveEnv = (id: string, name: string, kind: string, release: string) => ({
  name,
  source: "backend",
  lifecycle: kind === "local" ? "local" : "deployed",
  forge: null,
  live: { id, name, kind, release, promotedAt: undefined, promotedByActor: "", promotedByUserId: "" },
});

vi.mock("../../Console/consoleContext", () => ({
  useForgeConsole: () => ({
    forgeProject: "control-plane",
    projectId: consoleState.projectId,
    page: { kind: "releases" },
    env: "prod",
    setEnv: vi.fn(),
    href: (page: { kind: string; version?: string }, extra?: Record<string, unknown>) =>
      `/p/${page.kind}${page.version ? `/${page.version}` : ""}${extra ? `?${new URLSearchParams(extra as Record<string, string>)}` : ""}`,
    roster: {
      isLoading: false,
      resolvingName: false,
      live: { data: { availability: "available", detail: "" } },
      envs: [
        liveEnv("env-dev", "dev", "persistent", "v2"),
        liveEnv("env-pr", "pr-9", "preview", "v2"),
        liveEnv("env-prod", "prod", "persistent", "v1"),
        liveEnv("env-local", "local", "local", ""),
      ],
    },
  }),
}));

vi.mock("@tanstack/react-router", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-router")>()),
  useNavigate: () => vi.fn(),
  useSearch: () => ({}),
  useParams: () => ({ version: "v1" }),
}));

const dialogProps = vi.fn();
vi.mock("../../Promote/PromoteDialog", () => ({
  PromoteDialog: (props: { title?: string; env: string; release: string; projectId: string }) => {
    dialogProps(props);
    return <div data-testid="plan-dialog">{props.title}</div>;
  },
}));

const listProps = vi.fn();
function PromoteButtons({ promote }: { promote: PromoteControl }) {
  return (
    <>
      <button onClick={() => promote.onPromote({ version: "v2", env: "prod", intent: "promote" })}>promote</button>
      <button onClick={() => promote.onPromote({ version: "20261018.084518-53649dd6416d", env: "prod", intent: "rollback" })}>rollback</button>
      {promote.blockedReason && <p data-testid="blocked">{promote.blockedReason}</p>}
    </>
  );
}
vi.mock("../ReleasesList", () => ({
  ReleasesList: (props: ReleasesListProps) => {
    listProps(props);
    return <PromoteButtons promote={props.promote} />;
  },
}));
const detailProps = vi.fn();
vi.mock("../ReleaseDetail", () => ({
  ReleaseDetail: (props: ReleaseDetailProps) => {
    detailProps(props);
    return <PromoteButtons promote={props.promote} />;
  },
}));

import { ReleasePage, ReleasesPage } from "../ReleasesPages";

beforeEach(() => {
  consoleState.projectId = "proj-1";
  dialogProps.mockClear();
  listProps.mockClear();
  detailProps.mockClear();
});

describe("ReleasesPage", () => {
  it("hands the list the recorded envs: prod first, previews apart, no local ones", () => {
    render(<ReleasesPage />);
    const { envs } = listProps.mock.calls.at(-1)![0] as ReleasesListProps;
    expect(envs.map((env) => [env.name, env.kind, env.current])).toEqual([
      ["prod", "long", "v1"],
      ["dev", "long", "v2"],
      ["pr-9", "preview", "v2"],
    ]);
  });

  it("opens the plan dialog for a promote, titled with the release and the env", async () => {
    render(<ReleasesPage />);
    expect(screen.queryByTestId("plan-dialog")).not.toBeInTheDocument();
    await userEvent.setup().click(screen.getByRole("button", { name: "promote" }));
    expect(screen.getByTestId("plan-dialog")).toHaveTextContent("Promote v2 to prod");
    expect(dialogProps).toHaveBeenLastCalledWith(expect.objectContaining({ projectId: "proj-1", env: "prod", release: "v2" }));
  });

  it("titles a rollback as one, naming the env first", async () => {
    render(<ReleasesPage />);
    await userEvent.setup().click(screen.getByRole("button", { name: "rollback" }));
    expect(screen.getByTestId("plan-dialog")).toHaveTextContent("Roll back prod to 20261018.084518");
  });

  it("with no Reliant project to plan against, says why and opens nothing", async () => {
    consoleState.projectId = null;
    render(<ReleasesPage />);
    expect(screen.getByTestId("blocked")).toHaveTextContent("Open the project in Reliant, with its daemon running, to promote from here.");
    await userEvent.setup().click(screen.getByRole("button", { name: "promote" }));
    expect(screen.queryByTestId("plan-dialog")).not.toBeInTheDocument();
  });
});

describe("ReleasePage", () => {
  it("reads the version from the path and links Activity pre-filtered to it", async () => {
    render(<ReleasePage />);
    const props = detailProps.mock.calls.at(-1)![0] as ReleaseDetailProps;
    expect(props.version).toBe("v1");
    expect(props.listHref).toBe("/p/releases");
    expect(props.activityHref("prod", "release:v1")).toBe("/forge/p/control-plane/activity?env=prod&target=release%3Av1");
    await userEvent.setup().click(screen.getByRole("button", { name: "rollback" }));
    expect(screen.getByTestId("plan-dialog")).toHaveTextContent("Roll back prod to 20261018.084518");
  });
});
