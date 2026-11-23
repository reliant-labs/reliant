// Copyright (c) 2026 Reliant Labs

/**
 * The sample app the forge tour walks inside the signed-in shell
 * (Onboarding/journeys/forge.tsx): the demo's shop, at
 * `/forge/p/sample_shop/…`, rendered by the shell's real nav and console
 * pages.
 *
 * The name is reserved by construction: a forge project name is
 * `^[a-z][a-z0-9-]{0,62}$`, so no real app can be called `sample_shop`, and
 * nothing keyed by it (a query, a remembered pick, a page view) can be
 * mistaken for a person's own app. The public demo host keeps `shop`: it has
 * no apps of anyone's to confuse it with.
 *
 * Where the sample's DATA comes from is not decided here (the data seam,
 * demo-di/design.md: a request-scoped transport overlay or injected
 * transports). Every consumer reads the name from this module, so either seam
 * plugs in behind it.
 */

export const SAMPLE_APP = "sample_shop";

/** What the shell calls it where it names an app (the switcher, a breadcrumb). */
export const SAMPLE_APP_LABEL = "shop (sample)";

/** Whether `app` is the tour's sample: never a person's own app. */
export function isSampleApp(app: string | null | undefined): boolean {
  return app === SAMPLE_APP;
}

/** How the console names `app` where it shows its name: the sample by its label, anything else as is. */
export function appDisplayName(app: string): string {
  return isSampleApp(app) ? SAMPLE_APP_LABEL : app;
}
