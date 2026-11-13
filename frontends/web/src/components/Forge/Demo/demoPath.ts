// Copyright (c) 2025 Reliant Labs

/** The forge demo console's path, the same in both shells (Reliant and forge.reliantlabs.io). */
export const FORGE_DEMO_PATH = "/forge/demo";

/**
 * `?section=built`: the demo console shows "How this app was built" (howBuilt/)
 * in place of its Observability view. Any nav row leaves it.
 */
export const DEMO_SECTION_BUILT = "built";

/**
 * The plain-language pages of the public demo (approachable/), for readers
 * who are not engineers: the story-led home, releases explained, and the
 * "put an agent on it" workflows. Each is a `?section=` of the demo console.
 */
export const DEMO_SECTION_HOME = "home";
export const DEMO_SECTION_RELEASES = "releases";
export const DEMO_SECTION_AGENTS = "agents";

export const DEMO_SECTIONS = [DEMO_SECTION_BUILT, DEMO_SECTION_HOME, DEMO_SECTION_RELEASES, DEMO_SECTION_AGENTS] as const;
export type DemoSection = (typeof DEMO_SECTIONS)[number];

export const demoSectionHref = (section: DemoSection) => `${FORGE_DEMO_PATH}?section=${section}`;
