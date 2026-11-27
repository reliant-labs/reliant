import type { MetadataRoute } from "next";

import { SITE_URL } from "../lib/site";

// sitemap.xml, written into out/ by the static export. A sitemap lists
// absolute URLs, so it is generated from SITE_URL; with SITE_URL empty it
// lists none. Add each page you want indexed to `routes`.
export const dynamic = "force-static";

const routes = ["/"];

export default function sitemap(): MetadataRoute.Sitemap {
  if (!SITE_URL) return [];
  return routes.map((route) => ({ url: `${SITE_URL}${route}` }));
}
