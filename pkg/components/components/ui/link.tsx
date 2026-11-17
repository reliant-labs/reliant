import React from "react";

/**
 * Link — the navigation primitive every other library component routes
 * through (PageHeader actions/breadcrumbs, RowActionsMenu href items, ...).
 *
 * This is the framework-NEUTRAL fallback: a plain anchor. When forge
 * scaffolds a Next.js or Vite SPA frontend it overwrites this file with a
 * framework-aware version (next/link or tanstack-router history push) so
 * internal navigation is client-side and basePath-correct. External URLs
 * (http(s)://, mailto:, tel:) always render a plain <a>.
 */

export type LinkProps = React.AnchorHTMLAttributes<HTMLAnchorElement> & {
  href: string;
};

export default function Link({ href, children, ...rest }: LinkProps) {
  return (
    <a href={href} {...rest}>
      {children}
    </a>
  );
}
