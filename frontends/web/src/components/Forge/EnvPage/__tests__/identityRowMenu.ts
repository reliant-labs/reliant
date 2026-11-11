// Copyright (c) 2025 Reliant Labs

/** A user row's "⋯" menu, for the identity tests: every verb lives there now. */

import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

async function openRowMenu(row: HTMLElement) {
  if (screen.queryByRole("menu")) await userEvent.keyboard("{Escape}");
  await userEvent.click(within(row).getByRole("button", { name: /^Actions for / }));
  return screen.findByRole("menu");
}

/** Opens `row`'s menu and returns its item named `name`. */
export async function rowItem(row: HTMLElement, name: string | RegExp) {
  return within(await openRowMenu(row)).getByRole("menuitem", { name });
}

/** Opens `row`'s menu and returns its item named `name`, or null when the menu has none. */
export async function rowItemOrNull(row: HTMLElement, name: string | RegExp) {
  return within(await openRowMenu(row)).queryByRole("menuitem", { name });
}
