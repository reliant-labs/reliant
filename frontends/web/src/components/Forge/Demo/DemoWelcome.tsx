// Copyright (c) 2025 Reliant Labs
import { useNavigate } from "@tanstack/react-router";
import Button from "@/components/forge-ui/button";
import { trackEvent } from "@/lib/analytics";
import { TOUR_START } from "./DemoScreenActions";

/** The public entry's one hero: what Forge is, then the recording and the running app. */
export function DemoWelcome() {
  const navigate = useNavigate();
  return <section className="space-y-4" data-testid="demo-welcome" aria-labelledby="demo-welcome-title">
    <div className="max-w-3xl space-y-2">
      <h1 id="demo-welcome-title" className="text-2xl font-semibold text-ink sm:text-3xl">Forge builds your app. Then helps you keep it running.</h1>
      <p className="text-base leading-relaxed text-ink-muted">Tell your coding agent what to build; Forge gives it the code, the deploys and the monitoring. You own the code.</p>
    </div>
    <div className="flex flex-wrap gap-2">
      <Button className="min-h-[44px]" onClick={() => {
        trackEvent("demo_welcome_choice", { choice: "explore" });
        void navigate({ to: "/forge/p/shop", search: {} } as never);
      }}>Explore the running app</Button>
      <Button className="min-h-[44px]" variant="outline" onClick={() => {
        trackEvent("demo_welcome_choice", { choice: "tour" });
        void navigate(TOUR_START as never);
      }}>Take the tour</Button>
    </div>
  </section>;
}
