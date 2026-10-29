import { useEffect, useRef } from "react";
import { useRouterState } from "@tanstack/react-router";
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
 */
export function PageVisitTracker() {
  const visit = useRouterState({
    select: (s) => {
      const leaf = s.matches[s.matches.length - 1];
      return leaf ? `${leaf.pathname}\n${routeTemplate(leaf.fullPath)}` : "";
    },
  });
  const previous = useRef<string | undefined>(undefined);

  useEffect(() => {
    if (!visit) return;
    const template = visit.slice(visit.indexOf("\n") + 1);
    trackEvent("page_visited", {
      pageName: template,
      ...(previous.current ? { previousPage: previous.current } : {}),
    });
    previous.current = template;
  }, [visit]);

  return null;
}
