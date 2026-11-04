import { test, expect } from '@playwright/test';
import { devHarnessPresent } from './devHarness';

// Real React component + the LOCKED uPlot in a browser, not a constructor mock.
// Observe canvas calls so a caption alone cannot make an invisible chart pass.
test('October singleton, isolated sparse observations and crowded minimum-size tiles', async ({ page }) => {
  const errors: string[] = [];
  page.on('pageerror', error => errors.push(error.message));
  await page.addInitScript(() => {
    const records = new WeakMap<HTMLCanvasElement, { arcs: number[][]; texts: string[] }>();
    const record = (ctx: CanvasRenderingContext2D) => {
      let entry = records.get(ctx.canvas);
      if (!entry) { entry = { arcs: [], texts: [] }; records.set(ctx.canvas, entry); }
      return entry;
    };
    const arc = CanvasRenderingContext2D.prototype.arc;
    CanvasRenderingContext2D.prototype.arc = function (...args) {
      record(this).arcs.push(args.slice(0, 3));
      return arc.apply(this, args);
    };
    const fillText = CanvasRenderingContext2D.prototype.fillText;
    CanvasRenderingContext2D.prototype.fillText = function (...args) {
      record(this).texts.push(String(args[0]));
      return fillText.apply(this, args);
    };
    // uPlot's point paths are Path2D, not immediate context arcs.
    const pathArc = Path2D.prototype.arc;
    const paths = new WeakMap<Path2D, number[][]>();
    Path2D.prototype.arc = function (...args) {
      const arcs = paths.get(this) ?? [];
      arcs.push(args.slice(0, 3)); paths.set(this, arcs);
      return pathArc.apply(this, args);
    };
    const fill = CanvasRenderingContext2D.prototype.fill;
    CanvasRenderingContext2D.prototype.fill = function (...args: Parameters<typeof fill>) {
      if (args[0] instanceof Path2D) record(this).arcs.push(...(paths.get(args[0]) ?? []));
      return fill.apply(this, args);
    };
    Object.assign(window, { chartCanvasRecord: (canvas: HTMLCanvasElement) => records.get(canvas) });
  });
  await page.goto('/forge-dashboards-preview?page=chart-correctness');
  const harness = page.getByTestId('chart-correctness-preview');
  test.skip(!await devHarnessPresent(page, '/forge-dashboards-preview', harness), 'the fixture harness is a dev-build-only route');
  await expect(harness).toBeVisible();
  await expect(page.locator('[aria-label="October singleton"] canvas')).toBeVisible();

  const canvasRecord = async (label: string) => page.getByRole('group', { name: label, exact: true }).locator('canvas').evaluate(canvas => {
    const read = (window as unknown as { chartCanvasRecord: (c: HTMLCanvasElement) => { arcs: number[][]; texts: string[] } }).chartCanvasRecord;
    return { ...read(canvas as HTMLCanvasElement), width: (canvas as HTMLCanvasElement).width };
  });
  const singleton = await canvasRecord('October singleton');
  expect(singleton.arcs.length).toBeGreaterThan(0);
  // With the 1000-day bug the observation was at the extreme left. It must
  // now sit in the middle of the REAL plot (after the 64px value axis).
  const ratio = await page.evaluate(() => devicePixelRatio);
  const expectedMiddle = (64 * ratio + singleton.width) / 2;
  expect(Math.abs(singleton.arcs[0][0] - expectedMiddle)).toBeLessThan(16 * ratio);
  expect(singleton.texts.join(' ')).not.toMatch(/202[7-9]/);

  const sparse = await canvasRecord('Sparse observations');
  const uniqueDots = [...new Map(sparse.arcs.map(a => [a.join(','), a])).values()];
  expect(uniqueDots).toHaveLength(2);
  expect(uniqueDots[0][0]).toBeLessThan(uniqueDots[1][0]);
  expect(uniqueDots[0][1]).toBeGreaterThan(uniqueDots[1][1]);
  // The missing middle bucket must contain no series-colour pixels: no
  // fabricated zero, filled area, or line connecting the isolated samples.
  const gapInk = await page.getByRole('group', { name: 'Sparse observations', exact: true }).locator('canvas').evaluate(canvas => {
    const c = canvas as HTMLCanvasElement;
    const ctx = c.getContext('2d')!;
    const x = Math.round((64 * devicePixelRatio + c.width) / 2);
    const pixels = ctx.getImageData(x, 0, 1, c.height - 50 * devicePixelRatio).data;
    let blue = 0;
    for (let i = 0; i < pixels.length; i += 4) if (pixels[i + 2] > pixels[i] + 40 && pixels[i + 3] > 40) blue++;
    return blue;
  });
  expect(gapInk).toBe(0);

  const crowded = page.getByRole('group', { name: 'Crowded annotations', exact: true });
  const summary = crowded.getByTestId('chart-annotations');
  await expect(summary).toContainText('12 releases · 12 alerts:');
  await expect(summary).toContainText('12 alerts:');
  await expect(crowded.locator('figcaption')).toContainText('Production promoted release 11');
  await expect(crowded.locator('figcaption')).toContainText('RPCErrorRateHigh-11');
  const text = await canvasRecord('Crowded annotations');
  expect(text.texts.join(' ')).not.toMatch(/release-|RPCErrorRateHigh/);
  for (const width of [1440, 800]) {
    await page.setViewportSize({ width, height: 900 });
    const bounds = await summary.boundingBox();
    const canvas = await crowded.locator('canvas').boundingBox();
    expect(bounds!.height).toBeLessThanOrEqual(20);
    expect(bounds!.y).toBeGreaterThanOrEqual(canvas!.y + canvas!.height);
    expect(await crowded.evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true);
    await page.screenshot({ path: `${process.env.CHART_SCREENSHOTS ?? '/tmp/chart-correctness'}/charts-${width}.png`, fullPage: true });
  }
  expect(errors).toEqual([]);
});
