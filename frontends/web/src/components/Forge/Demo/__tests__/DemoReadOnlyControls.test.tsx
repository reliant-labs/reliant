// Copyright (c) 2025 Reliant Labs

/**
 * The demo console changes nothing, and says so on the control: every write
 * control on its tabs renders DISABLED with "Read-only demo" when the console
 * is read-only (ObsReadOnlyProvider, mounted by the demo shell), and as it
 * always did elsewhere. The transport refuses writes regardless
 * (demoTransport.ts); this is what a visitor sees.
 *
 *  - Secrets: Add secret (list and empty state), Set new version, and each
 *    version's Delete / Destroy.
 *  - Domains: Check DNS now, Change target, Stop serving, Remove domain.
 *  - Environments: Start / Stop environment, and no danger menu.
 *  - Releases, the Overview and a workload: Promote and Roll back, through
 *    the one promote flow (usePromoteFlow's blockedReason).
 */

import { describe, expect, it, vi } from "vitest";
import { render, renderHook, screen, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";

vi.mock("@/hooks/project-queries", async (importOriginal) => ({ ...(await importOriginal<typeof import("@/hooks/project-queries")>()), useProject: () => undefined }));
vi.mock("../../Console/consoleContext", () => ({ useForgeConsole: () => ({ projectId: "proj-1" }) }));

import type { ForgeDomain } from "@/services/forge/domains";
import type { ManagedSecretSummary } from "@/services/forge/secretStore";

import { DomainDetail } from "../../Domains/DomainDetail";
import { EnvLifecycleControls } from "../../EnvPage/EnvLifecycleControls";
import { ObsReadOnlyProvider } from "../../Observability/obsReadOnly";
import { usePromoteFlow } from "../../Promote/usePromoteFlow";
import { ManagedSecretsView, type ManagedSecretsViewProps } from "../../Secrets/ManagedSecretsView";

const REASON = "Read-only demo";
/** Words a write control's label starts with, on the console's tabs. */
const WRITE = /^(\+ ?)?(Add|Set|Save|Delete|Destroy|Undelete|Remove|Check DNS|Change target|Choose a target|Stop|Start|Promote|Roll back|Restart|Deploy|Bind|Unbind)/i;

function demo(children: ReactNode, readOnly = true) {
  const client = new QueryClient();
  return (
    <QueryClientProvider client={client}>
      <ObsReadOnlyProvider value={readOnly ? { readOnly: true, reason: REASON } : { readOnly: false }}>{children}</ObsReadOnlyProvider>
    </QueryClientProvider>
  );
}

/** Every button that writes, by its label. */
function writeButtons(root: HTMLElement = document.body): HTMLButtonElement[] {
  return within(root)
    .queryAllByRole("button")
    .filter((b): b is HTMLButtonElement => WRITE.test((b.textContent ?? "").trim()));
}

function expectAllLocked(buttons: HTMLButtonElement[], min: number) {
  expect(buttons.length).toBeGreaterThanOrEqual(min);
  for (const b of buttons) {
    expect(b, b.textContent ?? "").toBeDisabled();
  }
}

const summary = (name: string): ManagedSecretSummary => ({
  name,
  currentVersion: 2,
  oldestVersion: 1,
  maxVersions: 10,
  createdAt: "2026-01-02T03:04:05Z",
  updatedAt: "2026-02-03T04:05:06Z",
  currentVersionDeleted: false,
  currentVersionDestroyed: false,
});

function secretsProps(overrides: Partial<ManagedSecretsViewProps> = {}): ManagedSecretsViewProps {
  return {
    env: "prod",
    mode: "managed",
    availability: "available",
    report: { env: "prod", provider: "hosted", secrets: [{ name: "DATABASE_URL", present: true, declared_by: [{ workload: "api", kind: "service" }] }] },
    managed: [summary("DATABASE_URL"), summary("PAYMENTS_API_KEY")],
    isLoading: false,
    selectedName: null,
    onSelect: vi.fn(),
    versions: [],
    versionsLoading: false,
    onAdd: vi.fn(),
    onSet: vi.fn(),
    onDelete: vi.fn(),
    onUndelete: vi.fn(),
    onDestroy: vi.fn(),
    pendingVersion: null,
    ...overrides,
  };
}

const domain: ForgeDomain = {
  id: "dom-shop",
  hostname: "shop.example.com",
  state: "live",
  origin: "external",
  requiredRecords: [],
  lastError: "",
  binding: { id: "bind-1", domainId: "dom-shop", environmentId: "env-prod", target: "shop-web", redirectTo: "" },
} as unknown as ForgeDomain;

describe("the demo console's write controls are off, saying why", () => {
  it("Secrets: Add secret, Set new version, and each version's actions", () => {
    const list = render(demo(<ManagedSecretsView {...secretsProps()} />));
    expect(screen.getByTestId("add-secret")).toBeDisabled();
    expect(screen.getByTestId("add-secret")).toHaveAttribute("title", REASON);
    expectAllLocked(writeButtons(), 1);
    list.unmount();

    render(
      demo(
        <ManagedSecretsView
          {...secretsProps({
            selectedName: "DATABASE_URL",
            versions: [
              { version: 2, createdAt: "2026-02-03T04:05:06Z", destroyed: false },
              { version: 1, createdAt: "2026-01-02T03:04:05Z", destroyed: false },
            ] as ManagedSecretsViewProps["versions"],
          })}
        />,
      ),
    );
    expect(screen.getByTestId("set-new-version")).toBeDisabled();
    expectAllLocked(writeButtons(), 2);
  });

  it("Secrets: the empty state's Add secret too", () => {
    render(demo(<ManagedSecretsView {...secretsProps({ managed: [], report: { env: "prod", provider: "hosted", secrets: [] } })} />));
    for (const id of ["add-secret", "add-secret-empty"]) {
      const b = screen.queryByTestId(id);
      if (b) expect(b, id).toBeDisabled();
    }
    expectAllLocked(writeButtons(), 1);
  });

  it("Domains: every action on a domain", () => {
    render(
      demo(
        <DomainDetail domain={domain} envNameFor={() => "prod"} onVerify={vi.fn()} onRebind={vi.fn()} onUnbind={vi.fn()} onRemove={vi.fn()} isVerifying={false} actionError={null} />,
      ),
    );
    const buttons = writeButtons();
    expect(buttons.map((b) => b.textContent?.trim())).toEqual(["Check DNS now", "Change target", "Stop serving", "Remove domain"]);
    expectAllLocked(buttons, 4);
    for (const b of buttons) expect(b).toHaveAttribute("title", REASON);
  });

  it("Environments: Start / Stop, and no danger menu", () => {
    render(demo(<EnvLifecycleControls environmentId="env-prod" envName="prod" status={{ verdict: "converged", workloads: [], currentPromotion: null } as never} />));
    expectAllLocked(writeButtons(), 1);
    expect(screen.queryByTestId("env-danger-menu")).toBeNull();
  });

  it("Releases, the Overview and a workload: Promote and Roll back are blocked with the reason", () => {
    const wrapper = ({ children }: { children: ReactNode }) => demo(children);
    const { result } = renderHook(() => usePromoteFlow(), { wrapper });
    expect(result.current.control.blockedReason).toBe(REASON);
  });

  it("and outside the demo, the same controls are live", () => {
    render(demo(<DomainDetail domain={domain} envNameFor={() => "prod"} onVerify={vi.fn()} onRebind={vi.fn()} onUnbind={vi.fn()} onRemove={vi.fn()} isVerifying={false} actionError={null} />, false));
    for (const b of writeButtons()) expect(b, b.textContent ?? "").toBeEnabled();
    const wrapper = ({ children }: { children: ReactNode }) => demo(children, false);
    const { result } = renderHook(() => usePromoteFlow(), { wrapper });
    expect(result.current.control.blockedReason).toBeNull();
  });
});
