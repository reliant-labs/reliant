import { expect, type Locator, type Page } from '@playwright/test';

/** Available CSS widths, not device model claims. One journey per project. */
export const responsiveViewports = {
  phone: { width: 390, height: 844 },
  narrow: { width: 320, height: 740 },
  tablet: { width: 834, height: 1112 },
  // Generic split-window width; not a Duo posture/device emulation.
  splitPane: { width: 540, height: 720 },
  wide: { width: 1112, height: 834 },
} as const;

export async function expectNoPageOverflow(page: Page) {
  await expect.poll(() => page.evaluate(() => {
    const root = document.documentElement;
    return Math.max(root.scrollWidth, document.body.scrollWidth) - root.clientWidth;
  }), { message: 'Only named code/table regions may scroll horizontally, never the page' }).toBeLessThanOrEqual(1);
}

/** Check the actual clickable box and centre hit, not only CSS min-height. */
export async function expectReachable(control: Locator, touch = true) {
  await control.scrollIntoViewIfNeeded();
  await expect(control).toBeVisible();
  await expect.poll(() => control.evaluate((el) => {
    const rect = el.getBoundingClientRect();
    const hit = document.elementFromPoint(rect.x + rect.width / 2, rect.y + rect.height / 2);
    return {
      width: rect.width, height: rect.height,
      hit: !!hit && (hit === el || el.contains(hit)),
      inside: rect.left >= -1 && rect.top >= -1 && rect.right <= innerWidth + 1 && rect.bottom <= innerHeight + 1,
    };
  }), { message: 'Control must be unobscured, in the viewport, and at least 44×44 on touch' })
    .toEqual(expect.objectContaining({ hit: true, inside: true }));
  if (touch) {
    const box = await control.boundingBox();
    expect(box!.width, 'touch target width').toBeGreaterThanOrEqual(44);
    expect(box!.height, 'touch target height').toBeGreaterThanOrEqual(44);
  }
}
