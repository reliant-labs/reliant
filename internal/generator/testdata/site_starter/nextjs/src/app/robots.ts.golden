import type { MetadataRoute } from "next";

import { SITE_URL } from "../lib/site";

// robots.txt, written into out/ by the static export. Its Sitemap: line must
// be absolute, so it is generated from SITE_URL rather than kept in public/.
// To keep a path out of search results: rules: { userAgent: "*", allow: "/",
// disallow: "/private/" }. A claimable preview deploy is already served with
// X-Robots-Tag: noindex.
export const dynamic = "force-static";

export default function robots(): MetadataRoute.Robots {
  return {
    rules: { userAgent: "*", allow: "/" },
    ...(SITE_URL ? { sitemap: `${SITE_URL}/sitemap.xml` } : {}),
  };
}
