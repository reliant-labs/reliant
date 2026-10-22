/**
 * Placeholder for mobile destinations under active construction elsewhere.
 *
 * `/m/search`, `/m/workflows`, and `/m/settings` are being built by other
 * agents in parallel; registering their routes here (rather than leaving them
 * unregistered) lets the drawer link to all six destinations from day one
 * without three agents fighting over `routes.tsx`. Each real screen replaces
 * its `component:` in a follow-up — this file and its routes are not meant to
 * survive that.
 */

import { useNavigate } from "@tanstack/react-router";
import { Hammer } from "lucide-react";
import {
  MobileBackButton,
  MobileEmptyState,
  MobileScreenHeader,
} from "./MobileChrome";

export function MobileComingSoon({ title }: { title: string }) {
  const navigate = useNavigate();

  return (
    <div className="flex h-full min-h-0 flex-col">
      <MobileScreenHeader
        title={title}
        leading={
          <MobileBackButton
            onClick={() => void navigate({ to: "/m/chats" })}
            label="Back to chats"
          />
        }
      />

      <MobileEmptyState
        icon={Hammer}
        title="Coming soon"
        description={`${title} isn't available on mobile yet.`}
      />
    </div>
  );
}

// Named per-route wrappers, one per placeholder destination, so
// `lazyRouteComponent` in routes.tsx can import each by name without every
// route sharing one generic "MobileComingSoon" chunk label.
export function MobileSearchPlaceholder() {
  return <MobileComingSoon title="Search" />;
}

export function MobileWorkflowsPlaceholder() {
  return <MobileComingSoon title="Workflows" />;
}

export function MobileSettingsPlaceholder() {
  return <MobileComingSoon title="Settings" />;
}
