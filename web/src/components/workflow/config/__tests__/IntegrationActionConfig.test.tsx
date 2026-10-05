/**
 * An integration action's config (research/INTEGRATIONS_V1_BRIEF.md §3a):
 * the form is rendered from GetCatalogEntry(ref).params_schema through the
 * shared ProtoFieldRenderer, and every edit is written back to the step as
 * `with:` values — never hand-built YAML. The RPC clients are the only mocks.
 */

import { useState } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create } from "@bufbuild/protobuf";

vi.mock("../../../../lib/monacoManager", () => ({ useMonaco: () => null }));

import {
  CatalogConnectionRequirementSchema,
  CatalogEntryKind,
  CatalogEntrySchema,
  CatalogEntrySummarySchema,
  CatalogIntegrationSchema,
  GetCatalogEntryResponseSchema,
} from "@/gen/reliant/v1/catalog_pb";
import {
  ConnectionAuthKind,
  ConnectionSchema,
  ConnectionStatus,
  IntegrationAuthMethodSchema,
  ListConnectionsResponseSchema,
} from "@/gen/reliant/v1/connection_pb";
import { renderWithQuery } from "@/test/renderWithQuery";
import type { Step } from "@/types/workflow";

const getCatalogEntry = vi.fn();
const listConnections = vi.fn();

vi.mock("@/api/grpc-client", () => ({
  grpcClient: {
    catalog: () => ({ getCatalogEntry }),
    connection: () => ({ listConnections }),
  },
  getGRPCBaseURLPublic: () => null,
}));

import { IntegrationActionConfig } from "../IntegrationActionConfig";
import { getActionConnection, getActionParams, newActionStep } from "@/lib/actionNodeArgs";

const paramsSchema = {
  type: "object",
  required: ["owner", "repo", "title"],
  properties: {
    owner: { type: "string", description: "The account that owns the repository." },
    repo: { type: "string" },
    title: { type: "string", description: "The issue title." },
    body: { type: "string" },
    labels: { type: "array", items: { type: "string" } },
    draft: { type: "boolean" },
    priority: { type: "string", enum: ["low", "high"] },
    connection: { type: "string" },
  },
};

function catalogEntry(methods: Array<{ kind: ConnectionAuthKind; available?: boolean }>, opts: { required?: boolean } = {}) {
  return create(GetCatalogEntryResponseSchema, {
    entry: create(CatalogEntrySchema, {
      summary: create(CatalogEntrySummarySchema, {
        ref: "github/issue.create@1",
        kind: CatalogEntryKind.ACTION,
        id: "issue.create",
        displayName: "Create issue",
        summary: "Open a new issue in a repository.",
        integration: create(CatalogIntegrationSchema, { id: "github", version: 1, displayName: "GitHub", icon: "github", category: "engineering" }),
        connectionRequired: opts.required ?? true,
        mutates: true,
      }),
      description: "Open an issue in owner/repo.",
      paramsSchema,
      outputSchema: { type: "object", properties: { number: { type: "integer" } } },
      connection: create(CatalogConnectionRequirementSchema, {
        required: opts.required ?? true,
        methods: methods.map((m) => create(IntegrationAuthMethodSchema, { kind: m.kind, available: m.available ?? true })),
      }),
    }),
  });
}

function connection(id: string, name: string, opts: { isDefault?: boolean; status?: ConnectionStatus } = {}) {
  return create(ConnectionSchema, {
    id,
    integrationId: "github",
    authKind: ConnectionAuthKind.OAUTH2,
    name,
    accountLabel: "octocat",
    status: opts.status ?? ConnectionStatus.ACTIVE,
    isDefault: opts.isDefault ?? false,
  });
}

/** The real step flows through the config's onUpdate, like the builder's mutation path. */
function Harness({ initial, onStep }: { initial: Step; onStep: (step: Step) => void }) {
  const [step, setStep] = useState(initial);
  return (
    <IntegrationActionConfig
      step={step}
      onUpdate={(next) => {
        setStep(next);
        onStep(next);
      }}
    />
  );
}

function renderConfig(initial: Step = newActionStep("open_issue", "github/issue.create@1")) {
  let latest = initial;
  renderWithQuery(<Harness initial={initial} onStep={(s) => (latest = s)} />);
  return { latest: () => latest };
}

beforeEach(() => {
  getCatalogEntry.mockReset();
  listConnections.mockReset();
  listConnections.mockResolvedValue(create(ListConnectionsResponseSchema, { connections: [] }));
});

describe("IntegrationActionConfig", () => {
  it("renders the form from the params schema, without the reserved connection param", async () => {
    getCatalogEntry.mockResolvedValue(catalogEntry([{ kind: ConnectionAuthKind.OAUTH2 }]));
    renderConfig();

    expect(await screen.findByText("Create issue")).toBeInTheDocument();
    for (const label of ["Owner *", "Repo *", "Title *", "Body", "Labels", "Priority"]) {
      expect(screen.getByLabelText(label)).toBeInTheDocument();
    }
    expect(screen.getByRole("switch", { name: "Draft" })).toBeInTheDocument();
    expect(screen.queryByLabelText(/^Connection \*/)).not.toBeInTheDocument();
    expect(screen.getByText("Changes data in GitHub")).toBeInTheDocument();
    expect(screen.getByText(/Required: owner, repo, title/)).toBeInTheDocument();
    expect(getCatalogEntry).toHaveBeenCalledWith(expect.objectContaining({ ref: "github/issue.create@1" }));
  });

  it("writes typed values, lists and templates back as with: params", async () => {
    const user = userEvent.setup();
    getCatalogEntry.mockResolvedValue(catalogEntry([{ kind: ConnectionAuthKind.OAUTH2 }]));
    const { latest } = renderConfig();

    await user.type(await screen.findByLabelText("Owner *"), "acme");
    await user.type(screen.getByLabelText("Labels"), "bug, triage");
    await user.click(screen.getByRole("switch", { name: "Draft" }));
    await user.selectOptions(screen.getByLabelText("Priority"), "high");

    expect(getActionParams(latest())).toEqual({ owner: "acme", labels: ["bug", "triage"], draft: true, priority: "high" });
    expect(screen.getByText(/Required: repo, title/)).toBeInTheDocument();
  });

  it("reads existing params, including a template, back into the form", async () => {
    getCatalogEntry.mockResolvedValue(catalogEntry([{ kind: ConnectionAuthKind.OAUTH2 }]));
    renderConfig(
      newActionStep("open_issue", "github/issue.create@1", {
        owner: "acme",
        labels: ["bug"],
        title: "{{ trigger.payload.data.issue.title }}",
      }),
    );
    expect(await screen.findByLabelText("Owner *")).toHaveValue("acme");
    expect(screen.getByLabelText("Labels")).toHaveValue("bug");
    // A template switches its field into CEL mode (Monaco's plain fallback in jsdom).
    expect(screen.getByDisplayValue("{{ trigger.payload.data.issue.title }}")).toBeInTheDocument();
  });

  it("explains an action the catalog does not know", async () => {
    getCatalogEntry.mockRejectedValue(new Error("not found"));
    renderConfig(newActionStep("gone", "acme/removed@1"));
    expect(await screen.findByRole("alert")).toHaveTextContent("acme/removed@1 isn't in the integration catalog.");
  });

  describe("connection picker", () => {
    it("connected: offers the caller's connections and 'Use my default'", async () => {
      const user = userEvent.setup();
      getCatalogEntry.mockResolvedValue(catalogEntry([{ kind: ConnectionAuthKind.OAUTH2 }]));
      listConnections.mockResolvedValue(
        create(ListConnectionsResponseSchema, {
          connections: [connection("c1", "Work", { isDefault: true }), connection("c2", "Personal")],
        }),
      );
      const { latest } = renderConfig();

      const picker = await screen.findByLabelText("Connection");
      await within(picker).findByRole("option", { name: "Personal · octocat" });
      expect(within(picker).getByRole("option", { name: "Use my default" })).toBeInTheDocument();
      expect(within(picker).getByRole("option", { name: "Work · octocat (default)" })).toBeInTheDocument();

      await user.selectOptions(picker, "c2");
      expect(getActionConnection(latest())).toBe("c2");
      await user.selectOptions(picker, "");
      expect(getActionConnection(latest())).toBe("");
      expect(listConnections).toHaveBeenCalledWith(expect.objectContaining({ integrationId: "github" }));
    });

    it("unconnected: offers Connect, which opens the connect dialog", async () => {
      const user = userEvent.setup();
      getCatalogEntry.mockResolvedValue(catalogEntry([{ kind: ConnectionAuthKind.OAUTH2 }, { kind: ConnectionAuthKind.API_KEY }]));
      renderConfig();

      expect(await screen.findByText("GitHub isn't connected yet.")).toBeInTheDocument();
      await user.click(screen.getByRole("button", { name: "Connect GitHub" }));
      const dialog = await screen.findByRole("dialog");
      expect(within(dialog).getByText("Connect GitHub")).toBeInTheDocument();
      expect(within(dialog).getByRole("radio", { name: "Sign in" })).toBeInTheDocument();
      expect(within(dialog).getByRole("radio", { name: "API key" })).toBeInTheDocument();
    });

    it("delegated: says the connected account is used, with a link to Settings", async () => {
      getCatalogEntry.mockResolvedValue(
        catalogEntry([{ kind: ConnectionAuthKind.DELEGATED }, { kind: ConnectionAuthKind.OAUTH2, available: false }]),
      );
      renderConfig();

      expect(await screen.findByText("Uses your connected GitHub account.")).toBeInTheDocument();
      expect(screen.getByRole("link", { name: "Manage in Settings" })).toHaveAttribute("href", "/settings/git-connections");
      expect(screen.queryByRole("button", { name: /Connect/ })).not.toBeInTheDocument();
    });

    it("warns when the stored connection no longer exists", async () => {
      getCatalogEntry.mockResolvedValue(catalogEntry([{ kind: ConnectionAuthKind.OAUTH2 }]));
      listConnections.mockResolvedValue(create(ListConnectionsResponseSchema, { connections: [connection("c1", "Work")] }));
      const step = newActionStep("open_issue", "github/issue.create@1");
      const { withActionConnection } = await import("@/lib/actionNodeArgs");
      renderConfig(withActionConnection(step, "deleted-id"));

      expect(await screen.findByText(/This connection no longer exists/)).toBeInTheDocument();
    });
  });
});
