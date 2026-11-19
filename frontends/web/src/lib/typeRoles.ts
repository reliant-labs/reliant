/**
 * The type roles (`text-meta`, `text-ui`, …): the semantic font sizes in
 * tailwind.config.js `fontSize`, listed here so tailwind-merge (lib/utils.ts
 * `cn()`) knows each is a font size.
 *
 * Without this list tailwind-merge reads an unknown `text-<word>` as a COLOR,
 * so `cn("text-muted-foreground", "text-meta")` would drop the color, and
 * `cn("text-sm", "text-ui")` would keep both sizes. typeRoleContract.test.ts
 * holds this list equal to the config.
 */
export const TYPE_ROLES = ["meta", "ui", "copy", "code", "section", "title", "figure"] as const;

export type TypeRole = (typeof TYPE_ROLES)[number];
