import { useEffect, useRef } from "react";
import { useRouterState } from "@tanstack/react-router";
import { privacyConsentKnown, subscribePrivacyConsent } from "../hooks/privacy-queries";
import { trackEvent } from "../lib/analytics";
import { routeTemplate } from "../lib/pageVisit";

/**
 * Sends `product.page_visited` once per resolved navigation. Mounted at the
 * router root so every route, authenticated or not, is covered. A search-only
 * change is not a visit; moving between two ids of one template is.
 *
 * Both the template and the path come from the resolved leaf match. The
 * router's `location` commits ahead of its `matches`, so reading the template
 * from one and the path from the other reports the previous page.
 *
 * Visits made before the user's saved privacy settings load are held, not
 * sent: until then consent reads as the default (enabled), and the runtime
 * replays events queued before telemetry starts, so a returning user who
 * opted out would be tracked. When the settings load, the held visits are
 * sent if analytics is enabled and dropped if not.
 */
/** Visits held while consent loads; a signed-out stretch keeps only the latest. */
const MAX_HELD_VISITS = 20;

export function PageVisitTracker() {
  const visit = useRouterState({
    select: (s) => {
      const leaf = s.matches[s.matches.length - 1];
      return leaf ? `${leaf.pathname}\n${routeTemplate(leaf.fullPath)}` : "";
    },
  });
  const previous = useRef<string | undefined>(undefined);
  const held = useRef<Record<string, string>[]>([]);

  useEffect(
    () =>
      subscribePrivacyConsent((consent) => {
        const visits = held.current;
        held.current = [];
        if (!consent.analyticsEnabled) return;
        for (const props of visits) trackEvent("page_visited", props);
      }),
    [],
  );

  useEffect(() => {
    if (!visit) return;
    const template = visit.slice(visit.indexOf("\n") + 1);
    const props = {
      pageName: template,
      ...(previous.current ? { previousPage: previous.current } : {}),
    };
    previous.current = template;
    if (privacyConsentKnown()) {
      trackEvent("page_visited", props);
    } else {
      held.current.push(props);
      if (held.current.length > MAX_HELD_VISITS) held.current.shift();
    }
  }, [visit]);

  return null;
}
