/**
 * The connector consent form's defaults.
 *
 * These are deliberately permissive — every tool, the whole filesystem,
 * unrestricted shell — so a freshly-connected client can do what the user
 * asks without a round of "it says permission denied". That is a product
 * decision with a real security cost, so it is pinned here rather than left
 * to drift: a later refactor that quietly restores read-only defaults would
 * otherwise be invisible until someone hit a denial in the wild.
 */

import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

const listConnectors = vi.fn();
const listAvailableTools = vi.fn();
const listDaemons = vi.fn();
const authorizeClient = vi.fn();
const createDaemon = vi.fn();

vi.mock("../../../api/grpc-client", () => ({
  grpcClient: {
    connector: () => ({
      listConnectors,
      listAvailableTools,
      authorizeClient,
    }),
    daemonRegistry: () => ({ listDaemons }),
  },
}));

vi.mock("../../../services/controlPlane/daemon", () => ({
  createDaemon: (...a: unknown[]) => createDaemon(...a),
}));

vi.mock("../../../hooks/useOnboardingQueries", () => ({
  isReasonedQuotaError: () => false,
}));

const getCredential = vi.fn();
const cloneRepo = vi.fn();
vi.mock("../../../services/controlPlane/git", () => ({
  gitService: {
    getCredential: (...a: unknown[]) => getCredential(...a),
    getOAuthURL: () => "https://cp.example/auth/github/authorize",
    cloneRepo: (...a: unknown[]) => cloneRepo(...a),
  },
}));

// The real picker fetches the user's repo list over the network; the behaviour
// under test is what the consent form does with a selection.
vi.mock("../../Projects/RepoSelector", () => ({
  RepoSelector: ({
    onSelect,
    oauthReturnTo,
  }: {
    onSelect: (r: unknown) => void;
    oauthReturnTo?: string;
  }) => (
    <div>
      <span data-testid="repo-oauth-return">{oauthReturnTo}</span>
      <button
        onClick={() =>
          onSelect({
            fullName: "acme/api",
            cloneUrl: "https://github.com/acme/api.git",
            defaultBranch: "main",
          })
        }
      >
        pick acme/api
      </button>
    </div>
  ),
}));

vi.mock("../../../lib/supabase", () => ({
  supabase: {
    auth: {
      getSession: async () => ({
        data: { session: { access_token: "jwt-abc" } },
      }),
    },
  },
}));

import { ConnectorConsent } from "../ConnectorConsent";
import { ConnectorExecMode } from "../../../gen/reliant/v1/connector_pb";

const TOOLS = [
  { name: "read_file", description: "Read a file", mutating: false, needsExec: false },
  { name: "write_file", description: "Write a file", mutating: true, needsExec: false },
  { name: "run_command", description: "Run a command", mutating: true, needsExec: true },
];

beforeEach(() => {
  vi.clearAllMocks();
  listConnectors.mockResolvedValue({ connectors: [] });
  listAvailableTools.mockResolvedValue({ tools: TOOLS });
  listDaemons.mockResolvedValue({
    daemons: [
      {
        daemonId: "daemon-1",
        hostname: "sandbox-1",
        daemonType: "managed",
        projects: [],
      },
    ],
  });
  authorizeClient.mockResolvedValue({});
  createDaemon.mockResolvedValue(undefined);
  getCredential.mockResolvedValue({ hasToken: true, scopes: "repo" });
});

describe("git access on the consent screen", () => {
  const MACHINE = {
    daemonId: "daemon-1",
    hostname: "sandbox-1",
    daemonType: "managed",
    projects: [],
  };

  beforeEach(() => {
    listDaemons.mockResolvedValue({ daemons: [MACHINE] });
  });

  // There are no git tools in the connector catalog — git runs through the
  // shell against credentials the MACHINE already holds. A new machine has
  // none, so push and private clones fail with nothing on screen explaining
  // why. This is the only point where the user can fix that.
  it("explains missing git access once a machine is chosen", async () => {
    getCredential.mockResolvedValue({ hasToken: false, scopes: "" });

    render(<ConnectorConsent clientId="client-1" clientName="ChatGPT" />);
    await waitFor(() => expect(listDaemons).toHaveBeenCalled());
    await userEvent.selectOptions(
      screen.getAllByRole("combobox")[0],
      "daemon-1"
    );

    expect(await screen.findByText(/cannot clone private repositories/i)).toBeTruthy();
    expect(
      screen.getByRole("button", { name: /connect github/i })
    ).toBeTruthy();
  });

  // A connected account closes only half the gap: the machine can still be an
  // empty directory, and a model with nothing to read is as stuck as one that
  // cannot authenticate. So the clone offer appears even when GitHub is set up.
  it("offers a repo to clone even when GitHub is already connected", async () => {
    getCredential.mockResolvedValue({ hasToken: true, scopes: "repo" });

    render(<ConnectorConsent clientId="client-1" />);
    await waitFor(() => expect(getCredential).toHaveBeenCalled());
    await userEvent.selectOptions(
      screen.getAllByRole("combobox")[0],
      "daemon-1"
    );

    expect(screen.queryByRole("button", { name: /connect github/i })).toBeNull();
    expect(
      await screen.findByRole("button", { name: /clone a repo/i })
    ).toBeTruthy();
  });

  it("clones the chosen repo onto the selected machine", async () => {
    getCredential.mockResolvedValue({ hasToken: true, scopes: "repo" });
    cloneRepo.mockResolvedValue({ clonedPath: "/home/workspace/projects/api" });

    render(<ConnectorConsent clientId="client-1" />);
    await waitFor(() => expect(getCredential).toHaveBeenCalled());
    await userEvent.selectOptions(
      screen.getAllByRole("combobox")[0],
      "daemon-1"
    );

    await userEvent.click(
      await screen.findByRole("button", { name: /clone a repo/i })
    );
    await userEvent.click(await screen.findByText("pick acme/api"));

    await waitFor(() => expect(cloneRepo).toHaveBeenCalled());
    expect(cloneRepo.mock.calls[0][0]).toMatchObject({
      daemonId: "daemon-1",
      gitRepo: "https://github.com/acme/api.git",
      gitBranch: "main",
      // The destination is a contract with the workspace image — the daemon's
      // project resolver looks under this root.
      path: "/home/workspace/projects/api",
    });

    expect(await screen.findByText(/cloned/i)).toBeTruthy();
  });

  // The grant is what the user came here to authorize. A failed checkout is a
  // missing convenience, not a reason to lose the authorization.
  it("keeps the flow usable when a clone fails", async () => {
    getCredential.mockResolvedValue({ hasToken: true, scopes: "repo" });
    cloneRepo.mockRejectedValue(new Error("repository not found"));

    render(<ConnectorConsent clientId="client-1" clientName="ChatGPT" />);
    await waitFor(() => expect(getCredential).toHaveBeenCalled());
    await userEvent.selectOptions(
      screen.getAllByRole("combobox")[0],
      "daemon-1"
    );

    await userEvent.click(
      await screen.findByRole("button", { name: /clone a repo/i })
    );
    await userEvent.click(await screen.findByText("pick acme/api"));

    expect(await screen.findByText(/repository not found/i)).toBeTruthy();
    // Still authorizable.
    const submit = screen.getByRole("button", { name: /allow chatgpt/i });
    expect((submit as HTMLButtonElement).disabled).toBe(false);
  });

  it("returns to this exact page, preserving an in-flight authorization", async () => {
    getCredential.mockResolvedValue({ hasToken: false, scopes: "" });
    // The consent flow embeds this form and carries authorization_id in the
    // URL. Dropping it across the GitHub round trip would strand the
    // third-party client waiting for a code that never arrives.
    window.history.replaceState(
      {},
      "",
      "/oauth/consent?authorization_id=auth-123"
    );

    const assign = vi.fn();
    const original = window.location;
    Object.defineProperty(window, "location", {
      configurable: true,
      value: {
        ...original,
        pathname: "/oauth/consent",
        search: "?authorization_id=auth-123",
        set href(v: string) {
          assign(v);
        },
      },
    });

    try {
      render(<ConnectorConsent clientId="client-1" />);
      await waitFor(() => expect(getCredential).toHaveBeenCalled());
      await userEvent.selectOptions(
        screen.getAllByRole("combobox")[0],
        "daemon-1"
      );
      await userEvent.click(
        await screen.findByRole("button", { name: /connect github/i })
      );

      await waitFor(() => expect(assign).toHaveBeenCalled());
      const url = new URL(assign.mock.calls[0][0]);
      expect(url.searchParams.get("returnTo")).toBe(
        "/oauth/consent?authorization_id=auth-123"
      );
      expect(url.searchParams.get("token")).toBe("jwt-abc");
    } finally {
      Object.defineProperty(window, "location", {
        configurable: true,
        value: original,
      });
    }
  });
});

describe("creating a machine from the consent flow", () => {
  const NEW_DAEMON = {
    daemonId: "daemon-new",
    hostname: "sandbox-new",
    daemonType: "managed",
    projects: [],
  };

  it("offers creation instead of dead-ending when there are no machines", async () => {
    listDaemons.mockResolvedValue({ daemons: [] });

    render(<ConnectorConsent clientId="client-1" />);

    expect(
      await screen.findByRole("button", { name: /create a cloud machine/i })
    ).toBeTruthy();
    expect(screen.getByText(/no machines yet/i)).toBeTruthy();
  });

  it("provisions a managed machine and selects it once it registers", async () => {
    createDaemon.mockResolvedValue("daemon-new");
    // Empty until the machine registers, which is the gap the form polls
    // across: CreateDaemon returns before reliant knows about the machine.
    listDaemons
      .mockResolvedValueOnce({ daemons: [] })
      .mockResolvedValueOnce({ daemons: [] })
      .mockResolvedValue({ daemons: [NEW_DAEMON] });

    render(
      <ConnectorConsent
        clientId="client-1"
        clientName="ChatGPT"
        provisionTiming={{ pollMs: 5, timeoutMs: 2_000 }}
      />
    );

    await userEvent.click(
      await screen.findByRole("button", { name: /create a cloud machine/i })
    );

    await waitFor(() => expect(createDaemon).toHaveBeenCalled());

    // Managed, because the permissive defaults are only safe in a sandbox.
    expect(createDaemon.mock.calls[0][0]).toMatchObject({
      daemonType: 1,
      size: 1,
    });

    // Selected automatically, so the user does not have to notice it appear.
    await waitFor(() => {
      const select = screen.getAllByRole("combobox")[0] as HTMLSelectElement;
      expect(select.value).toBe("daemon-new");
    });
  });

  it("binds to the machine it created, not merely an unfamiliar one", async () => {
    // The bug this pins: selecting "any row I had not seen before" bound the
    // connector to a stale daemon from weeks earlier that happened to surface
    // in the same list, and every subsequent tool call failed against it.
    const STALE = {
      daemonId: "daemon-stale",
      hostname: "ws-old",
      daemonType: "managed",
      projects: [],
    };
    createDaemon.mockResolvedValue("daemon-new");
    listDaemons
      .mockResolvedValueOnce({ daemons: [] })
      // The stale row appears FIRST, so a set-difference match would take it.
      .mockResolvedValueOnce({ daemons: [STALE] })
      .mockResolvedValue({ daemons: [STALE, NEW_DAEMON] });

    render(
      <ConnectorConsent
        clientId="client-1"
        provisionTiming={{ pollMs: 5, timeoutMs: 2_000 }}
      />
    );

    await userEvent.click(
      await screen.findByRole("button", { name: /create a cloud machine/i })
    );

    await waitFor(() => {
      const select = screen.getAllByRole("combobox")[0] as HTMLSelectElement;
      expect(select.value).toBe("daemon-new");
    });
  });

  it("reports a timeout rather than polling forever", async () => {
    // The machine never registers — the form must give up and say so.
    listDaemons.mockResolvedValue({ daemons: [] });

    render(
      <ConnectorConsent
        clientId="client-1"
        provisionTiming={{ pollMs: 5, timeoutMs: 20 }}
      />
    );

    await userEvent.click(
      await screen.findByRole("button", { name: /create a cloud machine/i })
    );

    expect(await screen.findByText(/longer than expected/i)).toBeTruthy();
  });

  it("surfaces a creation failure instead of spinning forever", async () => {
    listDaemons.mockResolvedValue({ daemons: [] });
    createDaemon.mockRejectedValue(new Error("plan limit reached"));

    render(<ConnectorConsent clientId="client-1" />);

    await userEvent.click(
      await screen.findByRole("button", { name: /create a cloud machine/i })
    );

    expect(await screen.findByText(/plan limit reached/i)).toBeTruthy();
  });
});

describe("connector consent defaults", () => {
  it("selects every tool, including mutating and shell ones", async () => {
    const { container } = render(
      <ConnectorConsent clientId="client-1" clientName="ChatGPT" />
    );

    // Every catalog entry ticked — not just the read-only subset.
    await waitFor(() => {
      const boxes = Array.from(
        container.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')
      );
      expect(boxes).toHaveLength(TOOLS.length);
      expect(boxes.every((b) => b.checked)).toBe(true);
    });
  });

  it("defaults the path root to the whole filesystem", async () => {
    render(<ConnectorConsent clientId="client-1" />);

    await waitFor(() => {
      const root = screen.getByDisplayValue("/");
      expect(root).toBeTruthy();
    });
  });

  it("defaults shell access to unrestricted", async () => {
    render(<ConnectorConsent clientId="client-1" />);

    await waitFor(() => {
      const select = screen.getByDisplayValue(
        "Any command, through a shell"
      ) as HTMLSelectElement;
      expect(Number(select.value)).toBe(ConnectorExecMode.UNRESTRICTED);
    });
  });

  it("submits those defaults without the user touching anything", async () => {
    render(<ConnectorConsent clientId="client-1" clientName="ChatGPT" />);

    // Only the machine has no default — it is the one field with no safe
    // guess, so the test supplies it and leaves everything else untouched.
    await waitFor(() => expect(listDaemons).toHaveBeenCalled());
    await waitFor(() =>
      expect(screen.getAllByRole("combobox").length).toBeGreaterThan(0)
    );
    await userEvent.selectOptions(
      screen.getAllByRole("combobox")[0],
      "daemon-1"
    );

    await userEvent.click(
      screen.getByRole("button", { name: /allow chatgpt/i })
    );

    await waitFor(() => expect(authorizeClient).toHaveBeenCalled());
    const sent = authorizeClient.mock.calls[0][0];
    expect(sent.newConnector.pathRoot).toBe("/");
    expect(sent.newConnector.execMode).toBe(ConnectorExecMode.UNRESTRICTED);
    expect([...sent.newConnector.allowedTools].sort()).toEqual(
      TOOLS.map((t) => t.name).sort()
    );
  });

  it("still lets the user narrow the grant", async () => {
    // Scoped to this render's container rather than the whole screen: other
    // suites in the same worker can leave mounted DOM behind, and a global
    // checkbox query then sees their inputs too.
    const { container } = render(<ConnectorConsent clientId="client-1" />);
    const boxes = () =>
      Array.from(
        container.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')
      );

    // Both conditions in one waitFor: the checkboxes mount before the default
    // "grant everything" selection is applied, so waiting only on the count
    // can observe them rendered-but-unchecked. Under full-suite load that gap
    // widened enough to make this the suite's only intermittent failure.
    await waitFor(() => {
      expect(boxes()).toHaveLength(TOOLS.length);
      expect(boxes().every((b) => b.checked)).toBe(true);
    });

    // Deselect-all is the escape hatch from the permissive default.
    const deselect = Array.from(
      container.querySelectorAll("button")
    ).find((b) => /deselect all/i.test(b.textContent ?? ""));
    expect(deselect).toBeTruthy();
    await userEvent.click(deselect!);

    // `waitFor` rather than a bare assertion: the click schedules a React state
    // update, and under full-suite load that commit can land after userEvent's
    // own act() settles. Asserting synchronously made this the suite's only
    // intermittent failure while passing every time in isolation.
    await waitFor(() => expect(boxes().every((b) => !b.checked)).toBe(true));
  });
});
