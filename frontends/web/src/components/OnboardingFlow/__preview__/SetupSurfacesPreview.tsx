// Copyright (c) 2026 Reliant Labs
/**
 * The download / connect / buy-a-machine surfaces, standalone, against a fake
 * network: `/setup-surfaces-preview?surface=<id>`. Dev only (DevOnlyRoute).
 *
 * Each surface is the REAL component; only the network is fake. Every RPC is
 * answered here: a fixture when the surface needs one, otherwise the empty
 * response, so nothing reaches a backend and nothing needs signing in. Used to
 * screenshot the onboarding-polish changes before and after.
 */
import { useLayoutEffect, useState, type ReactNode } from "react";
import { create, type DescMessage } from "@bufbuild/protobuf";
import { Code, ConnectError, type Transport, type UnaryResponse } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

import { setTransport } from "@/lib/connect";
import { useAuthStore } from "@/store/authStore";
import { DaemonInfoSchema, DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";
import {
  LocalModelEndpointSchema,
  LocalModelInfoSchema,
  LocalModelInventorySchema,
} from "@/gen/reliant/v1/tools_daemon_pb";
import { ComputeStep } from "@/components/OnboardingFlow/steps/ComputeStep";
import type { LaunchPlan } from "@/components/OnboardingFlow/types";
import { MachineSaleConsent } from "@/components/Billing/MachineSaleConsent";
import { LocalModelsSection } from "@/components/Settings/LocalModelsSection";
import { TokenSettings } from "@/components/Settings/TokenSettings";
import { MobileAIProvidersPanel } from "@/components/Mobile/MobileAIProvidersPanel";
import { ApiKeySetupModal } from "@/components/ApiKeySetupModal";
import { PairMachinePanel } from "@/components/Machines/PairMachinePanel";
import { OAuthHelperPanel } from "@/components/OAuthHelperPanel";

type Fixtures = Record<string, unknown>;

/** Today's catalog (control-plane internal/plansconfig/pricing.yaml, pricing v2). */
const PLANS = {
  plans: [
    { id: "plan_compute_s2", name: "Compute S2", displayOrder: 1, priceCents: 2148n, structuredLimits: { allowedDaemonSizes: ["s1", "s2"], machineIncludedHours: 160n } },
    { id: "plan_compute_m", name: "Compute M", displayOrder: 2, priceCents: 3236n, structuredLimits: { allowedDaemonSizes: ["s1", "s2", "m"], machineIncludedHours: 160n } },
    { id: "plan_compute_l", name: "Compute L", displayOrder: 3, priceCents: 6112n, structuredLimits: { allowedDaemonSizes: ["s1", "s2", "m", "l"], machineIncludedHours: 160n } },
    { id: "plan_compute_xl", name: "Compute XL", displayOrder: 4, priceCents: 11504n, structuredLimits: { allowedDaemonSizes: ["s1", "s2", "m", "l", "xl"], machineIncludedHours: 160n } },
  ],
  daemonPricing: {
    sizes: [
      { size: "s1", multiplier: 1n, hourlyPriceCents: "10", storageGib: 30n },
      { size: "s2", multiplier: 1n, hourlyPriceCents: "10.05", storageGib: 30n },
      { size: "m", multiplier: 1n, hourlyPriceCents: "14.6", storageGib: 50n },
      { size: "l", multiplier: 2n, hourlyPriceCents: "29.2", storageGib: 80n },
      { size: "xl", multiplier: 4n, hourlyPriceCents: "58.4", storageGib: 120n },
    ],
    storageCentsPerGibMonth: "25",
    placeholder: false,
  },
};

const BASE: Fixtures = {
  "UserService.GetCurrentUser": { user: { id: "u1", email: "preview@reliantlabs.io", name: "Preview" } },
  "OrganizationService.ListMyOrganizations": {
    organizations: [{ id: "org-a", name: "Acme", slug: "acme", role: "owner", isOwner: true, isDefault: true }],
    defaultOrgId: "org-a",
  },
  "BillingService.ListPlans": PLANS,
  "BillingService.GetCurrentUserComputeEligibility": { eligible: false, reason: "no_subscription" },
  "BillingService.GetMachinePricingReadiness": {
    pricingVersion: "v2", termsSha256: "preview", routeId: "machine-pooled-v1/s2", size: "s2",
    activationReady: true, activationState: "ready_for_explicit_signup_checkout", holdReasons: [],
    monthlyPriceCents: 2148n, includedOwnSizeHours: 160n, includedDiskGib: 30n,
    exactHourlyRateCents: "1005/100", exactCostWeight: "1/1",
  },
};

const model = (name: string, contextWindow: bigint) =>
  create(LocalModelInfoSchema, { name, supportsChat: true, contextWindow, parameterSize: "8.2B" });

function daemonsWith(endpoints: ReturnType<typeof create<typeof LocalModelEndpointSchema>>[]) {
  return {
    daemons: [
      create(DaemonInfoSchema, {
        daemonId: "d1",
        hostname: "Preview MacBook",
        status: DaemonStatus.ACTIVE,
        daemonType: "local",
        localModels: create(LocalModelInventorySchema, { probedAt: new Date().toISOString(), endpoints }),
      }),
    ],
  };
}

/** Answers every unary RPC: the fixture, or the method's empty response. */
function previewTransport(fixtures: Fixtures): Transport {
  return {
    async unary(method, _signal, _timeoutMs, _header, input): Promise<UnaryResponse<never, never>> {
      const key = `${method.parent.name}.${method.name}`;
      const fixture = fixtures[key];
      if (fixture === undefined) console.debug(`[setup-surfaces-preview] empty response for ${key}`, input);
      const value = typeof fixture === "function" ? (fixture as () => unknown)() : fixture;
      return {
        service: method.parent as never,
        method: method as never,
        stream: false,
        header: new Headers(),
        message: create(method.output as DescMessage, (value ?? {}) as never) as never,
        trailer: new Headers(),
      };
    },
    async stream(method) {
      throw new ConnectError(`preview: no stream for ${method.parent.name}.${method.name}`, Code.Unimplemented);
    },
  };
}

function Compute() {
  // Starts on S2, the report's case: the row the owner clicked.
  const [plan, setPlan] = useState<Partial<LaunchPlan>>({ computePlanId: "plan_compute_s2" });
  return (
    <ComputeStep
      plan={plan}
      updatePlan={async (patch: Partial<LaunchPlan>) => setPlan((p) => ({ ...p, ...patch }))}
      onNext={() => {}}
      onBack={() => {}}
    />
  );
}

const SURFACES: Record<string, { title: string; fixtures?: Fixtures; render: () => ReactNode; width?: number; signedIn?: boolean }> = {
  compute: { title: "Onboarding: Choose your machine", render: () => <Compute /> },
  "machine-checkout": {
    title: "Onboarding checkout: machine plan",
    width: 560,
    render: () => (
      <MachineSaleConsent planId="plan_compute_s2">
        {() => (
          <div className="rounded-lg border border-dashed border-border p-6 text-center text-sm text-muted-foreground">
            Card payment (Stripe) mounts here.
          </div>
        )}
      </MachineSaleConsent>
    ),
  },
  "local-models": {
    title: "Settings → Local models: getting started",
    fixtures: { "DaemonRegistryService.ListDaemons": daemonsWith([]) },
    render: () => <LocalModelsSection />,
  },
  "local-models-context": {
    title: "Settings → Local models: Ollama context fix",
    fixtures: {
      "DaemonRegistryService.ListDaemons": daemonsWith([
        create(LocalModelEndpointSchema, {
          id: "ollama", kind: "ollama", baseUrl: "http://localhost:11434/v1", source: "detected",
          models: [model("qwen3:latest", 4096n)],
        }),
      ]),
    },
    render: () => <LocalModelsSection />,
  },
  tokens: { title: "Settings → Access tokens", render: () => <TokenSettings /> },
  "pair-machine": { title: "Settings → Machines: pair a machine", width: 560, render: () => <PairMachinePanel mode="reference" /> },
  "provider-helper": { title: "Provider sign-in helper", width: 560, render: () => <OAuthHelperPanel providerName="Claude" available={false} loading={false} connecting={false} onRetry={() => {}} onConnect={() => {}} /> },
  // Provider accounts list only for a session; the token is never sent anywhere.
  "mobile-providers": { title: "Mobile: AI providers", width: 390, signedIn: true, render: () => <MobileAIProvidersPanel /> },
  "api-key-modal": { title: "Provider connect dialog (Add an API key)", render: () => <ApiKeySetupModal isOpen onClose={() => {}} /> },
};

export default function SetupSurfacesPreview() {
  const id = new URLSearchParams(window.location.search).get("surface") ?? "";
  const surface = SURFACES[id];
  return <PreviewSession key={id} fixtures={{ ...BASE, ...surface?.fixtures }} signedIn={surface?.signedIn}>
    <PreviewSurface surface={surface} />
  </PreviewSession>;
}

/** Install before mounting children; restore auth/transport on exit (review B1). */
export function PreviewSession({ fixtures, signedIn, children }: { fixtures: Fixtures; signedIn?: boolean; children: ReactNode }) {
  const [client, setClient] = useState<QueryClient | null>(null);
  useLayoutEffect(() => {
    const restoreTransport = setTransport(previewTransport(fixtures));
    const originalSession = useAuthStore.getState().session;
    const fakeSession = { access_token: "preview" } as NonNullable<typeof originalSession>;
    if (signedIn) useAuthStore.setState({ session: fakeSession });
    const cache = new QueryClient({ defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false } } });
    setClient(cache);
    return () => {
      cache.clear();
      restoreTransport();
      if (signedIn && useAuthStore.getState().session === fakeSession) useAuthStore.setState({ session: originalSession });
    };
    // A keyed session owns one fixture set for its whole lifetime.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  return client ? <QueryClientProvider client={client}>{children}</QueryClientProvider> : null;
}

function PreviewSurface({ surface }: { surface: (typeof SURFACES)[string] | undefined }) {
  if (!surface) {
    return (
      <main className="mx-auto max-w-xl space-y-3 p-8 text-foreground">
        <h1 className="text-xl font-semibold">Setup surfaces preview</h1>
        <ul className="list-disc pl-5 text-sm">
          {Object.entries(SURFACES).map(([key, s]) => (
            <li key={key}><a href={`?surface=${key}`}>{s.title}</a> <code className="text-xs">?surface={key}</code></li>
          ))}
        </ul>
      </main>
    );
  }
  return (
    <main className="min-h-screen bg-background p-4 text-foreground sm:p-8" data-testid="setup-surface">
      <div className="mx-auto w-full" style={{ maxWidth: surface.width ?? 840 }}>
        <p role="note" className="mb-4 text-xs text-muted-foreground">Preview · pairing and sign-in are simulated. Commands use public Reliant CLI defaults; they connect to production, not this preview. No tokens are shown.</p>
        {surface.render()}
      </div>
    </main>
  );
}
