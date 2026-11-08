// Copyright (c) 2025 Reliant Labs
import { useNavigate } from "@tanstack/react-router";
import Button from "@/components/forge-ui/button";
import { trackEvent } from "@/lib/analytics";

/** Build first, then see it running. Inline, so the actual build is never hidden. */
export function DemoWelcome() {
  const navigate = useNavigate();
  return <section className="space-y-4" data-testid="demo-welcome" aria-labelledby="demo-welcome-title">
    <div className="space-y-2 max-w-3xl">
      <h1 id="demo-welcome-title" className="text-2xl font-semibold text-ink">Forge builds your app. Then helps you keep it running.</h1>
      <p className="text-sm leading-relaxed text-ink">A production framework that turns your app&apos;s data and API definitions into starting code and production setup, with tools to build, deploy and understand your running app.</p>
      <p className="text-xs text-ink-muted">Real build recording. Read-only sample data in the console. No sign-in or live production backend.</p>
    </div>
    <ol aria-label="From app code to a healthy app" className="grid grid-cols-3 divide-x divide-border rounded-lg border border-border bg-surface text-sm text-ink">
      <li className="p-3"><strong className="block">1. Build</strong><span className="text-xs text-ink-muted">Code + schema</span></li>
      <li className="p-3"><strong className="block">2. Deploy</strong><span className="text-xs text-ink-muted">Versioned releases</span></li>
      <li className="p-3"><strong className="block">3. Observe</strong><span className="text-xs text-ink-muted">Health + agent context</span></li>
    </ol>
    <div className="flex flex-wrap gap-2">
      <Button className="min-h-[44px]" onClick={() => {
        trackEvent("demo_welcome_choice", { choice: "explore" });
        void navigate({ to: "/forge/p/shop", search: {} } as never);
      }}>Explore the running app</Button>
      <Button className="min-h-[44px]" variant="outline" onClick={() => {
        trackEvent("demo_welcome_choice", { choice: "tour" });
        void navigate({ to: "/forge/demo", search: { view: "overview", tour: "forge:fg-intro" } } as never);
      }}>Take a guided tour</Button>
    </div>
    <p className="text-xs text-ink-muted">Explore on your own, or follow 10 guided stops. You can leave the tour at any time.</p>
  </section>;
}
