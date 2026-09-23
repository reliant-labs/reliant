/**
 * Link — the navigation primitive every other forge library component routes
 * through (SidebarLayout nav items, PageHeader actions/breadcrumbs, ...).
 *
 * This is the TANSTACK-ROUTER-AWARE version. Forge ships a framework-neutral
 * plain-anchor fallback and documents this file as the seam a host app
 * overwrites — see the note in forge's own `link` entry: "forge scaffolds
 * overwrite it with a next/link or tanstack-router aware version so internal
 * navigation is client-side". Reliant's app IS tanstack-router, so without
 * this every sidebar click would be a full page reload: a browser navigation
 * tears down the router, the query cache and the zustand stores, which on a
 * forge route means re-running the whole project-resolution ladder and a
 * visible white flash between two screens of the same surface.
 *
 * Internal hrefs are split into path + search here because tanstack's
 * `navigate` takes them as separate arguments, and the forge components'
 * `href` prop is a single string by design (it is the framework-neutral
 * shape). The forge surface carries `project` and `env` in the query string,
 * so dropping the search half would silently lose the project on every nav.
 *
 * External URLs (http(s)://, mailto:, tel:) always render a plain <a> — they
 * are not the router's business, and handing one to `navigate` would try to
 * match it against the route tree and fail.
 */

import React from "react";
import { useNavigate } from "@tanstack/react-router";

const EXTERNAL_HREF = /^(?:[a-z][a-z0-9+.-]*:)?\/\//i;

/** True for absolute/external URLs that must bypass client routing. */
export function isExternalHref(href: string): boolean {
  return (
    EXTERNAL_HREF.test(href) ||
    href.startsWith("mailto:") ||
    href.startsWith("tel:")
  );
}

export type LinkProps = React.AnchorHTMLAttributes<HTMLAnchorElement> & {
  href: string;
};

export default function Link({ href, children, onClick, ...rest }: LinkProps) {
  const navigate = useNavigate();

  if (isExternalHref(href)) {
    return (
      <a href={href} onClick={onClick} {...rest}>
        {children}
      </a>
    );
  }

  const handleClick = (event: React.MouseEvent<HTMLAnchorElement>) => {
    onClick?.(event);
    if (event.defaultPrevented) return;

    // Let the browser handle the modifier gestures that mean "somewhere other
    // than here" — new tab, new window, download. Intercepting those is the
    // classic SPA-link bug: middle-click silently does nothing.
    if (
      event.button !== 0 ||
      event.metaKey ||
      event.ctrlKey ||
      event.shiftKey ||
      event.altKey
    ) {
      return;
    }

    event.preventDefault();
    const [pathname, queryString] = href.split("?");
    const search = queryString
      ? Object.fromEntries(new URLSearchParams(queryString))
      : {};
    void navigate({ to: pathname, search });
  };

  return (
    <a href={href} onClick={handleClick} {...rest}>
      {children}
    </a>
  );
}
