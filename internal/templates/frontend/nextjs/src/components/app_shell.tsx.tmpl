"use client";

// yours: scaffolded once, never touched again — forge will not overwrite this file
// AppShell: the product's chrome, chosen on the client because the server root layout cannot
// read the pathname. Routes in `bareRoutes` (prefix match; "/auth" by default) render bare.
// The shell draws the app's one <main>: a page inside it must not draw another; a bare route's
// page draws its own. Depth: `forge skill load frontend`.

import { usePathname } from "next/navigation";

import { AppHeader } from "@/components/app_header";
import { MobileNav } from "@/components/mobile_nav";
import { Nav } from "@/components/nav";

// Credit link, shown at the bottom of every page. Yours to edit or delete:
// remove this constant and the footer element below. Forge never re-adds it.
const BUILT_WITH_FORGE_URL =
  "https://reliantlabs.io/?utm_source=forge&utm_medium=app-footer&utm_campaign=built-with-forge";

/**
 * Route prefixes that render WITHOUT the app chrome.
 *
 * `/auth` covers the sign-in screen, the credential form the issuer
 * redirects into, and the OAuth callback — none of which a signed-out
 * visitor should see wrapped in navigation they cannot use.
 */
const bareRoutes = ["/auth"];

function isBare(pathname: string): boolean {
  return bareRoutes.some((p) => pathname === p || pathname.startsWith(`${p}/`));
}

export function AppShell({
  children,
  banner,
}: {
  children: React.ReactNode;
  /**
   * Optional chrome rendered above the shell — the mock-mode banner the root
   * layout passes in. Passed in rather than imported so this component does
   * not depend on whether, or where, the layout keeps one.
   */
  banner?: React.ReactNode;
}) {
  const pathname = usePathname() ?? "";

  // Bare: the page owns the whole viewport and paints its own background.
  if (isBare(pathname)) {
    return <>{children}</>;
  }

  return (
    // Chrome vs canvas. The sidebar sits on `bg-surface` (the chrome) and the
    // content area on `bg-surface-muted` (the canvas), so the two read as
    // distinct planes and the cards on the canvas — which are themselves
    // `bg-surface` — separate from their background. Putting both on the same
    // color is what makes a scaffold look like an unstyled wireframe.
    <div className="flex h-screen flex-col overflow-hidden bg-surface text-ink">
      {/*
        Skip link: the first tab stop on every page. Without it a keyboard or
        switch user tabs through the whole sidebar and header before reaching
        the content. Visually hidden until focused, then pinned top-left.
      */}
      <a
        href="#main"
        className="sr-only focus:not-sr-only focus:fixed focus:top-2 focus:left-2 focus:z-50 focus:rounded-md focus:bg-surface focus:px-3 focus:py-2 focus:text-sm focus:font-medium focus:text-ink focus:shadow-lg focus:ring-2 focus:ring-accent focus:outline-none"
      >
        Skip to content
      </a>
      {banner}
      <div className="flex min-h-0 flex-1 overflow-hidden">
        <Nav />
        <MobileNav />
        {/*
          The header is a SIBLING of the scroll container, not inside it:
          identity has to stay put while the page scrolls, and the session
          control is the one thing a user must always be able to reach.
        */}
        <div className="flex min-w-0 flex-1 flex-col overflow-hidden">
          <AppHeader />
          {/*
            The page's ONE main landmark — pages render a section, never a
            second main. tabIndex={-1} lets the skip link move focus here.
          */}
          <main
            id="main"
            tabIndex={-1}
            className="flex-1 overflow-y-auto bg-surface-muted focus:outline-none"
          >
            {/*
              A max width on the content column. Full-bleed text on a 27"
              monitor is unreadable, and a scaffold that never sets one
              teaches every page added later to skip it too.
            */}
            <div className="mx-auto max-w-7xl p-6 lg:p-8">{children}</div>
          </main>
          {/*
            The credit is a SIBLING of the scroll container, like the header:
            it stays at the bottom of the shell while the page scrolls, and
            sits outside the main landmark.
          */}
          <footer className="shrink-0 bg-surface-muted px-6 py-2 text-center text-xs text-ink-muted lg:px-8">
            Built with ❤️ using{" "}
            <a
              href={BUILT_WITH_FORGE_URL}
              target="_blank"
              rel="noopener"
              className="rounded-sm underline underline-offset-2 hover:text-ink focus-visible:ring-2 focus-visible:ring-accent focus-visible:outline-none"
            >
              Forge
            </a>
          </footer>
        </div>
      </div>
    </div>
  );
}
