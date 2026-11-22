# Public demo screenshots

Captured on 2026-11-13 (UTC).

These are Chrome captures of the public [Forge demo](https://demo.reliantlabs.io/forge),
which identifies itself as **“Sample app · read-only data”**. They show the demo's
example payment-timeout scenario, not customer data or evidence of a live
production incident. `shop`, `prod`, `v1.8.3` and the trace ID below belong to
that example. Displayed timestamps and relative times can change between visits.

| Asset                  | Public source and what it depicts                                                                                                                                                                                                             | Crop (left, top, right, bottom) |
| ---------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------- |
| `forge-demo-issue.png` | [Payment-timeout issue](https://demo.reliantlabs.io/forge/p/shop/observability/errors?env=prod&issue=iss-orders-deadline): HTTP 504, two payment attempts, `CreateOrder`, and the stack through `chargeWithRetry`.                            | `(84, 650, 851, 981)`           |
| `forge-demo-trace.png` | [Trace opened from that issue](https://demo.reliantlabs.io/forge/p/shop/observability/traces?env=prod&trace=4bf92f3577b34da6a3ce929d0e0e4736&span=b3): waterfall, failed `payments.Charge` span, payment-provider attributes and retry event. | `(255, 113, 1250, 896)`         |
| `forge-demo-logs.png`  | [Logs for that trace, filtered to `api`](https://demo.reliantlabs.io/forge/p/shop/observability/logs?env=prod&trace=4bf92f3577b34da6a3ce929d0e0e4736&service=api): request start, retry warning and timeout error. Newest first.              | `(255, 113, 1250, 554)`         |

## Capture and refresh

- Use the unauthenticated public demo in an isolated Chrome context, desktop
  viewport 1280 CSS pixels wide, default zoom and light theme. Dismiss tour
  overlays using the UI. The issue source was a full-page capture at 1280 × 1180;
  the trace and logs source images were viewport captures at 1280 × 920 and
  1280 × 1180 respectively.
- Open the issue, follow **Open trace**, and select `payments.Charge`. For logs,
  use **Logs for this trace**, then filter the service to `api`. The span-level
  log link returned no matching logs when inspected; the trace-level view did.
- Crop only to the documented rectangles. The issue crop intentionally omits
  the sample assignee and user identifier above the attributes. The trace and
  log crops omit navigation and signup links. No names, emails, customer
  identifiers, credentials or claim links are included in these assets.
- Save RGB PNG with lossless optimization, without resizing. No DOM edits,
  substituted values, redrawn charts, overlays or compositing were used.
  The saved pixels were checked against the corresponding source crops.
- Recheck the actual UI before refreshing: do not manufacture data if a route
  becomes empty or unavailable. The public release **Health** tab also exists
  at [v1.8.3](https://demo.reliantlabs.io/forge/p/shop/releases/v1.8.3?env=prod&tab=health),
  but is not pictured; these three images keep the README to one diagnostic
  story and avoid exposing names in release history.
