import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { useProjectStore } from "@/store/projectStore";
import type { Project } from "@/hooks/project-queries";

const mocks = vi.hoisted(() => ({
  navigate: vi.fn(),
  search: {} as Record<string, unknown>,
  newChat: vi.fn(),
}));

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => mocks.navigate,
  useSearch: () => mocks.search,
}));
vi.mock("@/lib/analytics", () => ({ trackEvent: vi.fn() }));
// The composer is not under test: record the prefill and heading it was given.
vi.mock("../../Chat/MainNewChatView", () => ({
  MainNewChatView: (props: { composerPrefill?: { text: string }; heading?: React.ReactNode; welcomePanel?: React.ReactNode }) => {
    mocks.newChat(props);
    return (
      <div>
        {props.heading}
        <textarea aria-label="composer" defaultValue={props.composerPrefill?.text ?? ""} />
        {props.welcomePanel}
      </div>
    );
  },
}));

import { HomeNewChat } from "../HomeNewChat";

const composerText = () => (screen.getByLabelText("composer") as HTMLTextAreaElement).value;

const project = (name: string) =>
  ({
    id: "proj-1",
    user_id: "u",
    name,
    path: `/home/workspace/projects/${name}`,
    is_git_repo: true,
    is_forge: false,
    last_active: "2026-10-01T00:00:00Z",
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-01T00:00:00Z",
  }) as Project;

beforeEach(() => {
  vi.clearAllMocks();
  mocks.search = {};
  useProjectStore.setState({ currentProject: project("storefront") });
});

describe("HomeNewChat — the chat onboarding lands on", () => {
  it("opens with a first message about the user's project, ready to send", () => {
    mocks.search = { start: "chat" };
    render(<HomeNewChat />);

    expect(composerText()).toContain("storefront");
    expect(screen.getByRole("heading", { name: /what should we work on first/i })).toBeInTheDocument();
  });

  it("an existing repository gets the 'deploy with forge' prompt", () => {
    mocks.search = { start: "repo" };
    render(<HomeNewChat />);

    expect(composerText()).toMatch(/Deploy storefront with forge/);
    expect(screen.getByRole("heading", { name: /ready to deploy storefront/i })).toBeInTheDocument();
  });

  it("'build an app' still opens the app-building composer", () => {
    mocks.search = { start: "app" };
    render(<HomeNewChat />);

    expect(composerText()).toMatch(/Build me a web app with forge/);
    expect(screen.getByRole("heading", { name: /what do you want to build/i })).toBeInTheDocument();
  });

  it("keeps the tour one click away, opening the picker without the post-onboarding prompt", () => {
    mocks.search = { start: "chat" };
    render(<HomeNewChat />);

    fireEvent.click(screen.getByTestId("home-take-tour"));

    expect(mocks.navigate).toHaveBeenCalledTimes(1);
    const { search } = mocks.navigate.mock.calls[0][0] as { search: (prev: Record<string, unknown>) => Record<string, unknown> };
    expect(search({ start: "chat" })).toMatchObject({ tour: "pick:menu" });
  });

  it("an ordinary visit (no starter) is unchanged: empty composer and the build-an-app card", () => {
    render(<HomeNewChat />);

    expect(screen.getByLabelText("composer")).toHaveValue("");
    expect(screen.getByTestId("home-build-app")).toBeInTheDocument();
    expect(screen.queryByTestId("home-take-tour")).toBeNull();
  });

  it("ignores a start value it does not know", () => {
    mocks.search = { start: "bogus" };
    render(<HomeNewChat />);

    expect(screen.getByLabelText("composer")).toHaveValue("");
  });
});
